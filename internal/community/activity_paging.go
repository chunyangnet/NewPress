package community

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
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	var page []activity.Event
	for _, circle := range s.circles {
		candidates := activity.PendingAfter(circle.Activities, now, limit, after)
		page = activity.PendingAfter(append(page, candidates...), now, limit, after)
	}
	return page, nil
}

func (s *Source) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	if s.Enabled != nil && !s.Enabled() {
		return nil, nil
	}
	return s.Store.PendingActivityAfter(ctx, now, limit, after)
}
