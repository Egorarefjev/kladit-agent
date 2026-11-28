package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

func privateDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assignmentFixture(generation string) connection.JobAssignment {
	a := connection.JobAssignment{JobID: "12345678-1234-4234-8234-123456789aaa", AttemptID: "12345678-1234-4234-8234-123456789bbb", WorkerGeneration: generation, Spec: json.RawMessage(`{"argv":["/bin/true"],"network":"none"}`), SpecHash: strings.Repeat("a", 64), AssignmentToken: strings.Repeat("b", 43), State: "assigned", PrepareDeadlineAt: time.Now().Add(10 * time.Minute)}
	canonical, err := canonicalSpec(a.Spec)
	if err != nil {
		panic(err)
	}
	a.SpecHash = fmt.Sprintf("%x", sha256.Sum256(canonical))
	return a
}

func TestJournalRestartLockACKAndTombstone(t *testing.T) {
	path := privateDir(t)
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	generation := j.state.Generation
	if _, err = openJournal(path); err == nil {
		t.Fatal("second supervisor acquired lock")
	}
	a := assignmentFixture(generation)
	if err = j.accept(connection.JobsResponse{Assignment: &a}); err != nil {
		t.Fatal(err)
	}
	report := j.state.Current.Report
	if report.Event.State != "preparing" {
		t.Fatal("assignment was not durably prepared")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j.state.Generation != generation || j.state.Current.Report != report {
		t.Fatal("journal lost generation/pending report")
	}
	changed := a
	changed.Spec = json.RawMessage(`{"argv":["/bin/evil"]}`)
	if j.accept(connection.JobsResponse{Assignment: &changed}) == nil {
		t.Fatal("mutated assignment accepted")
	}
	if j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: 2}}) == nil {
		t.Fatal("unknown ACK accepted")
	}
	if err = j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if j.state.Current == nil || !j.state.Current.Acked {
		t.Fatal("preparing ACK lost active state")
	}
	if err = j.emit("failed", "PREPARATION_FAILED", nil); err != nil {
		t.Fatal(err)
	}
	report = j.state.Current.Report
	if err = j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: 2}}); err != nil {
		t.Fatal(err)
	}
	if j.state.Current != nil {
		t.Fatal("terminal ACK did not close report")
	}
	// Backend replay, even after ACK, must reuse exactly the old terminal event.
	if err = j.accept(connection.JobsResponse{Assignment: &a}); err != nil {
		t.Fatal(err)
	}
	if j.state.Current.Report != report {
		t.Fatal("tombstone allowed a new lifecycle")
	}
}

func TestJournalRejectsLostUnsafeOrUnknownState(t *testing.T) {
	t.Run("lost", func(t *testing.T) {
		path := privateDir(t)
		j, err := openJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		j.Close()
		if err = os.Remove(filepath.Join(path, "state.json")); err != nil {
			t.Fatal(err)
		}
		if _, err = openJournal(path); err == nil {
			t.Fatal("lost state created a new generation")
		}
	})
	t.Run("symlink-root", func(t *testing.T) {
		path := filepath.Join(privateDir(t), "link")
		if err := os.Symlink(privateDir(t), path); err != nil {
			t.Fatal(err)
		}
		if _, err := openJournal(path); err == nil {
			t.Fatal("symlink state accepted")
		}
	})
	for _, mode := range []string{"symlink-file", "public-file", "unknown-state", "unknown-attempt"} {
		t.Run(mode, func(t *testing.T) {
			path := privateDir(t)
			j, err := openJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			j.Close()
			state := filepath.Join(path, "state.json")
			switch mode {
			case "symlink-file":
				os.Remove(state)
				if err = os.Symlink(filepath.Join(privateDir(t), "missing"), state); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err = os.Chmod(state, 0644); err != nil {
					t.Fatal(err)
				}
			case "unknown-attempt":
				a := assignmentFixture(j.state.Generation)
				a.State = "running"
				j.state.Current = &record{Assignment: a, Report: connection.JobReport{AttemptID: a.AttemptID, SpecHash: a.SpecHash, AssignmentToken: a.AssignmentToken, Event: connection.JobEvent{Seq: 1, State: "failed", Reason: "RUNNER_NOT_IMPLEMENTED", FinishedAt: time.Now().UTC().Format(time.RFC3339Nano)}}}
				data, encodeErr := json.Marshal(j.state)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if err = os.WriteFile(state, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-state":
				if err = os.WriteFile(state, []byte(`{"version":99}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = openJournal(path); err == nil {
				t.Fatal("unsafe journal accepted")
			}
		})
	}
}

func fakeDocker(t *testing.T, info map[string]any) string {
	t.Helper()
	socket := filepath.Join(privateDir(t), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/info" {
			t.Error("preflight attempted a mutating Docker operation")
		}
		json.NewEncoder(w).Encode(info)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket
}

func engineInfo() map[string]any {
	return map[string]any{"OSType": "linux", "Architecture": runtime.GOARCH, "CgroupVersion": "2", "MemoryLimit": true, "SwapLimit": true, "CpuCfsQuota": true, "PidsLimit": true}
}

func TestPreflightRejectsMissingRuntimeAndUnsupportedLimits(t *testing.T) {
	p := policy.Worker{MinFreeMemoryBytes: 1, MinFreeDiskBytes: 1}
	state := filepath.Join(privateDir(t), "state")
	if preflight(context.Background(), p, state, "/missing/docker.sock") == nil {
		t.Fatal("missing runtime accepted")
	}
	for _, test := range []struct {
		field string
		value any
		valid bool
	}{
		{"CgroupVersion", "2", true}, {"MemoryLimit", false, false}, {"SwapLimit", false, false}, {"SecurityOptions", []string{"name=rootless"}, false},
	} {
		info := engineInfo()
		info[test.field] = test.value
		err := preflight(context.Background(), p, state, fakeDocker(t, info))
		if (err == nil) != test.valid {
			t.Fatalf("unexpected preflight for %s: %v", test.field, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if preflight(ctx, p, state, fakeDocker(t, engineInfo())) == nil {
		t.Fatal("cancelled preflight accepted")
	}
}

func TestPolicyDenyDoesNotCreateState(t *testing.T) {
	root := privateDir(t)
	path := filepath.Join(root, "policy.json")
	p := policy.Worker{Version: 1, Capabilities: map[string]bool{"jobs.execute": false}, Limits: policy.WorkerLimits{CPUMillis: 1, MemoryBytes: 1, TimeoutSeconds: 1}, MinFreeDiskBytes: 1, MinFreeMemoryBytes: 1}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if Run(context.Background(), Options{PolicyPath: path, StatePath: state}) == nil {
		t.Fatal("denied policy started worker")
	}
	if _, err = os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied worker touched its journal")
	}
}

func TestSupervisorRechecksPermissionAndReleasesLock(t *testing.T) {
	root := privateDir(t)
	profile := filepath.Join(root, "policy.json")
	p := policy.Worker{Version: 1, Capabilities: map[string]bool{"jobs.execute": true}, Limits: policy.WorkerLimits{CPUMillis: 1, MemoryBytes: 1, TimeoutSeconds: 1}, MinFreeDiskBytes: 1, MinFreeMemoryBytes: 1}
	writePolicy := func() {
		data, err := json.Marshal(p)
		if err != nil {
			t.Error(err)
		}
		if err = os.WriteFile(profile, data, 0600); err != nil {
			t.Error(err)
		}
	}
	writePolicy()
	state := filepath.Join(root, "state")
	calls := make(chan connection.JobsRequest, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request connection.JobsRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid sync")
			return
		}
		// Generation must be durable before the very first outgoing request.
		data, err := os.ReadFile(filepath.Join(state, "state.json"))
		if err != nil {
			t.Error(err)
		}
		var saved journalState
		if json.Unmarshal(data, &saved) != nil || saved.Generation != request.WorkerGeneration {
			t.Error("non-durable generation")
		}
		if request.PreflightOK {
			t.Error("foundation advertised execution readiness")
		}
		calls <- request
		if request.ExecuteAllowed {
			p.Capabilities["jobs.execute"] = false
			writePolicy()
		}
		json.NewEncoder(w).Encode(connection.JobsResponse{Version: 1, WorkerGeneration: request.WorkerGeneration})
	}))
	defer server.Close()
	cert := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", cert)
	config := filepath.Join(root, "connection.json")
	token := filepath.Join(root, "token")
	if err := os.WriteFile(config, []byte(`{"endpoint":"`+server.URL+`/api/v1/agent/metrics"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte(strings.Repeat("a", 43)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{PolicyPath: profile, ConfigPath: config, TokenPath: token, StatePath: state, DockerSocket: "/missing/docker.sock", Version: "test"})
	}()
	for _, allowed := range []bool{true, false} {
		select {
		case request := <-calls:
			if request.ExecuteAllowed != allowed {
				t.Fatal("local policy was not rechecked")
			}
		case <-time.After(8 * time.Second):
			t.Fatal("worker sync timed out")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
	j, err := openJournal(state)
	if err != nil {
		t.Fatal("lock was not released", err)
	}
	j.Close()
}

func TestAssignmentSpecComparison(t *testing.T) {
	a := assignmentFixture("12345678-1234-4234-8234-123456789abc")
	a.Spec = json.RawMessage(`{"count":9007199254740993,"argv":["/bin/true"]}`)
	for _, test := range []struct {
		name string
		spec string
		same bool
	}{
		{"key-order", `{"argv":["/bin/true"],"count":9007199254740993}`, true},
		{"large-integer", `{"count":9007199254740992,"argv":["/bin/true"]}`, false},
		{"changed-argv", `{"count":9007199254740993,"argv":["/bin/false"]}`, false},
		{"invalid-json", `{"count":`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := a
			b.Spec = json.RawMessage(test.spec)
			if sameAssignment(a, b) != test.same || sameAssignment(b, a) != test.same {
				t.Fatal("incorrect assignment comparison")
			}
		})
	}
}
