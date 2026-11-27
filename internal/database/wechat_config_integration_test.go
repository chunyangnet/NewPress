package database

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/wechat"
)

func TestPostgreSQLWeChatConfigProjection(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	repo, reader := NewWeChatRepository(pool), NewWeChatRepository(other)
	original, err := repo.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Update(context.Background(), func(s *wechat.State) error { *s = original; return nil }); err != nil {
			t.Error(err)
		}
	}()
	if err = repo.Update(ctx, func(s *wechat.State) error {
		*s = wechat.NewState()
		s.Config.Secret = strings.Repeat("s", 32)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := wechat.ReadConfig(ctx, reader)
	if err != nil || cfg != before.Config {
		t.Fatal(cfg, err)
	}
	cfg.CollectEnabled = false
	cfg.CollectDrafts = true
	service := &wechat.Service{Store: repo}
	if _, err = service.SaveConfig(ctx, cfg, false); err != nil {
		t.Fatal(err)
	}
	latest, err := wechat.ReadConfig(ctx, reader)
	if err != nil || latest.CollectEnabled || !latest.CollectDrafts || latest.Version != cfg.Version+1 || latest.Secret != cfg.Secret {
		t.Fatal(latest, err)
	}
	latest.CollectDrafts = false
	if _, err = service.SaveConfig(ctx, latest, false); err != nil {
		t.Fatal(err)
	}
	disabled, err := wechat.ReadConfig(ctx, reader)
	if err != nil || disabled.CollectDrafts || disabled.Version != latest.Version+1 {
		t.Fatal(disabled, err)
	}
	after, err := repo.View(ctx)
	if err != nil || !reflect.DeepEqual(before.Jobs, after.Jobs) {
		t.Fatal("projected read changed history", err)
	}
	stopCtx, stop := context.WithCancel(ctx)
	stop()
	if _, err = wechat.ReadConfig(stopCtx, reader); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read", err)
	}
}
