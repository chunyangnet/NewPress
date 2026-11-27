package wechat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type countedConfigStore struct {
	Store
	views int
	err   error
}

func (s *countedConfigStore) View(ctx context.Context) (State, error) {
	s.views++
	if s.err != nil {
		return State{}, s.err
	}
	return s.Store.View(ctx)
}

type projectedConfigStore struct {
	*countedConfigStore
	configViews int
}

func (s *projectedConfigStore) ViewConfig(ctx context.Context) (Config, error) {
	s.configViews++
	return s.Store.(*FileStore).ViewConfig(ctx)
}

func configHistoryState() State {
	state := NewState()
	state.Config.AppID = "wx0123456789abcdef"
	state.Config.Secret = strings.Repeat("s", 32)
	state.Config.Enabled = true
	for i := range MaxJobs {
		state.Jobs = append(state.Jobs, Job{ID: fmt.Sprintf("%064x", i+1), PostID: int64(i + 1), Version: 1, AppID: state.Config.AppID, Title: "已有同步记录", Status: "draft", Stage: "draft", CreatedAt: time.Now().UTC(), Images: map[string]string{"cover": "remote-id"}})
	}
	return state
}

func TestConfigReadUsesProjectionAndPreservesFileState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wechat.json")
	file, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := configHistoryState()
	if err = file.Update(t.Context(), func(state *State) error { *state = want; return nil }); err != nil {
		t.Fatal(err)
	}
	file, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := file.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store := &projectedConfigStore{countedConfigStore: &countedConfigStore{Store: file}}
	for range 101 {
		cfg, err := ReadConfig(t.Context(), store)
		if err != nil || cfg != want.Config {
			t.Fatal(cfg, err)
		}
		cfg.CollectEnabled = false
		cfg.Secret = "does-not-change-store"
	}
	if store.views != 0 || store.configViews != 101 {
		t.Fatal("guard cloned job history", store.views, store.configViews)
	}
	after, err := file.View(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("configuration reads changed persistent history", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = ReadConfig(ctx, store); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled configuration read", err)
	}

}

func BenchmarkWeChatConfigReadWithFullHistory(b *testing.B) {
	store, err := NewFileStore("")
	if err != nil {
		b.Fatal(err)
	}
	state := configHistoryState()
	if err = store.Update(b.Context(), func(s *State) error { *s = state; return nil }); err != nil {
		b.Fatal(err)
	}
	for _, projected := range []bool{false, true} {
		name := "full-history"
		if projected {
			name = "config-only"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if projected {
					if _, err := ReadConfig(b.Context(), store); err != nil {
						b.Fatal(err)
					}
				} else if _, err := store.View(b.Context()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
