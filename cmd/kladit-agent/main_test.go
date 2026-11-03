package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandErrors(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"metrics", "extra"}, {"version", "extra"}, {"metrics", "--config", "unused"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, &out, &stderr); code != 2 || out.Len() != 0 {
			t.Fatalf("unexpected command result for %v", args)
		}
	}
}

func TestSendRequiresLocalPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"capabilities":{"metrics.read":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"send", "--policy", path, "--config", "missing", "--token-file", "missing"}, &out, &stderr)
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "capability denied") {
		t.Fatal("send must check policy before config, secrets or collection")
	}
}

func TestSendRechecksPermission(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	configPath := filepath.Join(dir, "connection.json")
	tokenPath := filepath.Join(dir, "token")
	for path, content := range map[string]string{
		policyPath: `{"version":1,"capabilities":{"metrics.read":true}}`,
		configPath: `{"endpoint":"https://127.0.0.1:1/metrics"}`,
		tokenPath:  strings.Repeat("a", 43),
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	changed := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		changed <- os.WriteFile(policyPath, []byte(`{"version":1}`), 0600)
	}()
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"send", "--policy", policyPath, "--config", configPath, "--token-file", tokenPath}, &out, &stderr)
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "capability denied") {
		t.Fatal("revoked permission must prevent network delivery:", stderr.String())
	}
}
