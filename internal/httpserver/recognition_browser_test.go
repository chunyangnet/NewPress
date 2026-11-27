package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/virtualcurrency"
)

// Opt-in browser fixture uses isolated data and loopback-only test sessions.
func TestRecognitionBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_RECOGNITION_BROWSER")
	if dir == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	s, h, manager, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	service, coin, cfg := recognitionHTTPStore(t, s)
	u, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "recognition-reader", "recognition-reader@example.test", "林间来信", "subscriber")
	levels, err := progression.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	levelConfig := progression.DefaultSettings()
	levelConfig.Levels.Enabled, levelConfig.Experience.Enabled, levelConfig.Membership.Enabled = true, true, true
	levelConfig, err = levels.UpdateSettings(t.Context(), 1, levelConfig)
	if err != nil {
		t.Fatal(err)
	}
	s.SetProgressionService(&progression.Service{Store: levels, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog})
	for _, user := range []int64{u.ID, owner.UserID} {
		if _, err = levels.Adjust(t.Context(), progression.Adjustment{ActorID: owner.UserID, UserID: user, Delta: 750, ExpectedVersion: levelConfig.Version, RequestID: fmt.Sprintf("recognition-browser-level-%d", user), Reason: "测试等级"}); err != nil {
			t.Fatal(err)
		}
	}
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "把日常写成值得分享的故事", Slug: "recognition-preview", AuthorID: owner.UserID, AuthorName: "站点作者", ContentSource: "<h2>留意那些小事</h2><p>创作来自观察，也来自每一次认真回应。慢下来，给喜欢的文字留一点时间。</p><h2>让分享发生</h2><p>从一张照片、一段见闻开始，把你的发现带给更多人。</p>", Excerpt: "每一份认真表达，都值得被看见。", CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	// A second isolated wallet exercises balance/points switching and precision.
	balanceCurrency, _, err := s.currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "balance", Name: "余额", Symbol: "元", Precision: 2, ActorID: owner.UserID, RequestID: "recognition-browser-balance"})
	if err != nil {
		t.Fatal(err)
	}
	balanceStore, err := s.currencyCatalog.Store(t.Context(), "balance")
	if err != nil {
		t.Fatal(err)
	}
	balanceRules := balanceCurrency.Settings
	balanceRules.Enabled, balanceRules.AllowTransfers = true, true
	balanceRules, err = balanceStore.UpdateSettings(t.Context(), balanceRules.Version, balanceRules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = balanceStore.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: u.ID, Delta: 2000, ExpectedVersion: balanceRules.Version, RequestID: "recognition-browser-balance-credit", Reason: "测试余额"}); err != nil {
		t.Fatal(err)
	}
	state, err := service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Settings.Currencies = append(state.Settings.Currencies, recognition.TipCurrency{Key: "balance", Presets: []string{"1", "3", "6"}})
	if err = service.SaveSettings(t.Context(), state.Settings); err != nil {
		t.Fatal(err)
	}
	_, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: u.ID, Delta: 100000, ExpectedVersion: cfg.Version, RequestID: "recognition-browser-credit", Reason: "测试积分"})
	if err != nil {
		t.Fatal(err)
	}
	a := recognition.Identity{Version: 1}
	for _, key := range []string{"contributor", "original"} {
		a, err = service.UpdateIdentity(t.Context(), owner.UserID, true, recognition.IdentityInput{UserID: u.ID, Version: a.Version, Action: "grant", Key: key, Label: "独立摄影作者", Reason: "测试授予", Days: 30})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = service.Tip(t.Context(), u.ID, recognition.TipInput{PostID: post.ID, RequestID: "recognition-browser-tip", Currency: "main", Amount: "5", SettingsVersion: 3, CurrencyVersion: cfg.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: u.ID, AuthorName: u.DisplayName, AuthorEmail: u.Email, ContentSource: "喜欢这种认真观察生活的态度，期待更多分享。", Status: comment.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	mux := http.NewServeMux()
	ownerCookie.Secure = false
	readerCookie.Secure = false
	mux.HandleFunc("GET /fixture/admin", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, ownerCookie)
		http.Redirect(w, r, "/admin/recognition/", 303)
	})
	mux.HandleFunc("GET /fixture/reader", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, readerCookie)
		http.Redirect(w, r, "/posts/recognition-preview", 303)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, _, e := s.themes.SaveSettings(values); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/stop", func(w http.ResponseWriter, r *http.Request) { done <- struct{}{}; w.WriteHeader(204) })
	mux.Handle("/", h)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"url": host.URL})
	if err = os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("recognition browser fixture ready", host.URL)
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
	case <-t.Context().Done():
	}
}
