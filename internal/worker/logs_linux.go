package worker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/connection"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

const logByteLimit = 10 << 20
const logReadLimit = 16 << 20

// One pending 8 KiB chunk per attempt; the private journal is the durable spool.
// Timestamped Docker framing is hashed too, so rotation cannot silently rebase a cursor.
type logCursor struct {
	Since     string `json:"since,omitempty"`
	Offset    int64  `json:"offset"`
	Hash      string `json:"hash"`
	Remaining uint32 `json:"remaining"`
	EOF       bool   `json:"eof,omitempty"`
}
type logState struct {
	Cursors [2]logCursor            `json:"cursors"`
	Stream  int                     `json:"stream"`
	Seq     uint64                  `json:"seq"`
	Bytes   int                     `json:"bytes"`
	Pending *connection.JobLogChunk `json:"pending,omitempty"`
	Done    bool                    `json:"done,omitempty"`
	Blocked bool                    `json:"blocked,omitempty"`
}

func validLogs(l *logState) bool {
	if l == nil {
		return true
	}
	if l.Stream < 0 || l.Stream > 1 || l.Seq > 16385 || l.Bytes < 0 || l.Bytes > logByteLimit || !connection.ValidJobLogChunk(l.Pending) || (l.Pending != nil && (l.Pending.Seq != l.Seq+1 || l.Done)) {
		return false
	}
	for _, c := range l.Cursors {
		if c.Since != "" {
			if _, err := time.Parse(time.RFC3339Nano, c.Since); err != nil {
				return false
			}
		}
		if c.Offset < 0 || c.Offset > logReadLimit || c.Remaining > 1<<20 || (c.Offset > 0 && !imageID.MatchString("sha256:"+c.Hash)) {
			return false
		}
	}
	return true
}
func (l *logState) truncate() {
	l.Cursors = [2]logCursor{}
	l.Pending = &connection.JobLogChunk{Seq: l.Seq + 1, Stream: "system", Data: []byte{}, Truncated: true}
}

// readLogChunk replays a bounded prefix using constant memory. Only new payload
// bytes enter the spool; Docker headers/timestamps stay local. Inconsistent replay
// closes the log explicitly rather than uploading a potentially duplicated tail.
func readLogChunk(src io.Reader, l *logState, exited bool) error {
	c := &l.Cursors[l.Stream]
	h := sha256.New()
	if _, err := io.CopyN(h, src, c.Offset); err != nil {
		if errors.Is(err, io.EOF) {
			l.truncate()
			return nil
		}
		return err
	}
	if c.Offset > 0 && fmt.Sprintf("%x", h.Sum(nil)) != c.Hash {
		l.truncate()
		return nil
	}
	reader := io.TeeReader(src, h)
	read := func(b []byte) error {
		n, err := io.ReadFull(reader, b)
		c.Offset += int64(n)
		return err
	}
	data := make([]byte, 0, 8192)
	for len(data) < 8192 {
		if c.Remaining == 0 {
			if c.Offset+72 > logReadLimit {
				l.truncate()
				return nil
			}
			var header [8]byte
			if err := read(header[:]); err != nil {
				if errors.Is(err, io.EOF) {
					c.EOF = exited
					c.Hash = fmt.Sprintf("%x", h.Sum(nil))
					break
				}
				return err
			}
			size := binary.BigEndian.Uint32(header[4:])
			if header[0] != byte(l.Stream+1) || header[1] != 0 || header[2] != 0 || header[3] != 0 || size == 0 || size > 1<<20 {
				l.truncate()
				return nil
			}
			// timestamps=true supplies an RFC3339Nano prefix for each Docker message.
			stamp := make([]byte, 0, 64)
			for size > 0 && len(stamp) < 64 {
				var b [1]byte
				if err := read(b[:]); err != nil {
					return err
				}
				size--
				if b[0] == ' ' {
					break
				}
				stamp = append(stamp, b[0])
			}
			if _, err := time.Parse(time.RFC3339Nano, string(stamp)); err != nil || size == 0 {
				l.truncate()
				return nil
			}
			if string(stamp) != c.Since {
				// Only replay the current timestamp boundary, not the entire attempt.
				// Multiple messages sharing a timestamp remain protected by one prefix hash.
				c.Since = string(stamp)
				h.Reset()
				h.Write(header[:])
				h.Write(stamp)
				h.Write([]byte{' '})
				c.Offset = int64(8 + len(stamp) + 1)
			}
			c.Remaining = size
		}
		size := min(int(c.Remaining), 8192-len(data), logByteLimit-l.Bytes-len(data))
		if (size == 0 && len(data) == 0) || l.Seq >= 16384 || c.Offset+int64(size) > logReadLimit {
			l.truncate()
			return nil
		}
		if size == 0 {
			break
		}
		start := len(data)
		data = append(data, make([]byte, size)...)
		if err := read(data[start:]); err != nil {
			return err
		}
		c.Remaining -= uint32(size)
	}
	c.Hash = fmt.Sprintf("%x", h.Sum(nil))
	if len(data) == 0 {
		return nil
	}

	stream := []string{"stdout", "stderr"}[l.Stream]
	l.Pending = &connection.JobLogChunk{Seq: l.Seq + 1, Stream: stream, Data: data}
	l.Bytes += len(data)
	return nil
}

func collectLogs(ctx context.Context, d *dockerClient, r *record, l *logState) error {
	c, err := d.inspect(ctx, containerName(r))
	if err != nil {
		var status *dockerError
		if errors.As(err, &status) && status.status == 404 {
			l.truncate()
			return nil
		}
		return err
	}
	if !ownsContainer(r, c) || (r.Runtime.StartedAt != "" && c.State.StartedAt != r.Runtime.StartedAt) {
		l.truncate()
		return nil
	}
	if c.State.Status != "running" && c.State.Status != "exited" {
		return nil
	}
	if l.Cursors[l.Stream].EOF {
		l.Stream = 1 - l.Stream
	}
	stream := []string{"stdout", "stderr"}[l.Stream]
	since := "0"
	if stamp := l.Cursors[l.Stream].Since; stamp != "" {
		at, _ := time.Parse(time.RFC3339Nano, stamp) // Validated on journal load.
		since = fmt.Sprintf("%d.%09d", at.Unix(), at.Nanosecond())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+c.ID+"/logs?"+stream+"=true&timestamps=true&follow=false&tail=all&since="+since, nil)
	if err != nil {
		return fmt.Errorf("invalid Docker logs request")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("Docker logs unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			l.truncate()
			return nil
		}
		return fmt.Errorf("Docker logs unavailable")
	}
	if err := readLogChunk(io.LimitReader(resp.Body, logReadLimit+1), l, c.State.Status == "exited"); err != nil {
		return fmt.Errorf("Docker log read interrupted")
	}
	l.Stream = 1 - l.Stream
	if l.Cursors[0].EOF && l.Cursors[1].EOF && l.Pending == nil {
		l.Done = true
	}
	return nil
}

type logResult struct {
	attempt string
	logs    logState
	err     error
}

// No journal pointers cross goroutines. Collection and upload are separate tasks:
// a collected chunk must be fsynced before it can become a network request.
func runLogTask(ctx context.Context, d *dockerClient, client *connection.Client, options Options, r record) logResult {
	result := logResult{attempt: r.Assignment.AttemptID}
	if r.Logs != nil {
		result.logs = *r.Logs
	}
	p, err := policy.LoadWorker(options.PolicyPath)
	if err != nil || !p.ExecuteAllowed() {
		result.err = fmt.Errorf("log permission disabled")
		return result
	}
	if result.logs.Pending == nil {
		result.err = collectLogs(ctx, d, &r, &result.logs)
		return result
	}
	chunk := *result.logs.Pending
	err = client.SendJobLogs(ctx, connection.JobLogsRequest{Version: 1, WorkerGeneration: r.Assignment.WorkerGeneration, AttemptID: r.Assignment.AttemptID, SpecHash: r.Assignment.SpecHash, AssignmentToken: r.Assignment.AssignmentToken, JobLogChunk: chunk})
	if err == nil {
		result.logs.Seq = chunk.Seq
		result.logs.Pending = nil
		result.logs.Done = chunk.Truncated
		return result
	}
	var rejected *connection.JobLogsError
	if errors.As(err, &rejected) {
		if rejected.Status == 413 && rejected.Code == "JOB_LOG_LIMIT" && rejected.NextSeq == chunk.Seq && !chunk.Truncated {
			result.logs.truncate()
			return result
		}
		if rejected.Status == 410 || rejected.Status == 409 || (rejected.Status >= 300 && rejected.Status < 500 && rejected.Status != 408 && rejected.Status != 429 && rejected.Status != 401 && rejected.Status != 403) {
			// Retain the exact rejected chunk as evidence; never skip/rebase sequences.
			result.logs.Blocked = true
			result.err = nil
			return result
		}
	}
	result.err = err
	return result
}

func (j *journal) logRecord(id string) (*record, error) {
	if r := j.state.Current; r != nil && r.Assignment.AttemptID == id {
		return r, nil
	}
	var r record
	if err := j.read(".done-"+id+".json", &r); err != nil {
		return nil, err
	}
	if !validRecord(&r, j.state.Generation) {
		return nil, fmt.Errorf("invalid log history")
	}
	return &r, nil
}
func (j *journal) saveLogs(result logResult) error {
	r, err := j.logRecord(result.attempt)
	if err != nil {
		return err
	}
	r.Logs = &result.logs
	if r == j.state.Current {
		return j.save()
	}
	return j.write(".done-"+result.attempt+".json", r)
}

// Queue membership is only an optimization; restart reconstructs it from durable
// tombstones. Scan at most 16 old records per pass, never all 1024 in the supervisor.
func (j *journal) queueLogs(id string) {
	if !slices.Contains(j.logQueue, id) {
		j.logQueue = append(j.logQueue, id)
	}
}
func (j *journal) nextLogRecord() (*record, error) {
	if !j.logIndexed {
		dir, err := j.root.Open(".")
		if err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(1030)
		dir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".done-") && strings.HasSuffix(name, ".json") {
				id := strings.TrimSuffix(strings.TrimPrefix(name, ".done-"), ".json")
				if !connection.ValidJobIdentity(id) {
					return nil, fmt.Errorf("invalid worker log history")
				}
				j.queueLogs(id)
			}
		}
		j.logIndexed = true
	}
	if r := j.state.Current; r != nil {
		j.queueLogs(r.Assignment.AttemptID)
	}
	for range min(len(j.logQueue), 16) {
		id := j.logQueue[0]
		j.logQueue = j.logQueue[1:]
		r, err := j.logRecord(id)
		if err != nil {
			return nil, err
		}
		if r.Runtime == nil || r.Runtime.ContainerID == "" || r.Runtime.Deadline == "" || (r.Logs != nil && (r.Logs.Done || r.Logs.Blocked)) {
			continue
		}
		j.queueLogs(id)
		copy := *r
		runtime := *r.Runtime
		copy.Runtime = &runtime
		return &copy, nil
	}
	return nil, nil
}
