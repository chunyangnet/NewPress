package navigation

import (
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

// Readers take one permit; a writer takes all of them. Weighted queues writers
// ahead of later readers and removes cancelled waiters without helper goroutines.
// Keep the zero value usable and the ordinary lock methods for local callers.
type fileRWMutex struct {
	once    sync.Once
	permits *semaphore.Weighted
}

const fileReaderPermits int64 = 1 << 30

func (m *fileRWMutex) acquire(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.once.Do(func() { m.permits = semaphore.NewWeighted(fileReaderPermits) })
	if err := m.permits.Acquire(ctx, n); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		m.permits.Release(n)
		return err
	}
	return nil
}

func (m *fileRWMutex) LockContext(ctx context.Context) error {
	return m.acquire(ctx, fileReaderPermits)
}
func (m *fileRWMutex) RLockContext(ctx context.Context) error { return m.acquire(ctx, 1) }
func (m *fileRWMutex) Lock()                                  { _ = m.LockContext(context.Background()) }
func (m *fileRWMutex) RLock()                                 { _ = m.RLockContext(context.Background()) }
func (m *fileRWMutex) Unlock()                                { m.permits.Release(fileReaderPermits) }
func (m *fileRWMutex) RUnlock()                               { m.permits.Release(1) }
