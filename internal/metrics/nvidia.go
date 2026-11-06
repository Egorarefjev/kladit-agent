package metrics

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const mib = 1 << 20

type GPU struct {
	Index              uint64  `json:"index"`
	Name               string  `json:"name"`
	UtilizationPercent *uint64 `json:"utilization_percent"`
	VRAMTotalBytes     *uint64 `json:"vram_total_bytes"`
	VRAMUsedBytes      *uint64 `json:"vram_used_bytes"`
	TemperatureCelsius *uint64 `json:"temperature_celsius"`
}

func readNvidiaGPUs(ctx context.Context) []GPU {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu",
		"--format=csv,noheader,nounits")
	command.WaitDelay = 250 * time.Millisecond
	output, err := command.Output()
	if err != nil {
		return []GPU{}
	}
	gpus, err := parseNvidiaGPUs(output)
	if err != nil {
		return []GPU{}
	}
	return gpus
}

func parseNvidiaGPUs(output []byte) ([]GPU, error) {
	reader := csv.NewReader(bytes.NewReader(output))
	reader.FieldsPerRecord = 6
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	gpus := make([]GPU, 0, len(rows))
	for _, row := range rows {
		index, indexErr := strconv.ParseUint(strings.TrimSpace(row[0]), 10, 53)
		name := strings.TrimSpace(row[1])
		utilization, utilErr := gpuValue(row[2], 100, 1)
		total, totalErr := gpuValue(row[3], maxSafeInteger, mib)
		used, usedErr := gpuValue(row[4], maxSafeInteger, mib)
		temperature, tempErr := gpuValue(row[5], maxSafeInteger, 1)
		if indexErr != nil || name == "" || utilErr != nil || totalErr != nil || usedErr != nil ||
			tempErr != nil || (total != nil && used != nil && *used > *total) {
			return nil, fmt.Errorf("invalid NVIDIA GPU metrics")
		}
		gpus = append(gpus, GPU{
			Index:              index,
			Name:               name,
			UtilizationPercent: utilization,
			VRAMTotalBytes:     total,
			VRAMUsedBytes:      used,
			TemperatureCelsius: temperature,
		})
	}
	return gpus, nil
}

func gpuValue(field string, max, scale uint64) (*uint64, error) {
	switch field = strings.TrimSpace(field); field {
	case "N/A", "[N/A]", "[Not Supported]":
		return nil, nil
	}
	value, err := strconv.ParseUint(field, 10, 64)
	if err != nil || value > max/scale {
		return nil, fmt.Errorf("invalid NVIDIA GPU value")
	}
	value *= scale
	return &value, nil
}
