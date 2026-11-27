package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

type unavailableCurrency struct {
	virtualcurrency.Store
	fail bool
}

func (s *unavailableCurrency) Reward(ctx context.Context, in virtualcurrency.RewardInput) (virtualcurrency.RewardResult, error) {
	if s.fail {
		return virtualcurrency.RewardResult{}, virtualcurrency.ErrStoreUnavailable
	}
	return s.Store.Reward(ctx, in)
}

func TestPublicationCurrencyRewardSurvivesWorkerRetry(t *testing.T) {
	s, posts, _, _ := notificationFixture(t)
	store, err := virtualcurrency.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := virtualcurrency.DefaultSettings()
	cfg.Enabled, cfg.PublicationEnabled, cfg.PublicationMinLength = true, true, 1
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	post, err := posts.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "奖励事件", Slug: "currency-worker-retry", AuthorID: 1, ContentSource: "<p>公开正文</p>"})
	if err != nil {
		t.Fatal(err)
	}
	failing := &unavailableCurrency{Store: store, fail: true}
	rewards := &virtualcurrency.Service{Store: failing, Users: s.Users, Content: posts}
	s.BeforeAck = rewards.Observe
	if err = s.Tick(t.Context(), time.Now()); !errors.Is(err, virtualcurrency.ErrStoreUnavailable) {
		t.Fatal(err)
	}
	pending, err := posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 1 {
		t.Fatal("event acknowledged before credit", pending, err)
	}
	failing.fail = false
	downstream := errors.New("downstream observer temporarily unavailable")
	s.BeforeAck = func(ctx context.Context, event activity.Event) error {
		if err := rewards.Observe(ctx, event); err != nil {
			return err
		}
		return downstream
	}
	if err = s.Tick(t.Context(), time.Now()); !errors.Is(err, downstream) {
		t.Fatal(err)
	}
	if balance, _ := store.Balance(t.Context(), post.AuthorID); balance != cfg.PublicationReward {
		t.Fatal("credit missing before acknowledgement", balance)
	}
	s.BeforeAck = rewards.Observe
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = posts.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 0 {
		t.Fatal("event did not finish", pending, err)
	}
	history, err := store.History(t.Context(), post.AuthorID, 0, 30)
	if err != nil || len(history) != 1 || history[0].SourceID != post.ID {
		t.Fatal("worker replay duplicated reward", history, err)
	}
}
