package plugin

import (
	"context"
	"crypto/sha256"
	"sync"

	"github.com/tetratelabs/wazero"
)

const (
	maxBusinessCompilationCaches = 16
	// This bounds the source bytes admitted to the cache. Compiled code has
	// additional overhead, so this is not a bound on total process memory.
	maxBusinessCachedModuleBytes = 32 << 20
)

var businessCompilations = newBusinessCompilationCaches(maxBusinessCompilationCaches, maxBusinessCachedModuleBytes)

type businessCompilationKey struct {
	root       string
	slug       string
	generation uint64
	digest     [sha256.Size]byte
}

// Each cache contains one guest binary for the fixed interpreter/ABI/memory
// configuration in executeBusinessCached. Runtime instances and host callbacks
// remain request-local; only guest compilation results survive a request.
type businessCompilationEntry struct {
	key      businessCompilationKey
	cache    wazero.CompilationCache
	bytes    int64
	users    int
	lastUsed uint64
	retired  bool

	compileGate chan struct{}
	anchor      wazero.CompiledModule
}

type businessCompilationCaches struct {
	mu         sync.Mutex
	entries    map[businessCompilationKey]*businessCompilationEntry
	clock      uint64
	bytes      int64
	maxEntries int
	maxBytes   int64
}

func newBusinessCompilationCaches(maxEntries int, maxBytes int64) *businessCompilationCaches {
	return &businessCompilationCaches{
		entries:    make(map[businessCompilationKey]*businessCompilationEntry),
		maxEntries: maxEntries, maxBytes: maxBytes,
	}
}

// acquire must run under the manager lifecycle lock. That prevents an old
// generation from adding an entry after its invalidation. A nil lease uses a
// private runtime cache, preserving execution when every cache slot is busy.
func (c *businessCompilationCaches) acquire(root, slug string, generation uint64, code []byte) *businessCompilationEntry {
	if c.maxEntries <= 0 || int64(len(code)) > c.maxBytes {
		return nil
	}
	key := businessCompilationKey{root: root, slug: slug, generation: generation, digest: sha256.Sum256(code)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil {
		if entry.retired {
			return nil
		}
		c.clock++
		entry.lastUsed = c.clock
		entry.users++
		return entry
	}
	for len(c.entries) >= c.maxEntries || c.bytes+int64(len(code)) > c.maxBytes {
		var oldest *businessCompilationEntry
		for _, entry := range c.entries {
			if entry.users == 0 && (oldest == nil || entry.lastUsed < oldest.lastUsed) {
				oldest = entry
			}
		}
		if oldest == nil {
			return nil
		}
		c.remove(oldest)
	}
	c.clock++
	entry := &businessCompilationEntry{
		key: key, cache: wazero.NewCompilationCache(), bytes: int64(len(code)),
		users: 1, lastUsed: c.clock,
		compileGate: make(chan struct{}, 1),
	}
	c.entries[key] = entry
	c.bytes += entry.bytes
	return entry
}

// release follows runtime.Close, so an evicted cache cannot invalidate a live
// module. Retired entries still count toward both capacity limits until then.
func (c *businessCompilationCaches) release(entry *businessCompilationEntry) {
	if entry == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.users--
	if entry.users == 0 && entry.retired {
		c.remove(entry)
	}
}

func (c *businessCompilationCaches) invalidate(root, slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.key.root == root && entry.key.slug == slug {
			entry.retired = true
			if entry.users == 0 {
				c.remove(entry)
			}
		}
	}
}

// remove is called with c.mu held and no runtime using the entry.
func (c *businessCompilationCaches) remove(entry *businessCompilationEntry) {
	delete(c.entries, entry.key)
	c.bytes -= entry.bytes
	_ = entry.cache.Close(context.Background())
	entry.cache = nil
	entry.anchor = nil
}

func (entry *businessCompilationEntry) compile(ctx context.Context, runtime wazero.Runtime, code []byte) (wazero.CompiledModule, bool, error) {
	// Cold compilation can outlast a waiting request. Let each waiter honor
	// its own deadline instead of retaining an execution slot behind a mutex.
	select {
	case entry.compileGate <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-entry.compileGate
		return nil, false, err
	}
	if entry.anchor == nil {
		// Keep one compiled reference until eviction. Wazero reference-counts
		// CompileModule cache hits; subsequent calls close their own reference.
		compiled, err := runtime.CompileModule(ctx, code)
		if err == nil {
			entry.anchor = compiled
		}
		<-entry.compileGate
		return compiled, false, err
	}
	<-entry.compileGate
	compiled, err := runtime.CompileModule(ctx, code)
	return compiled, err == nil, err
}
