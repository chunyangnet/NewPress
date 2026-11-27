package virtualcurrency

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type rewardUsers struct {
	users map[int64]auth.User
	err   error
}

func (u *rewardUsers) GetUser(_ context.Context, id int64) (auth.User, error) {
	if u.err != nil {
		return auth.User{}, u.err
	}
	user, ok := u.users[id]
	if !ok {
		return user, auth.ErrUserNotFound
	}
	return user, nil
}

type rewardContent struct {
	content.Repository
	post content.Post
	err  error
}

func (r *rewardContent) GetByID(_ context.Context, id int64) (content.Post, error) {
	if r.err != nil {
		return content.Post{}, r.err
	}
	if id != r.post.ID {
		return content.Post{}, content.ErrNotFound
	}
	return r.post, nil
}

type rewardFailure struct {
	Store
	err error
}

func TestRewardServiceFanoutRetriesEachCurrencyIndependently(t *testing.T) {
	service, main, _, _, cfg, event := rewardServiceFixture(t)
	catalog := NewMemoryCatalog(main)
	createTestCurrency(t, catalog, "gold")
	goldStore, _ := catalog.Store(t.Context(), "gold")
	gold := goldStore.(*FileStore)
	gold.now = func() time.Time { return main.now().Add(-time.Hour) }
	goldCfg := cfg
	goldCfg.Name, goldCfg.Symbol = "金币", "枚"
	if _, err := gold.UpdateSettings(t.Context(), 1, goldCfg); err != nil {
		t.Fatal(err)
	}
	gold.now = main.now
	failure := &rewardFailure{Store: gold, err: errors.New("temporary currency outage")}
	catalog.stores["gold"] = failure
	service.Catalog = catalog
	if err := service.Observe(t.Context(), event); err == nil {
		t.Fatal("partly failed publication was acknowledged")
	}
	if balance, _ := main.Balance(t.Context(), 1); balance != cfg.PublicationReward {
		t.Fatal(balance)
	}
	if balance, _ := gold.Balance(t.Context(), 1); balance != 0 {
		t.Fatal(balance)
	}
	failure.err = nil
	for range 2 {
		if err := service.Observe(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	for _, store := range []Store{main, gold} {
		if balance, _ := store.Balance(t.Context(), 1); balance != cfg.PublicationReward {
			t.Fatal("duplicate or missing currency reward", balance)
		}
	}
	catalog.stores[DefaultCurrencyKey] = &rewardFailure{Store: main, err: errors.New("main unavailable")}
	if err := service.RegistrationAll(t.Context(), 2); err == nil {
		t.Fatal("failed registration hidden")
	}
	if balance, _ := gold.Balance(t.Context(), 2); balance != cfg.RegistrationReward {
		t.Fatal("main failure skipped another currency", balance)
	}
	catalog.stores[DefaultCurrencyKey] = main
	for range 2 {
		if err := service.RegistrationAll(t.Context(), 2); err != nil {
			t.Fatal(err)
		}
	}
	for _, store := range []Store{main, gold} {
		if balance, _ := store.Balance(t.Context(), 2); balance != cfg.RegistrationReward {
			t.Fatal(balance)
		}
	}
}

func (r *rewardFailure) Reward(ctx context.Context, in RewardInput) (RewardResult, error) {
	if r.err != nil {
		return RewardResult{}, r.err
	}
	return r.Store.Reward(ctx, in)
}

func rewardServiceFixture(t *testing.T) (*Service, *FileStore, *rewardUsers, *rewardContent, Settings, activity.Event) {
	t.Helper()
	now := time.Now().Add(-time.Second)
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(-time.Hour) }
	cfg := DefaultSettings()
	cfg.Enabled, cfg.CheckInEnabled, cfg.RegistrationEnabled, cfg.PublicationEnabled = true, true, true, true
	cfg.PublicationMinLength = 4
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }
	users := &rewardUsers{users: map[int64]auth.User{1: {ID: 1, Status: auth.UserStatusActive, CreatedAt: now.Add(-time.Minute), EmailVerifiedAt: &now}, 2: {ID: 2, Status: auth.UserStatusActive, CreatedAt: now.Add(-time.Minute), EmailVerifiedAt: &now}}}
	posts := &rewardContent{post: content.Post{ID: 10, AuthorID: 1, Type: content.TypePost, Status: content.StatusPublish, CreatedAt: now.Add(-time.Minute), PublishedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Version: 1, ContentHTML: "<p>这是正文</p>"}}
	event := activity.New(activity.Publication, 10, 10, 1, 1, content.StatusPublish, posts.post.PublishedAt)
	return &Service{Store: store, Users: users, Content: posts}, store, users, posts, cfg, event
}

func TestRewardServiceChecksCurrentPublicationFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*rewardUsers, *rewardContent, *activity.Event)
	}{
		{"private", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.post.Status = content.StatusPrivate }},
		{"draft", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.post.Status = content.StatusDraft }},
		{"scheduled", func(_ *rewardUsers, p *rewardContent, e *activity.Event) {
			p.post.Status = content.StatusFuture
			p.post.PublishedAt = time.Now().Add(time.Hour)
			e.Due = p.post.PublishedAt
		}},
		{"rescheduled", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) {
			p.post.PublishedAt = time.Now().Add(time.Hour)
		}},
		{"page", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.post.Type = content.TypePage }},
		{"changed author", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.post.AuthorID = 2 }},
		{"short", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.post.ContentHTML = "<p>短文</p>" }},
		{"invisible padding", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) {
			p.post.ContentHTML = "<p>短文</p><!-- 很多隐藏字符 --><style>body{color:red}</style><script>alert('padding')</script>\u200b\u200d&nbsp;\n"
		}},
		{"old article", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) {
			p.post.CreatedAt = time.Now().Add(-24 * time.Hour)
		}},
		{"unverified", func(u *rewardUsers, _ *rewardContent, _ *activity.Event) {
			v := u.users[1]
			v.EmailVerifiedAt = nil
			u.users[1] = v
		}},
		{"suspended", func(u *rewardUsers, _ *rewardContent, _ *activity.Event) {
			v := u.users[1]
			v.Status = auth.UserStatusSuspended
			u.users[1] = v
		}},
		{"deleted user", func(u *rewardUsers, _ *rewardContent, _ *activity.Event) { delete(u.users, 1) }},
		{"deleted article", func(_ *rewardUsers, p *rewardContent, _ *activity.Event) { p.err = content.ErrNotFound }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, users, posts, _, event := rewardServiceFixture(t)
			tc.change(users, posts, &event)
			if err := s.Observe(t.Context(), event); err != nil {
				t.Fatal(err)
			}
			if balance, err := store.Balance(t.Context(), 1); err != nil || balance != 0 {
				t.Fatal("ineligible publication rewarded", balance, err)
			}
		})
	}
}

func TestRewardServicePublicationReceiptSurvivesRevisions(t *testing.T) {
	s, store, _, posts, cfg, event := rewardServiceFixture(t)
	for range 2 {
		if err := s.Observe(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	cfg.PublicationReward *= 2
	if _, err := store.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	posts.post.Version++
	event = activity.New(activity.Publication, 10, 10, 1, posts.post.Version, content.StatusPublish, posts.post.PublishedAt)
	if err := s.Observe(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	posts.post.AuthorID = 2
	event.UserID = 2
	if err := s.Observe(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if balance, _ := store.Balance(t.Context(), 1); balance != cfg.PublicationReward/2 {
		t.Fatal("republish rewarded again", balance)
	}
	if balance, _ := store.Balance(t.Context(), 2); balance != 0 {
		t.Fatal("reassigned author rewarded again", balance)
	}
	if history, _ := store.History(t.Context(), 1, 0, 10); len(history) != 1 {
		t.Fatal(history)
	}
}

func TestRewardServiceRegistrationFactsAndFailures(t *testing.T) {
	s, store, users, posts, cfg, event := rewardServiceFixture(t)
	u := users.users[1]
	u.CreatedAt = cfg.RegistrationSince.Add(-time.Second)
	users.users[1] = u
	if _, err := s.Registration(t.Context(), 1); !errors.Is(err, ErrRewardIneligible) {
		t.Fatal("old account received registration reward", err)
	}
	u.CreatedAt = cfg.RegistrationSince.Add(time.Minute)
	u.EmailVerifiedAt = nil
	users.users[1] = u
	if _, err := s.Registration(t.Context(), 1); !errors.Is(err, ErrRewardIneligible) {
		t.Fatal("unverified account rewarded", err)
	}
	verified := time.Now()
	u.EmailVerifiedAt = &verified
	users.users[1] = u
	first, err := s.Registration(t.Context(), 1)
	if err != nil || first.Replayed {
		t.Fatal(first, err)
	}
	second, err := s.Registration(t.Context(), 1)
	if err != nil || !second.Replayed || second.Entry.ID != first.Entry.ID {
		t.Fatal(second, err)
	}
	unavailable := errors.New("durable store unavailable")
	users.err = unavailable
	if _, err = s.CheckIn(t.Context(), 1, cfg.Version, "service-checkin-request"); !errors.Is(err, unavailable) {
		t.Fatal("user storage failure swallowed", err)
	}
	users.err = nil
	posts.err = unavailable
	if err = s.Observe(t.Context(), event); !errors.Is(err, unavailable) {
		t.Fatal("post storage failure swallowed", err)
	}
	posts.err = nil
	s.Store = &rewardFailure{Store: store, err: unavailable}
	if err = s.Observe(t.Context(), event); !errors.Is(err, unavailable) {
		t.Fatal("ledger failure acknowledged", err)
	}
	s.Store = store
	if err = s.Observe(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if balance, _ := store.Balance(t.Context(), 1); balance != cfg.RegistrationReward+cfg.PublicationReward {
		t.Fatal(balance)
	}
}
