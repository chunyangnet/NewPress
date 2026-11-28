package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/virtualcurrency"
)

// Real pages and APIs against isolated stores; all browser mutations are intercepted.
func TestRecognitionMigrationBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_RECOGNITION_MIGRATION_BROWSER") != "1" {
		t.Skip("opt-in recognition migration browser fixture")
	}
	dir := os.Getenv("NEWPRESS_RECOGNITION_MIGRATION_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("explicit absolute evidence directory required")
	}
	s, handler, accounts, repo, adminCookie, owner := newPersistentAdminTestServer(t)
	service, coin, cfg := recognitionHTTPStore(t, s)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "recognition-migration-reader", "recognition-migration@example.test", "认可读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "认可与支持创作验收", Slug: "recognition-migration-browser", AuthorID: owner.UserID, AuthorName: owner.DisplayName, ContentSource: "<h2>认真创作</h2><p>勋章、认证与支持创作的实际页面验收。</p>"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: reader.ID, Delta: 100000, ExpectedVersion: cfg.Version, RequestID: "recognition-migration-credit", Reason: "验收准备"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{reader.ID, owner.UserID} {
		if _, err = service.UpdateIdentity(t.Context(), owner.UserID, true, recognition.IdentityInput{UserID: id, Version: 1, Action: "grant", Key: "original", Label: "原创作者", Reason: "验收准备", Days: 30}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.UpdateIdentity(t.Context(), reader.ID, false, recognition.IdentityInput{UserID: reader.ID, Version: 2, Action: "apply", Key: "creator", Label: "摄影作者", Evidence: "PRIVATE_RECOGNITION_EVIDENCE"}); err != nil {
		t.Fatal(err)
	}
	before, err := service.Backend.Read(t.Context())
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
	revisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
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
	login, readerLogin, stop := "/__recognition_owner/"+nonce, "/__recognition_reader/"+nonce, "/__recognition_stop/"+nonce
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
	write("fixture.json", map[string]any{"base": fixture.URL, "login": login, "reader_login": readerLogin, "stop": stop, "post_id": post.ID, "owner_id": owner.UserID, "reader_id": reader.ID, "reader_username": reader.Username, "owner_username": owner.Username, "paths": map[string]string{"article": "/posts/" + post.Slug, "account": "/account/?view=identity", "admin": "/admin/recognition/"}})
	t.Log("recognition migration fixture ready")
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
	after, err := service.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("recognition state changed", err)
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
	write("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "content_unchanged": true, "revisions_unchanged": true, "recognition_unchanged": true, "balances_unchanged": true, "settings_unchanged": true, "unintercepted_writes": escaped.Load()})
}
