package notification

import (
	"context"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/maildelivery"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testUsers map[int64]auth.User

func (u testUsers) GetUser(_ context.Context, id int64) (auth.User, error) {
	if user, ok := u[id]; ok {
		return user, nil
	}
	return auth.User{}, auth.ErrUserNotFound
}
func (u testUsers) FindUserByUsername(_ context.Context, name string) (auth.User, error) {
	for _, user := range u {
		if strings.EqualFold(user.Username, name) {
			return user, nil
		}
	}
	return auth.User{}, auth.ErrUserNotFound
}

type testSender struct {
	Messages []maildelivery.Message
	Err      error
}

func (s *testSender) SendMessage(_ context.Context, message maildelivery.Message) error {
	s.Messages = append(s.Messages, message)
	return s.Err
}
func notificationFixture(t *testing.T) (*Service, *content.MemoryRepository, *comment.FileStore, *testSender) {
	t.Helper()
	now := time.Now().UTC()
	users := testUsers{}
	for id, name := range map[int64]string{1: "writer", 2: "reader", 3: "third-reader"} {
		users[id] = auth.User{ID: id, Username: name, DisplayName: name, Status: auth.UserStatusActive, Email: name + "@example.test", EmailVerifiedAt: &now}
	}
	posts := content.NewMemoryRepository(false)
	comments, err := comment.NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	eng, _ := engagement.NewFileStore("")
	store, _ := NewFileStore("")
	sender := &testSender{}
	s := &Service{Store: store, Users: users, Content: posts, Comments: comments, Engagement: eng, Sender: sender, Sources: []activity.Source{posts, comments}, Settings: func() Settings {
		return Settings{Title: "通知测试", PublicURL: "https://example.test", Timezone: "Asia/Shanghai"}
	}}
	return s, posts, comments, sender
}
func enableMail(t *testing.T, s Store, user int64, at time.Time) {
	t.Helper()
	p, err := s.Preferences(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	p.Email = true
	if _, err = s.SavePreferences(t.Context(), p, at); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionRepliesMentionsAndVisibility(t *testing.T) {
	s, posts, comments, sender := notificationFixture(t)
	ctx := t.Context()
	enableMail(t, s.Store, 2, time.Now().Add(-time.Hour))
	enableMail(t, s.Store, 3, time.Now().Add(-time.Hour))
	if err := s.Engagement.Set(ctx, engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
		t.Fatal(err)
	}
	p, err := posts.Create(ctx, content.PostInput{Title: "公开文章", Slug: "public-event", AuthorID: 1, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := comments.Create(ctx, comment.CreateInput{PostID: p.ID, UserID: 2, AuthorName: "reader", AuthorEmail: "reader@example.test", ContentSource: "提问", Status: comment.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := comments.Create(ctx, comment.CreateInput{PostID: p.ID, ParentID: parent.ID, UserID: 1, AuthorName: "writer", AuthorEmail: "writer@example.test", ContentSource: "回答，@reader；也请 @third-reader 看看。", Status: comment.StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 1 {
		t.Fatalf("pending comment leaked: %d", len(sender.Messages))
	}
	reply, err = comments.ChangeStatus(ctx, reply.ID, comment.Mutation{ExpectedVersion: reply.Version, Actor: "moderator"}, comment.StatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	reader, err := s.Store.Notices(ctx, 2, Query{})
	if err != nil || len(reader.Items) != 2 {
		t.Fatal(reader, err)
	}
	third, _ := s.Store.Notices(ctx, 3, Query{})
	if len(third.Items) != 1 || third.Items[0].Kind != Mention {
		t.Fatal(third)
	}
	owner, _ := s.Store.Notices(ctx, 1, Query{})
	if len(owner.Items) != 1 || owner.Items[0].Kind != Moderation {
		t.Fatal(owner)
	}
	if len(sender.Messages) != 3 {
		t.Fatalf("reply plus mention duplicated recipient: %d", len(sender.Messages))
	}
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 3 {
		t.Fatal("processed mail sent twice")
	}
	if _, err = posts.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	for _, n := range append(reader.Items, third.Items...) {
		view, visible, err := s.Resolve(ctx, n.Delivery, time.Now())
		if err != nil || visible || view.Title != "" || view.URL != "" {
			t.Fatal("private content leaked", view, err)
		}
	}
}

func TestDeliveryRechecksPrivateContentAndMailPreferences(t *testing.T) {
	s, posts, _, sender := notificationFixture(t)
	ctx := t.Context()
	enableMail(t, s.Store, 2, time.Now().Add(-time.Hour))
	if err := s.Engagement.Set(ctx, engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
		t.Fatal(err)
	}
	p, err := posts.Create(ctx, content.PostInput{Title: "秘密标题", Slug: "withdraw-before-mail", AuthorID: 1, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := posts.PendingActivity(ctx, time.Now(), 50)
	if len(events) != 1 {
		t.Fatal(events)
	}
	if err = s.project(ctx, events[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = posts.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	job, ok, err := s.Store.ClaimMail(ctx, time.Now())
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.deliver(ctx, job, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 0 {
		t.Fatal("private title sent")
	}
	p, err = posts.Create(ctx, content.PostInput{Title: "已取消订阅", Slug: "unsubscribe-before-mail", AuthorID: 1, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	e := content.ActivityEvents(content.Post{}, p)[0]
	if err = s.project(ctx, e, time.Now()); err != nil {
		t.Fatal(err)
	}
	prefs, _ := s.Store.Preferences(ctx, 2)
	if err = s.Store.Unsubscribe(ctx, 2, prefs.UnsubscribeKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	job, ok, err = s.Store.ClaimMail(ctx, time.Now())
	if err != nil || ok {
		t.Fatal(err)
	}
	if len(sender.Messages) != 0 {
		t.Fatal("opt-out ignored")
	}
}

func TestAmbiguousSMTPResultAndWeeklyWindow(t *testing.T) {
	s, posts, _, sender := notificationFixture(t)
	ctx := t.Context()
	enableMail(t, s.Store, 2, time.Now().Add(-time.Hour))
	sender.Err = maildelivery.ErrUncertain
	if err := s.Engagement.Set(ctx, engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Create(ctx, content.PostInput{Title: "公开", Slug: "ambiguous-acceptance", AuthorID: 1, Status: content.StatusPublish}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, _ := s.Store.MailStatus(ctx, 2)
	if status.Uncertain != 1 {
		t.Fatal(status)
	}
	if err := s.Tick(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(sender.Messages) != 1 {
		t.Fatal("ambiguous message automatically resent")
	}
	end := WeekEnd(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), "America/New_York")
	if end.Sub(end.AddDate(0, 0, -7)) != 167*time.Hour {
		t.Fatal("weekly boundary ignored DST", end)
	}
	end = WeekEnd(time.Now(), "Asia/Shanghai")
	for i, fixture := range []struct {
		title, status string
		at            time.Time
	}{{"应该出现在周报", content.StatusPublish, end.AddDate(0, 0, -2)}, {"本周还未结算", content.StatusPublish, end.Add(time.Hour)}, {"旧文章", content.StatusPublish, end.AddDate(0, 0, -8)}, {"秘密不能出现", content.StatusPrivate, end.AddDate(0, 0, -3)}} {
		if _, err := posts.Create(ctx, content.PostInput{Title: fixture.title, Slug: fmtSlug(i), AuthorID: 1, Status: fixture.status, PublishedAt: fixture.at}); err != nil {
			t.Fatal(err)
		}
	}
	job := Job{Delivery: Delivery{Kind: Weekly, Event: activity.Event{At: end.AddDate(0, 0, -7), Due: end}}}
	prefs := Defaults(2)
	prefs.WeeklySince = job.Event.At
	body, ok, err := s.weeklyBody(ctx, job, prefs, s.Settings())
	if err != nil || !ok || !strings.Contains(body, "应该出现在周报") || strings.Contains(body, "本周还未结算") || strings.Contains(body, "旧文章") || strings.Contains(body, "秘密不能出现") {
		t.Fatal(body, err)
	}
	prefs.WeeklySince = end
	_, ok, err = s.weeklyBody(ctx, job, prefs, s.Settings())
	if err != nil || ok {
		t.Fatal("included pre-subscription content", err)
	}
}
func fmtSlug(i int) string {
	return []string{"digest-included", "digest-current", "digest-old", "digest-private"}[i]
}

func TestWeekEndFallsBackToUTCNotHostLocal(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("host-local", 14*60*60)
	t.Cleanup(func() { time.Local = oldLocal })
	// Sunday in UTC is already Monday on the simulated host.
	now := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	want := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, zone := range []string{"", "Local", " Local ", "not/a-zone", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			if got := WeekEnd(now, zone); !got.Equal(want) || got.Location() != time.UTC {
				t.Fatalf("WeekEnd(%q) = %v in %v, want %v in UTC", zone, got, got.Location(), want)
			}
		})
	}
	if got := WeekEnd(now, " Asia/Shanghai "); got.Format("2006-01-02 15:04 -0700") != "2026-10-26 00:00 +0800" {
		t.Fatalf("explicit timezone was not normalized: %v", got)
	}
}

func TestWeeklyDSTWindowAndDedupSurviveReloadAndTimezoneChange(t *testing.T) {
	for _, tc := range []struct {
		name, now, start, end, key string
		hours                      int
	}{
		{"spring-167-hours", "2026-03-09T13:00:00Z", "2026-03-02T00:00:00-05:00", "2026-03-09T00:00:00-04:00", "weekly:2:20260309T0400", 167},
		{"autumn-169-hours", "2026-11-02T14:00:00Z", "2026-10-26T00:00:00-04:00", "2026-11-02T00:00:00-05:00", "weekly:2:20261102T0500", 169},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, posts, _, sender := notificationFixture(t)
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			settings := Settings{Title: "原站点", PublicURL: "https://old.example.test", Timezone: "America/New_York"}
			settingsCalls := 0
			s.Settings = func() Settings { settingsCalls++; return settings }
			end := WeekEnd(now, settings.Timezone)
			start := end.AddDate(0, 0, -7)
			if start.Format(time.RFC3339) != tc.start || end.Format(time.RFC3339) != tc.end || end.Sub(start) != time.Duration(tc.hours)*time.Hour {
				t.Fatalf("natural week = [%v, %v), want [%s, %s), %d hours", start, end, tc.start, tc.end, tc.hours)
			}
			path := filepath.Join(t.TempDir(), "notifications.json")
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Store = store
			p := Defaults(2)
			p.Weekly = true
			if _, err := store.SavePreferences(ctx, p, start); err != nil {
				t.Fatal(err)
			}
			// No mail before 09:00 local; at 09:00 queue the completed natural week.
			if err := s.queueWeekly(ctx, now.Add(-time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			if settingsCalls != 1 || len(store.state.Jobs) != 0 {
				t.Fatalf("queued before local 09:00 or reread settings: calls=%d jobs=%d", settingsCalls, len(store.state.Jobs))
			}
			for range 2 {
				settingsCalls = 0
				if err := s.queueWeekly(ctx, now); err != nil {
					t.Fatal(err)
				}
				if settingsCalls != 1 || len(store.state.Jobs) != 1 {
					t.Fatalf("weekly queue snapshot/dedup: calls=%d jobs=%d", settingsCalls, len(store.state.Jobs))
				}
			}
			checkWindow := func(store *FileStore) {
				t.Helper()
				j, ok := store.state.Jobs[tc.key]
				if !ok || len(store.state.Jobs) != 1 || j.Key != tc.key || !j.Event.At.Equal(start) || !j.Event.Due.Equal(end) || j.Event.At.Format(time.RFC3339) != tc.start || j.Event.Due.Format(time.RFC3339) != tc.end {
					t.Fatalf("frozen weekly interval/key changed: %+v, jobs=%d", j, len(store.state.Jobs))
				}
			}
			checkWindow(store)
			settings = Settings{Title: "更新后的站点", PublicURL: "https://new.example.test", Timezone: "Asia/Shanghai"}
			store, err = NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Store = store
			checkWindow(store)
			newZone, err := time.LoadLocation(settings.Timezone)
			if err != nil {
				t.Fatal(err)
			}
			// The same boundary represented in the new zone retains its UTC key;
			// dedup must not recompute the already-stored DST interval.
			if err := store.QueueWeekly(ctx, 2, end.In(newZone), now); err != nil {
				t.Fatal(err)
			}
			checkWindow(store)
			for _, post := range []struct {
				title, slug string
				at          time.Time
			}{
				{"早于窗口", "before-window", start.Add(-time.Nanosecond)},
				{"窗口起点", "window-start", start},
				{"窗口末尾", "window-last", end.Add(-time.Nanosecond)},
				{"下周起点", "after-window", end},
			} {
				if _, err := posts.Create(ctx, content.PostInput{Title: post.title, Slug: post.slug, AuthorID: 1, Status: content.StatusPublish, PublishedAt: post.at}); err != nil {
					t.Fatal(err)
				}
			}
			settingsCalls = 0
			s.Settings = func() Settings {
				settingsCalls++
				if settingsCalls == 1 {
					return settings
				}
				return Settings{Title: "不应混入", PublicURL: "https://reread.example.test", Timezone: "Pacific/Kiritimati"}
			}
			job, ok, err := store.ClaimMail(ctx, now)
			if err != nil || !ok {
				t.Fatalf("claim persisted weekly job: ok=%v err=%v", ok, err)
			}
			if err := s.deliver(ctx, job, now); err != nil {
				t.Fatal(err)
			}
			if settingsCalls != 1 || len(sender.Messages) != 1 {
				t.Fatalf("weekly delivery settings calls=%d messages=%d", settingsCalls, len(sender.Messages))
			}
			message := sender.Messages[0]
			if message.Subject != settings.Title+" · 每周精选" || !strings.Contains(message.Body, "窗口起点") || !strings.Contains(message.Body, "窗口末尾") || strings.Contains(message.Body, "早于窗口") || strings.Contains(message.Body, "下周起点") || strings.Contains(message.Body, "reread.example.test") || strings.Contains(message.Body, "old.example.test") || !strings.Contains(message.Body, settings.PublicURL+"/posts/window-start") || !strings.Contains(message.Body, settings.PublicURL+"/account/?view=notifications") {
				t.Fatalf("delivery did not use the frozen interval and one current settings snapshot: %+v", message)
			}
			store, err = NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.QueueWeekly(ctx, 2, end.In(newZone), now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			checkWindow(store)
			if _, ok, err := store.ClaimMail(ctx, now.Add(time.Hour)); err != nil || ok {
				t.Fatalf("completed weekly job was duplicated after reload: ok=%v err=%v", ok, err)
			}
		})
	}
}
