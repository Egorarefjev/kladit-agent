package metrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiskUsage(t *testing.T) {
	got, err := diskUsage(4096, 1000, 400, 350)
	want := Disk{TotalBytes: 4096000, UsedBytes: 2457600, AvailableBytes: 1433600}
	if err != nil || got != want {
		t.Fatalf("diskUsage = %+v, %v; want %+v", got, err, want)
	}
	for _, counts := range [][4]uint64{
		{0, 1000, 400, 350},
		{4096, 0, 0, 0},
		{4096, 1000, 1001, 350},
		{4096, 1000, 400, 401},
		{4096, maxSafeInteger, 0, 0},
	} {
		if _, err := diskUsage(counts[0], counts[1], counts[2], counts[3]); err == nil {
			t.Errorf("accepted invalid block counts %v", counts)
		}
	}
}

func TestCollectWhenNvidiaQueryFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CPUUsagePercent < 0 || snapshot.CPUUsagePercent > 100 ||
		snapshot.MemoryTotalBytes == 0 || snapshot.Disk.TotalBytes == 0 || snapshot.UptimeSeconds <= 0 {
		t.Fatalf("missing host metrics: %+v", snapshot)
	}
	if gpus, err := json.Marshal(snapshot.GPUs); err != nil || string(gpus) != "[]" {
		t.Fatalf("gpus = %s, %v; want []", gpus, err)
	}
}
