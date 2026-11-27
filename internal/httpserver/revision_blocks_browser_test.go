package httpserver

import (
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// Explicitly opt-in; all content, accounts and settings are synthetic. The
// fixture binds to loopback and exits only after an authenticated stop request.
func TestRevisionBlocksBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_REVISION_BLOCKS_BROWSER") != "1" {
		t.Skip("opt-in browser fixture")
	}
	dir := os.Getenv("NEWPRESS_REVISION_BLOCKS_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("browser evidence directory must be explicit and absolute")
	}
	s, handler, accounts, repository, cookie, owner := newPersistentAdminTestServer(t)
	type snapshot struct {
		Post      content.Post
		Revisions []content.Revision
		URL       string
	}
	snapshots := map[string]snapshot{}
	stable := strings.Repeat("<p>保留这一段已核对的背景。</p>", 10)
	before := `<h2>写作核对</h2><p>第一版正文，保留作者观点。</p>` + stable +
		"<pre><code>old_value = 1\nprint(old_value)\n</code></pre>" +
		`<aside data-callout="note"><p>发布前核对版本。</p></aside><script>window.revisionInjected = true</script>` +
		`<table><tbody><tr><th>项目</th><th>值</th></tr><tr><td>已核对</td><td>3</td></tr></tbody></table>`
	for _, sample := range []struct{ name, title, old, current string }{
		{"blocks", "历史正文与当前正文", before, strings.ReplaceAll(strings.Replace(before, "第一版正文", "第二版正文", 1), "old_value", "current_value") + `<p>新补充的结尾。</p>`},
		{"fallback", "旧格式正文核对", "<p>旧正文</p>", "<p>未闭合正文，保留源码核对"},
		{"capacity", "超过内容块范围", "<p>旧正文</p>", strings.Repeat("<p>完整保留正文</p>", revisionBlockMaxCount+1)},
	} {
		input := content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: sample.title,
			Slug: "revision-fixture-" + sample.name, AuthorID: owner.UserID, AuthorName: "核对人员",
			Actor: "fixture", ContentSource: sample.old}
		post, err := repository.Create(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		input.ContentSource = sample.current
		post, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "fixture"}, input)
		if err != nil {
			t.Fatal(err)
		}
		revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
		if err != nil || len(revisions) != 2 {
			t.Fatal("fixture revisions unavailable", err)
		}
		snapshots[sample.name] = snapshot{Post: post, Revisions: revisions,
			URL: "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/?from=" + strconv.FormatInt(revisions[1].ID, 10) + "&to=current"}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(random[:])
	login, stop := "/__revision_fixture_login/"+nonce, "/__revision_fixture_stop/"+nonce
	done, once := make(chan struct{}), sync.Once{}
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == login && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, snapshots["blocks"].URL, http.StatusSeeOther)
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
	writeEvidence := func(name string, value any) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr := json.NewEncoder(f).Encode(value)
		closeErr := f.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("write evidence: %v / %v", encodeErr, closeErr)
		}
	}
	urls := map[string]string{}
	for name, snapshot := range snapshots {
		urls[name] = server.URL + snapshot.URL
	}
	writeEvidence("fixture.json", map[string]any{"base": server.URL, "login": server.URL + login,
		"stop": server.URL + stop, "urls": urls, "account_id": owner.UserID})
	t.Log("revision block browser fixture ready")
	select {
	case <-done:
	case <-time.After(15 * time.Minute):
		t.Fatal("browser fixture was not explicitly stopped within 15 minutes")
	case <-t.Context().Done():
		t.Fatal("browser fixture interrupted before explicit stop")
	}
	proof := map[string]any{}
	for name, before := range snapshots {
		post, err := repository.GetByID(t.Context(), before.Post.ID)
		if err != nil || !reflect.DeepEqual(post, before.Post) {
			t.Fatal("browser comparison changed current content", name, err)
		}
		revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
		if err != nil || !reflect.DeepEqual(revisions, before.Revisions) {
			t.Fatal("browser comparison changed revisions", name, err)
		}
		digest := sha256.Sum256([]byte(post.ContentSource))
		proof[name] = map[string]any{"post_id": post.ID, "version": post.Version, "revisions": len(revisions),
			"source_sha256": fmt.Sprintf("%x", digest), "unchanged": true}
	}
	writeEvidence("read-only-confirmation.json", proof)
}
