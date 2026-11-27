package systeminfo

import (
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCPUCounterDeltas(t *testing.T) {
	for _, test := range []struct {
		name          string
		before, after cpuCounters
		want          *float64
	}{
		{"first observation", cpuCounters{}, cpuCounters{200, 100, true}, nil},
		{"half busy", cpuCounters{100, 50, true}, cpuCounters{200, 100, true}, new(float64(50))},
		{"fully idle", cpuCounters{100, 50, true}, cpuCounters{200, 150, true}, new(float64(0))},
		{"fully busy", cpuCounters{100, 50, true}, cpuCounters{200, 50, true}, new(float64(100))},
		{"reset counters", cpuCounters{200, 100, true}, cpuCounters{100, 50, true}, nil},
		{"unchanged counters", cpuCounters{100, 50, true}, cpuCounters{100, 50, true}, nil},
		{"invalid idle", cpuCounters{100, 50, true}, cpuCounters{110, 65, true}, nil},
		{"missing observation", cpuCounters{100, 50, true}, cpuCounters{}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := cpuPercent(test.before, test.after)
			if (got == nil) != (test.want == nil) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
			if got != nil && *got != *test.want {
				t.Fatalf("got %f, want %f", *got, *test.want)
			}
		})
	}
}

func TestCollectorNativeSnapshotAndMissingDisk(t *testing.T) {
	var c Collector
	first := c.Collect(t.TempDir())
	if first.CPUPercent != nil {
		t.Fatal("first CPU observation must not fabricate a percentage")
	}
	if first.OS != runtime.GOOS || first.Architecture != runtime.GOARCH || first.LogicalCPUs < 1 || first.CollectedAt.IsZero() {
		t.Fatalf("invalid platform snapshot: %+v", first)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		if first.Memory == nil || first.Disk == nil || first.ProcessRSSBytes == nil || *first.ProcessRSSBytes == 0 || first.UptimeSeconds == nil || *first.UptimeSeconds < 0 {
			t.Fatalf("native resource readings unavailable: %+v", first)
		}
	}
	missing := c.Collect(filepath.Join(t.TempDir(), "not-present"))
	if missing.Disk != nil {
		t.Fatal("an invalid data path must not borrow statistics from another disk")
	}
	if c.Collect("").Disk != nil {
		t.Fatal("an unset path must remain unavailable")
	}
	// Exercise concurrent callers and prove all returned percentages are bounded.
	c.previousAt = time.Now().Add(-2 * time.Second)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			result := c.Collect("")
			if p := result.CPUPercent; p != nil && (math.IsNaN(*p) || *p < 0 || *p > 100) {
				t.Errorf("invalid percentage: %f", *p)
			}
		})
	}
	wg.Wait()
}
