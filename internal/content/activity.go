package content

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"time"
)

// ActivityEvents contains identifiers only; delivery resolves current public
// content again. Ordinary edits do not invalidate an unprocessed publication.
func ActivityEvents(before, after Post) []activity.Event {
	result := []activity.Event{}
	if after.AuthorID <= 0 {
		return result
	}
	public := func(p Post) bool { return p.Status == StatusPublish || p.Status == StatusFuture }
	if after.Type == TypePost && public(after) && (before.ID == 0 || !public(before) || before.Type != after.Type || before.AuthorID != after.AuthorID || !before.PublishedAt.Equal(after.PublishedAt)) {
		e := activity.New(activity.Publication, after.ID, after.ID, after.AuthorID, after.Version, after.Status, after.UpdatedAt)
		if after.PublishedAt.After(e.Due) {
			e.Due = after.PublishedAt
		}
		result = append(result, e)
	}
	if before.Status == StatusPending && after.Status != StatusPending {
		result = append(result, activity.New(activity.PostModeration, after.ID, after.ID, after.AuthorID, after.Version, after.Status, after.UpdatedAt))
	}
	if before.ID > 0 && after.Version > before.Version {
		result = append(result, activity.New(activity.ContentUpdated, after.ID, after.ID, after.AuthorID, after.Version, after.Status, after.UpdatedAt))
	}
	return result
}

func (r *MemoryRepository) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return activity.Pending(r.activities, now, limit), ctx.Err()
}
func (r *MemoryRepository) AckActivity(ctx context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.activities = activity.Without(r.activities, key)
	return nil
}
func (r *FileRepository) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PendingActivity(ctx, now, limit)
}
func (r *FileRepository) AckActivity(ctx context.Context, key string) error {
	_, err := fileContentChange(ctx, r, func(state *MemoryRepository) (struct{}, error) { return struct{}{}, state.AckActivity(ctx, key) })
	return err
}
