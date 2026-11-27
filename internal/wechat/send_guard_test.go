package wechat

import (
	"errors"
	"maps"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestSyncRechecksChangesAfterTokenBeforeEachWrite(t *testing.T) {
	endpoints := map[string]string{
		"cover": "/cgi-bin/material/add_material", "body": "/cgi-bin/media/uploadimg",
		"draft": "/cgi-bin/draft/add", "publish": "/cgi-bin/freepublish/submit",
	}
	for _, phase := range []string{"cover", "body", "draft", "publish"} {
		for _, timing := range []string{"initial", "refresh"} {
			for _, change := range []string{"disabled", "article"} {
				t.Run(phase+"/"+timing+"/"+change, func(t *testing.T) {
					s, post, source := checkFixture(t)
					cfg, err := ReadConfig(t.Context(), s.Store)
					if err != nil {
						t.Fatal(err)
					}
					cfg.AutoPublish = true
					if _, err = s.SaveConfig(t.Context(), cfg, false); err != nil {
						t.Fatal(err)
					}
					cfg, err = ReadConfig(t.Context(), s.Store)
					if err != nil {
						t.Fatal(err)
					}
					job, err := s.Queue(t.Context(), post.ID, post.Version, "owner")
					if err != nil {
						t.Fatal(err)
					}
					images := map[string]string{}
					if phase != "cover" {
						images["cover:"+source] = "confirmed-cover"
					}
					if phase == "draft" || phase == "publish" {
						images[source] = "https://mmbiz.qpic.cn/confirmed-body"
					}
					mediaID := ""
					if phase == "publish" {
						mediaID = "confirmed-draft"
					}
					state, err := s.Store.View(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					state.Jobs[0].Images, state.Jobs[0].MediaID = images, mediaID
					path := filepath.Join(t.TempDir(), "wechat.json")
					file, err := NewFileStore(path)
					if err != nil {
						t.Fatal(err)
					}
					if err = file.Update(t.Context(), func(saved *State) error { *saved = state; return nil }); err != nil {
						t.Fatal(err)
					}
					s.Store = file
					armed, changed := timing == "initial", false
					calls := map[string]int{}
					s.Client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
						calls[req.URL.Path]++
						if req.URL.Path == "/cgi-bin/stable_token" {
							if armed && !changed {
								changed = true
								if change == "disabled" {
									next := cfg
									next.Enabled = false
									if _, err := s.SaveConfig(t.Context(), next, false); err != nil {
										t.Fatal(err)
									}
								} else {
									_, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "等待凭证时修改", Slug: post.Slug, ContentSource: "更新的正文", CoverURL: source})
									if err != nil {
										t.Fatal(err)
									}
								}
							}
							return reply(req, `{"access_token":"token","expires_in":7200}`), nil
						}
						if timing == "refresh" && req.URL.Path == endpoints[phase] && calls[req.URL.Path] == 1 {
							return reply(req, `{"errcode":42001}`), nil
						}
						switch req.URL.Path {
						case "/cgi-bin/material/add_material":
							return reply(req, `{"media_id":"unexpected-cover"}`), nil
						case "/cgi-bin/media/uploadimg":
							return reply(req, `{"url":"https://mmbiz.qpic.cn/unexpected-body"}`), nil
						case "/cgi-bin/draft/add":
							return reply(req, `{"media_id":"unexpected-draft"}`), nil
						case "/cgi-bin/freepublish/submit":
							return reply(req, `{"publish_id":123}`), nil
						default:
							return nil, errors.New("unexpected endpoint")
						}
					})}
					if timing == "refresh" {
						if _, err = s.Client.Token(t.Context(), cfg); err != nil {
							t.Fatal(err)
						}
						armed = true
					}
					if err = s.Tick(t.Context(), time.Now()); err != nil {
						t.Fatal(err)
					}
					got := stateJob(t, s)
					wantCalls, wantTokens := 0, 1
					if timing == "refresh" {
						wantCalls, wantTokens = 1, 2
					}
					for _, endpoint := range endpoints {
						want := 0
						if endpoint == endpoints[phase] {
							want = wantCalls
						}
						if calls[endpoint] != want {
							t.Errorf("state change during token acquisition still sent %s: got=%d want=%d", endpoint, calls[endpoint], want)
						}
					}
					wantError := "已变化"
					if change == "article" {
						wantError = "已更新"
					}
					if !changed || calls["/cgi-bin/stable_token"] != wantTokens || got.ID != job.ID || got.Status != "failed" || !strings.Contains(got.Error, wantError) || got.MediaID != mediaID || got.PublishID != "" || !maps.Equal(got.Images, images) {
						t.Errorf("unstarted write changed receipts or status: job=%+v calls=%v", got, calls)
					}
					reopened, err := NewFileStore(path)
					if err != nil {
						t.Fatal(err)
					}
					s.Store = reopened
					if restored := stateJob(t, s); !reflect.DeepEqual(restored, got) {
						t.Fatal("guard failure did not persist", restored, got)
					}
				})
			}
		}
	}
}

func TestSyncAfterTokenReadFailureStopsBeforeDraft(t *testing.T) {
	s, post := fixture(t)
	file := s.Store.(*FileStore)
	store := &configGuardStore{FileStore: file}
	s.Store = store
	unavailable := errors.New("configuration unavailable after token")
	writes := 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/cgi-bin/stable_token" {
			store.beforeRead = func(int) error { return unavailable }
			return reply(req, `{"access_token":"token","expires_in":7200}`), nil
		}
		writes++
		return reply(req, `{"media_id":"unexpected-draft"}`), nil
	})}
	if _, err := s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	if writes != 0 || job.Status != "failed" || job.Error != unavailable.Error() || job.MediaID != "" {
		t.Fatal("read failure after token did not stop draft", writes, job)
	}
	store.beforeRead = nil
	if err := s.Action(t.Context(), job.ID, "retry", job.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := stateJob(t, s); got.Status != "draft" || writes != 1 || got.MediaID == "" {
		t.Fatal("explicit retry failed after configuration recovered", got, writes)
	}
}
