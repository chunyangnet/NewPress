package questions

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"time"
)

func (s *FileStore) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	state, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return activity.PendingAfter(state.Events, now, limit, after), nil
}
