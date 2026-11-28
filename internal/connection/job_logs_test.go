package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJobLogsProtocolAndErrorBounds(t *testing.T) {
	req := JobLogsRequest{Version: 1, WorkerGeneration: workerGeneration, AttemptID: workerGeneration, SpecHash: strings.Repeat("a", 64), AssignmentToken: strings.Repeat("b", 43), JobLogChunk: JobLogChunk{Seq: 1, Stream: "stdout", Data: []byte{0, 255, 1}}}
	for _, mode := range []string{"ok", "marker", "wrong-ack", "interrupted", "oversized", "quota", "rate", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			sent := req
			if mode == "marker" {
				sent.JobLogChunk = JobLogChunk{Seq: 1, Stream: "system", Truncated: true}
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/agent/jobs/logs" || r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Error("wrong auth/path")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || (mode == "marker" && body["data_base64"] != "") || (mode != "marker" && body["data_base64"] != "AP8B") {
					t.Error("binary/empty encoding changed")
				}
				switch mode {
				case "wrong-ack":
					fmt.Fprintf(w, `{"ack":{"attempt_id":%q,"seq":2},"next_seq":3}`, workerGeneration)
				case "interrupted":
					w.Header().Set("Content-Length", "500")
					fmt.Fprint(w, `{"ack":`)
				case "oversized":
					fmt.Fprint(w, strings.Repeat("secret-canary", 100))
				case "quota":
					w.WriteHeader(413)
					fmt.Fprint(w, `{"error":{"code":"JOB_LOG_LIMIT"},"next_seq":1}`)
				case "rate":
					w.Header().Set("Retry-After", "900")
					w.WriteHeader(429)
					fmt.Fprint(w, `{"error":{"code":"secret-canary"}}`)
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/secret-canary")
					w.WriteHeader(307)
				default:
					fmt.Fprintf(w, `{"ack":{"attempt_id":%q,"seq":1},"next_seq":2}`, workerGeneration)
				}
			}))
			defer server.Close()
			c := trustedClient(t, server)
			err := c.SendJobLogs(context.Background(), sent)
			if mode == "ok" || mode == "marker" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("invalid reply accepted/reflected", err)
			}
			var delivery *JobLogsError
			if !errors.As(err, &delivery) {
				t.Fatal("missing retry status", err)
			}
			if mode == "quota" && (delivery.Code != "JOB_LOG_LIMIT" || delivery.NextSeq != 1) {
				t.Fatal("quota lost")
			}
			if mode == "rate" && delivery.RetryAfter != 300*time.Second {
				t.Fatal("unbounded retry")
			}
		})
	}
}
