package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// JobLogChunk is persisted unchanged before its first delivery.
type JobLogChunk struct {
	Seq       uint64 `json:"seq"`
	Stream    string `json:"stream"`
	Data      []byte `json:"data_base64"`
	Truncated bool   `json:"truncated,omitempty"`
}

// JobLogsRequest uses the existing machine/assignment bindings, independently of events.
type JobLogsRequest struct {
	Version          int    `json:"version"`
	WorkerGeneration string `json:"worker_generation"`
	AttemptID        string `json:"attempt_id"`
	SpecHash         string `json:"spec_hash"`
	AssignmentToken  string `json:"assignment_token"`
	JobLogChunk
}

// JobLogsError exposes only allowlisted protocol codes and a bounded retry delay.
type JobLogsError struct {
	Status     int
	Code       string
	NextSeq    uint64
	RetryAfter time.Duration
}

func (e *JobLogsError) Error() string { return fmt.Sprintf("worker logs failed (HTTP %d)", e.Status) }

// SendJobLogs returns only after validating the durable ACK of this exact chunk.
func (c *Client) SendJobLogs(ctx context.Context, r JobLogsRequest) error {
	if !ValidJobIdentity(r.WorkerGeneration) || !ValidJobIdentity(r.AttemptID) || !jobHash.MatchString(r.SpecHash) || !jobToken.MatchString(r.AssignmentToken) || r.Version != 1 || !ValidJobLogChunk(&r.JobLogChunk) {
		return fmt.Errorf("invalid worker log chunk")
	}

	endpoint, err := url.Parse(c.endpoint)
	if err != nil || endpoint.Path != "/api/v1/agent/metrics" || endpoint.RawPath != "" {
		return fmt.Errorf("worker requires the Kladit metrics endpoint")
	}
	endpoint.Path = "/api/v1/agent/jobs/logs"
	// []byte marshals to padded standard base64; an empty marker must be "", never null.
	if r.Data == nil {
		r.Data = []byte{}
	}
	body, err := json.Marshal(r)
	if err != nil || len(body) > 16384 {
		return fmt.Errorf("worker log request exceeds its bound")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid worker log request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kladit-agent/"+c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return &JobLogsError{}
	}
	defer resp.Body.Close()
	rejected := &JobLogsError{Status: resp.StatusCode}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		rejected.RetryAfter = time.Duration(min(seconds, 300)) * time.Second
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil || len(data) > 1024 {
		return rejected
	}
	var result struct {
		ACK     *JobACK `json:"ack"`
		NextSeq uint64  `json:"next_seq"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &result) != nil {
		return rejected
	}
	if resp.StatusCode != http.StatusOK {
		e := rejected
		e.NextSeq = result.NextSeq
		switch result.Error.Code {
		case "JOB_LOG_LIMIT", "JOB_LOG_CONFLICT", "JOB_LOG_GAP", "JOB_LOG_CLOSED", "JOB_LOGS_EXPIRED":
			e.Code = result.Error.Code
		}

		return e
	}
	if result.ACK == nil || result.ACK.AttemptID != r.AttemptID || result.ACK.Seq != r.Seq || result.NextSeq <= r.Seq || result.NextSeq > 16386 {
		return &JobLogsError{}
	}
	return nil
}

// ValidJobLogChunk checks journal data without converting workload bytes to text.
func ValidJobLogChunk(c *JobLogChunk) bool {
	if c == nil {
		return true
	}
	// Keep this validation consistent with the upload boundary.
	return c.Seq > 0 && c.Seq <= 16385 && len(c.Data) <= 8192 && ((c.Truncated && c.Stream == "system" && len(c.Data) == 0) || (!c.Truncated && (c.Stream == "stdout" || c.Stream == "stderr") && len(c.Data) > 0))
}
