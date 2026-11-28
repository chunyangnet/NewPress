package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAutosaveNormalizationMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_AUTOSAVE_NORMALIZATION_LOAD") != "1" {
		t.Skip("opt-in autosave normalization allocation and timing measurements")
	}
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for _, shape := range []struct {
		name                 string
		bodyBytes, metaBytes int
	}{
		{"incomplete", 0, 0},
		{"body-1KiB", 1 << 10, 0},
		{"body-64KiB", 64 << 10, 0},
		{"body-1MiB", 1 << 20, 0},
		{"metadata-1MiB", 1 << 10, 1 << 20},
	} {
		t.Run(shape.name, func(t *testing.T) {
			input := Autosave{PostID: 1, SourceVersion: 1, OwnerKey: "user:1", UpdatedAt: stamp, ExpiresAt: stamp.Add(time.Hour),
				Snapshot: Post{ID: 1, Version: 1, ContentSource: strings.Repeat("x", shape.bodyBytes), ContentHTML: "<script>stale()</script>",
					Meta: map[string]any{"nested": []any{"中文😀", json.Number("9007199254740993"), json.Number("0.1234567890123456789")},
						"text": strings.Repeat("y", shape.metaBytes)}}}
			encode := func(value Autosave) []byte {
				payload, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return payload
			}
			original := encode(input)
			var loaded Autosave
			run := func() {
				var err error
				loaded, err = NormalizeAutosave(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			runtime.GC()
			runtime.GC()
			run()
			var before, after, first, second runtime.MemStats
			runtime.ReadMemStats(&before)
			operationsPerSample := 1
			if shape.bodyBytes <= 1<<10 && shape.metaBytes == 0 {
				// Small drafts can finish between two Windows timer ticks.
				// Batch them while retaining the per-operation allocation rate.
				operationsPerSample = 256
			}
			var milliseconds [6]float64
			var elapsedMilliseconds [6]float64
			for i := range milliseconds {
				started := time.Now()
				for range operationsPerSample {
					run()
				}
				elapsedMilliseconds[i] = float64(time.Since(started).Nanoseconds()) / 1e6
				milliseconds[i] = elapsedMilliseconds[i] / float64(operationsPerSample)
			}
			runtime.ReadMemStats(&after)
			runtime.GC()
			runtime.ReadMemStats(&first)
			runtime.GC()
			runtime.ReadMemStats(&second)
			want := input
			want.Snapshot.ContentHTML = sanitizeContent(input.Snapshot.ContentSource)
			if !bytes.Equal(encode(input), original) || !bytes.Equal(encode(loaded), encode(want)) {
				t.Fatal("normalization changed input or recovery data")
			}
			report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": "normalize", "milliseconds": milliseconds,
				"operations": len(milliseconds) * operationsPerSample, "operations_per_sample": operationsPerSample, "elapsed_milliseconds": elapsedMilliseconds,
				"input_bytes": len(original), "fixture_sha256": fmt.Sprintf("%x", sha256.Sum256(original)),
				"result_sha256": fmt.Sprintf("%x", sha256.Sum256(encode(loaded))), "allocated_bytes": after.TotalAlloc - before.TotalAlloc,
				"allocations": after.Mallocs - before.Mallocs, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
				"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
			if err != nil {
				t.Fatal(err)
			}
			t.Log("AUTOSAVE_NORMALIZATION " + string(report))
			runtime.KeepAlive(loaded)
			runtime.KeepAlive(input)
			runtime.KeepAlive(original)
		})
	}
}
