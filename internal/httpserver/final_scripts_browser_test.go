package httpserver

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

// Opt-in real pages with isolated data and a hard boundary against browser writes.
func TestFinalScriptsBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_FINAL_SCRIPTS_BROWSER") != "1" {
		t.Skip("opt-in final scripts fixture")
	}
	dir := os.Getenv("NEWPRESS_FINAL_SCRIPTS_BROWSER_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute evidence directory required")
	}
	s, handler, accounts, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "final-reader", "final-reader@example.test", "报告读者", "subscriber")
	mediaDir := t.TempDir()
	var err error
	s.media, err = media.NewManager(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 800, 400)), media.UploadInput{Filename: "focus.png", Title: "焦点与裁切预览", AltText: "横向测试图片", UploadedBy: owner.Username}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "轻量报告验收", Slug: "lite-browser", AuthorID: owner.UserID, AuthorName: owner.Username, ContentSource: "<p>隔离报告。</p>"})
	if err != nil {
		t.Fatal(err)
	}
	s.config.AnalyticsEnabled = true
	now := time.Now().In(s.siteLocation(t.Context()))
	for day := 89; day >= 0; day-- {
		for count := 0; count < 2+(89-day)%17; count++ {
			s.analytics.Record(analytics.Event{At: now.AddDate(0, 0, -day), PostID: post.ID, Source: []string{"direct", "example.test", "internal"}[count%3], Search: count%3 == 0, Miss: count%6 == 0, Term: "内容组织"})
		}
	}
	if err := s.analytics.Flush(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		state := map[string]any{"media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot(), "theme": s.themes.SettingsSnapshot()}
		for _, id := range []int64{owner.UserID, reader.ID} {
			account, err := accounts.GetUser(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("account-%d", id)] = account
		}
		posts, err := repo.ListAdmin(t.Context(), content.AdminQuery{Type: content.TypePost, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["posts"] = posts
		revisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		state["revisions"] = revisions
		report, err := s.analytics.Snapshot(t.Context(), now)
		if err != nil {
			t.Fatal(err)
		}
		state["analytics"] = report
		if err := filepath.WalkDir(mediaDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(mediaDir, path)
			digest := sha256.Sum256(data)
			state["file-"+relative] = hex.EncodeToString(digest[:])
			return err
		}); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "/__final_scripts/" + hex.EncodeToString(token[:])
	login, stop := prefix+"/login", prefix+"/stop"
	done, once := make(chan struct{}), sync.Once{}
	var escaped atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == login {
			cookie := ownerCookie
			if r.URL.Query().Get("role") == "reader" {
				cookie = readerCookie
			}
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == stop {
			cookie, err := r.Cookie(auth.CookieName)
			if err != nil {
				http.Error(w, "session required", 401)
				return
			}
			principal, ok := accounts.ValidateSessionContext(r.Context(), cookie.Value)
			if !ok || principal.UserID != owner.UserID {
				http.Error(w, "owner required", 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			once.Do(func() { close(done) })
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			escaped.Add(1)
			http.Error(w, "browser writes must be intercepted", 403)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	s.config.PublicURL = server.URL
	write := func(name string, value any) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	write("fixture.json", map[string]any{"base": server.URL, "login": login, "stop": stop, "media_id": item.ID, "media_version": item.Version, "paths": map[string]string{"media": fmt.Sprintf("/admin/media/%d/edit/", item.ID), "analytics": "/admin/analytics/?days=30"}})
	t.Log("final scripts fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("explicit stop required within 25 minutes")
	case <-t.Context().Done():
		t.Fatal("fixture interrupted")
	}
	after := readState()
	if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
		t.Fatalf("isolated data changed or %d writes escaped", escaped.Load())
	}
	digest := sha256.Sum256(after)
	write("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "content_and_revisions_unchanged": true, "media_and_files_unchanged": true, "analytics_unchanged": true, "settings_unchanged": true, "unintercepted_writes": escaped.Load(), "state_sha256": hex.EncodeToString(digest[:])})
}
