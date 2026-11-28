package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

type syncResult struct {
	response connection.JobsResponse
	err      error
}

// Run owns all journal mutations. One bounded outgoing sync runs independently so
// a blocked API request cannot delay the local timeout/stop observer.
func Run(ctx context.Context, options Options) error {
	p, policyErr := policy.LoadWorker(options.PolicyPath)
	if policyErr != nil || !p.ExecuteAllowed() {
		if _, err := os.Lstat(filepath.Join(options.StatePath, "state.json")); err != nil || options.Check {
			if policyErr != nil {
				return policyErr
			}
			return fmt.Errorf("capability denied: jobs.execute")
		}
	}
	if options.Check {
		if err := preflight(ctx, p, options.StatePath, options.DockerSocket); err != nil {
			return err
		}
		if options.Log != nil {
			_, err := fmt.Fprintln(options.Log, "Worker prerequisites passed; execution readiness remains disabled")
			return err
		}
		return nil
	}
	j, err := openJournal(options.StatePath)
	if err != nil {
		return err
	}
	defer j.Close()
	docker := newDocker(options.DockerSocket)
	defer docker.close()
	client, fatal := workerClient(options)
	if client != nil {
		defer client.Close()
	}
	if fatal != nil {
		if err = j.noteStop("CONTROL_REJECTED"); err != nil {
			return err
		}
	}
	log := options.Log
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintln(log, "Worker runner active; execution readiness remains disabled")
	ctx, cancel := context.WithCancel(ctx)
	var calls sync.WaitGroup
	defer func() { cancel(); calls.Wait() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var pending <-chan syncResult
	var logPending <-chan logResult
	var logCancel context.CancelFunc
	nextLog := time.Time{}
	lastLog := ""
	logBackoff := time.Second
	nextSync := time.Time{}
	notifiedDenied, sentAllowed, failed := false, false, false
	for {
		if ctx.Err() != nil {
			return nil
		}
		observeCtx, done := context.WithTimeout(ctx, 2*time.Second)
		err = j.tick(observeCtx, docker, options, nil, false)
		done()
		if err != nil {
			return err
		}
		if fatal != nil && logCancel != nil {
			logCancel()
		}
		if fatal != nil && j.stopped() {
			return fatal
		}
		p, policyErr = policy.LoadWorker(options.PolicyPath)
		allowed := policyErr == nil && p.ExecuteAllowed()
		r := j.state.Current
		settled := r == nil || (r.Runtime != nil && j.stopped() && r.Runtime.StopReason == "" && r.Acked && r.Report.Event.State == "finalizing")
		if !allowed && notifiedDenied && settled {
			if policyErr != nil {
				return policyErr
			}
			return fmt.Errorf("capability denied: jobs.execute")
		}
		if fatal == nil && pending == nil && !time.Now().Before(nextSync) {
			request := connection.JobsRequest{Version: 1, WorkerGeneration: j.state.Generation, AgentVersion: options.Version, Capabilities: []string{"jobs.execute.v1"}, ExecuteAllowed: allowed, PreflightOK: false}
			if r := j.state.Current; r != nil && !r.Acked {
				report := r.Report
				request.Report = &report
			}
			sentAllowed = allowed
			result := make(chan syncResult, 1)
			pending = result
			calls.Add(1)
			go func() {
				defer calls.Done()
				response, err := client.SyncJobs(ctx, request)
				result <- syncResult{response, err}
			}()
		}
		if fatal == nil && allowed && logPending == nil && !time.Now().Before(nextLog) {
			record, logErr := j.nextLogRecord(lastLog)
			if logErr != nil {
				return logErr
			}
			if record != nil {
				lastLog = record.Assignment.AttemptID
				result := make(chan logResult, 1)
				logPending = result
				budget := 2 * time.Second
				if record.Logs != nil && record.Logs.Pending != nil {
					budget = 10 * time.Second
				}
				taskCtx, taskCancel := context.WithTimeout(ctx, budget)
				logCancel = taskCancel
				calls.Add(1)
				go func() {
					defer calls.Done()
					defer taskCancel()
					result <- runLogTask(taskCtx, docker, client, options, *record)
				}()
			} else {
				nextLog = time.Now().Add(5 * time.Second)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case result := <-logPending:
			logPending = nil
			logCancel = nil
			nextLog = time.Now().Add(time.Second)
			if result.err != nil {
				logBackoff = min(logBackoff*2, 60*time.Second)
				var rejected *connection.JobLogsError
				if errors.As(result.err, &rejected) {
					logBackoff = max(logBackoff, rejected.RetryAfter)
					if rejected.Status == 401 || rejected.Status == 403 {
						fatal = result.err
						if err = j.noteStop("AGENT_REVOKED"); err != nil {
							return err
						}
					}
				}
				nextLog = time.Now().Add(logBackoff)
			} else {
				logBackoff = time.Second
				if err = j.saveLogs(result); err != nil {
					return err
				}
			}
		case result := <-pending:
			pending = nil
			notifiedDenied = !sentAllowed
			nextSync = time.Now().Add(5 * time.Second)
			if result.err != nil {
				var delivery *connection.JobsError
				if !errors.As(result.err, &delivery) || delivery.Permanent() {
					fatal = result.err
					reason := "CONTROL_REJECTED"
					if delivery != nil && (delivery.Status == 401 || delivery.Status == 403) {
						reason = "AGENT_REVOKED"
					}
					if err = j.noteStop(reason); err != nil {
						return err
					}
				} else if !failed {
					fmt.Fprintln(log, "Worker sync unavailable; preserving local state")
					failed = true
				}
				continue
			}
			if failed {
				fmt.Fprintln(log, "Worker sync recovered")
				failed = false
			}
			if err = j.accept(result.response); err != nil {
				fatal = err
				if err = j.noteStop("CONTROL_REJECTED"); err != nil {
					return err
				}
				continue
			}
			if err = j.control(result.response.Assignment, result.response.Enabled); err != nil {
				fatal = err
				if err = j.noteStop("CONTROL_REJECTED"); err != nil {
					return err
				}
				continue
			}
			// Preparation may pull an image; an active execution only gets a bounded observation.
			budget := 2 * time.Second
			if r := j.state.Current; r != nil && r.Runtime != nil && r.Runtime.Phase == "preparing" {
				budget = 30 * time.Second
			}
			observeCtx, done := context.WithTimeout(ctx, budget)
			err = j.tick(observeCtx, docker, options, result.response.Assignment, result.response.Enabled)
			done()
			if err != nil {
				return err
			}
		}
	}
}

func workerClient(options Options) (*connection.Client, error) {
	config, err := connection.LoadConfig(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	token, err := connection.LoadToken(options.TokenPath)
	if err != nil {
		return nil, err
	}
	return connection.NewClient(config, token, options.Version)
}
