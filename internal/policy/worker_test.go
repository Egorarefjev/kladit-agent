package policy

import (
	"strings"
	"testing"
)

const validWorker = `{"version":1,"capabilities":{"jobs.execute":true},"limits":{"cpu_millis":1000,"memory_bytes":1073741824,"timeout_seconds":3600},"min_free_disk_bytes":1,"min_free_memory_bytes":1}`

func TestWorkerPolicy(t *testing.T) {
	p, err := parseWorker([]byte(validWorker))
	if err != nil || !p.ExecuteAllowed() {
		t.Fatal("valid profile rejected", err)
	}
	for _, capabilities := range []string{`{}`, `{"jobs.execute":false}`} {
		input := strings.Replace(validWorker, `{"jobs.execute":true}`, capabilities, 1)
		p, err := parseWorker([]byte(input))
		if err != nil || p.ExecuteAllowed() {
			t.Fatal("missing/false permission allowed", err)
		}
	}
	for _, input := range []string{
		strings.Replace(validWorker, `"version":1`, `"version":2`, 1),
		strings.Replace(validWorker, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(validWorker, `"jobs.execute":true`, `"jobs.execute":true,"jobs.execute":false`, 1),
		strings.Replace(validWorker, `"cpu_millis":1000`, `"cpu_millis":1000,"cpu_millis":2000`, 1),
		strings.Replace(validWorker, `"jobs.execute":true`, `"jobs.execute":"true"`, 1),
		strings.Replace(validWorker, `"cpu_millis":1000`, `"cpu_millis":0`, 1),
		strings.Replace(validWorker, `"timeout_seconds":3600`, `"timeout_seconds":86401`, 1),
		strings.Replace(validWorker, `"cpu_millis":1000`, `"mounts":[],"cpu_millis":1000`, 1),
		strings.Replace(validWorker, `"memory_bytes":1073741824`, `"memory_bytes":9007199254740992`, 1),
	} {
		if _, err := parseWorker([]byte(input)); err == nil {
			t.Fatal("accepted invalid worker policy")
		}
	}
	// Opt-in execution is not silently accepted by the existing metrics-only parser.
	if _, err := parse([]byte(validWorker)); err == nil {
		t.Fatal("metrics policy accepted worker privileges")
	}
}
