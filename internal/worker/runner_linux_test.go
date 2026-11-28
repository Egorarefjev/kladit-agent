package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

func runnerAssignment(t *testing.T, generation, image string) connection.JobAssignment {
	t.Helper()
	a := assignmentFixture(generation)
	s := jobSpec{ServerID: a.JobID, Image: image, Argv: []string{"/bin/sh", "-c", "echo done > /output/done"}, CPUMillis: 100, MemoryBytes: 32 << 20, TimeoutSeconds: 30, Env: map[string]string{}, SecretRefs: []string{}, Network: "none"}
	s.ResultDestination.ServerID = s.ServerID
	s.ResultDestination.StorageID = "default"
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	a.Spec = data
	canonical, err := canonicalSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	a.SpecHash = fmt.Sprintf("%x", sha256.Sum256(canonical))
	return a
}
func runnerFixture(t *testing.T, image string) (*journal, Options, connection.JobAssignment) {
	t.Helper()
	path := privateDir(t)
	options := Options{StatePath: path, PolicyPath: filepath.Join(path, "policy.json")}
	p := policy.Worker{Version: 1, Capabilities: map[string]bool{"jobs.execute": true}, Limits: policy.WorkerLimits{CPUMillis: 1000, MemoryBytes: 1 << 30, TimeoutSeconds: 3600}, MinFreeDiskBytes: 1, MinFreeMemoryBytes: 1}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(options.PolicyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	a := runnerAssignment(t, j.state.Generation, image)
	if err = j.accept(connection.JobsResponse{Assignment: &a}); err != nil {
		t.Fatal(err)
	}
	if err = j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	permit := time.Now().Add(20 * time.Second)
	a.StartPermitExpiresAt = &permit
	a.State = "preparing"
	a.AcknowledgedSeq = 1
	return j, options, a
}

type fakeEngine struct {
	mu              sync.Mutex
	container       *containerInfo
	creates, starts int
	fault           string
	config          containerConfig
}

func fakeRunnerDocker(t *testing.T) (*dockerClient, string, *fakeEngine) {
	t.Helper()
	f := &fakeEngine{}
	socket := filepath.Join(privateDir(t), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/info":
			json.NewEncoder(w).Encode(engineInfo())
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/images/"):
			json.NewEncoder(w).Encode(imageInfo{ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", Architecture: runtime.GOARCH})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/containers/"):
			if f.container == nil {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(f.container)
		case r.Method == "POST" && r.URL.Path == "/containers/create":
			f.creates++
			if f.container != nil {
				w.WriteHeader(409)
				return
			}
			if json.NewDecoder(r.Body).Decode(&f.config) != nil {
				t.Error("invalid config")
			}
			c := &containerInfo{ID: strings.Repeat("b", 64), Image: f.config.Image}
			c.Config.Labels = f.config.Labels
			c.Config.User = f.config.User
			c.Config.WorkingDir = f.config.WorkingDir
			c.Config.Entrypoint = f.config.Entrypoint
			c.Config.Cmd = f.config.Cmd
			c.Config.Env = f.config.Env
			c.HostConfig = f.config.HostConfig
			c.State.Status = "created"
			f.container = c
			if f.fault == "create-response" {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"Id": c.ID})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/start"):
			f.starts++
			f.container.State.Status = "running"
			f.container.State.Running = true
			f.container.State.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if f.fault == "start-response" {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected Docker operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})}
	go server.Serve(listener)
	d := newDocker(socket)
	t.Cleanup(func() { d.close(); server.Close() })
	return d, socket, f
}

func TestRunnerCreateStartFaultsAndRestart(t *testing.T) {
	for _, fault := range []string{"none", "create-response", "start-response", "before-start", "missing-after-intent", "foreign-container"} {
		t.Run(fault, func(t *testing.T) {
			d, socket, f := fakeRunnerDocker(t)
			j, options, a := runnerFixture(t, "registry.example/jobs@sha256:"+strings.Repeat("a", 64))
			options.DockerSocket = socket
			f.mu.Lock()
			f.fault = fault
			if fault == "before-start" || fault == "missing-after-intent" || fault == "foreign-container" {
				r := j.state.Current
				r.Runtime.ImageID = "sha256:" + strings.Repeat("a", 64)
				r.Runtime.ContainerID = strings.Repeat("b", 64)
				r.Runtime.TimeoutSeconds = 30
				r.Runtime.Phase = "start_intent"
				c := &containerInfo{ID: r.Runtime.ContainerID, Image: r.Runtime.ImageID}
				config := buildContainer(r, jobSpec{Argv: []string{"/bin/sh", "-c", "echo done > /output/done"}, CPUMillis: 100, MemoryBytes: 32 << 20, Network: "none"}, filepath.Join(options.StatePath, "jobs", a.JobID, a.AttemptID, "output"))
				c.Config.Labels = containerLabels(r)
				c.Config.User = config.User
				c.Config.WorkingDir = config.WorkingDir
				c.Config.Entrypoint = config.Entrypoint
				c.Config.Cmd = config.Cmd
				c.Config.Env = config.Env
				c.HostConfig = config.HostConfig
				c.State.Status = "created"
				f.container = c
				if fault == "missing-after-intent" {
					f.container = nil
				}
				if fault == "foreign-container" {
					c.Config.Labels = map[string]string{}
				}
				if err := j.save(); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Unlock()
			for i := 0; i < 4; i++ {
				if err := j.tick(context.Background(), d, options, &a, true); err != nil {
					t.Fatal(err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				j, err = openJournal(options.StatePath)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer j.Close()
			f.mu.Lock()
			if fault == "before-start" || fault == "missing-after-intent" || fault == "foreign-container" {
				if f.starts != 0 || j.state.Current.Report.Event.State != "unknown" {
					f.mu.Unlock()
					t.Fatal("ambiguous start was retried")
				}
				f.mu.Unlock()
				return
			}
			creates, starts := f.creates, f.starts
			h := f.config.HostConfig
			f.mu.Unlock()
			if creates != 1 || starts != 1 || j.state.Current.Report.Event.State != "running" {
				t.Fatalf("duplicate/lost start: creates=%d starts=%d state=%s", creates, starts, j.state.Current.Report.Event.State)
			}
			if h.RestartPolicy.Name != "no" || h.AutoRemove || h.Privileged || !h.ReadonlyRootfs || h.MemorySwap != h.Memory || h.PidsLimit != 128 || h.CPUQuota != 10000 || h.NetworkMode != "none" || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || len(h.Mounts) != 1 || h.Mounts[0].Target != "/output" {
				t.Fatal("unsafe container profile")
			}
			report := j.state.Current.Report
			if err := j.tick(context.Background(), d, options, &a, true); err != nil || j.state.Current.Report != report {
				t.Fatal("unacknowledged event changed")
			}
			if err := j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: report.Event.Seq}}); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.container.State.Status = "exited"
			f.container.State.Running = false
			f.mu.Unlock()
			if err := j.tick(context.Background(), d, options, &a, true); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			starts = f.starts
			f.mu.Unlock()
			if j.state.Current.Report.Event.State != "finalizing" || starts != 1 {
				t.Fatal("exited container restarted or reported success before results")
			}
		})
	}
}

func TestRunnerRejectsSpecAndRuntimeOverrides(t *testing.T) {
	p := policy.Worker{Limits: policy.WorkerLimits{CPUMillis: 1000, MemoryBytes: 1 << 30, TimeoutSeconds: 3600}}
	a := runnerAssignment(t, "12345678-1234-4234-8234-123456789abc", "registry.example/jobs@sha256:"+strings.Repeat("a", 64))
	if _, err := parseSpec(a, p); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []struct{ old, new string }{
		{`"network":"none"`, `"network":"host"`}, {`"env":{}`, `"env":{"NVIDIA_VISIBLE_DEVICES":"all"}`}, {`"input_id":null`, `"input_id":"arbitrary"`}, {`"secret_refs":[]`, `"secret_refs":["machine-token"]`},
	} {
		bad := a
		bad.Spec = json.RawMessage(strings.Replace(string(a.Spec), replacement.old, replacement.new, 1))
		canonical, _ := canonicalSpec(bad.Spec)
		bad.SpecHash = fmt.Sprintf("%x", sha256.Sum256(canonical))
		if _, err := parseSpec(bad, p); err == nil {
			t.Fatal("unsafe spec accepted")
		}
	}
	a.SpecHash = strings.Repeat("f", 64)
	if _, err := parseSpec(a, p); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}

func TestDockerNativeRunner(t *testing.T) {
	image := os.Getenv("KLADIT_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("native Docker integration is run explicitly in CI")
	}
	j, options, a := runnerFixture(t, image)
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
	for i := 0; i < 40; i++ {
		if err := j.tick(context.Background(), d, options, &a, true); err != nil {
			t.Fatal(err)
		}
		r := j.state.Current
		if r.Report.Event.State == "unknown" || r.Report.Event.State == "failed" {
			c, inspectErr := d.inspect(context.Background(), containerName(r))
			if inspectErr == nil {
				t.Fatalf("native execution failed %s %s: Docker status=%s error=%s", r.Report.Event.State, r.Report.Event.Reason, c.State.Status, c.State.Error)
			}
			t.Fatal("native execution failed", r.Report.Event.State, r.Report.Event.Reason, inspectErr)
		}
		if r.Report.Event.State == "finalizing" {
			if r.Report.Event.ExitCode == nil || *r.Report.Event.ExitCode != 0 {
				t.Fatal("native exit failed")
			}
			data, err := os.ReadFile(filepath.Join(options.StatePath, "jobs", a.JobID, a.AttemptID, "output", "done"))
			if err != nil || string(data) != "done\n" {
				t.Fatal("native output missing", err)
			}
			return
		}
		if !r.Acked {
			if err := j.accept(connection.JobsResponse{ACK: &connection.JobACK{AttemptID: a.AttemptID, Seq: r.Report.Event.Seq}}); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("native runner did not reach finalizing")
}

func TestCanonicalSpecMatchesBackend(t *testing.T) {
	// Golden produced by Kladit job-spec.js canonicalJson/jobHash, including JSON
	// characters Go's default encoder escapes differently from JSON.stringify.
	raw := []byte("{\"z\":\"<>&\u2028\u2029\\\\u2028\",\"env\":{\"A\":\"quote\\\"\\\\\\n\",\"B\":\"é😀\"},\"n\":9007199254740991}")
	canonical, err := canonicalSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(canonical)) != "9327126faf6e8ab4c6108d0419267d3806cb9fc35f0f723e664e1c5d7d94d630" {
		t.Fatal("backend hash differs")
	}
}

func TestRunnerRequiresFreshStartPermission(t *testing.T) {
	for _, mode := range []string{"missing", "expired", "cancelled", "owner-disabled", "unknown", "unacknowledged"} {
		t.Run(mode, func(t *testing.T) {
			d, socket, engine := fakeRunnerDocker(t)
			j, options, a := runnerFixture(t, "registry.example/jobs@sha256:"+strings.Repeat("a", 64))
			defer j.Close()
			options.DockerSocket = socket
			control := &a
			enabled := true
			switch mode {
			case "missing":
				control = nil
			case "expired":
				past := time.Now().Add(-time.Second)
				a.StartPermitExpiresAt = &past
			case "cancelled":
				a.CancelRequested = true
			case "owner-disabled":
				enabled = false
			case "unknown":
				a.State = "unknown"
			case "unacknowledged":
				j.state.Current.Acked = false
			}
			if err := j.tick(context.Background(), d, options, control, enabled); err != nil {
				t.Fatal(err)
			}
			engine.mu.Lock()
			defer engine.mu.Unlock()
			if engine.creates != 0 || engine.starts != 0 {
				t.Fatal("started without permission")
			}
		})
	}
}
