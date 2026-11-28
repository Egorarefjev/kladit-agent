package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

func TestGPUPreflight(t *testing.T) {
	directory := privateDir(t)
	script := `#!/bin/sh
case "$1" in
 --query-gpu=*) printf '%s\n' "$GPU_ROWS" ;;
 --query-compute-apps=*) if [ -n "$GPU_PROCESSES" ]; then printf '%s\n' "$GPU_PROCESSES"; fi ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "nvidia-smi"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	id := "GPU-fixture"
	vram := uint64(1024 << 20)
	s := jobSpec{GPUUUID: &id, MinVRAMBytes: &vram}
	p := policy.Worker{GPUIdleMemoryBytes: 16 << 20}
	for _, test := range []struct {
		name, rows, processes string
		allowed               bool
	}{
		{"idle", "GPU-fixture, 4096, 8, 0, Disabled, Disabled, Default", "", true},
		{"driver-without-mig", "GPU-fixture, 4096, 8, 0, Disabled, [N/A], Default", "", true},
		{"process", "GPU-fixture, 4096, 8, 0, Disabled, Disabled, Default", "GPU-fixture, 123", false},
		{"busy-memory", "GPU-fixture, 4096, 100, 0, Disabled, Disabled, Default", "", false},
		{"busy-compute", "GPU-fixture, 4096, 8, 10, Disabled, Disabled, Default", "", false},
		{"mig", "GPU-fixture, 4096, 8, 0, Disabled, Enabled, Default", "", false},
		{"display", "GPU-fixture, 4096, 8, 0, Enabled, Disabled, Default", "", false},
		{"unknown", "GPU-fixture, N/A, 8, 0, Disabled, Disabled, Default", "", false},
		{"process-state-unknown", "GPU-fixture, 4096, 8, 0, Disabled, Disabled, Default", "malformed", false},
		{"unknown-process-device", "GPU-fixture, 4096, 8, 0, Disabled, Disabled, Default", "N/A, 123", false},
		{"unknown-process-id", "GPU-fixture, 4096, 8, 0, Disabled, Disabled, Default", "GPU-other, N/A", false},
		{"missing-device", "GPU-other, 4096, 8, 0, Disabled, Disabled, Default", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GPU_ROWS", test.rows)
			t.Setenv("GPU_PROCESSES", test.processes)
			err := gpuPreflight(context.Background(), s, p)
			if (err == nil) != test.allowed {
				t.Fatalf("unexpected GPU preflight: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gpuPreflight(ctx, s, p) == nil {
		t.Fatal("cancelled GPU query accepted")
	}
}
