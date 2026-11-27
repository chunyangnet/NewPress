package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// Opt-in, isolated fixture for before/after measurements and browser regression.
// Test accounts, report events, and control routes never enter the production app.
func TestAnalyticsBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_ANALYTICS_BROWSER_DIR")
	if dir == "" {
		t.Skip("set NEWPRESS_ANALYTICS_BROWSER_DIR to an absolute evidence directory")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("evidence directory must be absolute")
	}
	s, handler, accounts, repository, cookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, _ := createAndLoginTestUser(t, accounts, owner, "analytics-editor", "editor@example.test", "统计编辑", "editor")
	_, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "analytics-reader", "reader@example.test", "统计读者", "subscriber")
	post, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish,
		Title: "读者如何发现内容：一次观察", Slug: "analytics-browser-report", AuthorID: owner.UserID, AuthorName: "统计核对",
		ContentSource: "<p>隔离统计验收内容。</p>"})
	if err != nil {
		t.Fatal(err)
	}
	s.config.AnalyticsEnabled = true
	now := time.Now().In(s.siteLocation(t.Context()))
	for day := 89; day >= 0; day-- {
		for count := 0; count < 2+(89-day)%17; count++ {
			s.analytics.Record(analytics.Event{At: now.AddDate(0, 0, -day), PostID: post.ID,
				Source: []string{"direct", "example.test", "internal"}[count%3], Search: count%3 == 0,
				Miss: count%6 == 0, Term: "内容组织"})
		}
	}
	if err := s.analytics.Flush(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "/__analytics_fixture/" + hex.EncodeToString(token[:])
	login, stop := prefix+"/login", prefix+"/stop"
	done, once := make(chan struct{}), sync.Once{}
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == login && r.Method == http.MethodGet {
			selected := cookie
			switch r.URL.Query().Get("role") {
			case "editor":
				selected = editorCookie
			case "reader":
				selected = readerCookie
			}
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: selected.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/admin/analytics/?days=30", http.StatusSeeOther)
			return
		}
		if r.URL.Path == stop && r.Method == http.MethodPost {
			value, err := r.Cookie(auth.CookieName)
			if err != nil {
				http.Error(w, "session required", http.StatusUnauthorized)
				return
			}
			principal, ok := accounts.ValidateSessionContext(r.Context(), value.Value)
			if !ok || principal.UserID != owner.UserID {
				http.Error(w, "fixture owner required", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			once.Do(func() { close(done) })
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	s.config.PublicURL = server.URL
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(map[string]any{"base": server.URL, "login": login, "stop": stop}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "fixture.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated analytics browser fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("analytics browser fixture was not stopped")
	case <-t.Context().Done():
		t.Fatal("analytics browser fixture interrupted")
	}
}
