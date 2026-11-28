package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

// Disposable actual pages and read APIs; every browser write must be intercepted.
func TestWeChatBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_WECHAT_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated WeChat browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "wechat-reader", "wechat-reader@example.test", "读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	post := createOwnedTestPost(t, repository, owner.UserID, owner.Username, "wechat-fixture", "已保存的示例文章")
	other := createOwnedTestPost(t, repository, owner.UserID, owner.Username, "wechat-other", "另一篇文章")
	cfg := wechat.Defaults()
	cfg.AppID = "wx0123456789abcdef"
	cfg.Secret = strings.Repeat("s", 32)
	cfg.Enabled = true
	cfg.CollectDrafts = true
	cfg.ThumbMediaID = "fixture-cover"
	config, err := s.wechat.SaveConfig(t.Context(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.wechat.QueueWithConfig(t.Context(), post.ID, post.Version, config.Version, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	otherJob, err := s.wechat.QueueWithConfig(t.Context(), other.ID, other.Version, config.Version, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.wechat.Store.Update(t.Context(), func(state *wechat.State) error {
		state.Jobs[0].Status = "draft"
		state.Jobs[0].Stage = "draft"
		state.Jobs[0].MediaID = "fixture-draft"
		state.Jobs[1].Status = "uncertain"
		state.Jobs[1].Stage = "publish"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int64
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		if r.URL.Hostname() != "mp.weixin.qq.com" || r.URL.Path == "/s/failed" {
			return nil, fmt.Errorf("fixture upstream unavailable")
		}
		body := `<meta property="og:title" content="` + r.URL.Path + `"><meta name="description" content="来源摘要"><div id="js_content"><p>可核对的采集正文。</p><a href="https://example.test/source">参考来源</a></div>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	headers := authenticatedAPIHeaders(owner)
	capture := func(method, path, body string) json.RawMessage {
		t.Helper()
		response := serveContentAPI(handler, ownerCookie, method, "https://example.test"+path, body, headers)
		if response.Code < 200 || response.Code >= 300 {
			t.Fatal(path, response.Code, response.Body.String())
		}
		return append(json.RawMessage{}, response.Body.Bytes()...)
	}
	preview := capture("POST", "/api/v1/admin/wechat/collect/preview", `{"urls":["https://mp.weixin.qq.com/s/firstx","https://mp.weixin.qq.com/s/second","https://mp.weixin.qq.com/s/failed"]}`)
	var selected wechatSelectionPreview
	if err = json.Unmarshal(preview, &selected); err != nil || len(selected.Articles) != 2 {
		t.Fatal(err, string(preview))
	}
	applied := capture("POST", "/api/v1/admin/wechat/collect/apply", wechatJSON(t, map[string]any{"articles": selected.Articles, "receipts": selected.Receipts}))
	check := capture("POST", "/api/v1/admin/wechat/sync/check", wechatJSON(t, map[string]any{"post_id": post.ID, "version": post.Version, "config_version": config.Version}))
	workspace := capture("GET", fmt.Sprintf("/api/v1/admin/wechat?post=%d", post.ID), "")
	beforeCalls := upstream.Load()
	writeEvidence := func(name string, value any) {
		data, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(directory, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	readState := func() []byte {
		accounts, e := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		posts, e := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		revisions := map[int64]any{}
		for _, p := range posts.Items {
			v, e := repository.ListRevisions(t.Context(), p.ID, 100)
			if e != nil {
				t.Fatal(e)
			}
			revisions[p.ID] = v
		}
		state, e := s.wechat.Store.View(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		values := map[string]any{"accounts": accounts, "posts": posts, "revisions": revisions, "wechat": state, "media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot()}
		for _, path := range paths {
			data, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			values[filepath.Base(path)] = string(data)
		}
		theme := s.themes.SettingsSnapshot().Values
		delete(theme, "visual_style")
		values["theme_settings"] = theme
		data, e := json.Marshal(values)
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	before := readState()
	var escaped atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, fmt.Sprintf("/admin/wechat/?post=%d", post.ID), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, e := s.themes.SaveSettings(values); e != nil || len(fields) > 0 {
			http.Error(w, "style failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			escaped.Add(1)
			http.Error(w, "browser mutation not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "post_id": post.ID, "other_post_id": other.ID, "queued_job": job, "other_job": otherJob, "workspace": workspace, "preview": preview, "applied": applied, "check": check})
	t.Log("WeChat fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("WeChat fixture timed out")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(directory, "stop")); e == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || escaped.Load() != 0 || upstream.Load() != beforeCalls {
					writeEvidence("state-before.json", json.RawMessage(before))
					writeEvidence("state-after.json", json.RawMessage(after))
					t.Fatalf("fixture changed, escaped=%d upstream=%d", escaped.Load(), upstream.Load()-beforeCalls)
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "posts_revisions_imports_unchanged": true, "wechat_settings_jobs_unchanged": true, "media_ledger_orders_settings_unchanged": true, "unintercepted_mutations": escaped.Load(), "upstream_calls_after_setup": upstream.Load() - beforeCalls, "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
