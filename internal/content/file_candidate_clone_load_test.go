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

func TestContentCandidateCloneMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_CONTENT_CANDIDATE_LOAD") != "1" {
		t.Skip("opt-in full candidate cloning and durable mutations")
	}
	for _, shape := range []struct {
		name            string
		posts, versions int
	}{
		{"posts-10000", 10000, 0},
		{"posts-1000-history-10", 1000, 10},
		{"post-1-history-10000", 1, 10000},
		{"snippets-1000", 1, 0},
	} {
		t.Run(shape.name, func(t *testing.T) {
			state := contentFileEncodingFixture(t, shape.posts, 1024)
			stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
			state.now = func() time.Time { return stamp.Add(time.Hour) }
			for i := range state.terms {
				state.terms[i].CreatedAt, state.terms[i].UpdatedAt = stamp, stamp
			}
			for i := range state.posts {
				post := &state.posts[i]
				post.CreatedAt, post.UpdatedAt, post.PublishedAt = stamp, stamp, stamp
				for _, terms := range [][]Term{post.Categories, post.Tags} {
					for j := range terms {
						terms[j].CreatedAt, terms[j].UpdatedAt = stamp, stamp
					}
				}
				post.Meta = map[string]any{"nested": map[string]any{"labels": []any{"one", "two"}}}
				post.Version = int64(max(1, shape.versions))
				for version := range shape.versions {
					snapshot := clonePost(*post)
					snapshot.Version = int64(version + 1)
					state.revisions[post.ID] = append(state.revisions[post.ID], Revision{ID: state.nextRevisionID,
						PostID: post.ID, Version: snapshot.Version, Snapshot: snapshot, Actor: "editor", CreatedAt: post.UpdatedAt})
					state.nextRevisionID++
				}
			}
			if shape.name == "snippets-1000" {
				fixture := contentFileSnippetFixture(t, 1, 128, false)
				state.snippets = make([]SnippetRecord, MaxSnippets)
				for i := range state.snippets {
					state.snippets[i] = fixture.snippets[0]
					state.snippets[i].ID = int64(i + 1)
				}
				state.contentSchema, state.nextSnippetID = 2, MaxSnippets+1
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if _, err := writeContentState(t.Context(), path, state); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			state = nil // Measure independently decoded maps and slices.
			file.state.now = func() time.Time { return stamp.Add(time.Hour) }
			retained := file.state
			beforeBytes := contentCandidateBytes(t, retained)
			diskBytes, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(diskBytes, beforeBytes) {
				t.Fatal("fixture changed when fully decoded", err)
			}
			fixtureHash := fmt.Sprintf("%x", sha256.Sum256(diskBytes))
			diskBytes = nil
			for _, operation := range []string{"clone", "write"} {
				var last *MemoryRepository
				count := 0
				run := func() {
					if operation == "clone" {
						last = cloneContentState(retained)
						return
					}
					_, err := file.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: int64(max(1, shape.versions) + count), Actor: "editor"},
						[]string{StatusPrivate, StatusPublish}[count%2])
					if err != nil {
						t.Fatal(err)
					}
					count++
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
				if !bytes.Equal(beforeBytes, contentCandidateBytes(t, retained)) || last != nil && !bytes.Equal(beforeBytes, contentCandidateBytes(t, last)) {
					t.Fatal("clone or mutation changed the retained snapshot")
				}
				reopened, err := NewFileRepository(path, false)
				if err != nil || !bytes.Equal(contentCandidateBytes(t, file.state), contentCandidateBytes(t, reopened.state)) {
					t.Fatal("committed state differs after reopen", err)
				}
				if operation == "write" && (file.state.posts[0].Version != int64(max(1, shape.versions)+7) || len(file.state.revisions[1]) != shape.versions+7) {
					t.Fatal("missing canonical versions or revisions")
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "milliseconds": milliseconds,
					"operations": len(milliseconds), "file_bytes_before": len(beforeBytes), "fixture_sha256": fixtureHash, "posts": len(retained.posts), "revisions": shape.posts * shape.versions,
					"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
					"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("CONTENT_CANDIDATE_CLONE " + string(report))
				runtime.KeepAlive(last)
			}
			runtime.KeepAlive(retained)
			runtime.KeepAlive(file)
		})
	}
}
