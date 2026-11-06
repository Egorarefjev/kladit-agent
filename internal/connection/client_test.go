package connection

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/metrics"
)

var testToken = strings.Repeat("a", 43)

func trustedClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{Endpoint: server.URL + "/api/v1/agent/metrics"}, testToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client.http.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	t.Cleanup(client.http.CloseIdleConnections)
	return client
}

func TestSend(t *testing.T) {
	utilization := uint64(50)
	snapshot := metrics.Snapshot{
		CollectedAt: time.Now().UTC().Truncate(time.Second), CPUUsagePercent: 25,
		MemoryTotalBytes: 1024, MemoryAvailableBytes: 768, MemoryUsedBytes: 256, UptimeSeconds: 123,
		Disk: metrics.Disk{TotalBytes: 4096, UsedBytes: 1024, AvailableBytes: 2048},
		GPUs: []metrics.GPU{{Index: 2, Name: "NVIDIA A100", UtilizationPercent: &utilization}},
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/agent/metrics" {
			t.Error("unexpected method/path")
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "kladit-agent/test" {
			t.Error("missing request headers")
		}
		var envelope struct {
			Version      int              `json:"version"`
			AgentVersion string           `json:"agent_version"`
			Metrics      metrics.Snapshot `json:"metrics"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
		}
		if envelope.Version != 1 || envelope.AgentVersion != "test" || !reflect.DeepEqual(envelope.Metrics, snapshot) {
			t.Error("unexpected metrics envelope")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := trustedClient(t, server)
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("proxy environment must not override the direct connection")
	}
	if err := client.Send(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("expected exactly one request")
	}
}

func TestSendRejectsStatusAndRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(204)
	}))
	defer target.Close()
	for _, status := range []int{200, 202, 301, 307, 308, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, testToken)
			}))
			defer server.Close()
			client := trustedClient(t, server)
			err := client.Send(context.Background(), metrics.Snapshot{})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), testToken) {
				t.Fatal("expected a redacted HTTP status error")
			}
			if requests.Load() != 1 {
				t.Fatal("failed snapshots must not be retried automatically")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target received a request")
	}
}

func TestSendVerifiesTLS(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted TLS endpoint received metrics")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	client, err := NewClient(Config{Endpoint: server.URL}, testToken, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	if err := client.Send(context.Background(), metrics.Snapshot{}); err == nil || strings.Contains(err.Error(), server.URL) {
		t.Fatal("expected redacted TLS verification failure")
	}
}

func TestSendTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer server.Close()
	client := trustedClient(t, server)
	client.http.Timeout = 20 * time.Millisecond
	if err := client.Send(context.Background(), metrics.Snapshot{}); err == nil {
		t.Fatal("request exceeded timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Send(ctx, metrics.Snapshot{}); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}
