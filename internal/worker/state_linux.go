package worker

import (
	"bytes"
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
		if err := j.write(".done-"+r.Assignment.AttemptID+".json", r); err != nil {
			return err
		}
		j.state.Current = nil
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
			Report: connection.JobReport{
				AttemptID:       a.AttemptID,
				SpecHash:        a.SpecHash,
				AssignmentToken: a.AssignmentToken,
				Event: connection.JobEvent{
					Seq:        1,
					State:      "failed",
					FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
					Reason:     "RUNNER_NOT_IMPLEMENTED",
				},
			},
		}
	}
	j.state.Current = &r
	return j.save() // Persist the report before any subsequent sync can acknowledge receipt.
}

func validRecord(r *record, generation string) bool {
	a := r.Assignment
	p := r.Report
	_, err := time.Parse(time.RFC3339Nano, p.Event.FinishedAt)
	return err == nil &&
		a.State == "assigned" &&
		a.AcknowledgedSeq == 0 &&
		connection.ValidJobAssignment(&a, generation) &&
		p.AttemptID == a.AttemptID &&
		p.SpecHash == a.SpecHash &&
		p.AssignmentToken == a.AssignmentToken &&
		p.Event.Seq == 1 &&
		p.Event.State == "failed" &&
		p.Event.Reason == "RUNNER_NOT_IMPLEMENTED"
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

func canonicalSpec(spec json.RawMessage) ([]byte, error) {
	// Compare JSON values, not object-key order. This is not the backend's spec_hash algorithm.
	var value any
	decoder := json.NewDecoder(bytes.NewReader(spec))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
