package worker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
)

func logFrame(stream byte, stamp string, data []byte) []byte {
	payload := append([]byte(stamp+" "), data...)
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

const logStamp = "2026-11-28T12:00:00.000000001Z"

func TestLogsBinaryResumeAndRotation(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 0xe2, 0x82, 0xac}, 4000)
	snapshot := logFrame(1, logStamp, data)
	l := logState{}
	var received []byte
	for i := 0; i < 4; i++ {
		if err := readLogChunk(bytes.NewReader(snapshot), &l, true); err != nil {
			t.Fatal(err)
		}
		if l.Pending == nil {
			break
		}
		if l.Pending.Truncated {
			t.Fatal("valid replay truncated")
		}
		received = append(received, l.Pending.Data...)
		l.Seq = l.Pending.Seq
		l.Pending = nil
		// A process restart serializes the exact cursor, including a partial frame.
		b, _ := json.Marshal(l)
		if err := json.Unmarshal(b, &l); err != nil || !validLogs(&l) {
			t.Fatal("invalid resumed cursor", err)
		}
	}
	if !bytes.Equal(received, data) || !l.Cursors[0].EOF {
		t.Fatal("binary output changed or duplicated")
	}
	for _, replacement := range [][]byte{snapshot[:10], logFrame(1, "2026-11-28T12:00:01.000000001Z", data)} {
		resumed := l
		if err := readLogChunk(bytes.NewReader(replacement), &resumed, false); err != nil || resumed.Pending == nil || !resumed.Pending.Truncated {
			t.Fatal("lost prefix was not marked", err)
		}
	}
}

func TestLogsLimitsAndMalformedFrames(t *testing.T) {
	for _, mode := range []string{"bytes", "chunks", "stream", "size", "timestamp"} {
		t.Run(mode, func(t *testing.T) {
			l := logState{}
			snapshot := logFrame(1, logStamp, []byte("x"))
			switch mode {
			case "bytes":
				l.Bytes = logByteLimit
			case "chunks":
				l.Seq = 16384
			case "stream":
				snapshot[0] = 3
			case "size":
				binary.BigEndian.PutUint32(snapshot[4:], 1<<30)
			case "timestamp":
				snapshot = logFrame(1, "invalid", []byte("x"))
			}
			if err := readLogChunk(bytes.NewReader(snapshot), &l, false); err != nil || l.Pending == nil || !l.Pending.Truncated || !validLogs(&l) {
				t.Fatal("missing bounded truncation", err)
			}
		})
	}
	l := logState{}
	if err := readLogChunk(bytes.NewReader(logFrame(1, logStamp, []byte("test"))[:20]), &l, false); err == nil {
		t.Fatal("interrupted frame committed")
	}
}

func TestLogsDurableReplayTerminalTailAndQuota(t *testing.T) {
	j, options, _, d, engine := activeFixture(t)
	defer func() { j.Close() }()
	r := j.state.Current
	// Stage actual binary bytes before a first request, then restart the journal.
	r.Logs = &logState{Pending: &connection.JobLogChunk{Seq: 1, Stream: "stderr", Data: []byte{0, 255, 1}}, Bytes: 3}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	j.Close()
	var err error
	j, err = openJournal(options.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var original []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req connection.JobLogsRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad log request")
		}
		data, _ := json.Marshal(req)
		calls++
		if calls == 1 {
			original = data
			w.WriteHeader(503)
			return
		}
		if calls == 2 && !bytes.Equal(original, data) {
			t.Error("unacknowledged chunk changed")
		}
		if calls == 3 {
			w.WriteHeader(413)
			fmt.Fprint(w, `{"error":{"code":"JOB_LOG_LIMIT"},"next_seq":2}`)
			return
		}
		if calls == 4 && (!req.Truncated || req.Seq != 2 || req.Stream != "system") {
			t.Error("quota did not produce one marker at same seq")
		}
		fmt.Fprintf(w, `{"ack":{"attempt_id":%q,"seq":%d},"next_seq":%d}`, req.AttemptID, req.Seq, req.Seq+1)
	}))
	defer server.Close()
	options.ConfigPath, options.TokenPath = connectionFixture(t, options.StatePath, server)
	client, err := workerClient(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result := runLogTask(context.Background(), d, client, options, *j.state.Current)
	if result.err == nil {
		t.Fatal("lost ACK accepted")
	}
	// A lifecycle ACK may move the record to history while a log call is in flight.
	engine.mu.Lock()
	engine.container.State.Status = "exited"
	engine.container.State.Running = false
	engine.container.State.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	engine.mu.Unlock()
	for i := 0; i < 3; i++ {
		poll(t, j, d, options)
		if !j.state.Current.Acked {
			acknowledge(t, j)
		}
	}
	if err := j.emit("cancelled", "CANCEL_REQUESTED", nil); err != nil {
		t.Fatal(err)
	}
	id := j.state.Current.Assignment.AttemptID
	saved := *j.state.Current
	result = runLogTask(context.Background(), d, client, options, saved)
	if result.err != nil {
		t.Fatal(result.err)
	}
	acknowledge(t, j)
	if j.state.Current != nil {
		t.Fatal("log upload blocked terminal ACK")
	}
	if err := j.saveLogs(result); err != nil {
		t.Fatal(err)
	}
	history, err := j.nextLogRecord()
	if err != nil || history == nil || history.Assignment.AttemptID != id || history.Logs.Seq != 1 {
		t.Fatal("terminal tail lost", err)
	}
	history.Logs.Pending = &connection.JobLogChunk{Seq: 2, Stream: "stdout", Data: []byte("tail")}
	history.Logs.Bytes += 4
	if err := j.saveLogs(logResult{attempt: id, logs: *history.Logs}); err != nil {
		t.Fatal(err)
	}
	result = runLogTask(context.Background(), d, client, options, *history)
	if result.err != nil || result.logs.Pending == nil || !result.logs.Pending.Truncated {
		t.Fatal("quota handling failed", result.err)
	}
	if err := j.saveLogs(result); err != nil {
		t.Fatal(err)
	}
	history, _ = j.logRecord(id)
	result = runLogTask(context.Background(), d, client, options, *history)
	if result.err != nil || !result.logs.Done || result.logs.Pending != nil {
		t.Fatal("marker not acknowledged", result.err)
	}
	if err := j.saveLogs(result); err != nil {
		t.Fatal(err)
	}
	if next, err := j.nextLogRecord(); err != nil || next != nil {
		t.Fatal("closed logs retried", err)
	}
}

func TestLogsBindingPolicyAndPermanentErrors(t *testing.T) {
	j, options, _, d, engine := activeFixture(t)
	defer j.Close()
	r := j.state.Current
	engine.mu.Lock()
	engine.container.Config.Labels["kladit.spec"] = "foreign"
	engine.mu.Unlock()
	l := logState{}
	if err := collectLogs(context.Background(), d, r, &l); err != nil || l.Pending == nil || !l.Pending.Truncated {
		t.Fatal("foreign logs read", err)
	}
	for _, status := range []int{401, 403, 409, 410, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"JOB_LOG_CONFLICT"}}`)
			}))
			defer server.Close()
			options.ConfigPath, options.TokenPath = connectionFixture(t, options.StatePath, server)
			client, err := workerClient(options)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			r.Logs = &logState{Pending: &connection.JobLogChunk{Seq: 1, Stream: "stdout", Data: []byte("data")}, Bytes: 4}
			result := runLogTask(context.Background(), d, client, options, *r)
			if status == 409 || status == 410 {
				if result.err != nil || !result.logs.Blocked || result.logs.Pending == nil {
					t.Fatal("conflict discarded evidence")
				}
			} else {
				var delivery *connection.JobLogsError
				if !errors.As(result.err, &delivery) || delivery.Status != status {
					t.Fatal("revoke/retry status lost", result.err)
				}
			}
		})
	}
	// Local deny suppresses both collection and delivery, including pending bytes.
	options.PolicyPath = options.StatePath + "/missing-policy"
	result := runLogTask(context.Background(), d, nil, options, *r)
	if result.err == nil {
		t.Fatal("policy deny ignored")
	}
	if strings.Contains(result.err.Error(), r.Assignment.AssignmentToken) {
		t.Fatal("credential reflected")
	}
}

func verifyNativeLogs(t *testing.T, d *dockerClient, r *record) {
	t.Helper()
	l := logState{}
	got := map[string][]byte{}
	for i := 0; i < 20 && !l.Done; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := collectLogs(ctx, d, r, &l)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if l.Pending != nil {
			if l.Pending.Truncated {
				t.Fatal("native logs unexpectedly truncated")
			}
			got[l.Pending.Stream] = append(got[l.Pending.Stream], l.Pending.Data...)
			l.Seq = l.Pending.Seq
			l.Pending = nil
		}
	}
	if !l.Done || !bytes.Equal(got["stdout"], []byte{'o', 'u', 't', 0, 255}) || string(got["stderr"]) != "err€" {
		t.Fatalf("native binary logs differ: stdout=%q stderr=%q done=%t", got["stdout"], got["stderr"], l.Done)
	}
}

func TestLogTimestampBoundaryCompactsReplay(t *testing.T) {
	first := logFrame(1, logStamp, bytes.Repeat([]byte("x"), 8192))
	stamp2 := "2026-11-28T12:00:01.000000002Z"
	second := logFrame(1, stamp2, []byte("second"))
	l := logState{}
	for i := 0; i < 2; i++ {
		if err := readLogChunk(bytes.NewReader(append(append([]byte{}, first...), second...)), &l, false); err != nil || l.Pending == nil {
			t.Fatal("missing boundary data", err)
		}
		if string(l.Pending.Data) != []string{strings.Repeat("x", 8192), "second"}[i] {
			t.Fatal("changed order")
		}
		l.Seq = l.Pending.Seq
		l.Pending = nil
	}
	if l.Cursors[0].Since != stamp2 || l.Cursors[0].Offset != int64(len(second)) {
		t.Fatal("old prefix retained")
	}
	if err := readLogChunk(bytes.NewReader(second), &l, true); err != nil || !l.Cursors[0].EOF || l.Pending != nil {
		t.Fatal("inclusive replay duplicated boundary", err)
	}
}

func TestLogsBatchShortLines(t *testing.T) {
	var snapshot []byte
	for i := 0; i < 100; i++ {
		snapshot = append(snapshot, logFrame(1, logStamp, []byte("line\n"))...)
	}
	l := logState{}
	if err := readLogChunk(bytes.NewReader(snapshot), &l, true); err != nil || l.Pending == nil || string(l.Pending.Data) != strings.Repeat("line\n", 100) || !l.Cursors[0].EOF {
		t.Fatal("short lines were not batched", err)
	}
}
