package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/metrics"
	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

// preflight only reads runtime/host state. Per-job and GPU preflight belongs to the runner step.
func preflight(ctx context.Context, p policy.Worker, statePath, socket string) error {
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("DOCKER_UNAVAILABLE")
	}
	docker := newDocker(socket)
	defer docker.close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
	if err != nil {
		return fmt.Errorf("DOCKER_UNAVAILABLE")
	}
	response, err := docker.http.Do(req)
	if err != nil {
		return fmt.Errorf("DOCKER_UNAVAILABLE")
	}
	defer response.Body.Close()
	var engine struct {
		OSType          string
		Architecture    string
		CgroupVersion   string
		MemoryLimit     bool
		SwapLimit       bool
		CpuCfsQuota     bool
		PidsLimit       bool
		SecurityOptions []string
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 262144)).Decode(&engine) != nil {
		return fmt.Errorf("DOCKER_STATE_UNKNOWN")
	}
	arch := engine.Architecture
	if arch == "x86_64" {
		arch = "amd64"
	}
	if arch == "aarch64" {
		arch = "arm64"
	}
	if engine.OSType != "linux" || arch != runtime.GOARCH || engine.CgroupVersion != "2" || !engine.MemoryLimit || !engine.SwapLimit || !engine.CpuCfsQuota || !engine.PidsLimit {
		return fmt.Errorf("RUNTIME_LIMITS_UNSUPPORTED")
	}
	for _, option := range engine.SecurityOptions {
		if strings.Contains(option, "rootless") {
			return fmt.Errorf("ROOTLESS_PROFILE_UNSUPPORTED")
		}
	}
	_, available, err := metrics.ReadMemory()
	if err != nil || available < p.MinFreeMemoryBytes {
		return fmt.Errorf("MEMORY_UNAVAILABLE")
	}
	diskPath := statePath
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		diskPath = filepath.Dir(statePath)
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(diskPath, &disk) != nil || disk.Bsize <= 0 || disk.Bavail < uint64(p.MinFreeDiskBytes)/uint64(disk.Bsize)+1 {
		return fmt.Errorf("DISK_UNAVAILABLE")
	}
	return nil
}
