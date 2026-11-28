package content

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func contentFileSnippetFixture(t *testing.T, count, bodyBytes int, nearFileLimit bool) *MemoryRepository {
	t.Helper()
	r := contentFileEncodingFixture(t, 1, 1024)
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	record, err := NextSnippetRecord(SnippetRecord{}, 1, Mutation{}, SnippetInput{Title: "history",
		ContentSource: "<p>" + strings.Repeat("x", bodyBytes) + "</p>", Visibility: SnippetShared}, snippetManager(), stamp)
	if err != nil {
		t.Fatal(err)
	}
	first := record.Revisions[0]
	record.Revisions = make([]SnippetRevision, count)
	for i := range record.Revisions {
		record.Revisions[i] = first
		record.Revisions[i].Version = int64(i + 1)
	}
	record.Version, record.CurrentVersion = int64(count), int64(count)
	r.snippets, r.nextSnippetID, r.contentSchema = []SnippetRecord{record}, 2, 2
	if err := ValidateSnippetState(r.snippets, nil, r.nextSnippetID); err != nil {
		t.Fatal(err)
	}
	if !nearFileLimit {
		return r
	}
	// Fill a valid document to the real file budget minus the existing 64-byte
	// withdrawal reservation. All added posts obey the 2 MiB body limit.
	size, err := writeContentJSON(io.Discard, contentDocumentForEncoding(r), maxContentFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	remaining := maxContentFileBytes - 64 - size
	for remaining > 0 {
		if remaining < 4096 {
			p := &r.posts[0]
			p.ContentSource = "<p>" + strings.Repeat("x", remaining/2) + p.ContentSource[len("<p>"):]
			p.ContentHTML = p.ContentSource
			if remaining%2 != 0 {
				p.Title += "x"
			}
			break
		}
		p := clonePost(r.posts[0])
		p.ID, p.Slug = int64(len(r.posts)+1), fmt.Sprintf("capacity-%d", len(r.posts)+1)
		p.ContentSource, p.ContentHTML = "<p></p>", "<p></p>"
		metadata, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		baseBytes := len(metadata) + 1 // comma before the new post
		baseBytes += len(fmt.Sprint(p.ID+1)) - len(fmt.Sprint(r.nextID))
		padding := min(MaxContentBytes-7, (remaining-baseBytes)/2)
		if padding < 1 {
			t.Fatal("invalid capacity fixture padding", remaining, baseBytes)
		}
		p.ContentSource = "<p>" + strings.Repeat("x", padding) + "</p>"
		p.ContentHTML = p.ContentSource
		used := baseBytes + 2*padding
		if remaining-used == 1 {
			p.Title += "x"
			used++
		}
		r.posts = append(r.posts, p)
		r.nextID = p.ID + 1
		remaining -= used
	}
	r.nextID = int64(len(r.posts) + 1)
	size, err = writeContentJSON(io.Discard, contentDocumentForEncoding(r), maxContentFileBytes)
	if err != nil || size+64 != maxContentFileBytes {
		t.Fatal("fixture does not reach reserved file capacity", size, err)
	}
	return r
}

// Opt-in observations include real writes, full reopen validation and a full
// file-budget refusal. The fixture is loaded from disk before measuring so its
// repeated revision bodies are not shared strings from fixture construction.
func TestContentFileSnippetEncodingMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_CONTENT_SNIPPET_ENCODING_LOAD") != "1" {
		t.Skip("opt-in large snippet history and file capacity measurements")
	}
	for _, shape := range []struct {
		name      string
		count     int
		bodyBytes int
		nearLimit bool
	}{{"many-small", 1, 128, false},
		{"history-10000", MaxSnippetRevisions, 1450, false},
		{"large-revisions", 127, MaxSnippetBodyBytes - 7, false},
		{"file-limit", 127, MaxSnippetBodyBytes - 7, true}} {
		t.Run(shape.name, func(t *testing.T) {
			state := contentFileSnippetFixture(t, shape.count, shape.bodyBytes, shape.nearLimit)
			if shape.name == "many-small" {
				first := state.snippets[0]
				state.snippets = make([]SnippetRecord, MaxSnippets)
				for i := range state.snippets {
					state.snippets[i] = first
					state.snippets[i].ID = int64(i + 1)
				}
				state.nextSnippetID = MaxSnippets + 1
			}
			logicalBytes := SnippetStorageSize(state.snippets[0])
			path := filepath.Join(t.TempDir(), "content.json")
			if _, err := writeContentState(t.Context(), path, state); err != nil {
				t.Fatal(err)
			}
			state = nil
			began := time.Now()
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			openMillis := float64(time.Since(began).Microseconds()) / 1000
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if shape.nearLimit && info.Size()+64 != maxContentFileBytes {
				t.Fatal("initial file capacity differs", info.Size())
			}
			var samples []map[string]any
			for _, operation := range []string{"encode", "withdraw", "withdraw-replay"} {
				runtime.GC()
				runtime.GC()
				var before, after, first, second runtime.MemStats
				runtime.ReadMemStats(&before)
				began := time.Now()
				if operation == "encode" {
					err = encodeContentState(io.Discard, file.state)
				} else {
					_, err = file.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: file.state.snippets[0].Version}, snippetManager())
				}
				elapsed := time.Since(began)
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(operation, err)
				}
				runtime.GC()
				runtime.ReadMemStats(&first)
				runtime.GC()
				runtime.ReadMemStats(&second)
				samples = append(samples, map[string]any{"operation": operation, "milliseconds": float64(elapsed.Microseconds()) / 1000,
					"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
					"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
			}
			if shape.nearLimit || shape.count == MaxSnippetRevisions {
				committed, digest := file.state, file.digest
				if shape.nearLimit {
					_, err = file.Create(t.Context(), PostInput{Title: "over capacity", Slug: "too-large",
						ContentSource: "<p>" + strings.Repeat("x", 64<<10) + "</p>"})
					if !errors.Is(err, errContentFileCapacity) {
						t.Fatal("full file accepted another post", err)
					}
				} else {
					_, err = file.SaveSnippet(t.Context(), 1, Mutation{ExpectedVersion: committed.snippets[0].Version},
						SnippetInput{Title: "new revision", ContentSource: "<p>over count</p>", Visibility: SnippetShared}, snippetManager())
					if !errors.Is(err, ErrSnippetCapacity) {
						t.Fatal("full history accepted another revision", err)
					}
				}
				if file.state != committed || file.searchState.Load() != committed || file.digest != digest {
					t.Fatal("capacity refusal published candidate")
				}
				if err = file.Ready(t.Context()); err != nil {
					t.Fatal("capacity refusal changed disk", err)
				}
				leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".newpress-content-*.tmp"))
				if err != nil || len(leftovers) != 0 {
					t.Fatal("capacity refusal left temporary files", leftovers, err)
				}
			}
			began = time.Now()
			reopened, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			reopenMillis := float64(time.Since(began).Microseconds()) / 1000
			if !reflect.DeepEqual(reopened.state.snippets, file.state.snippets) || !reflect.DeepEqual(reopened.state.posts, file.state.posts) {
				t.Fatal("full history or posts changed on reopen")
			}
			if reopened.state.snippets[0].Visibility != SnippetWithdrawn || len(reopened.state.snippets[0].Revisions) != shape.count {
				t.Fatal("withdrawal or immutable history was lost")
			}
			result, err := json.Marshal(map[string]any{"shape": shape.name, "snippets": len(file.state.snippets), "revisions_per_snippet": shape.count, "revision_body_bytes": shape.bodyBytes + 7,
				"snippet_logical_bytes": logicalBytes, "initial_file_bytes": info.Size(), "posts": len(file.state.posts),
				"open_ms": openMillis, "reopen_ms": reopenMillis, "samples": samples})
			if err != nil {
				t.Fatal(err)
			}
			t.Log("CONTENT_SNIPPET_ENCODING " + string(result))
			runtime.KeepAlive(file)
		})
	}
}
