package comment

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/audit"
	"time"
)

func ActivityEvents(before, after Comment) []activity.Event {
	result := []activity.Event{}
	if after.Status == StatusApproved && before.Status != StatusApproved {
		e := activity.New(activity.CommentPublic, after.ID, after.PostID, after.UserID, after.Version, after.Status, after.UpdatedAt)
		e.ParentID = after.ParentID
		if after.UserID > 0 {
			e.Mentions = activity.Mentions(after.ContentSource)
		}
		result = append(result, e)
	}
	if before.ID > 0 && before.Status != after.Status && after.UserID > 0 {
		result = append(result, activity.New(activity.CommentModeration, after.ID, after.PostID, after.UserID, after.Version, after.Status, after.UpdatedAt))
	}
	return result
}

func (s *FileStore) PendingActivity(ctx context.Context, now time.Time, limit int) ([]activity.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return activity.Pending(s.activities, now, limit), ctx.Err()
}

func (s *FileStore) AckActivity(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate := activity.Without(s.activities, key)
	if len(candidate) == len(s.activities) {
		return nil
	}
	event := audit.Event{Action: "comment.notification.ack", ObjectType: "comment_registry", ObjectID: "all", Actor: "system", Metadata: map[string]any{"event_count": 1}}
	if err := s.persistActivitiesLocked(s.items, s.revisions, s.nextID, s.updatedAt, &event, candidate); err != nil {
		return err
	}
	s.activities = candidate
	return nil
}
