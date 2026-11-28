package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// JobEvent is one durable report replayed unchanged until acknowledged.
type JobEvent struct {
	Seq         uint64 `json:"seq"`
	State       string `json:"state"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	OOMKilled   *bool  `json:"oom_killed,omitempty"`
}

// JobReport binds an event to the existing machine's assignment.
type JobReport struct {
	AttemptID       string   `json:"attempt_id"`
	SpecHash        string   `json:"spec_hash"`
	AssignmentToken string   `json:"assignment_token"`
	Event           JobEvent `json:"event"`
}

// JobsRequest is independent from the metrics POST and its 204 response.
type JobsRequest struct {
	Version          int        `json:"version"`
	WorkerGeneration string     `json:"worker_generation"`
	AgentVersion     string     `json:"agent_version"`
	Capabilities     []string   `json:"capabilities"`
	ExecuteAllowed   bool       `json:"execute_allowed"`
	PreflightOK      bool       `json:"preflight_ok"`
	Report           *JobReport `json:"report,omitempty"`
}

// JobAssignment is transport data; the worker validates the executable spec locally.
type JobAssignment struct {
	JobID                string          `json:"job_id"`
	AttemptID            string          `json:"attempt_id"`
	WorkerGeneration     string          `json:"worker_generation"`
	Spec                 json.RawMessage `json:"spec"`
	SpecHash             string          `json:"spec_hash"`
	AssignmentToken      string          `json:"assignment_token"`
	State                string          `json:"state"`
	AcknowledgedSeq      uint64          `json:"acknowledged_seq"`
	PrepareDeadlineAt    time.Time       `json:"prepare_deadline_at"`
	DeadlineAt           *time.Time      `json:"deadline_at"`
	CancelRequested      bool            `json:"cancel_requested"`
	StopReason           *string         `json:"stop_reason"`
	StartPermitExpiresAt *time.Time      `json:"start_permit_expires_at"`
}

// JobACK acknowledges a persisted event, not successful execution.
type JobACK struct {
	AttemptID string `json:"attempt_id"`
	Seq       uint64 `json:"seq"`
}

// JobsResponse contains at most one assignment, on the same backend origin.
type JobsResponse struct {
	Version          int            `json:"version"`
	WorkerGeneration string         `json:"worker_generation"`
	Enabled          bool           `json:"enabled"`
	ACK              *JobACK        `json:"ack"`
	Assignment       *JobAssignment `json:"assignment"`
}

// JobsError contains only a status, never a URL, response body or secret.
type JobsError struct {
	Status int
	Code   string
}

func (e *JobsError) Error() string { return fmt.Sprintf("worker sync failed (HTTP %d)", e.Status) }

// Permanent means retry cannot safely adopt another identity or revoked permission.
func (e *JobsError) Permanent() bool {
	if e.Status == 409 && e.Code == "WORKER_BUSY" {
		return false
	}
	return e.Status >= 300 && e.Status < 500 && e.Status != 408 && e.Status != 429
}

var jobUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var jobHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var jobToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidJobIdentity checks the bounded identifiers used in journal filenames and bindings.
func ValidJobIdentity(id string) bool { return jobUUID.MatchString(id) }

// SyncJobs reuses the verified HTTPS transport without proxy/redirect fallback.
func (c *Client) SyncJobs(ctx context.Context, request JobsRequest) (JobsResponse, error) {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Path != "/api/v1/agent/metrics" || endpoint.RawPath != "" {
		return JobsResponse{}, fmt.Errorf("worker requires the Kladit metrics endpoint")
	}
	endpoint.Path = "/api/v1/agent/jobs/sync"
	body, err := json.Marshal(request)
	if err != nil || len(body) > 16384 {
		return JobsResponse{}, fmt.Errorf("worker request exceeds its bound")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return JobsResponse{}, fmt.Errorf("invalid worker request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kladit-agent/"+c.version)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return JobsResponse{}, ctx.Err()
		}
		return JobsResponse{}, &JobsError{}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1025))
		code := ""
		if len(data) <= 1024 && json.Unmarshal(data, &body) == nil && body.Error.Code == "WORKER_BUSY" {
			code = "WORKER_BUSY"
		}
		return JobsResponse{}, &JobsError{Status: response.StatusCode, Code: code}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	if err != nil || len(data) > 32768 {
		return JobsResponse{}, fmt.Errorf("invalid worker response size")
	}
	var result JobsResponse
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.Version != 1 || result.WorkerGeneration != request.WorkerGeneration {
		return JobsResponse{}, fmt.Errorf("invalid worker response")
	}
	if result.ACK != nil && (!ValidJobIdentity(result.ACK.AttemptID) || result.ACK.Seq == 0 || result.ACK.Seq > 9007199254740991) {
		return JobsResponse{}, fmt.Errorf("invalid worker ACK")
	}
	if result.Assignment != nil && !ValidJobAssignment(result.Assignment, request.WorkerGeneration) {
		return JobsResponse{}, fmt.Errorf("invalid worker assignment")
	}

	return result, nil
}

// ValidJobAssignment checks transport data before it can become a journal record or filename.
func ValidJobAssignment(a *JobAssignment, generation string) bool {
	if a == nil {
		return false
	}
	var spec map[string]json.RawMessage
	return ValidJobIdentity(a.JobID) && ValidJobIdentity(a.AttemptID) && a.WorkerGeneration == generation && jobHash.MatchString(a.SpecHash) && jobToken.MatchString(a.AssignmentToken) && len(a.Spec) <= 16384 && json.Unmarshal(a.Spec, &spec) == nil && spec != nil && !a.PrepareDeadlineAt.IsZero() && a.AcknowledgedSeq <= 9007199254740991
}
