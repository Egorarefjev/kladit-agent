package worker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
)

type record struct {
	Assignment connection.JobAssignment `json:"assignment"`
	Report     connection.JobReport     `json:"report"`
	Acked      bool                     `json:"acked,omitempty"`
	Runtime    *execution               `json:"runtime,omitempty"`
}

type journalState struct {
	Version    int     `json:"version"`
	Generation string  `json:"generation"`
	Current    *record `json:"current"`
}

func (j *journal) accept(response connection.JobsResponse) error {
	if response.ACK != nil {
		r := j.state.Current
		if r == nil || response.ACK.AttemptID != r.Report.AttemptID || response.ACK.Seq != r.Report.Event.Seq {
			return fmt.Errorf("unexpected worker ACK; reconciliation required")
		}
		if terminalState(r.Report.Event.State) {
			if err := j.write(".done-"+r.Assignment.AttemptID+".json", r); err != nil {
				return err
			}
			j.state.Current = nil
		} else {
			r.Acked = true
		}
		if err := j.save(); err != nil {
			return err
		}
	}
	a := response.Assignment
	if a == nil {
		return nil
	}
	if !connection.ValidJobAssignment(a, j.state.Generation) {
		return fmt.Errorf("invalid assignment; reconciliation required")
	}
	if r := j.state.Current; r != nil {
		if !sameAssignment(r.Assignment, *a) {
			return fmt.Errorf("assignment changed before ACK; reconciliation required")
		}
		return nil
	}
	var r record
	err := j.read(".done-"+a.AttemptID+".json", &r)
	if err == nil {
		if !validRecord(&r, j.state.Generation) || !sameAssignment(r.Assignment, *a) {
			return fmt.Errorf("completed assignment changed; reconciliation required")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("invalid assignment history; reconciliation required")
	}

	if err != nil {
		if err := j.checkHistoryBound(); err != nil {
			return err
		}
		if a.State != "assigned" || a.AcknowledgedSeq != 0 {
			return fmt.Errorf("unknown assignment; reconciliation required")
		}
		r = record{
			Assignment: *a,
			Runtime:    &execution{Phase: "preparing"},
			Report: connection.JobReport{
				AttemptID:       a.AttemptID,
				SpecHash:        a.SpecHash,
				AssignmentToken: a.AssignmentToken,
				Event: connection.JobEvent{
					Seq:   1,
					State: "preparing",
				},
			},
		}
	}
	j.state.Current = &r
	return j.save() // Persist the report before any subsequent sync can acknowledge receipt.
}

func terminalState(state string) bool {
	return state == "failed" || state == "succeeded" || state == "cancelled" || state == "timed_out"
}

func validRecord(r *record, generation string) bool {
	a := r.Assignment
	p := r.Report
	if !connection.ValidJobAssignment(&a, generation) || p.AttemptID != a.AttemptID || p.SpecHash != a.SpecHash || p.AssignmentToken != a.AssignmentToken || p.Event.Seq == 0 || p.Event.Seq > 9007199254740991 {
		return false
	}
	if r.Runtime == nil {
		_, err := time.Parse(time.RFC3339Nano, p.Event.FinishedAt)
		return err == nil && a.State == "assigned" && a.AcknowledgedSeq == 0 && p.Event.Seq == 1 && p.Event.State == "failed" && p.Event.Reason == "RUNNER_NOT_IMPLEMENTED"
	}
	canonical, err := canonicalSpec(a.Spec)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(canonical)) != a.SpecHash || a.State != "assigned" || a.AcknowledgedSeq != 0 {
		return false
	}
	switch r.Runtime.Phase {
	case "preparing", "start_intent", "running", "exited", "unknown":
	default:
		return false
	}
	switch p.Event.State {
	case "preparing", "running", "finalizing", "unknown", "failed", "cancelled", "timed_out":
	default:
		return false
	}
	if r.Runtime.ContainerID != "" && !containerID.MatchString(r.Runtime.ContainerID) {
		return false
	}
	if r.Runtime.ImageID != "" && !imageID.MatchString(r.Runtime.ImageID) {
		return false
	}
	if r.Runtime.Phase == "start_intent" || r.Runtime.Phase == "running" || r.Runtime.Phase == "exited" {
		var spec jobSpec
		if json.Unmarshal(a.Spec, &spec) != nil || spec.TimeoutSeconds != r.Runtime.TimeoutSeconds {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, r.Runtime.Deadline); err != nil {
			return false
		}
		if !containerID.MatchString(r.Runtime.ContainerID) || !imageID.MatchString(r.Runtime.ImageID) || r.Runtime.TimeoutSeconds == 0 || r.Runtime.TimeoutSeconds > 86400 {
			return false
		}
	}
	if terminalState(p.Event.State) {
		_, err := time.Parse(time.RFC3339Nano, p.Event.FinishedAt)
		return err == nil
	}
	return p.Event.FinishedAt == ""
}

func sameAssignment(a, b connection.JobAssignment) bool {
	first, err := canonicalSpec(a.Spec)
	if err != nil {
		return false
	}
	second, err := canonicalSpec(b.Spec)
	if err != nil {
		return false
	}
	return a.AttemptID == b.AttemptID &&
		a.JobID == b.JobID &&
		a.WorkerGeneration == b.WorkerGeneration &&
		a.SpecHash == b.SpecHash &&
		a.AssignmentToken == b.AssignmentToken &&
		bytes.Equal(first, second)
}
