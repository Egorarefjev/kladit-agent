package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalBuildVersion(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &stderr); code != 0 || out.String() != "kladit-agent dev\n" || stderr.Len() != 0 {
		t.Fatalf("local build must identify itself as dev: %d %q %q", code, out.String(), stderr.String())
	}
}

func TestLinkedVersionReachesCLIAndTelemetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "kladit-agent")
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X main.version=1.2.3", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	out, err := exec.CommandContext(ctx, binary, "version").CombinedOutput()
	if err != nil || string(out) != "kladit-agent 1.2.3\n" {
		t.Fatalf("linked CLI version: %v %s", err, out)
	}
	var received bool
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AgentVersion string `json:"agent_version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.AgentVersion != "1.2.3" || r.Header.Get("User-Agent") != "kladit-agent/1.2.3" {
			t.Errorf("linked version missing in telemetry: %q %q", body.AgentVersion, r.Header.Get("User-Agent"))
		}
		received = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	for name, data := range map[string][]byte{
		"policy": []byte(`{"version":1,"capabilities":{"metrics.read":true}}`),
		"config": []byte(`{"endpoint":"` + receiver.URL + `/metrics"}`),
		"token":  []byte(strings.Repeat("a", 43)),
		"ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	send := exec.CommandContext(ctx, binary, "send", "--policy", filepath.Join(dir, "policy"), "--config", filepath.Join(dir, "config"), "--token-file", filepath.Join(dir, "token"))
	send.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(dir, "ca.pem"))
	if out, err := send.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("send: %v %s", err, out)
	}
	receiver.Close()
	if !received {
		t.Fatal("linked binary did not send metrics")
	}
}
