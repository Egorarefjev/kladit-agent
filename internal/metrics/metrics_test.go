package metrics

import "testing"

func TestCPUUsage(t *testing.T) {
	before, err := parseCPU("cpu 100 0 20 500 10 0 0 0 90 0\n")
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseCPU("cpu 130 0 30 550 20 0 0 0 120 0\n")
	if err != nil {
		t.Fatal(err)
	}
	got, err := cpuUsage(before, after)
	if err != nil || got != 40 {
		t.Fatalf("got %v, %v; want 40", got, err)
	}
	if _, err := cpuUsage(after, before); err == nil {
		t.Fatal("decreasing counters accepted")
	}
	if _, err := cpuUsage(before, before); err == nil {
		t.Fatal("unchanged counters accepted")
	}
}

func TestInvalidCPU(t *testing.T) {
	for _, input := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 bad 4 5 6 7 8"} {
		if _, err := parseCPU(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestMemory(t *testing.T) {
	total, available, err := parseMemory("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\n")
	if err != nil || total != 1024000 || available != 409600 {
		t.Fatalf("got %d, %d, %v", total, available, err)
	}
	for _, input := range []string{"", "MemTotal: 1000 kB", "MemTotal: 1 kB\nMemAvailable: 2 kB", "MemTotal: 1 MB\nMemAvailable: 0 kB", "MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB"} {
		if _, _, err := parseMemory(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
