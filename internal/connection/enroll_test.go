package connection

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollment(t *testing.T) {
	// System root pools are process-global; isolate test CA trust from TLS rejection tests.
	if os.Getenv("KLADIT_ENROLL_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEnrollment$")
		cmd.Env = append(os.Environ(), "KLADIT_ENROLL_TEST=1", "GODEBUG=x509usefallbackroots=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		return
	}
	token := strings.Repeat("a", 43)
	var endpoint string
	status := 201
	body := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authorization")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, endpoint, http.StatusFound)
			return
		}
		w.WriteHeader(status)
		if body != "" {
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprintf(w, `{"version":1,"machine_token":%q,"metrics_url":%q}`, strings.Repeat("b", 43), endpoint+"/api/v1/agent/metrics")
	}))
	defer server.Close()
	endpoint = server.URL
	// Use the real system trust configuration, never InsecureSkipVerify.
	cert, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", trust)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	x509.SetFallbackRoots(roots)
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	dir := t.TempDir()
	if err := Enroll(context.Background(), endpoint, strings.NewReader(token), dir, "test"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, "token"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("token permissions")
	}
	stored, _ := LoadToken(filepath.Join(dir, "token"))
	if stored != strings.Repeat("b", 43) {
		t.Fatal("token mismatch")
	}
	if err := Enroll(context.Background(), endpoint, strings.NewReader(token), dir, "test"); err == nil {
		t.Fatal("overwrites credentials")
	}
	stored, _ = LoadToken(filepath.Join(dir, "token"))
	if stored != strings.Repeat("b", 43) {
		t.Fatal("removed existing token")
	}
	for _, scenario := range []struct {
		name, url, body string
		status          int
	}{
		{"expired", endpoint, "", 401},
		{"redirect", endpoint + "/redirect", "", 201},
		{"http", strings.Replace(endpoint, "https:", "http:", 1), "", 201},
		{"reflected secret", endpoint, token, 201},
		{"oversized", endpoint, strings.Repeat("x", 4097), 201},
		{"foreign metrics origin", endpoint, `{"version":1,"machine_token":"` + strings.Repeat("b", 43) + `","metrics_url":"https://other.example/api/v1/agent/metrics"}`, 201},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			status = scenario.status
			body = scenario.body
			dir := t.TempDir()
			err := Enroll(context.Background(), scenario.url, strings.NewReader(token), dir, "test")
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), strings.Repeat("b", 43)) {
				t.Fatal("secret in error")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("partial credentials retained")
			}
		})
	}
}
