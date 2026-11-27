package content

import (
	"newpress.local/newpress/internal/activity"
	"testing"
	"time"
)

func TestOrdinaryEditEmitsVersionedUpdateWithoutPublication(t *testing.T) {
	now := time.Now().UTC()
	before := Post{ID: 1, AuthorID: 1, Type: TypePost, Status: StatusPublish, Version: 1, PublishedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)}
	after := before
	after.Version = 2
	after.Title = "Edited title"
	after.UpdatedAt = now
	events := ActivityEvents(before, after)
	if len(events) != 1 || events[0].Kind != activity.ContentUpdated || events[0].Version != 2 {
		t.Fatalf("unexpected edit events: %+v", events)
	}
	if err := events[0].Validate(); err != nil {
		t.Fatal(err)
	}
	if got := ActivityEvents(after, after); len(got) != 0 {
		t.Fatal("unchanged version emitted event", got)
	}
	for _, status := range []string{StatusDraft, StatusPending, StatusPrivate, StatusTrash} {
		after.Status = status
		events = ActivityEvents(before, after)
		if len(events) != 1 || events[0].Kind != activity.ContentUpdated {
			t.Fatal(status, events)
		}
		if err := events[0].Validate(); err != nil {
			t.Fatal(status, err)
		}
	}
}
