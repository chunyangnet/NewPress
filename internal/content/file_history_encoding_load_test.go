package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestContentHistoryEncodingMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_CONTENT_HISTORY_LOAD") != "1" {
		t.Skip("opt-in ordered history encoding and complete file writes")
	}
	for _, shape := range []struct {
		name                     string
		posts, revisions, owners int
		expireHalf               bool
	}{
		{"posts-10000", 10000, 0, 0, false},
		{"history-1000x10", 1000, 10, 0, false},
		{"history-1x10000", 1, 10000, 0, false},
		{"autosaves-1000x4", 1000, 0, 4, false},
		{"autosaves-half-expired", 1000, 0, 4, true},
	} {
		t.Run(shape.name, func(t *testing.T) {
			stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
			state := contentFileEncodingFixture(t, shape.posts, 1024)
			state.now = func() time.Time { return stamp }
			for i := range state.terms {
				state.terms[i].CreatedAt, state.terms[i].UpdatedAt = stamp, stamp
			}
			for i := range state.posts {
				p := &state.posts[i]
				p.CreatedAt, p.UpdatedAt, p.PublishedAt = stamp, stamp, stamp
				for _, terms := range [][]Term{p.Categories, p.Tags} {
					for j := range terms {
						terms[j].CreatedAt, terms[j].UpdatedAt = stamp, stamp
					}
				}
				p.Meta = map[string]any{"nested": []any{"one", json.Number("9007199254740993")}}
				p.Version = int64(max(1, shape.revisions))
				for version := range shape.revisions {
					snapshot := clonePost(*p)
					snapshot.Version = int64(version + 1)
					state.revisions[p.ID] = append(state.revisions[p.ID], Revision{ID: state.nextRevisionID, PostID: p.ID,
						Version: snapshot.Version, Snapshot: snapshot, Actor: "editor", CreatedAt: stamp})
					state.nextRevisionID++
				}
				if shape.owners > 0 {
					state.autosaves[p.ID] = map[string]Autosave{}
				}
				for owner := range shape.owners {
					key := fmt.Sprintf("user:%d", owner+1)
					expires := stamp.Add(4 * time.Hour)
					if shape.expireHalf && owner%2 == 0 {
						expires = stamp.Add(time.Hour)
					}
					state.autosaves[p.ID][key] = Autosave{PostID: p.ID, OwnerKey: key, SourceVersion: p.Version,
						Snapshot: clonePost(*p), UpdatedAt: stamp, ExpiresAt: expires}
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
			state = nil
			file.state.now = func() time.Time { return stamp }
			disk, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(disk, contentCandidateBytes(t, file.state)) {
				t.Fatal("initial file changed during full decode", err)
			}
			fileBytes, fixtureHash := len(disk), fmt.Sprintf("%x", sha256.Sum256(disk))
			disk = nil
			file.state.now = func() time.Time { return stamp.Add(2 * time.Hour) }
			retained := file.state
			before := historyEncodingSourceBytes(t, retained)
			for _, operation := range []string{"encode", "write"} {
				count := 0
				run := func() {
					if operation == "encode" {
						if err := encodeContentState(io.Discard, retained); err != nil {
							t.Fatal(err)
						}
						return
					}
					_, err := file.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: int64(max(1, shape.revisions) + count), Actor: "editor"},
						[]string{StatusPrivate, StatusPublish}[count%2])
					if err != nil {
						t.Fatal(err)
					}
					count++
				}
				runtime.GC()
				runtime.GC()
				run()
				var start, end, first, second runtime.MemStats
				runtime.ReadMemStats(&start)
				var milliseconds [6]float64
				for i := range milliseconds {
					began := time.Now()
					run()
					milliseconds[i] = float64(time.Since(began).Nanoseconds()) / 1e6
				}
				runtime.ReadMemStats(&end)
				runtime.GC()
				runtime.ReadMemStats(&first)
				runtime.GC()
				runtime.ReadMemStats(&second)
				if !bytes.Equal(before, historyEncodingSourceBytes(t, retained)) {
					t.Fatal("encoding or committing changed retained history")
				}
				if !bytes.Equal(contentCandidateBytes(t, retained), historyEncodingExpected(t, retained)) ||
					!bytes.Equal(contentCandidateBytes(t, file.state), historyEncodingExpected(t, file.state)) {
					t.Fatal("ordered bytes differ from the standard full document")
				}
				if operation == "write" {
					reopened, err := NewFileRepository(path, false)
					if err != nil {
						t.Fatal(err)
					}
					reopened.state.now = file.state.now
					if !bytes.Equal(contentCandidateBytes(t, reopened.state), contentCandidateBytes(t, file.state)) ||
						file.state.posts[0].Version != int64(max(1, shape.revisions)+7) || len(file.state.revisions[1]) != shape.revisions+7 {
						t.Fatal("new versions or complete history did not survive reopen")
					}
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "milliseconds": milliseconds,
					"operations": len(milliseconds), "file_bytes_before": fileBytes, "fixture_sha256": fixtureHash,
					"posts": shape.posts, "revisions": shape.posts * shape.revisions, "autosaves": shape.posts * shape.owners,
					"allocated_bytes": end.TotalAlloc - start.TotalAlloc, "heap_before": start.HeapAlloc, "heap_after": end.HeapAlloc,
					"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": end.NumGC - start.NumGC})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("CONTENT_HISTORY_ENCODING " + string(report))
			}
			runtime.KeepAlive(retained)
			runtime.KeepAlive(file)
		})
	}
}
