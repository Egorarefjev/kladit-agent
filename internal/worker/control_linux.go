package worker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
)

const stopGrace = 10 * time.Second

type exitObservation struct {
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	ExitCode   int    `json:"exit_code"`
	OOMKilled  bool   `json:"oom_killed"`
}

func (j *journal) noteStop(reason string) error {
	r := j.state.Current
	if r == nil || r.Runtime == nil || terminalState(r.Report.Event.State) || r.Runtime.DesiredStop != "" {
		return nil
	}
	r.Runtime.DesiredStop = reason
	return j.save()
}
func (j *journal) control(a *connection.JobAssignment, enabled bool) error {
	if a == nil {
		return nil
	}
	reason := ""
	if a.CancelRequested {
		reason = "CANCEL_REQUESTED"
	} else if !enabled {
		reason = "EXECUTION_DISABLED"
	} else if a.StopReason != nil {
		switch *a.StopReason {
		case "CANCEL_REQUESTED", "EXECUTION_DISABLED", "PREPARATION_TIMEOUT":
			reason = *a.StopReason
		default:
			return fmt.Errorf("invalid worker stop control")
		}
	}
	if reason != "" {
		return j.noteStop(reason)
	}
	return nil
}
func (j *journal) stopped() bool {
	r := j.state.Current
	return r == nil || r.Runtime == nil || terminalState(r.Report.Event.State) || r.Runtime.Exit != nil || r.Runtime.Phase == "exited" || r.Runtime.Phase == "stopped_before_start"
}

// enforce is local: no API request/ACK can delay its deadline or cleanup checks.
// Only a full ID with matching identity labels and image is ever signalled.
func (j *journal) enforce(ctx context.Context, d *dockerClient, c *containerInfo, allowed bool) error {
	r := j.state.Current
	if r == nil || r.Runtime == nil || terminalState(r.Report.Event.State) {
		return nil
	}
	run := r.Runtime
	if run.Exit != nil || run.Phase == "exited" || run.Phase == "stopped_before_start" {
		return nil
	}
	missing := c == nil // The caller has proved 404, not merely a failed inspect.
	if !missing && c.State.Status == "exited" {
		// A recorded start intent is required before adopting execution evidence.
		if run.Deadline == "" {
			return nil
		}
		started, e1 := time.Parse(time.RFC3339Nano, c.State.StartedAt)
		finished, e2 := time.Parse(time.RFC3339Nano, c.State.FinishedAt)
		if e1 != nil || e2 != nil || started.IsZero() || finished.Before(started) || c.State.ExitCode < 0 || c.State.ExitCode > 255 {
			return j.markUnknown("EXIT_STATE_UNKNOWN")
		}
		if run.StartedAt != "" && run.StartedAt != c.State.StartedAt {
			return j.markUnknown("CONTAINER_START_CHANGED")
		}
		run.Exit = &exitObservation{StartedAt: c.State.StartedAt, FinishedAt: c.State.FinishedAt, ExitCode: c.State.ExitCode, OOMKilled: c.State.OOMKilled}
		return j.save() // Preserve observed exit before considering a late cancellation.
	}
	reason := run.DesiredStop
	now := time.Now()
	if !allowed && reason == "" {
		reason = "EXECUTION_DISABLED"
	}
	if run.Phase == "preparing" && !now.Before(r.Assignment.PrepareDeadlineAt) {
		reason = "PREPARATION_TIMEOUT"
	}
	if !missing && c.State.Status == "running" && run.Deadline != "" {
		started, err := time.Parse(time.RFC3339Nano, c.State.StartedAt)
		if err != nil || started.IsZero() || run.TimeoutSeconds == 0 {
			return j.markUnknown("START_OUTCOME_UNKNOWN")
		}
		if run.StartedAt != "" && run.StartedAt != c.State.StartedAt {
			return j.markUnknown("CONTAINER_START_CHANGED")
		}
		deadline := started.Add(time.Duration(run.TimeoutSeconds) * time.Second)
		if !now.Before(deadline) {
			reason = "TIMEOUT"
		}
	}
	if run.StopReason == "" && reason != "" {
		run.StopReason = reason
		run.StopRequestedAt = now.UTC().Format(time.RFC3339Nano)
		if err := j.save(); err != nil {
			return err
		}
	}
	if run.StopReason == "" {
		return nil
	}
	if run.Phase == "preparing" && (missing || c.State.Status == "created") {
		run.Phase = "stopped_before_start"
		return j.save()
	}
	if missing || c.State.Status != "running" {
		return nil
	} // Missing/ambiguous execution never frees the slot.
	if run.TermSentAt == "" {
		run.TermSentAt = now.UTC().Format(time.RFC3339Nano)
		if err := j.save(); err != nil {
			return err
		}
	}
	term, _ := time.Parse(time.RFC3339Nano, run.TermSentAt)
	signal := "TERM"
	if !now.Before(term.Add(stopGrace)) {
		signal = "KILL"
	} else if run.TermAccepted {
		return nil
	}
	err := d.request(ctx, http.MethodPost, "/containers/"+c.ID+"/kill?signal="+signal, nil, nil)
	if err == nil && signal == "TERM" {
		run.TermAccepted = true
		return j.save()
	}
	return nil // Lost replies can repeat a stop signal; they can never repeat start.
}

// advanceExit respects the one-unacknowledged-event rule, including while offline.
func (j *journal) advanceExit() (bool, error) {
	r := j.state.Current
	if r == nil || r.Runtime == nil {
		return false, nil
	}
	run := r.Runtime
	if run.Exit == nil && run.Phase != "stopped_before_start" {
		return false, nil
	}
	if !r.Acked {
		return true, nil
	}
	if run.Exit != nil {
		if run.StartedAt == "" {
			run.StartedAt = run.Exit.StartedAt
			started, _ := time.Parse(time.RFC3339Nano, run.StartedAt)
			run.Deadline = started.Add(time.Duration(run.TimeoutSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
			run.Phase = "running"
			return true, j.emit("running", "", nil)
		}
		if r.Report.Event.State != "finalizing" {
			run.Phase = "exited"
			c := &containerInfo{}
			c.State.ExitCode = run.Exit.ExitCode
			c.State.OOMKilled = run.Exit.OOMKilled
			return true, j.emit("finalizing", run.StopReason, c)
		}
	}
	if run.StopReason != "" {
		state := "cancelled"
		if run.StopReason == "TIMEOUT" || run.StopReason == "PREPARATION_TIMEOUT" {
			state = "timed_out"
		}
		return true, j.emit(state, run.StopReason, nil)
	}
	return true, nil // Natural success still requires durable output completion in step 8.
}
