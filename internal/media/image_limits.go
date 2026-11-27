package media

import "sync"

// imageProcessingLimiter admits independent images without queuing decoded
// buffers. Pixel and compressed-byte budgets are shared across all workers so
// the former single-image input allowance does not multiply with concurrency.
// Encoder buffers and Go runtime overhead still require additional memory.
type imageProcessingLimiter struct {
	mu     sync.Mutex
	limit  int
	active map[int64]struct{}
	pixels int64
	bytes  int64
}

func (l *imageProcessingLimiter) tryAcquire(item Item) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.active[item.ID]; exists || len(l.active) >= l.limit {
		return false
	}
	pixels := int64(item.Width) * int64(item.Height)
	if pixels <= 0 || item.SizeBytes <= 0 || pixels > MaxProcessPixels-l.pixels || item.SizeBytes > MaxProcessBytes-l.bytes {
		return false
	}
	l.active[item.ID] = struct{}{}
	l.pixels += pixels
	l.bytes += item.SizeBytes
	return true
}

func (l *imageProcessingLimiter) release(item Item) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, item.ID)
	l.pixels -= int64(item.Width) * int64(item.Height)
	l.bytes -= item.SizeBytes
}
