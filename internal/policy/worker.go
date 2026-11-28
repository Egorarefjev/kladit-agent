package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Worker is a separate local opt-in profile. The metrics policy is unchanged.
type Worker struct {
	Version            int             `json:"version"`
	Capabilities       map[string]bool `json:"capabilities"`
	Limits             WorkerLimits    `json:"limits"`
	MinFreeDiskBytes   uint64          `json:"min_free_disk_bytes"`
	MinFreeMemoryBytes uint64          `json:"min_free_memory_bytes"`
}

// WorkerLimits bounds later per-job resource requests; it never reserves resources.
type WorkerLimits struct {
	CPUMillis      uint64 `json:"cpu_millis"`
	MemoryBytes    uint64 `json:"memory_bytes"`
	TimeoutSeconds uint64 `json:"timeout_seconds"`
}

// LoadWorker rejects unknown fields, duplicate keys and invalid/missing limits.
func LoadWorker(path string) (Worker, error) {
	f, err := os.Open(path)
	if err != nil {
		return Worker{}, fmt.Errorf("read worker policy")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return Worker{}, fmt.Errorf("invalid worker policy size")
	}
	return parseWorker(data)
}

func parseWorker(data []byte) (Worker, error) {
	fields, err := object(data)
	if err != nil {
		return Worker{}, fmt.Errorf("invalid worker policy")
	}
	for key := range fields {
		if key != "version" && key != "capabilities" && key != "limits" && key != "min_free_disk_bytes" && key != "min_free_memory_bytes" {
			return Worker{}, fmt.Errorf("unknown worker policy field")
		}
	}
	caps, err := object(fields["capabilities"])
	if err != nil {
		return Worker{}, fmt.Errorf("invalid worker capabilities")
	}
	for key, raw := range caps {
		if key != "jobs.execute" || (string(raw) != "true" && string(raw) != "false") {
			return Worker{}, fmt.Errorf("unsupported worker capability")
		}
	}
	limits, err := object(fields["limits"])
	if err != nil {
		return Worker{}, fmt.Errorf("invalid worker limits")
	}
	for key := range limits {
		if key != "cpu_millis" && key != "memory_bytes" && key != "timeout_seconds" {
			return Worker{}, fmt.Errorf("unknown worker limit")
		}
	}
	var p Worker
	if json.Unmarshal(data, &p) != nil || p.Version != 1 || p.Limits.CPUMillis == 0 || p.Limits.CPUMillis > 9007199254740991 || p.Limits.MemoryBytes == 0 || p.Limits.MemoryBytes > 9007199254740991 || p.Limits.TimeoutSeconds == 0 || p.Limits.TimeoutSeconds > 86400 || p.MinFreeDiskBytes == 0 || p.MinFreeDiskBytes > 9007199254740991 || p.MinFreeMemoryBytes == 0 || p.MinFreeMemoryBytes > 9007199254740991 {
		return Worker{}, fmt.Errorf("invalid worker policy or limits")
	}
	return p, nil
}

// ExecuteAllowed is false for missing or explicitly disabled local permission.
func (p Worker) ExecuteAllowed() bool { return p.Capabilities["jobs.execute"] }
