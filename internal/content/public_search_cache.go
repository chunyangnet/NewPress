package content

import (
	"container/list"
	"crypto/sha256"
	"strings"
	"sync"
)

const (
	maxPublicSearchBodies    = 10000
	maxPublicSearchBodyBytes = 16 << 20
)

// Cache only a pure projection of sanitized HTML. Visibility, metadata, scores
// and query results always come from the current repository snapshot. Content
// addressing also allows file transaction clones to share the cache safely.
type publicSearchBodyCache struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]*list.Element
	recent     list.List
	bytes      int
	maxEntries int
	maxBytes   int
}

type publicSearchBodyEntry struct {
	digest [sha256.Size]byte
	text   string
}

func newPublicSearchBodyCache() *publicSearchBodyCache {
	return &publicSearchBodyCache{maxEntries: maxPublicSearchBodies, maxBytes: maxPublicSearchBodyBytes}
}

func (cache *publicSearchBodyCache) body(contentHTML string) string {
	if cache == nil || cache.maxEntries <= 0 || cache.maxBytes <= 0 {
		return PublicSearchBody(contentHTML)
	}
	digest := publicSearchBodyDigest(contentHTML)
	cache.mu.Lock()
	if entry := cache.entries[digest]; entry != nil {
		cache.recent.MoveToFront(entry)
		text := entry.Value.(publicSearchBodyEntry).text
		cache.mu.Unlock()
		return text
	}
	cache.mu.Unlock()

	// Parsing happens outside the cache lock. Concurrent misses may duplicate
	// this work, but they cannot block hits for unrelated bodies while parsing.
	text := PublicSearchBody(contentHTML)
	if len(text) > cache.maxBytes {
		return text
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if entry := cache.entries[digest]; entry != nil {
		cache.recent.MoveToFront(entry)
		return entry.Value.(publicSearchBodyEntry).text
	}
	for len(cache.entries) >= cache.maxEntries || cache.bytes+len(text) > cache.maxBytes {
		oldest := cache.recent.Back()
		entry := oldest.Value.(publicSearchBodyEntry)
		delete(cache.entries, entry.digest)
		cache.bytes -= len(entry.text)
		cache.recent.Remove(oldest)
	}
	if cache.entries == nil {
		cache.entries = make(map[[sha256.Size]byte]*list.Element)
	}
	// The extractor's builder can reserve the whole HTML even for a short or
	// empty projection. Retain only the text bytes charged to the cache budget.
	text = strings.Clone(text)
	cache.entries[digest] = cache.recent.PushFront(publicSearchBodyEntry{digest, text})
	cache.bytes += len(text)
	return text
}

func publicSearchBodyDigest(body string) [sha256.Size]byte {
	// Converting a large string to []byte for Sum256 copies the whole body on
	// every hit. A fixed scratch buffer keeps hashing allocation independent
	// of article length without retaining or using unsafe views of its bytes.
	hash := sha256.New()
	var scratch [4096]byte
	for len(body) > 0 {
		n := copy(scratch[:], body)
		hash.Write(scratch[:n])
		body = body[n:]
	}
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest
}
