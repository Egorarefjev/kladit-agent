package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
)

func activeFixture(t *testing.T) (*journal, Options, connection.JobAssignment, *dockerClient, *fakeEngine) {
	t.Helper()
	d, socket, f := fakeRunnerDocker(t)
	j, options, a := runnerFixture(t, "registry.example/jobs@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	options.DockerSocket = socket
	for i := 0; i < 2; i++ {
		if err := j.tick(context.Background(), d, options, &a, true); err != nil {
			t.Fatal(err)
		}
	}
	if j.state.Current.Report.Event.State != "running" {
		t.Fatal("fixture did not start")
	}
	acknowledge(t, j)
	return j, options, a, d, f
}
func acknowledge(t *testing.T, j *journal) {
	t.Helper()
	r := j.state.Current
	if err := j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: r.Assignment.AttemptID, Seq: r.Report.Event.Seq}}); err != nil {
		t.Fatal(err)
	}
}
func poll(t *testing.T, j *journal, d *dockerClient, options Options) {
	t.Helper()
	if err := j.tick(context.Background(), d, options, nil, false); err != nil {
		t.Fatal(err)
	}
}
func seedStart(t *testing.T, j *journal, f *fakeEngine, at time.Time) {
	t.Helper()
	started := at.UTC().Format(time.RFC3339Nano)
	r := j.state.Current
	f.mu.Lock()
	f.container.State.StartedAt = started
	f.mu.Unlock()
	r.Runtime.StartedAt = started
	r.Runtime.Deadline = at.Add(time.Duration(r.Runtime.TimeoutSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	r.Report.Event.StartedAt = started
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
}

func TestStopPreservesPendingReportAndFirstReasonAcrossRestart(t *testing.T) {
	j, options, a, d, f := activeFixture(t)
	f.mu.Lock()
	f.fault = "ignore-term"
	f.mu.Unlock()
	seedStart(t, j, f, time.Now().Add(-28*time.Second))
	// Stop must not wait for the running report's ACK.
	j.state.Current.Acked = false
	report := j.state.Current.Report
	a.CancelRequested = true
	if err := j.control(&a, true); err != nil {
		t.Fatal(err)
	}
	poll(t, j, d, options)
	if j.state.Current.Report != report || j.state.Current.Runtime.TermSentAt == "" {
		t.Fatal("stop waited for ACK or changed pending event")
	}
	first := j.state.Current.Runtime.StopReason
	j.state.Current.Runtime.DesiredStop = "TIMEOUT"
	j.state.Current.Runtime.TermSentAt = time.Now().Add(-11 * time.Second).UTC().Format(time.RFC3339Nano)
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	j.Close()
	var err error
	j, err = openJournal(options.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	poll(t, j, d, options)
	poll(t, j, d, options)
	if j.state.Current.Report != report {
		t.Fatal("pending report changed across restart")
	}
	if j.state.Current.Runtime.StopReason != first {
		t.Fatal("timeout overwrote earlier cancel")
	}
	f.mu.Lock()
	terms, kills, starts := f.terms, f.kills, f.starts
	f.mu.Unlock()
	if terms != 1 || kills != 1 || starts != 1 {
		t.Fatalf("wrong stop lifecycle: TERM=%d KILL=%d start=%d", terms, kills, starts)
	}
	acknowledge(t, j)
	poll(t, j, d, options)
	if j.state.Current.Report.Event.State != "finalizing" {
		t.Fatal("exit evidence skipped finalizing")
	}
	acknowledge(t, j)
	poll(t, j, d, options)
	if j.state.Current.Report.Event.State != "cancelled" {
		t.Fatal("confirmed stop did not cancel")
	}
}

func TestPolicyStopIntentSurvivesUnavailableDocker(t *testing.T) {
	j, options, _ := runnerFixture(t, "registry.example/jobs@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	defer j.Close()
	if err := os.WriteFile(options.PolicyPath, []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	d := newDocker("/missing/docker.sock")
	defer d.close()
	poll(t, j, d, options)
	if j.state.Current.Runtime.DesiredStop != "EXECUTION_DISABLED" || j.stopped() {
		t.Fatal("lost policy stop or falsely confirmed exit")
	}
	// Backend disable remains authoritative even if no assignment is repeated.
	j.state.Current.Runtime.DesiredStop = ""
	if err := j.control(nil, false); err != nil {
		t.Fatal(err)
	}
	if j.state.Current.Runtime.DesiredStop != "EXECUTION_DISABLED" {
		t.Fatal("ignored disable without assignment")
	}
}

func TestTimeoutAndPreparationStopWithoutAPI(t *testing.T) {
	j, options, _, d, f := activeFixture(t)
	defer j.Close()
	seedStart(t, j, f, time.Now().Add(-31*time.Second))
	poll(t, j, d, options)
	poll(t, j, d, options)
	if j.state.Current.Runtime.StopReason != "TIMEOUT" {
		t.Fatal("local deadline ignored")
	}
	acknowledge(t, j)
	poll(t, j, d, options)
	if j.state.Current.Report.Event.State != "timed_out" {
		t.Fatal("timeout was not confirmed after exit")
	}
	j.Close()
	j, options, _ = runnerFixture(t, "registry.example/jobs@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	defer j.Close()
	// The fixture socket below gives a proved 404, rather than a failed inspect.
	fresh, socket, _ := fakeRunnerDocker(t)
	options.DockerSocket = socket
	j.state.Current.Assignment.PrepareDeadlineAt = time.Now().Add(-time.Minute)
	poll(t, j, fresh, options)
	if j.state.Current.Report.Event.State != "timed_out" || j.state.Current.Runtime.Phase != "stopped_before_start" {
		t.Fatal("preparation timeout did not close safely")
	}
}

func TestNaturalExitWinsLateCancelAndMissingContainerStaysUnknown(t *testing.T) {
	j, options, a, d, f := activeFixture(t)
	defer j.Close()
	f.mu.Lock()
	f.container.State.Status = "exited"
	f.container.State.Running = false
	f.container.State.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	f.mu.Unlock()
	a.CancelRequested = true
	if err := j.control(&a, true); err != nil {
		t.Fatal(err)
	}
	poll(t, j, d, options)
	if j.state.Current.Runtime.StopReason != "" || j.state.Current.Report.Event.State != "finalizing" {
		t.Fatal("late cancel rewrote natural exit")
	}
	f.mu.Lock()
	signals := f.terms + f.kills
	f.mu.Unlock()
	if signals != 0 {
		t.Fatal("signalled an exited container")
	}
	j.Close()
	j, options, a, d, f = activeFixture(t)
	defer j.Close()
	f.mu.Lock()
	f.container = nil
	f.mu.Unlock()
	a.CancelRequested = true
	if err := j.control(&a, true); err != nil {
		t.Fatal(err)
	}
	poll(t, j, d, options)
	if j.state.Current.Report.Event.State != "unknown" || j.stopped() {
		t.Fatal("absence falsely confirmed stop")
	}
}

func TestSupervisorStopsDuringBlockedSyncAndOnRevocation(t *testing.T) {
	for _, mode := range []string{"blocked-sync", "revoked", "invalid-policy", "blocked-logs", "logs-revoked"} {
		t.Run(mode, func(t *testing.T) {
			j, options, _, _, f := activeFixture(t)
			if mode == "blocked-sync" || mode == "blocked-logs" {
				seedStart(t, j, f, time.Now().Add(-28*time.Second))
			}
			if mode == "blocked-logs" || mode == "logs-revoked" {
				j.state.Current.Logs = &logState{Pending: &connection.JobLogChunk{Seq: 1, Stream: "stdout", Data: []byte("pending")}, Bytes: 7}
				if err := j.save(); err != nil {
					t.Fatal(err)
				}
			}
			generation := j.state.Generation
			j.Close()
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (mode == "blocked-logs" || mode == "logs-revoked") && r.URL.Path != "/api/v1/agent/jobs/logs" {
					json.NewEncoder(w).Encode(connection.JobsResponse{Version: 1, WorkerGeneration: generation, Enabled: true})
					return
				}
				once.Do(func() { close(entered) })
				if mode == "revoked" || mode == "logs-revoked" {
					w.WriteHeader(401)
					return
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				json.NewEncoder(w).Encode(connection.JobsResponse{Version: 1, WorkerGeneration: generation, Enabled: true})
			}))
			defer server.Close()
			defer close(release)
			options.ConfigPath, options.TokenPath = connectionFixture(t, filepath.Dir(options.StatePath), server)
			if mode == "invalid-policy" {
				if err := os.WriteFile(options.PolicyPath, []byte(`{"version":99}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- Run(ctx, options) }()
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("sync not started")
			}
			select {
			case signal := <-f.signals:
				if signal != "TERM" {
					t.Fatal("unexpected signal")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("API blocked local stop")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("supervisor did not drain")
			}
			saved, err := openJournal(options.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			want := map[string]string{"blocked-sync": "TIMEOUT", "revoked": "AGENT_REVOKED", "invalid-policy": "EXECUTION_DISABLED", "blocked-logs": "TIMEOUT", "logs-revoked": "AGENT_REVOKED"}[mode]
			if saved.state.Current.Runtime.StopReason != want {
				t.Fatal("stop intent was not durable")
			}
		})
	}
}

func TestDockerNativeStop(t *testing.T) {
	image := os.Getenv("KLADIT_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("native Docker integration is run explicitly in CI")
	}
	j, options, a := runnerFixture(t, image, "/bin/sh", "-c", "trap '' TERM; echo ready >/output/ready; while :; do sleep 1; done")
	defer j.Close()
	options.DockerSocket = "/run/docker.sock"
	d := newDocker(options.DockerSocket)
	defer d.close()
	t.Cleanup(func() {
		if c, err := d.inspect(context.Background(), containerName(j.state.Current)); err == nil && ownsContainer(j.state.Current, c) {
			_ = d.request(context.Background(), "POST", "/containers/"+c.ID+"/stop?t=1", nil, nil)
			_ = d.request(context.Background(), "DELETE", "/containers/"+c.ID, nil, nil)
		}
	})
	for i := 0; i < 30; i++ {
		if err := j.tick(context.Background(), d, options, &a, true); err != nil {
			t.Fatal(err)
		}
		if j.state.Current.Report.Event.State == "running" {
			acknowledge(t, j)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	ready := filepath.Join(options.StatePath, "jobs", a.JobID, a.AttemptID, "output", "ready")
	readyUntil := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(readyUntil) {
			t.Fatal("native workload not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.CancelRequested = true
	if err := j.control(&a, true); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(16 * time.Second)
	for time.Now().Before(until) {
		poll(t, j, d, options)
		r := j.state.Current
		if r.Report.Event.State == "cancelled" {
			if r.Runtime.Exit == nil || r.Runtime.Exit.ExitCode != 137 {
				t.Fatal("native KILL did not confirm exit")
			}
			return
		}
		if !r.Acked {
			acknowledge(t, j)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native TERM/KILL stop did not complete")
}
