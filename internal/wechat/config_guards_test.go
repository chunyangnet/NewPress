package wechat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
)

type configGuardStore struct {
	*FileStore
	views, configReads int
	beforeRead         func(int) error
}

func (s *configGuardStore) View(ctx context.Context) (State, error) {
	s.views++
	return s.FileStore.View(ctx)
}

func (s *configGuardStore) ViewConfig(ctx context.Context) (Config, error) {
	s.configReads++
	if s.beforeRead != nil {
		if err := s.beforeRead(s.configReads); err != nil {
			return Config{}, err
		}
	}
	return s.FileStore.ViewConfig(ctx)
}

func TestServiceConfigGuardsReadOnlyConfigWithFullHistory(t *testing.T) {
	s, post := fixture(t)
	cfg, err := ReadConfig(t.Context(), s.Store)
	if err != nil {
		t.Fatal(err)
	}
	state := configHistoryState()
	state.Config = cfg
	path := filepath.Join(t.TempDir(), "wechat.json")
	file, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Update(t.Context(), func(stored *State) error { *stored = state; return nil }); err != nil {
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
	store := &configGuardStore{FileStore: file}
	s.Store = store
	check, err := s.CheckSync(t.Context(), post.ID, post.Version, cfg.Version)
	if err != nil || !check.Ready {
		t.Fatal(check, err)
	}
	if err = s.stillEnabled(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	event := activity.Event{Kind: activity.Publication, PostID: post.ID, At: time.Now()}
	if err = s.Observe(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if store.views != 0 || store.configReads != 4 {
		t.Fatalf("configuration guards cloned history: full=%d config=%d", store.views, store.configReads)
	}
	after, err := file.View(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reads changed persistent configuration/history", err)
	}

}

func TestServiceConfigGuardsKeepFreshnessErrorsAndCancellation(t *testing.T) {
	for _, phase := range []string{"first-read", "final-read", "config-changed", "disabled", "canceled", "worker-guard", "observer"} {
		t.Run(phase, func(t *testing.T) {
			s, post := fixture(t)
			file := s.Store.(*FileStore)
			cfg, err := file.ViewConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			unavailable := errors.New("configuration unavailable")
			store := &configGuardStore{FileStore: file}
			s.Store = store
			want := unavailable
			store.beforeRead = func(read int) error {
				if phase == "first-read" || phase == "worker-guard" || phase == "observer" || phase == "final-read" && read == 2 {
					return unavailable
				}
				if phase == "config-changed" && read == 2 || phase == "disabled" && read == 1 {
					next := cfg
					next.Enabled = false
					_, err := s.SaveConfig(t.Context(), next, false)
					return err
				}
				return nil
			}
			ctx := t.Context()
			if phase == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			} else if phase == "config-changed" || phase == "disabled" {
				want = ErrConflict
			}
			if phase == "worker-guard" {
				err = s.stillEnabled(ctx, cfg)
			} else if phase == "observer" {
				err = s.Observe(ctx, activity.Event{Kind: activity.Publication, PostID: post.ID, At: time.Now()})
			} else {
				_, err = s.CheckSync(ctx, post.ID, post.Version, cfg.Version)
			}
			if !errors.Is(err, want) || store.views != 0 {
				t.Fatalf("read failure/change was bypassed: err=%v want=%v full=%d config=%d", err, want, store.views, store.configReads)
			}
		})
	}
}

func TestSyncConfigChangesStopSubsequentWritesUsingFreshReads(t *testing.T) {
	for _, phase := range []string{"cover", "body", "draft"} {
		t.Run(phase, func(t *testing.T) {
			s, post, _ := checkFixture(t)
			file := s.Store.(*FileStore)
			cfg, err := file.ViewConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cfg.AutoPublish = true
			cfg, err = s.SaveConfig(t.Context(), cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			store := &configGuardStore{FileStore: file}
			s.Store = store
			calls := map[string]int{}
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls[r.URL.Path]++
				if r.URL.Path == map[string]string{"cover": "/cgi-bin/material/add_material", "body": "/cgi-bin/media/uploadimg", "draft": "/cgi-bin/draft/add"}[phase] {
					next := cfg
					next.Enabled = false
					if _, err := s.SaveConfig(t.Context(), next, false); err != nil {
						t.Fatal(err)
					}
				}
				switch r.URL.Path {
				case "/cgi-bin/stable_token":
					return reply(r, `{"access_token":"token","expires_in":7200}`), nil
				case "/cgi-bin/material/add_material":
					return reply(r, `{"media_id":"cover-id"}`), nil
				case "/cgi-bin/media/uploadimg":
					return reply(r, `{"url":"https://mmbiz.qpic.cn/image"}`), nil
				case "/cgi-bin/draft/add":
					return reply(r, `{"media_id":"draft-id"}`), nil
				default:
					t.Fatalf("unexpected write after module disabled: %s", r.URL.Path)
					return nil, errors.New("unexpected write")
				}
			})}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if store.views != 0 || store.configReads < 2 {
				t.Fatalf("worker guards read complete history: full=%d config=%d", store.views, store.configReads)
			}
			state, err := file.View(t.Context())
			if err != nil || len(state.Jobs) != 1 {
				t.Fatal(state, err)
			}
			job := state.Jobs[0]
			if job.Status != "failed" || !strings.Contains(job.Error, "配置已变化") || job.PublishID != "" {
				t.Fatalf("module change ignored: %+v", job)
			}
			if phase == "draft" {
				if job.MediaID != "draft-id" {
					t.Fatal("confirmed draft receipt lost", job)
				}
			} else if calls["/cgi-bin/draft/add"] != 0 || job.MediaID != "" {
				t.Fatal("draft created after disable", job, calls)
			}
			if phase == "cover" && calls["/cgi-bin/media/uploadimg"] != 0 {
				t.Fatal("next image uploaded after disable", calls)
			}
		})
	}
}

func BenchmarkWeChatServiceConfigGuards(b *testing.B) {
	for _, size := range []int{0, MaxJobs} {
		for _, operation := range []string{"worker-guard", "preflight", "observer-disabled"} {
			b.Run(fmt.Sprintf("jobs-%d/%s", size, operation), func(b *testing.B) {
				store, err := NewFileStore("")
				if err != nil {
					b.Fatal(err)
				}
				state := configHistoryState()
				state.Jobs = state.Jobs[:size]
				state.Config.ThumbMediaID = "cover"
				if err = store.Update(b.Context(), func(s *State) error { *s = state; return nil }); err != nil {
					b.Fatal(err)
				}
				repo := content.NewMemoryRepository(false)
				post, err := repo.Create(b.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "基准文章", Slug: "config-check", ContentSource: "检查正文"})
				if err != nil {
					b.Fatal(err)
				}
				service := &Service{Store: store, Content: repo, PublicURL: "https://example.test"}
				event := activity.Event{Kind: activity.Publication, PostID: post.ID, At: time.Now()}
				b.ReportAllocs()
				for b.Loop() {
					switch operation {
					case "worker-guard":
						err = service.stillEnabled(b.Context(), state.Config)
					case "preflight":
						var result SyncCheck
						result, err = service.CheckSync(b.Context(), post.ID, post.Version, state.Config.Version)
						if err == nil && !result.Ready {
							b.Fatal(result)
						}
					case "observer-disabled":
						err = service.Observe(b.Context(), event)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
