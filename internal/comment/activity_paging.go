package comment

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"time"
)

func (s *FileStore) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return activity.PendingAfter(s.activities, now, limit, after), nil
}
