package metrics

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxSafeInteger = 1<<53 - 1

type Disk struct {
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type Snapshot struct {
	CollectedAt          time.Time `json:"collected_at"`
	CPUUsagePercent      float64   `json:"cpu_usage_percent"`
	MemoryTotalBytes     uint64    `json:"memory_total_bytes"`
	MemoryAvailableBytes uint64    `json:"memory_available_bytes"`
	MemoryUsedBytes      uint64    `json:"memory_used_bytes"`
	UptimeSeconds        float64   `json:"uptime_seconds"`
	Disk                 Disk      `json:"disk"`
	GPUs                 []GPU     `json:"gpus"`
}

// Collect samples host CPU counters over one second. It does not report container limits.
func Collect(ctx context.Context) (Snapshot, error) {
	if runtime.GOOS != "linux" {
		return Snapshot{}, fmt.Errorf("metrics requires Linux")
	}
	first, err := readCPU()
	if err != nil {
		return Snapshot{}, err
	}
	time.Sleep(time.Second)
	second, err := readCPU()
	if err != nil {
		return Snapshot{}, err
	}
	usage, err := cpuUsage(first, second)
	if err != nil {
		return Snapshot{}, err
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Snapshot{}, err
	}
	total, available, err := parseMemory(string(data))
	if err != nil {
		return Snapshot{}, err
	}
	data, err = os.ReadFile("/proc/uptime")
	if err != nil {
		return Snapshot{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return Snapshot{}, fmt.Errorf("invalid /proc/uptime")
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || uptime < 0 || math.IsNaN(uptime) || math.IsInf(uptime, 0) {
		return Snapshot{}, fmt.Errorf("invalid uptime")
	}
	disk, err := readRootDisk()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read root filesystem /: %w", err)
	}
	gpus := readNvidiaGPUs(ctx)
	return Snapshot{
		CollectedAt: time.Now().UTC(), CPUUsagePercent: usage,
		MemoryTotalBytes: total, MemoryAvailableBytes: available, MemoryUsedBytes: total - available,
		UptimeSeconds: uptime,
		Disk:          disk,
		GPUs:          gpus,
	}, nil
}

func readCPU() ([]uint64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	return parseCPU(string(data))
}

func parseCPU(data string) ([]uint64, error) {
	line, _, _ := strings.Cut(data, "\n")
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return nil, fmt.Errorf("invalid aggregate CPU counters")
	}
	counters := make([]uint64, 8)
	// guest and guest_nice are already included in user and nice.
	for i := range counters {
		value, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU counter: %w", err)
		}
		counters[i] = value
	}
	return counters, nil
}

func cpuUsage(before, after []uint64) (float64, error) {
	var total, idle float64
	for i := range before {
		if after[i] < before[i] {
			return 0, fmt.Errorf("CPU counters decreased; retry sampling")
		}
		delta := float64(after[i] - before[i])
		total += delta
		if i == 3 || i == 4 {
			idle += delta
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("CPU counters did not advance")
	}
	return (total - idle) / total * 100, nil
}

func parseMemory(data string) (uint64, uint64, error) {
	values := make(map[string]uint64)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, 0, fmt.Errorf("invalid memory field")
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			return 0, 0, fmt.Errorf("invalid memory value")
		}
		values[fields[0]] = value * 1024
	}
	total := values["MemTotal:"]
	available, ok := values["MemAvailable:"]
	if total == 0 || !ok || available > total {
		return 0, 0, fmt.Errorf("missing or inconsistent memory counters")
	}
	return total, available, nil
}
