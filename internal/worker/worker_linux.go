package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

// Run owns one synchronous supervisor under an exclusive lock. Execution readiness remains disabled until the lifecycle/results steps.
func Run(ctx context.Context, options Options) error {
	p, err := policy.LoadWorker(options.PolicyPath)
	if err != nil {
		return err
	}
	if !p.ExecuteAllowed() {
		// Existing state may still need control/ACK delivery after permission is revoked.
		if _, err := os.Lstat(filepath.Join(options.StatePath, "state.json")); err != nil {
			return fmt.Errorf("capability denied: jobs.execute")
		}
	}
	config, err := connection.LoadConfig(options.ConfigPath)
	if err != nil {
		return err
	}
	token, err := connection.LoadToken(options.TokenPath)
	if err != nil {
		return err
	}
	client, err := connection.NewClient(config, token, options.Version)
	if err != nil {
		return err
	}
	defer client.Close()
	log := options.Log
	if log == nil {
		log = io.Discard
	}
	if options.Check {
		if err = preflight(ctx, p, options.StatePath, options.DockerSocket); err != nil {
			return err
		}
		_, err = fmt.Fprintln(log, "Worker prerequisites passed; execution readiness remains disabled")
		return err
	}
	j, err := openJournal(options.StatePath)
	if err != nil {
		return err
	}
	defer j.Close()
	docker := newDocker(options.DockerSocket)
	defer docker.close()
	fmt.Fprintln(log, "Worker runner active; execution readiness remains disabled")
	failed := false
	wasHealthy := true
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err = j.tick(ctx, docker, options, nil, false); err != nil {
			return err
		}
		p, err = policy.LoadWorker(options.PolicyPath)
		if err != nil {
			return err
		}
		// Read-only preflight is evaluated locally, but this build never advertises execution readiness.
		healthy := false
		if p.ExecuteAllowed() {
			healthy = preflight(ctx, p, options.StatePath, options.DockerSocket) == nil
		}
		request := connection.JobsRequest{Version: 1, WorkerGeneration: j.state.Generation, AgentVersion: options.Version, Capabilities: []string{"jobs.execute.v1"}, ExecuteAllowed: p.ExecuteAllowed(), PreflightOK: false}
		if j.state.Current != nil {
			request.Report = &j.state.Current.Report
		}
		response, syncErr := client.SyncJobs(ctx, request)
		if syncErr == nil {
			if err = j.accept(response); err != nil {
				return err
			}
			if err = j.tick(ctx, docker, options, response.Assignment, response.Enabled); err != nil {
				return err
			}
			if failed {
				fmt.Fprintln(log, "Worker sync recovered")
				failed = false
			}
			if !healthy && wasHealthy {
				fmt.Fprintln(log, "Worker runtime preflight unavailable; execution remains disabled")
			}
			wasHealthy = healthy
		} else {
			if ctx.Err() != nil {
				return nil
			}
			var delivery *connection.JobsError
			if !errors.As(syncErr, &delivery) || delivery.Permanent() {
				return syncErr
			}
			if !failed {
				fmt.Fprintln(log, "Worker sync unavailable; preserving local state")
				failed = true
			}
		}
		if !p.ExecuteAllowed() {
			return fmt.Errorf("capability denied: jobs.execute")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
