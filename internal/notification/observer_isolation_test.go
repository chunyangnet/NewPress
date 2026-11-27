package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

type isolatedActivitySource struct {
	events  []activity.Event
	failure error
}

func (s *isolatedActivitySource) PendingActivity(context.Context, time.Time, int) ([]activity.Event, error) {
	return append([]activity.Event(nil), s.events...), s.failure
}
func (s *isolatedActivitySource) AckActivity(_ context.Context, key string) error {
	s.events = activity.Without(s.events, key)
	return nil
}

func TestObserverFailureStillDeliversQueuedMail(t *testing.T) {
	s, posts, _, sender := notificationFixture(t)
	enableMail(t, s.Store, 2, time.Now().Add(-time.Hour))
	if err := s.Engagement.Set(t.Context(), engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Create(t.Context(), content.PostInput{Title: "Published", Slug: "observer-mail", AuthorID: 1, Status: content.StatusPublish}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("observer queue full")
	s.BeforeAck = func(context.Context, activity.Event) error { return failure }
	if err := s.Tick(t.Context(), time.Now()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(sender.Messages) != 1 {
		t.Fatal("queued mail blocked", len(sender.Messages))
	}
	pending, err := posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 1 {
		t.Fatal("failed observer acknowledged", err)
	}
	s.BeforeAck = nil
	if err := s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 1 {
		t.Fatal("retry duplicated mail")
	}
}

func TestObserverFailureDoesNotBlockOtherEventsOrSources(t *testing.T) {
	s, _, _, _ := notificationFixture(t)
	s.Sender = nil
	now := time.Now().UTC()
	event := func(id int64) activity.Event {
		return activity.New(activity.ContentUpdated, id, id, 1, 2, "draft", now)
	}
	failure := errors.New("observer unavailable")
	sourceFailure := errors.New("source unavailable")
	first := &isolatedActivitySource{events: []activity.Event{event(1), event(2)}}
	broken := &isolatedActivitySource{failure: sourceFailure}
	last := &isolatedActivitySource{events: []activity.Event{event(3)}}
	s.Sources = []activity.Source{first, broken, last}
	calls := 0
	s.BeforeAck = func(_ context.Context, e activity.Event) error {
		calls++
		if e.ObjectID == 1 {
			return failure
		}
		return nil
	}
	err := s.Tick(t.Context(), now)
	if !errors.Is(err, failure) || !errors.Is(err, sourceFailure) {
		t.Fatal("lost failure", err)
	}
	if calls != 3 || len(first.events) != 1 || first.events[0].ObjectID != 1 || len(last.events) != 0 {
		t.Fatal("independent events blocked or failed event lost", calls, first.events, last.events)
	}
	broken.failure = nil
	s.BeforeAck = func(context.Context, activity.Event) error { return nil }
	if err = s.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(first.events) != 0 {
		t.Fatal("failed event not retried")
	}
}

func (s *isolatedActivitySource) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	events, err := s.PendingActivity(ctx, now, limit)
	return activity.PendingAfter(events, now, limit, after), err
}
