package community

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"time"
)

type Source struct {
	Store
	Enabled func() bool
}

func (s *Source) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	if s.Enabled != nil && !s.Enabled() {
		return nil, nil
	}
	return s.Store.PendingActivity(ctx, now, limit)
}
