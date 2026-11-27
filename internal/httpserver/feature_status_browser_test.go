package httpserver

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/questions"
)

// Opt-in, disposable fixture for actual browser interaction. No production
// settings, accounts, wallets or external providers are touched.
func TestFeatureStatusBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_FEATURE_STATUS_BROWSER")
	if dir == "" {
		t.Skip("opt-in feature status browser fixture")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("evidence directory must be absolute")
	}
	s, h, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	_, coin, _ := recognitionHTTPStore(t, s)
	cfg, _ := coin.Settings(t.Context())
	cfg.CheckInEnabled = true
	if _, err := coin.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := progression.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetProgressionService(&progression.Service{Store: store, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog})
	questionStore, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(questionStore)
	post := createOwnedTestPost(t, repo, owner.UserID, owner.DisplayName, "feature-status-browser", "业务状态验收文章")
	board := feedback.Defaults()
	board.Name, board.Enabled = "读者建议", false
	if _, err := s.feedback.Apply(t.Context(), "status", feedbackActor(owner), feedback.Command{Action: "create", Key: "status-fixture-feedback", Settings: &board}, time.Now()); err != nil {
		t.Fatal(err)
	}
	links := linkpage.Defaults()
	links.Name = "参考资料"
	if _, err := s.linkPages.Apply(t.Context(), "status", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "create", Key: "status-fixture-links", ExpectedUserID: owner.UserID, Settings: &links}, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"comments": false}
	if _, _, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil {
		t.Fatal(err)
	}
	login, stop := "/__feature_login/"+rand.Text(), "/__feature_stop/"+rand.Text()
	done, once := make(chan struct{}), sync.Once{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == login {
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/admin/comments/", http.StatusSeeOther)
			return
		}
		if r.Method == "POST" && r.URL.Path == stop {
			p, ok := s.currentAdmin(r)
			if !ok || p.UserID != owner.UserID {
				http.Error(w, "unauthorized", 401)
				return
			}
			w.WriteHeader(204)
			once.Do(func() { close(done) })
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	s.config.PublicURL = server.URL
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"base": server.URL, "login": login, "stop": stop, "post_id": post.ID})
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated feature status browser fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("fixture timed out")
	case <-t.Context().Done():
		t.Fatal("fixture interrupted")
	}
	if _, err := repo.GetByID(t.Context(), post.ID); err != nil {
		t.Fatal("fixture content lost", err)
	}
}
