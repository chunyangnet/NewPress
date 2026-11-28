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

func TestAutosaveCandidateMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_AUTOSAVE_CANDIDATE_LOAD") != "1" {
		t.Skip("opt-in durable autosave mutations with retained snapshots")
	}
	for _, shape := range []struct {
		name                    string
		posts, versions, owners int
	}{
		{"posts-10000", 10000, 0, 0},
		{"history-1000x10", 1000, 10, 0},
		{"history-1x10000", 1, 10000, 0},
		{"autosaves-1000x4", 1000, 0, 4},
		{"combined-1000x10x4", 1000, 10, 4},
	} {
		t.Run(shape.name, func(t *testing.T) {
			for _, operation := range []string{"save", "delete"} {
				state := contentHistoryLoadingFixture(t, shape.posts, shape.versions, shape.owners)
				stamp := state.now()
				// Seven independent owners let delete warm up and remove one per
				// timed operation without adding saves inside the measured interval.
				if operation == "delete" {
					if state.autosaves[1] == nil {
						state.autosaves[1] = make(map[string]Autosave)
					}
					for i := range 7 {
						key := fmt.Sprintf("user:%d", 100+i)
						state.autosaves[1][key] = Autosave{PostID: 1, OwnerKey: key, SourceVersion: state.posts[0].Version,
							Snapshot: clonePost(state.posts[0]), UpdatedAt: stamp, ExpiresAt: stamp.Add(24 * time.Hour)}
					}
				}
				path := filepath.Join(t.TempDir(), "content.json")
				if _, err := writeContentState(t.Context(), path, state); err != nil {
					t.Fatal(err)
				}
				file, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				file.state.now = func() time.Time { return stamp.Add(time.Hour) }
				state = nil
				initial := file.state
				initialBytes := contentCandidateBytes(t, initial)
				fixtureHash := fmt.Sprintf("%x", sha256.Sum256(initialBytes))
				input := Autosave{PostID: 1, OwnerKey: "user:100", SourceVersion: initial.posts[0].Version,
					Snapshot: clonePost(initial.posts[0]), ExpiresAt: stamp.Add(24 * time.Hour)}
				retained := make([]*MemoryRepository, 0, 8)
				retained = append(retained, initial)
				count := 0
				run := func() {
					if operation == "save" {
						if _, err := file.SaveAutosave(t.Context(), input); err != nil {
							t.Fatal(err)
						}
					} else if err := file.DeleteAutosave(t.Context(), 1, fmt.Sprintf("user:%d", 100+count)); err != nil {
						t.Fatal(err)
					}
					count++
					retained = append(retained, file.state)
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
				if !bytes.Equal(initialBytes, contentCandidateBytes(t, initial)) {
					t.Fatal("autosave mutated the retained initial snapshot")
				}
				for i, snapshot := range retained {
					if snapshot.posts[0].Version != initial.posts[0].Version || len(snapshot.revisions[1]) != len(initial.revisions[1]) {
						t.Fatal("autosave changed canonical versions or revision history")
					}
					if operation == "save" && i > 0 {
						want := stamp.Add(time.Hour).Add(time.Duration(i-1) * time.Nanosecond)
						if !snapshot.autosaves[1]["user:100"].UpdatedAt.Equal(want) {
							t.Fatal("save overwrote an earlier retained recovery")
						}
					} else if operation == "delete" {
						if len(snapshot.autosaves[1]) != len(initial.autosaves[1])-i {
							t.Fatal("delete overwrote an earlier retained owner map")
						}
					}
				}
				loaded, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				loaded.state.now = file.state.now
				finalBytes := contentCandidateBytes(t, file.state)
				disk, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(finalBytes, disk) || !bytes.Equal(finalBytes, contentCandidateBytes(t, loaded.state)) {
					t.Fatal("final state differs from durable reopen", err)
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "milliseconds": milliseconds,
					"operations": len(milliseconds), "retained_states": len(retained), "fixture_sha256": fixtureHash,
					"result_sha256": fmt.Sprintf("%x", sha256.Sum256(finalBytes)), "file_bytes_before": len(initialBytes), "file_bytes_after": len(finalBytes),
					"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
					"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("AUTOSAVE_CANDIDATE " + string(report))
				runtime.KeepAlive(retained)
				runtime.KeepAlive(initialBytes)
				runtime.KeepAlive(file)
			}
		})
	}
}
