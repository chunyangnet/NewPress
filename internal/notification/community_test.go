package notification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/community"
)

func TestCommunityDurableNotificationsPrivacyMailAndRetry(t *testing.T) {
	s, _, _, sender := notificationFixture(t)
	store, _ := community.NewFileStore("")
	s.Community = store
	s.Sources = []activity.Source{store}
	now := time.Now().UTC()
	sequence := 0
	apply := func(user int64, cmd community.Command) community.Result {
		t.Helper()
		sequence++
		cmd.Key = fmt.Sprintf("notice-circle-key-%016d", sequence)
		if c, e := store.Get(t.Context(), "private"); e == nil {
			cmd.Version = c.Version
		}
		r, e := store.Apply(t.Context(), "private", community.Actor{ID: user, Admin: user == 1}, cmd, now)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	apply(1, community.Command{Action: "create", Name: "私密圈", JoinPolicy: "open", Visibility: "members", OwnerID: 1})
	apply(2, community.Command{Action: "join"})
	enableMail(t, s.Store, 2, now.Add(-time.Hour))
	topic := apply(1, community.Command{Action: "topic", Title: "私密标题", Body: "@third-reader 请看看", Kind: "question"})
	failure := errors.New("after projection failure")
	s.BeforeAck = func(context.Context, activity.Event) error { return failure }
	if err := s.Tick(t.Context(), now); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	notices, err := s.Store.Notices(t.Context(), 2, Query{})
	if err != nil || len(notices.Items) != 1 {
		t.Fatal(notices, err)
	}
	other, _ := s.Store.Notices(t.Context(), 3, Query{})
	if len(other.Items) != 0 {
		t.Fatal("mention leaked private circle", other)
	}
	s.BeforeAck = nil
	if err = s.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	notices, _ = s.Store.Notices(t.Context(), 2, Query{})
	if len(notices.Items) != 1 || len(sender.Messages) != 1 || !strings.Contains(sender.Messages[0].Body, "私密标题") {
		t.Fatal("replay duplicated or lost mail", notices, sender.Messages)
	}
	if pending, _ := store.PendingActivity(t.Context(), now, 100); len(pending) != 0 {
		t.Fatal("event not acknowledged")
	}
	s.Sender = nil
	apply(2, community.Command{Action: "reply", TopicID: topic.TopicID, Body: "我的问题"})
	apply(1, community.Command{Action: "reply", TopicID: topic.TopicID, Body: "新的私密答复"})
	if err = s.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	apply(2, community.Command{Action: "leave"})
	notices, _ = s.Store.Notices(t.Context(), 2, Query{})
	for _, notice := range notices.Items {
		if _, visible, e := s.Resolve(t.Context(), notice.Delivery, now); e != nil || visible {
			t.Fatal("left member retained access", e)
		}
	}
	s.Sender = sender
	if err = s.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 1 {
		t.Fatal("private mail sent after leaving", sender.Messages)
	}
	s.CommunityEnabled = func() bool { return false }
	if _, visible, e := s.Resolve(t.Context(), notices.Items[0].Delivery, now); e != nil || visible {
		t.Fatal("disabled module leaks", e)
	}
}

func TestCommunityMentionPreferenceTakesPrecedence(t *testing.T) {
	s, _, _, sender := notificationFixture(t)
	store, _ := community.NewFileStore("")
	s.Community, s.Sources = store, []activity.Source{store}
	now := time.Now().UTC()
	prefs, err := s.Store.Preferences(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	prefs.Publication, prefs.Reply, prefs.Mention, prefs.Email = false, false, true, true
	if _, err = s.Store.SavePreferences(t.Context(), prefs, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	owner, reader := community.Actor{ID: 1, Admin: true}, community.Actor{ID: 2}
	apply := func(a community.Actor, cmd community.Command) community.Result {
		t.Helper()
		if c, e := store.Get(t.Context(), "mentions"); e == nil {
			cmd.Version = c.Version
		}
		result, e := store.Apply(t.Context(), "mentions", a, cmd, now)
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	apply(owner, community.Command{Action: "create", Key: "mentions-create-key", Name: "提及偏好", JoinPolicy: "open", Visibility: "members", OwnerID: 1})
	apply(reader, community.Command{Action: "join", Key: "mentions-reader-join"})
	topic := apply(owner, community.Command{Action: "topic", Key: "mentions-topic-key", Title: "明确提及", Body: "@reader 请补充", Kind: "discussion"})
	apply(reader, community.Command{Action: "reply", Key: "mentions-reader-reply", TopicID: topic.TopicID, Body: "已参与"})
	apply(owner, community.Command{Action: "reply", Key: "mentions-writer-reply", TopicID: topic.TopicID, Body: "@reader 再看一下"})
	if err = s.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	notices, err := s.Store.Notices(t.Context(), 2, Query{})
	if err != nil || len(notices.Items) != 2 {
		t.Fatal("mention preferences lost", notices, err)
	}
	for _, notice := range notices.Items {
		if notice.Kind != Mention {
			t.Fatal("mention deduped as general update", notice.Kind)
		}
	}
	if len(sender.Messages) != 2 {
		t.Fatal("mention mail did not follow enabled preference", len(sender.Messages))
	}
}
