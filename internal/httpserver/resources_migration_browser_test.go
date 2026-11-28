package httpserver

import (
	"crypto/rand"
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

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

// Browser mutations are intercepted. The real Go pages, session cookies,
// resource projections and private preview render against isolated stores.
func TestResourcesMigrationBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_RESOURCE_MIGRATION_BROWSER") != "1" {
		t.Skip("opt-in resource migration browser fixture")
	}
	dir := os.Getenv("NEWPRESS_RESOURCE_MIGRATION_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("explicit absolute evidence directory required")
	}
	s, handler, accounts, repo, adminCookie, owner := newPersistentAdminTestServer(t)
	service, _, coin := commerceHTTPStore(t, s)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "resource-migration-reader", "resource-migration@example.test", "资源读者", "subscriber")
	cfg := resourceFixtureConfig()
	item := &cfg.Items[0]
	item.Variants = append(item.Variants, resources.Variant{ID: "quiz", Name: "答题版本", Kind: "content", Body: "<p>答题解锁正文</p>", Groups: []resources.Group{{Name: "答题与资料", Conditions: []resources.Condition{{ID: "quiz", Kind: "quiz"}, {ID: "form", Kind: "form"}}}}, Questions: []resources.Question{{ID: "q", Title: "选择正确答案", Options: []string{"正确", "错误"}, Answer: "正确"}}, Fields: []resources.Field{{ID: "name", Label: "称呼", Type: "text", Required: true}, {ID: "agree", Label: "同意资料说明", Type: "checkbox", Required: true}}, PassPercent: 100, AttemptsPerDay: 3})
	item.Variants = append(item.Variants, resources.Variant{ID: "public", Name: "公开下载", Kind: "download", Body: "<p>公开下载正文</p>", Downloads: []resources.Download{{ID: "file", Name: "验收文件", Provider: "direct", URL: "/core-assets/images/favicon.svg"}}, Groups: []resources.Group{{Name: "公开", Conditions: []resources.Condition{{ID: "public", Kind: "public"}}}}})
	post := createWidgetPost(t, repo, content.PostInput{Type: content.TypePost, AuthorID: owner.UserID, AuthorName: owner.DisplayName, Title: "资源业务验收", Slug: "resource-migration-browser", Status: content.StatusPublish, ContentSource: "<p>资源解锁、答题与下载验收。</p><p>[resource:bundle]</p>", Resources: &cfg})
	form := autosaveTestForm(owner.CSRFToken, post.Version, post.Title, post.Slug, post.ContentSource)
	form.Set("type", content.TypePost)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	form.Set("resources", string(raw))
	saved := serveContentForm(handler, adminCookie, fmt.Sprintf("https://example.test/admin/content/%d/autosave/", post.ID), "https://example.test", form)
	var autosave autosaveAPIResponse
	if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &autosave) != nil || autosave.PreviewURL == "" {
		t.Fatal("preview setup", saved.Code, saved.Body.String())
	}
	post, err = repo.GetByID(t.Context(), post.ID)
	if err != nil {
		t.Fatal(err)
	}
	resourceBefore, err := s.resources.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	commerceBefore, err := service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	ownerBefore, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readerBefore, err := accounts.GetUser(t.Context(), reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownerBalance, err := coin.Balance(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readerBalance, err := coin.Balance(t.Context(), reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, _ := json.Marshal(s.siteSettings.Snapshot())
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(bytes[:])
	login, readerLogin, stop := "/__resources_owner/"+nonce, "/__resources_reader/"+nonce, "/__resources_stop/"+nonce
	done, once := make(chan struct{}), sync.Once{}
	var escaped atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == login || r.URL.Path == readerLogin) {
			cookie := adminCookie
			if r.URL.Path == readerLogin {
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
			http.Error(w, "browser mutations must be intercepted", 403)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.Close)
	s.config.PublicURL = fixture.URL
	service.PublicURL = fixture.URL
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
	write("fixture.json", map[string]any{"base": fixture.URL, "login": login, "reader_login": readerLogin, "stop": stop, "post_id": post.ID, "owner_id": owner.UserID, "reader_id": reader.ID, "paths": map[string]string{"article": "/posts/" + post.Slug, "preview": autosave.PreviewURL, "history": "/downloads/"}})
	t.Log("resource migration fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("explicit stop required within 25 minutes")
	case <-t.Context().Done():
		t.Fatal("fixture interrupted before stop")
	}
	currentPost, err := repo.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(currentPost, post) {
		t.Fatal("article changed", err)
	}
	currentRevisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
	if err != nil || !reflect.DeepEqual(currentRevisions, revisions) {
		t.Fatal("revisions changed", err)
	}
	currentOwner, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil || !reflect.DeepEqual(currentOwner, ownerBefore) {
		t.Fatal("owner changed", err)
	}
	currentReader, err := accounts.GetUser(t.Context(), reader.ID)
	if err != nil || !reflect.DeepEqual(currentReader, readerBefore) {
		t.Fatal("reader changed", err)
	}
	resourceAfter, err := s.resources.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(resourceAfter, resourceBefore) {
		t.Fatal("resource state changed", err)
	}
	commerceAfter, err := service.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(commerceAfter, commerceBefore) {
		t.Fatal("orders changed", err)
	}
	currentOwnerBalance, err := coin.Balance(t.Context(), owner.UserID)
	if err != nil || ownerBalance != currentOwnerBalance {
		t.Fatal("owner balance changed", err)
	}
	currentReaderBalance, err := coin.Balance(t.Context(), reader.ID)
	if err != nil || readerBalance != currentReaderBalance {
		t.Fatal("reader balance changed", err)
	}
	settingsAfter, _ := json.Marshal(s.siteSettings.Snapshot())
	if !reflect.DeepEqual(settingsBefore, settingsAfter) || escaped.Load() != 0 {
		t.Fatal("settings changed or unintercepted writes", escaped.Load())
	}
	write("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "content_unchanged": true, "revisions_unchanged": true, "resources_unchanged": true, "orders_unchanged": true, "balances_unchanged": true, "settings_unchanged": true, "unintercepted_writes": escaped.Load()})
}
