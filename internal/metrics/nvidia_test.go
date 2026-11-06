package metrics

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParseNvidiaGPUs(t *testing.T) {
	const first = `{"index":0,"name":"NVIDIA A100","utilization_percent":25,"vram_total_bytes":42949672960,"vram_used_bytes":1073741824,"temperature_celsius":42}`
	const second = `{"index":3,"name":"NVIDIA B200, test","utilization_percent":null,"vram_total_bytes":1073741824,"vram_used_bytes":0,"temperature_celsius":null}`
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"zero", "", "[]"},
		{"one", "0, NVIDIA A100, 25, 40960, 1024, 42\n", "[" + first + "]"},
		{
			"multiple with unavailable readings",
			"0, NVIDIA A100, 25, 40960, 1024, 42\r\n3, \"NVIDIA B200, test\", [N/A], 1024, 0, [Not Supported]\r\n",
			"[" + first + "," + second + "]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gpus, err := parseNvidiaGPUs([]byte(tc.output))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(gpus)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestParseNvidiaGPUsInvalidOutput(t *testing.T) {
	for _, output := range []string{
		"Unable to determine the device handle",
		"0, NVIDIA A100, 101, 40960, 1024, 42\n",
		"0, NVIDIA A100, 25, 4, 8, 42\n",
		"0, NVIDIA A100, 25, 8589934592, 0, 42\n",
		"0, NVIDIA A100, 25, 40960, 1024, broken\n",
	} {
		if _, err := parseNvidiaGPUs([]byte(output)); err == nil {
			t.Errorf("accepted invalid output %q", output)
		}
	}
}

func TestNvidiaSMIMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := readNvidiaGPUs(context.Background())
	if got == nil || len(got) != 0 {
		t.Fatalf("expected empty array, got %#v", got)
	}
}
