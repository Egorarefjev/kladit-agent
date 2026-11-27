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
)

const workerGeneration = "12345678-1234-4234-8234-123456789abc"

func TestJobsProtocol(t *testing.T) {
	request := JobsRequest{Version: 1, WorkerGeneration: workerGeneration, AgentVersion: "test", Capabilities: []string{"jobs.execute.v1"}, ExecuteAllowed: true}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/jobs/sync" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("wrong jobs destination/authentication")
		}
		var sent JobsRequest
		if json.NewDecoder(r.Body).Decode(&sent) != nil || sent.PreflightOK || sent.WorkerGeneration != workerGeneration {
			t.Error("bad worker handshake")
		}
		fmt.Fprintf(w, `{"version":1,"worker_generation":%q,"enabled":false,"ack":null,"assignment":null}`, workerGeneration)
	}))
	defer server.Close()
	c := trustedClient(t, server)
	response, err := c.SyncJobs(context.Background(), request)
	if err != nil || response.Assignment != nil || response.Enabled {
		t.Fatal("handshake failed", err)
	}
	c.endpoint = server.URL + "/custom-metrics"
	if _, err = c.SyncJobs(context.Background(), request); err == nil {
		t.Fatal("custom path used for worker")
	}
}

func TestJobsResponsesAreBoundedAndDoNotReflectSecrets(t *testing.T) {
	for _, test := range []struct {
		status    int
		body      string
		permanent bool
	}{
		{401, `{"error":{"code":"secret-canary"}}`, true},
		{409, `{"error":{"code":"WORKER_BUSY"}}`, false},
		{409, `{"error":{"code":"WORKER_GENERATION_MISMATCH"}}`, true},
		{200, strings.Repeat("x", 32769), false},
		{200, `{"version":1,"worker_generation":"wrong","secret":"secret-canary"}`, false},
		{200, `{"version":1,"worker_generation":"12345678-1234-4234-8234-123456789abc","assignment":{"attempt_id":"../../escape"}}`, false},
	} {
		t.Run(fmt.Sprintf("%d-%d", test.status, len(test.body)), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			c := trustedClient(t, server)
			_, err := c.SyncJobs(context.Background(), JobsRequest{Version: 1, WorkerGeneration: workerGeneration})
			if err == nil || strings.Contains(err.Error(), "secret-canary") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), testToken) {
				t.Fatal("invalid response accepted or leaked")
			}
			var rejected *JobsError
			if test.status != 200 && (!errors.As(err, &rejected) || rejected.Permanent() != test.permanent) {
				t.Fatal("wrong retry classification")
			}
		})
	}
}
