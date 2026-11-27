package wechat

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestStoppedTaskResumesWithExplicitDraftPolicy(t *testing.T) {
	s, post := fixture(t)
	state, _ := s.Store.View(t.Context())
	path := filepath.Join(t.TempDir(), "wechat.json")
	s.Store, _ = NewFileStore(path)
	cfg := state.Config
	cfg.Version = 1
	cfg.AutoPublish = true
	if _, err := s.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	j, err := s.Queue(t.Context(), post.ID, post.Version, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "cancel", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	state, _ = s.Store.View(t.Context())
	state.Config.Enabled = false
	state.Config.AutoPublish = false
	if _, err = s.SaveConfig(t.Context(), state.Config, false); err != nil {
		t.Fatal(err)
	}
	if !stateJob(t, s).AutoPublish {
		t.Fatal("default silently changed an existing job")
	}
	if err = s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled worker resumed", err)
	}
	if err = s.Action(t.Context(), j.ID, "draft_only", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatal("stale action accepted", err)
	}
	// Both the stopped state and explicit policy survive a process restart.
	s.Store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	if j.Status != "skipped" || j.AutoPublish {
		t.Fatal(j)
	}
	state, _ = s.Store.View(t.Context())
	state.Config.Enabled = true
	if _, err = s.SaveConfig(t.Context(), state.Config, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	drafts, publishes := 0, 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"test-token","expires_in":7200}`), nil
		case "/cgi-bin/draft/add":
			drafts++
			return reply(r, `{"media_id":"draft-result"}`), nil
		default:
			publishes++
			return reply(r, `{}`), nil
		}
	})}
	if err = s.Tick(t.Context(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if j = stateJob(t, s); j.Status != "draft" || drafts != 1 || publishes != 0 {
		t.Fatal(j, drafts, publishes)
	}
}

func TestResumeRejectsChangedContentAndActiveNewerTasks(t *testing.T) {
	s, post := fixture(t)
	j, _ := s.Queue(t.Context(), post.ID, post.Version, "operator")
	if err := s.Action(t.Context(), j.ID, "cancel", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	newer, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "更新后的文章", Slug: post.Slug, ContentSource: "更新后的正文"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); err == nil {
		t.Fatal("outdated article resumed")
	}
	if _, err = s.Queue(t.Context(), newer.ID, newer.Version, "operator"); err != nil {
		t.Fatal(err)
	}
	// A previously submitted publication resumes polling, even after a local
	// edit, but cannot run alongside a newer active job for the same article.
	if err = s.Store.Update(t.Context(), func(state *State) error {
		state.Jobs[0].PublishID = "remote-publish"
		state.Jobs[0].Stage = "poll"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err = s.Action(t.Context(), j.ID, "draft_only", j.UpdatedAt); err == nil {
		t.Fatal("publication downgraded")
	}
}

func TestKeepExistingRemoteDraftWithoutPublishing(t *testing.T) {
	s, post := fixture(t)
	j, _ := s.Queue(t.Context(), post.ID, post.Version, "operator")
	if err := s.Store.Update(t.Context(), func(state *State) error {
		job := &state.Jobs[0]
		job.Status = "failed"
		job.Stage = "publish"
		job.MediaID = "already-created"
		job.AutoPublish = true
		job.Error = "明确发表失败"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Action(t.Context(), j.ID, "draft_only", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	if j.AutoPublish || j.Status != "draft" || j.MediaID != "already-created" || j.Error != "" {
		t.Fatal(j)
	}
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		t.Error("unnecessary remote request")
		return nil, errors.New("unexpected")
	})}
	if err := s.Tick(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestResumedPublicationOnlyPollsItsExistingID(t *testing.T) {
	s, post := fixture(t)
	j, _ := s.Queue(t.Context(), post.ID, post.Version, "operator")
	if err := s.Store.Update(t.Context(), func(state *State) error {
		job := &state.Jobs[0]
		job.Status = "skipped"
		job.Stage = "poll"
		job.PublishID = "existing-publish"
		job.AutoPublish = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Polling an already submitted publication does not need local content.
	s.Content = nil
	if err := s.Action(t.Context(), j.ID, "resume", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	polls := 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"test-token","expires_in":7200}`), nil
		case "/cgi-bin/freepublish/get":
			polls++
			return reply(r, `{"publish_id":"existing-publish","publish_status":0,"article_id":"published-article"}`), nil
		default:
			t.Error("unexpected remote mutation", r.URL.Path)
			return nil, errors.New("unexpected")
		}
	})}
	if err := s.Tick(t.Context(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	if polls != 1 || j.Status != "published" || j.PublishID != "existing-publish" {
		t.Fatal(j, polls)
	}
}

func TestCollectionDeduplicatesAndBoundsConcurrentReads(t *testing.T) {
	var active, peak atomic.Int32
	var mu sync.Mutex
	counts := map[string]int{}
	started, release := make(chan struct{}, 10), make(chan struct{})
	c := NewClient()
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		if strings.HasSuffix(r.URL.Path, "failed") {
			result := reply(r, "")
			result.StatusCode = 404
			return result, nil
		}
		return reply(r, `<html><h1 id="activity-name">`+r.URL.Path+`</h1><div id="js_content"><p>正文</p></div></html>`), nil
	})}
	urls := []string{"https://mp.weixin.qq.com/s/alpha1?scene=1", "https://mp.weixin.qq.com/s/alpha1", "https://mp.weixin.qq.com/s/beta22", "https://mp.weixin.qq.com/s/gamma3", "https://mp.weixin.qq.com/s/failed", "http://127.0.0.1/private"}
	type result struct {
		articles []Collected
		failures []CollectFailure
		err      error
	}
	done := make(chan result, 1)
	go func() { a, f, e := c.CollectArticles(t.Context(), urls); done <- result{a, f, e} }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent reads did not start")
		}
	}
	close(release)
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collection did not finish")
	}
	if got.err != nil || len(got.articles) != 3 || len(got.failures) != 2 || peak.Load() != 3 {
		t.Fatal(got, peak.Load())
	}
	for i, suffix := range []string{"alpha1", "beta22", "gamma3"} {
		if !strings.HasSuffix(got.articles[i].URL, suffix) {
			t.Fatal("input order changed", got.articles)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["/s/alpha1"] != 1 || len(counts) != 4 {
		t.Fatal(counts)
	}
}

func TestCollectionTimeoutReturnsSuccessfulArticles(t *testing.T) {
	for _, batchTimeout := range []time.Duration{time.Second, 40 * time.Millisecond} {
		c := NewClient()
		c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "quick1") {
				return reply(r, `<html><h1 id="activity-name">成功文章</h1><div id="js_content">正文</div></html>`), nil
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		requestTimeout := 30 * time.Millisecond
		if batchTimeout < time.Second {
			requestTimeout = time.Second
		}
		articles, failures, err := c.collectArticles(t.Context(), []string{"https://mp.weixin.qq.com/s/quick1", "https://mp.weixin.qq.com/s/slow22"}, requestTimeout, batchTimeout)
		if err != nil || len(articles) != 1 || len(failures) != 1 || !strings.Contains(failures[0].Message, "超时") {
			t.Fatal(articles, failures, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := NewClient().CollectArticles(ctx, []string{"https://mp.weixin.qq.com/s/quick1"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
