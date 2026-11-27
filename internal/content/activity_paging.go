package content

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"time"
)

func (r *MemoryRepository) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return activity.PendingAfter(r.activities, now, limit, after), nil
}

func (r *FileRepository) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PendingActivityAfter(ctx, now, limit, after)
}
