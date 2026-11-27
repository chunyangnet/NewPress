package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
)

func TestContentUpdateWaitsForObserverWithoutSendingNotifications(t *testing.T) {
	s, posts, _, sender := notificationFixture(t)
	input := content.PostInput{Title: "Draft", Slug: "observer-update", AuthorID: 1, Status: content.StatusDraft}
	p, err := posts.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Title = "Edited draft"
	if _, err = posts.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, input); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("durable enqueue unavailable")
	calls := 0
	s.BeforeAck = func(_ context.Context, event activity.Event) error {
		calls++
		if event.Kind != activity.ContentUpdated {
			t.Fatal("unexpected event", event.Kind)
		}
		return failure
	}
	if err = s.Tick(t.Context(), time.Now()); !errors.Is(err, failure) {
		t.Fatal("observer failure swallowed", err)
	}
	pending, err := posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 1 {
		t.Fatal("source acknowledged before durable enqueue", err)
	}
	s.BeforeAck = func(context.Context, activity.Event) error { calls++; return nil }
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 0 || calls != 2 {
		t.Fatal("source retry not acknowledged", err, calls)
	}
	if len(sender.Messages) != 0 {
		t.Fatal("edit sent mail")
	}
	for _, id := range []int64{1, 2, 3} {
		notices, err := s.Store.Notices(t.Context(), id, Query{})
		if err != nil || len(notices.Items) != 0 {
			t.Fatal("edit created notification", id, err)
		}
	}
}
