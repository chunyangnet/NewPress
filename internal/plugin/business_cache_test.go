package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func cleanupBusinessCompilations(t testing.TB, caches *businessCompilationCaches) {
	t.Helper()
	t.Cleanup(func() {
		caches.mu.Lock()
		defer caches.mu.Unlock()
		for _, entry := range caches.entries {
			if entry.users != 0 {
				t.Errorf("compilation lease leaked: %s has %d users", entry.key.slug, entry.users)
			}
			caches.remove(entry)
		}
	})
}

func TestBusinessCompilationCacheReuseAndBounds(t *testing.T) {
	caches := newBusinessCompilationCaches(2, 6)
	cleanupBusinessCompilations(t, caches)
	first := caches.acquire("root", "first", 1, []byte("aaa"))
	again := caches.acquire("root", "first", 1, []byte("aaa"))
	if first == nil || again != first {
		t.Fatal("identical module did not share its compilation cache")
	}
	caches.release(again)
	second := caches.acquire("root", "second", 1, []byte("bb"))
	if second == nil {
		t.Fatal("second module was not cached")
	}
	if overflow := caches.acquire("root", "third", 1, []byte("c")); overflow != nil {
		t.Fatal("busy caches exceeded entry limit")
	}
	caches.release(first)
	caches.release(second)
	// Refresh first, making second the least recently used entry.
	again = caches.acquire("root", "first", 1, []byte("aaa"))
	caches.release(again)
	third := caches.acquire("root", "third", 1, []byte("ccc"))
	if third == nil || first.cache == nil || second.cache != nil {
		t.Fatal("least recently used idle cache was not evicted")
	}
	if len(caches.entries) > 2 || caches.bytes > 6 {
		t.Fatal("cache capacity exceeded")
	}
	caches.release(third)
	if oversized := caches.acquire("root", "large", 1, []byte("1234567")); oversized != nil {
		t.Fatal("oversized module was cached")
	}
}

func TestBusinessCompilationInvalidationRetainsActiveLeases(t *testing.T) {
	caches := newBusinessCompilationCaches(3, 100)
	cleanupBusinessCompilations(t, caches)
	active := caches.acquire("root", "plugin", 1, []byte("old"))
	otherRoot := caches.acquire("other-root", "plugin", 1, []byte("old"))
	otherSlug := caches.acquire("root", "other-plugin", 1, []byte("old"))
	caches.invalidate("root", "plugin")
	if active.cache == nil || otherRoot.retired || otherSlug.retired {
		t.Fatal("invalidation closed an active cache or affected another installation")
	}
	if retired := caches.acquire("root", "plugin", 1, []byte("old")); retired != nil {
		t.Fatal("retired generation admitted another lease")
	}
	if overflow := caches.acquire("root", "plugin", 2, []byte("new")); overflow != nil {
		t.Fatal("retired active entry did not count toward the capacity limit")
	}
	caches.release(active)
	if active.cache != nil {
		t.Fatal("retired cache survived its last lease")
	}
	current := caches.acquire("root", "plugin", 2, []byte("new"))
	if current == nil || current == active {
		t.Fatal("new generation did not get a fresh cache")
	}
	caches.release(current)
	caches.release(otherRoot)
	caches.release(otherSlug)
	caches.invalidate("root", "plugin")
	if current.cache != nil || otherRoot.cache == nil || otherSlug.cache == nil {
		t.Fatal("idle invalidation did not close only the matching installation")
	}
}

func TestBusinessCompilationCacheKeysIncludeGenerationAndCode(t *testing.T) {
	caches := newBusinessCompilationCaches(4, 100)
	cleanupBusinessCompilations(t, caches)
	first := caches.acquire("root", "plugin", 1, []byte("old"))
	replaced := caches.acquire("root", "plugin", 1, []byte("new"))
	generation := caches.acquire("root", "plugin", 2, []byte("old"))
	if first == replaced || first == generation || replaced == generation {
		t.Fatal("different module bytes or generations reused a cache")
	}
	caches.release(first)
	caches.release(replaced)
	caches.release(generation)
}

func TestBusinessCompilationCacheByteBudget(t *testing.T) {
	caches := newBusinessCompilationCaches(8, 4)
	cleanupBusinessCompilations(t, caches)
	first := caches.acquire("root", "first", 1, []byte("aaa"))
	if first == nil {
		t.Fatal("module within byte budget was not cached")
	}
	if overflow := caches.acquire("root", "second", 1, []byte("bb")); overflow != nil {
		t.Fatal("busy module allowed the byte budget to be exceeded")
	}
	caches.release(first)
	second := caches.acquire("root", "second", 1, []byte("bb"))
	if second == nil || first.cache != nil || caches.bytes != 2 {
		t.Fatal("idle module was not evicted to satisfy the byte budget")
	}
	caches.release(second)
}

func TestBusinessCompilationWaitHonorsDeadline(t *testing.T) {
	caches := newBusinessCompilationCaches(1, 100)
	cleanupBusinessCompilations(t, caches)
	entry := caches.acquire("root", "plugin", 1, []byte("code"))
	defer caches.release(entry)
	// Model a cold compile already in progress. A waiting request must return
	// on cancellation even though the first compiler has not finished.
	entry.compileGate <- struct{}{}
	defer func() { <-entry.compileGate }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := entry.compile(ctx, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting compile returned %v", err)
		}
	case <-time.After(time.Second):
		t.Error("waiting compile ignored its deadline")
		// Unblock a broken implementation before releasing its cache lease.
		<-entry.compileGate
		<-done
		entry.compileGate <- struct{}{}
	}
}

func loadBusinessCacheFixture(t testing.TB) (Manifest, []byte) {
	t.Helper()
	source := filepath.Join("..", "..", "examples", "plugins", "content-age")
	manifest, err := readManifest(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join(source, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return manifest, code
}

func TestBusinessCachedExecutionIsolatesHostsAndMemory(t *testing.T) {
	manifest, code := loadBusinessCacheFixture(t)
	caches := newBusinessCompilationCaches(2, maxBusinessCachedModuleBytes)
	cleanupBusinessCompilations(t, caches)
	input := []byte(`{"event":"admin.action","payload":{"action":"preview"}}`)
	check := func(id int) {
		entry := caches.acquire("root", manifest.Slug, 1, code)
		if entry == nil {
			t.Error("missing cache lease")
			return
		}
		defer caches.release(entry)
		title := fmt.Sprintf("request-%d", id)
		calls := 0
		host := func(context.Context, []byte) (any, error) {
			calls++
			return map[string]any{"days": 180, "title": title, "message": title}, nil
		}
		out, err := executeBusinessCached(t.Context(), code, manifest, "np_handle", input, host, entry)
		if err != nil || calls != 1 || out.Fragment == nil || out.Fragment.Title != title || out.Fragment.Text != title {
			t.Errorf("request %d received another host or memory state: %+v, calls=%d, err=%v", id, out, calls, err)
		}
	}
	// First exercise cold-cache contention, then reuse it after all runtimes close.
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Go(func() { check(i) })
	}
	workers.Wait()
	check(16)
	entry := caches.acquire("root", manifest.Slug, 1, code)
	defer caches.release(entry)
	out, err := executeBusinessCached(t.Context(), code, manifest, "np_handle", input, nil, entry)
	if err != nil || out.Fragment != nil {
		t.Fatal("previous request host survived runtime closure", out, err)
	}
}

func TestBusinessCachedExecutionPreservesTimeouts(t *testing.T) {
	manifest := Manifest{Slug: "hostile", Hooks: map[string][]string{"content.render": {"np_handle"}}}
	loop := []byte{0x03, 0x40, 0x0c, 0, 0x0b, 0x42, 0, 0x0b}
	for _, start := range []bool{false, true} {
		t.Run(fmt.Sprintf("start=%v", start), func(t *testing.T) {
			caches := newBusinessCompilationCaches(1, maxBusinessCachedModuleBytes)
			cleanupBusinessCompilations(t, caches)
			code := limitModule(loop, start)
			for range 2 {
				entry := caches.acquire("root", manifest.Slug, 1, code)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
				began := time.Now()
				_, err := executeBusinessCached(ctx, code, manifest, "np_handle", []byte(`{}`), nil, entry)
				cancel()
				caches.release(entry)
				if err == nil || time.Since(began) > 2*time.Second {
					t.Fatal("cached hostile execution was not interrupted", err)
				}
			}
		})
	}
}

func TestBusinessLifecycleInvalidatesCompilation(t *testing.T) {
	manager, manifest, code := businessFixture(t)
	if _, err := manager.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action"}, nil); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	entry := businessCompilations.acquire(manager.root, manifest.Slug, manager.generationID(manifest.Slug), code)
	manager.mu.Unlock()
	if entry == nil || entry.anchor == nil {
		t.Fatal("Dispatch did not populate the compilation cache")
	}
	businessCompilations.release(entry)
	if err := manager.SetEnabled(manifest.Slug, false); err != nil {
		t.Fatal(err)
	}
	if entry.cache != nil {
		t.Fatal("disabled plugin retained a compilation cache")
	}
}

func BenchmarkBusinessExecution(b *testing.B) {
	manifest, code := loadBusinessCacheFixture(b)
	input := []byte(`{"event":"admin.action","payload":{"action":"preview"}}`)
	host := func(context.Context, []byte) (any, error) {
		return map[string]any{"days": 180, "title": "Benchmark", "message": "Benchmark"}, nil
	}
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := executeBusiness(b.Context(), code, manifest, "np_handle", input, host); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		caches := newBusinessCompilationCaches(1, maxBusinessCachedModuleBytes)
		cleanupBusinessCompilations(b, caches)
		entry := caches.acquire("root", manifest.Slug, 1, code)
		_, err := executeBusinessCached(b.Context(), code, manifest, "np_handle", input, host, entry)
		caches.release(entry)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			entry = caches.acquire("root", manifest.Slug, 1, code)
			_, err = executeBusinessCached(b.Context(), code, manifest, "np_handle", input, host, entry)
			caches.release(entry)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
