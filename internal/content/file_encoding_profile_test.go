package content

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

func contentFileEncodingFixture(tb testing.TB, count, bodyBytes int) *MemoryRepository {
	tb.Helper()
	r := NewMemoryRepository(false)
	p, err := r.Create(tb.Context(), PostInput{Title: "ordinary", Slug: "fixture", Status: StatusPublish,
		ContentSource: "<p>plain</p>", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		tb.Fatal(err)
	}
	r.posts, r.revisions = nil, make(map[int64][]Revision)
	for i := range count {
		post := clonePost(p)
		post.ID, post.Slug = int64(i+1), fmt.Sprintf("encoding-%d", i)
		body := fmt.Sprintf("<p>%d needle %s</p>", i, strings.Repeat("plain 中文 ", bodyBytes/13))
		post.ContentSource, post.ContentHTML = body, body
		r.posts = append(r.posts, post)
	}
	r.nextID = int64(count + 1)
	return r
}

// Isolated stages help locate costs; only the mixed load measures complete
// public mutations with concurrent searches and changing revision history.
func BenchmarkContentFileWriteStages(b *testing.B) {
	for _, fixture := range []struct{ count, bytes int }{{1000, 1024}, {10000, 1024}, {1000, 16384}} {
		b.Run(fmt.Sprintf("%d-%d", fixture.count, fixture.bytes), func(b *testing.B) {
			state := contentFileEncodingFixture(b, fixture.count, fixture.bytes)
			payload, err := encodeContentDocument(state)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "content.json")
			if err := writeContentFile(path, payload); err != nil {
				b.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				b.Fatal(err)
			}
			for _, stage := range []string{"check", "clone", "encode", "persist"} {
				b.Run(stage, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						switch stage {
						case "check":
							if err := file.Ready(b.Context()); err != nil {
								b.Fatal(err)
							}
						case "clone":
							runtime.KeepAlive(cloneContentState(file.state))
						case "encode":
							data, err := encodeContentDocument(file.state)
							if err != nil {
								b.Fatal(err)
							}
							runtime.KeepAlive(data)
						case "persist":
							if err := writeContentFile(path, payload); err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}

func TestContentFileEncodingHeapProfile(t *testing.T) {
	directory := os.Getenv("NEWPRESS_CONTENT_ENCODING_PROFILE")
	if directory == "" {
		t.Skip("opt-in encoding heap profiles")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	state := contentFileEncodingFixture(t, 1000, 16384)
	runtime.GC()
	runtime.GC()
	var before, encoded, first, second runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := encodeContentDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	length := len(data)
	runtime.ReadMemStats(&encoded)
	data = nil
	save := func(name string) {
		t.Helper()
		file, err := os.OpenFile(filepath.Join(directory, name+".heap"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.WriteHeapProfile(file)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&first)
	save("first-gc")
	runtime.GC()
	runtime.ReadMemStats(&second)
	save("second-gc")
	result, _ := json.Marshal(map[string]any{"payload_bytes": length, "allocated_bytes": encoded.TotalAlloc - before.TotalAlloc,
		"heap_before": before.HeapAlloc, "heap_encoded": encoded.HeapAlloc, "heap_first_gc": first.HeapAlloc, "heap_second_gc": second.HeapAlloc})
	t.Log("CONTENT_ENCODING_HEAP " + string(result))
	runtime.KeepAlive(state)
}
