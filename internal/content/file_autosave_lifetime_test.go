package content

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"
)

type autosaveMixedSample struct {
	Operation string  `json:"operation"`
	Millis    float64 `json:"milliseconds"`
}

type autosaveMixedSamples struct {
	mu     sync.Mutex
	rows   []autosaveMixedSample
	counts map[string]int
}

func (samples *autosaveMixedSamples) add(operation string, began time.Time) {
	row := autosaveMixedSample{operation, float64(time.Since(began).Nanoseconds()) / 1e6}
	samples.mu.Lock()
	defer samples.mu.Unlock()
	samples.rows = append(samples.rows, row)
	samples.counts[operation]++
}

func autosaveMixedLog(t *testing.T, phase string, data map[string]any) {
	t.Helper()
	data["phase"], data["pid"] = phase, os.Getpid()
	data["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("AUTOSAVE_MIXED_LIFETIME " + string(raw))
}

func (samples *autosaveMixedSamples) flush(t *testing.T, elapsed time.Duration, writes, cycles int, state *MemoryRepository) {
	samples.mu.Lock()
	rows := samples.rows
	samples.rows = nil
	samples.mu.Unlock()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	autosaveMixedLog(t, "checkpoint", map[string]any{"elapsed_seconds": elapsed.Seconds(), "writes": writes, "cycles": cycles,
		"samples": rows, "heap_bytes": memory.HeapAlloc, "heap_objects": memory.HeapObjects, "heap_inuse": memory.HeapInuse,
		"heap_sys": memory.HeapSys, "heap_released": memory.HeapReleased, "total_allocated_bytes": memory.TotalAlloc,
		"gc_cycles": memory.NumGC, "revision_count": autosaveMixedRevisionCount(state)})
}

func autosaveMixedRevisionCount(state *MemoryRepository) int {
	count := 0
	for _, revisions := range state.revisions {
		count += len(revisions)
	}
	return count
}

func autosaveMixedDigest(t *testing.T, state *MemoryRepository) [32]byte {
	t.Helper()
	hash := sha256.New()
	if err := encodeContentState(hash, state); err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

type autosaveMixedRetained struct {
	state  *MemoryRepository
	digest [32]byte
}

type autosaveMixedSession struct {
	file     *FileRepository
	retained []autosaveMixedRetained
	roots    []weak.Pointer[MemoryRepository]
	writes   int
	samples  autosaveMixedSamples
}

func (session *autosaveMixedSession) retain(t *testing.T) {
	t.Helper()
	if len(session.retained) == 8 {
		old := session.retained[0]
		if autosaveMixedDigest(t, old.state) != old.digest {
			t.Fatal("later mutation changed a retained snapshot")
		}
		copy(session.retained, session.retained[1:])
		session.retained = session.retained[:7]
	}
	state := session.file.state
	session.retained = append(session.retained, autosaveMixedRetained{state, autosaveMixedDigest(t, state)})
}

func (session *autosaveMixedSession) committed(t *testing.T) {
	session.writes++
	session.roots = append(session.roots, weak.Make(session.file.state))
	if session.writes%8 == 0 {
		session.retain(t)
	}
}

func autosaveMixedRootCount(roots []weak.Pointer[MemoryRepository]) int {
	count := 0
	for _, root := range roots {
		if root.Value() != nil {
			count++
		}
	}
	return count
}

func autosaveMixedMemory() map[string]any {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return map[string]any{"heap_bytes": memory.HeapAlloc, "heap_objects": memory.HeapObjects, "heap_inuse": memory.HeapInuse,
		"heap_sys": memory.HeapSys, "heap_released": memory.HeapReleased, "total_allocated_bytes": memory.TotalAlloc, "gc_cycles": memory.NumGC}
}

func (session *autosaveMixedSession) checkRetained(t *testing.T) {
	for _, old := range session.retained {
		if autosaveMixedDigest(t, old.state) != old.digest {
			t.Fatal("mixed workload changed a retained snapshot")
		}
	}
}

func autosaveMixedSearch(ctx context.Context, file *FileRepository, posts, worker int, samples *autosaveMixedSamples, ready *sync.WaitGroup) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for iteration := 0; ; iteration++ {
		began := time.Now()
		page, err := file.SearchPublished(ctx, PublishedQuery{Search: "needle", Limit: 10, Offset: 10 * (worker % 2)})
		if iteration == 0 {
			ready.Done()
		}
		if err != nil {
			if ctx.Err() != nil {
				samples.add("search-cancelled", began)
				return nil
			}
			return err
		}
		samples.add("search", began)
		if page.Total != posts && page.Total != posts-1 || len(page.Items) != 10 {
			return fmt.Errorf("public search membership changed: %d/%d", page.Total, len(page.Items))
		}
		seen := make(map[int64]bool)
		for _, post := range page.Items {
			if seen[post.ID] || post.Status != StatusPublish || !strings.Contains(post.ContentHTML, "needle") ||
				strings.HasPrefix(post.Title, "mixed-") && !strings.Contains(post.ContentHTML, post.Title) {
				return fmt.Errorf("mixed or private public search row: %d", post.ID)
			}
			seen[post.ID] = true
			post.Meta["nested"].([]any)[0] = "caller search mutation"
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (session *autosaveMixedSession) save(t *testing.T, post Post, label string) {
	t.Helper()
	input := Autosave{PostID: post.ID, OwnerKey: "user:100", SourceVersion: post.Version,
		Snapshot: clonePost(post), ExpiresAt: session.file.state.now().Add(24 * time.Hour)}
	input.Snapshot.Title = label
	input.Snapshot.Meta["nested"].([]any)[0] = label
	began := time.Now()
	saved, err := session.file.SaveAutosave(t.Context(), input)
	session.samples.add("autosave", began)
	if err != nil {
		t.Fatal(err)
	}
	session.committed(t)
	input.Snapshot.Meta["nested"].([]any)[0] = "caller input mutation"
	saved.Snapshot.Meta["nested"].([]any)[0] = "caller output mutation"
	got, err := session.file.GetAutosave(t.Context(), post.ID, "user:100")
	if err != nil || got.Snapshot.Title != label || got.Snapshot.Meta["nested"].([]any)[0] != label {
		t.Fatal("recovery input/output isolation failed", err)
	}
	got.Snapshot.Meta["nested"].([]any)[0] = "caller recovery mutation"
}

func autosaveMixedFile(t *testing.T, posts, versions, owners int) *FileRepository {
	t.Helper()
	state := contentHistoryLoadingFixture(t, posts, versions, owners)
	stamp := state.now().Add(time.Hour)
	path := filepath.Join(t.TempDir(), "content.json")
	if _, err := writeContentState(t.Context(), path, state); err != nil {
		t.Fatal(err)
	}
	file, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	file.state.now = func() time.Time { return stamp }
	return file
}

func (session *autosaveMixedSession) cycle(t *testing.T, cycle, posts int) {
	t.Helper()
	id := int64(cycle%min(posts, 32) + 1)
	post, err := session.file.GetByID(t.Context(), id)
	if err != nil || post.Meta["nested"].([]any)[0] != "中文" {
		t.Fatal("canonical metadata changed through a caller alias", err)
	}
	session.save(t, post, "first draft")
	session.save(t, post, "second draft")
	title := fmt.Sprintf("mixed-%d-version-%d", id, post.Version+1)
	input := PostInput{Type: post.Type, Status: StatusPublish, Slug: post.Slug, Title: title,
		ContentSource: "<p>needle " + strings.Repeat("plain 中文 ", 78) + "</p><p>" + title + "</p>",
		AuthorID:      post.AuthorID, AuthorName: post.AuthorName, PublishedAt: post.PublishedAt,
		CommentStatus: post.CommentStatus, CategoryIDs: []int64{1}, Actor: "editor"}
	version := post.Version
	began := time.Now()
	post, err = session.file.Update(t.Context(), id, Mutation{ExpectedVersion: version, Actor: "editor"}, input)
	session.samples.add("update", began)
	if err != nil || post.Version != version+1 {
		t.Fatal("canonical update failed", err)
	}
	session.committed(t)
	if _, err := session.file.GetAutosave(t.Context(), id, "user:100"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatal("canonical update retained an obsolete recovery", err)
	}
	if cycle%4 == 0 {
		for _, status := range []string{StatusPrivate, StatusPublish} {
			began = time.Now()
			post, err = session.file.ChangeStatus(t.Context(), id, Mutation{ExpectedVersion: post.Version, Actor: "editor"}, status)
			session.samples.add("status", began)
			if err != nil {
				t.Fatal(err)
			}
			session.committed(t)
		}
	}
	session.save(t, post, "current draft")
	began = time.Now()
	err = session.file.DeleteAutosave(t.Context(), id, "user:100")
	session.samples.add("delete", began)
	if err != nil {
		t.Fatal(err)
	}
	session.committed(t)
	if _, err := session.file.GetAutosave(t.Context(), id, "user:100"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatal("deleted recovery survived", err)
	}
}

func autosaveMixedFinal(t *testing.T, file *FileRepository, posts, versions, owners, cycles int) map[string]any {
	t.Helper()
	wantRevisions := posts*versions + cycles + 2*((cycles+3)/4)
	if autosaveMixedRevisionCount(file.state) != wantRevisions {
		t.Fatal("mixed workload lost or added a revision")
	}
	for i, post := range file.state.posts {
		changes := 0
		for cycle := i; i < 32 && cycle < cycles; cycle += min(posts, 32) {
			changes++
			if cycle%4 == 0 {
				changes += 2
			}
		}
		if post.Version != int64(versions+changes) || post.Status != StatusPublish {
			t.Fatal("mixed workload lost a canonical version", post.ID, post.Version, changes)
		}
		if changes > 0 && len(file.state.autosaves[post.ID]) != 0 || changes == 0 && len(file.state.autosaves[post.ID]) != owners {
			t.Fatal("mixed workload changed an unrelated owner or retained an obsolete recovery", post.ID)
		}
	}
	current := autosaveMixedDigest(t, file.state)
	disk, err := contentFileDigest(file.path)
	if err != nil || disk != current || file.digest != current || file.searchState.Load() != file.state {
		t.Fatal("published content differs from disk", err)
	}
	reopened, err := NewFileRepository(file.path, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened.state.now = file.state.now
	if autosaveMixedDigest(t, reopened.state) != current {
		t.Fatal("mixed workload differs after a strict file reopen")
	}
	info, err := os.Stat(file.path)
	if err != nil {
		t.Fatal(err)
	}
	cache := file.state.searchBodies
	if cache.bytes > cache.maxBytes || len(cache.entries) > cache.maxEntries {
		t.Fatal("shared search cache exceeded its declared bounds")
	}
	return map[string]any{"file_bytes": info.Size(), "sha256": fmt.Sprintf("%x", current), "revision_count": wantRevisions,
		"cache_entries": len(cache.entries), "cache_text_bytes": cache.bytes}
}

// The session owns all strong snapshot references. Returning only weak roots
// makes the final collection measurement independent of caller stack liveness.
func autosaveMixedRun(t *testing.T, posts, versions, owners, minimumCycles int, duration time.Duration) []weak.Pointer[MemoryRepository] {
	t.Helper()
	session := &autosaveMixedSession{file: autosaveMixedFile(t, posts, versions, owners), samples: autosaveMixedSamples{counts: make(map[string]int)}}
	session.roots = append(session.roots, weak.Make(session.file.state))
	session.retain(t)
	fixture := autosaveMixedDigest(t, session.file.state)
	if page, err := session.file.SearchPublished(t.Context(), PublishedQuery{Search: "needle", Limit: 10}); err != nil || page.Total != posts {
		t.Fatal("mixed load warmup failed", err)
	}
	runtime.GC()
	runtime.GC()
	initialMemory := autosaveMixedMemory()
	autosaveMixedLog(t, "start", map[string]any{"posts": posts, "versions": versions, "owners": owners, "minimum_cycles": minimumCycles,
		"duration_seconds": duration.Seconds(), "fixture_sha256": fmt.Sprintf("%x", fixture), "memory": initialMemory})
	ctx, cancel := context.WithCancel(t.Context())
	var workers, ready sync.WaitGroup
	ready.Add(2)
	failures := make(chan error, 2)
	for worker := range 2 {
		workers.Go(func() {
			if err := autosaveMixedSearch(ctx, session.file, posts, worker, &session.samples, &ready); err != nil {
				failures <- err
			}
		})
	}
	defer func() { cancel(); workers.Wait() }()
	ready.Wait()
	began, checkpoint := time.Now(), time.Now().Add(10*time.Second)
	cycles := 0
	for cycles < minimumCycles || time.Since(began) < duration {
		select {
		case err := <-failures:
			t.Fatal(err)
		default:
		}
		session.cycle(t, cycles, posts)
		cycles++
		if time.Now().After(checkpoint) {
			session.samples.flush(t, time.Since(began), session.writes, cycles, session.file.state)
			checkpoint = time.Now().Add(10 * time.Second)
		}
	}
	elapsed := time.Since(began)
	cancel()
	workers.Wait()
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	session.samples.flush(t, elapsed, session.writes, cycles, session.file.state)
	session.checkRetained(t)
	final := autosaveMixedFinal(t, session.file, posts, versions, owners, cycles)
	if session.writes != cycles*5+2*((cycles+3)/4) || len(session.roots) != session.writes+1 {
		t.Fatal("mixed workload write accounting changed")
	}
	runtime.GC()
	runtime.GC()
	retainedMemory, retainedRoots := autosaveMixedMemory(), autosaveMixedRootCount(session.roots)
	retainedCount := len(session.retained)
	session.retained = nil
	runtime.GC()
	runtime.GC()
	releasedMemory, releasedRoots := autosaveMixedMemory(), autosaveMixedRootCount(session.roots)
	autosaveMixedLog(t, "finished", map[string]any{"elapsed_seconds": elapsed.Seconds(), "cycles": cycles, "writes": session.writes,
		"counts": session.samples.counts, "final": final, "tracked_roots": len(session.roots), "retained_count": retainedCount,
		"retained_roots_alive": retainedRoots, "released_roots_alive": releasedRoots,
		"retained_memory": retainedMemory, "released_memory": releasedMemory})
	runtime.KeepAlive(session.file)
	return session.roots
}

func autosaveMixedCollect(t *testing.T, roots []weak.Pointer[MemoryRepository]) {
	runtime.GC()
	runtime.GC()
	autosaveMixedLog(t, "repository-released", map[string]any{"tracked_roots": len(roots), "roots_alive": autosaveMixedRootCount(roots), "memory": autosaveMixedMemory()})
	runtime.KeepAlive(roots)
}

func TestAutosaveMixedLifetimeIsolation(t *testing.T) {
	roots := autosaveMixedRun(t, 32, 3, 2, 12, 0)
	autosaveMixedCollect(t, roots)
}

func TestAutosaveMixedLifetimeMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_AUTOSAVE_MIXED_LOAD") != "1" {
		t.Skip("opt-in sustained mixed file operations and snapshot lifetime observations")
	}
	seconds := 900
	if value := os.Getenv("NEWPRESS_AUTOSAVE_MIXED_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 3600 {
			t.Fatal("mixed load seconds must be between 1 and 3600")
		}
		seconds = parsed
	}
	roots := autosaveMixedRun(t, 1000, 10, 4, 0, time.Duration(seconds)*time.Second)
	autosaveMixedCollect(t, roots)
}
