package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestContentHistoryLoadingMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_CONTENT_HISTORY_LOADING_LOAD") != "1" {
		t.Skip("opt-in complete history decoding and file reopening")
	}
	for _, shape := range []struct {
		name                    string
		posts, versions, owners int
	}{
		{"posts-10000", 10000, 0, 0},
		{"history-1000x10", 1000, 10, 0},
		{"history-1x10000", 1, 10000, 0},
		{"autosaves-1000x4", 1000, 0, 4},
		{"autosaves-1x4000", 1, 0, 4000},
		{"combined-1000x10x4", 1000, 10, 4},
	} {
		t.Run(shape.name, func(t *testing.T) {
			source := contentHistoryLoadingFixture(t, shape.posts, shape.versions, shape.owners)
			path := filepath.Join(t.TempDir(), "content.json")
			if _, err := writeContentState(t.Context(), path, source); err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fixtureHash := fmt.Sprintf("%x", sha256.Sum256(payload))
			for _, operation := range []string{"decode", "open"} {
				var loaded *MemoryRepository
				run := func() {
					var err error
					if operation == "decode" {
						loaded, err = decodeContentDocument(payload)
					} else {
						var file *FileRepository
						file, err = NewFileRepository(path, false)
						if err == nil {
							loaded = file.state
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				runtime.GC()
				runtime.GC()
				run()
				var before, after, first, second runtime.MemStats
				runtime.ReadMemStats(&before)
				var milliseconds [6]float64
				for i := range milliseconds {
					started := time.Now()
					run()
					milliseconds[i] = float64(time.Since(started).Nanoseconds()) / 1e6
				}
				runtime.ReadMemStats(&after)
				runtime.GC()
				runtime.ReadMemStats(&first)
				runtime.GC()
				runtime.ReadMemStats(&second)
				loaded.now = source.now
				if !bytes.Equal(payload, contentCandidateBytes(t, loaded)) || !bytes.Equal(payload, contentCandidateBytes(t, source)) {
					t.Fatal("loading changed complete histories or original state")
				}
				disk, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(disk, payload) || fmt.Sprintf("%x", sha256.Sum256(payload)) != fixtureHash {
					t.Fatal("loading changed input bytes or file", err)
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "milliseconds": milliseconds,
					"operations": len(milliseconds), "file_bytes": len(payload), "fixture_sha256": fixtureHash,
					"posts": shape.posts, "revisions": shape.posts * shape.versions, "autosaves": shape.posts * shape.owners,
					"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
					"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("CONTENT_HISTORY_LOADING " + string(report))
				runtime.KeepAlive(loaded)
			}
			runtime.KeepAlive(source)
			runtime.KeepAlive(payload)
		})
	}
}
