package systeminfo

import "testing"

func TestProcCPUExcludesDuplicateGuestTime(t *testing.T) {
	value := parseCPU("cpu 100 20 30 400 50 6 7 8 90 10\ncpu0 1 2 3 4\n")
	if !value.valid || value.total != 621 || value.idle != 450 {
		t.Fatalf("incorrect Linux counters: %+v", value)
	}
	for _, input := range []string{"", "cpu0 1 2 3 4", "cpu 1 2 3", "cpu 1 broken 3 4", "cpu 18446744073709551615 2 3 4", "cpu -1 2 3 4"} {
		if parseCPU(input).valid {
			t.Fatalf("malformed /proc/stat accepted: %q", input)
		}
	}
}

func TestProcMemoryUsesAvailableRatherThanFree(t *testing.T) {
	memory := parseMemory("MemTotal: 100000 kB\nMemFree: 1000 kB\nMemAvailable: 45000 kB\nCached: 44000 kB\n")
	if memory == nil || memory.TotalBytes != 100000*1024 || memory.AvailableBytes != 45000*1024 {
		t.Fatalf("incorrect Linux memory: %+v", memory)
	}
	for _, input := range []string{"", "MemTotal: 1 kB\nMemFree: 1 kB", "MemTotal: 0 kB\nMemAvailable: 0 kB", "MemTotal: 1 kB\nMemAvailable: 2 kB", "MemTotal: 18446744073709551615 kB\nMemAvailable: 1 kB", "MemTotal: 1 MB\nMemAvailable: 1 kB"} {
		if parseMemory(input) != nil {
			t.Fatalf("unavailable /proc/meminfo must remain unknown: %q", input)
		}
	}
	if m := parseMemory("MemTotal: 100 kB\nMemAvailable: 0 kB"); m == nil || m.AvailableBytes != 0 {
		t.Fatal("measured zero is valid")
	}
}
