package connection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	config, err := LoadConfig(writeFile(t, `{"endpoint":"https://example.test/api/v1/agent/metrics"}`, 0600))
	if err != nil || config.Endpoint != "https://example.test/api/v1/agent/metrics" {
		t.Fatal("valid config rejected", err)
	}
	for _, input := range []string{
		``, `null`, `{}`, `{"endpoint":"http://localhost/metrics"}`,
		`{"endpoint":"https://user:password@example.test/metrics"}`,
		`{"endpoint":"https://example.test/metrics?token=secret"}`,
		`{"endpoint":"https://example.test/metrics#secret"}`,
		`{"endpoint":"https://example.test/metrics","token":"secret"}`,
		`{"endpoint":"https://example.test/metrics"} {}`,
		`{"endpoint":"https://example.test/metrics"}` + strings.Repeat(" ", 65536),
	} {
		if _, err := LoadConfig(writeFile(t, input, 0600)); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestLoadToken(t *testing.T) {
	path := writeFile(t, testToken+"\n", 0600)
	if token, err := LoadToken(path); err != nil || token != testToken {
		t.Fatal("valid token rejected", err)
	}
	for _, input := range []string{"", "short", strings.Repeat("a", 4097), testToken + "\r\nInjected: value"} {
		if _, err := LoadToken(writeFile(t, input, 0600)); err == nil || strings.Contains(err.Error(), input) && len(input) > 0 {
			t.Fatal("invalid token must be rejected without being logged")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Fatal("world-readable token accepted")
	}
	if _, err := LoadToken(t.TempDir()); err == nil {
		t.Fatal("directory accepted as token")
	}
}
