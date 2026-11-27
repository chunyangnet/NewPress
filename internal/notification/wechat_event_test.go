package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
)

func TestPublicationObserverMustCommitBeforeAcknowledgement(t *testing.T) {
	s, posts, _, _ := notificationFixture(t)
	post, err := posts.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "同步事件", Slug: "wechat-event", AuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	unavailable := errors.New("queue unavailable")
	calls := 0
	s.BeforeAck = func(_ context.Context, e activity.Event) error {
		calls++
		if e.PostID != post.ID {
			t.Fatal(e)
		}
		return unavailable
	}
	if err = s.Tick(t.Context(), time.Now()); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	pending, err := posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 1 {
		t.Fatal("event lost", pending, err)
	}
	s.BeforeAck = func(context.Context, activity.Event) error { calls++; return nil }
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 0 || calls != 2 {
		t.Fatal("event not acknowledged", pending, err, calls)
	}
}
