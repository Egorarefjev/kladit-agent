package worker

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Egorarefjev/kladit-agent/internal/policy"
)

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, fmt.Errorf("GPU_STATE_UNKNOWN")
	}
	return b.Buffer.Write(p)
}
func nvidiaQuery(ctx context.Context, query string) ([][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", query, "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	var out boundedOutput
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return nil, fmt.Errorf("GPU_STATE_UNKNOWN")
	}
	reader := csv.NewReader(&out)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("GPU_STATE_UNKNOWN")
	}
	return rows, nil
}
func gpuPreflight(ctx context.Context, s jobSpec, p policy.Worker) error {
	if s.GPUUUID == nil {
		return nil
	}
	rows, err := nvidiaQuery(ctx, "--query-gpu=uuid,memory.total,memory.used,utilization.gpu,display_active,mig.mode.current,compute_mode")
	if err != nil {
		return err
	}
	found := false
	for _, row := range rows {
		if len(row) != 7 {
			return fmt.Errorf("GPU_STATE_UNKNOWN")
		}
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		if row[0] != *s.GPUUUID {
			continue
		}
		if found {
			return fmt.Errorf("GPU_STATE_UNKNOWN")
		}
		found = true
		total, e1 := strconv.ParseUint(row[1], 10, 43)
		used, e2 := strconv.ParseUint(row[2], 10, 43)
		util, e3 := strconv.ParseUint(row[3], 10, 7)
		if e1 != nil || e2 != nil || e3 != nil || used > total || util > 100 || row[4] != "Disabled" || (row[5] != "Disabled" && row[5] != "[N/A]" && row[5] != "N/A") || row[6] != "Default" {
			return fmt.Errorf("GPU_STATE_UNKNOWN")
		}
		if util != 0 || used*(1<<20) > p.GPUIdleMemoryBytes || (total-used)*(1<<20) < *s.MinVRAMBytes {
			return fmt.Errorf("GPU_BUSY")
		}
	}
	if !found {
		return fmt.Errorf("GPU_STATE_UNKNOWN")
	}
	rows, err = nvidiaQuery(ctx, "--query-compute-apps=gpu_uuid,pid")
	if err != nil {
		return err
	}
	for _, row := range rows {
		if len(row) != 2 {
			return fmt.Errorf("GPU_STATE_UNKNOWN")
		}
		if strings.TrimSpace(row[0]) == *s.GPUUUID {
			return fmt.Errorf("GPU_BUSY")
		}
	}
	return nil
}
