package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/plugin"
)

func TestLinkPageHTTPPermissionsStylesAndPrivacy(t *testing.T) {
	s, h, users, _, cookie, p := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, p, "link-reader", "link-reader@example.test", "读者", "subscriber")
	cfg := linkpage.Defaults()
	sequence := 0
	write := func(c *http.Cookie, a auth.Principal, command linkpage.Command, want int) {
		t.Helper()
		sequence++
		command.Key = fmt.Sprintf("link-command-%08d", sequence)
		command.ExpectedUserID = a.UserID
		b, _ := s.linkPages.Get(t.Context(), "friends")
		command.Version = b.Version
		raw, _ := json.Marshal(command)
		res := serveContentAPI(h, c, "POST", "https://example.test/api/v1/admin/link-pages/friends", string(raw), authenticatedAPIHeaders(a))
		if res.Code != want {
			t.Fatalf("%s: %d %s", command.Action, res.Code, res.Body.String())
		}
	}
	write(readerCookie, reader, linkpage.Command{Action: "create", Settings: &cfg}, 403)
	write(cookie, p, linkpage.Command{Action: "create", Settings: &cfg}, 200)
	write(cookie, p, linkpage.Command{Action: "save_item", Item: &linkpage.Item{Name: "Visible <script>", URL: "example.com", Description: "A useful site", Category: "设计", Enabled: true}}, 200)
	write(cookie, p, linkpage.Command{Action: "save_item", Item: &linkpage.Item{Name: "SECRET DRAFT", URL: "hidden.example.com", Enabled: false}}, 200)
	for _, style := range []string{"cards", "list", "wall"} {
		res := serveContentAPI(h, nil, "GET", "https://example.test/links/friends/?style="+style, "", nil)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `data-style="`+style+`"`) || strings.Contains(res.Body.String(), "SECRET DRAFT") || strings.Contains(res.Body.String(), "Visible <script>") {
			t.Fatal(style, res.Code, res.Body.String())
		}
	}
	res := serveContentAPI(h, nil, "GET", "https://example.test/links/friends/?q=missing", "", nil)
	if strings.Contains(res.Body.String(), "A useful site") {
		t.Fatal("search not applied")
	}
	for _, path := range []string{"/admin/features/", "/admin/features/links/", "/admin/features/links/?edit=new", "/admin/features/links/?edit=friends", "/admin/features/links/?edit=friends&item=1", "/admin/navigation/"} {
		res = serveContentAPI(h, cookie, "GET", "https://example.test"+path, "", nil)
		if res.Code != 200 || strings.Contains(res.Body.String(), "ZgotmplZ") {
			t.Fatal(path, res.Code, res.Body.String())
		}
		if strings.HasPrefix(path, "/admin/features/links/") && !strings.Contains(res.Body.String(), "js/chunnel-links.js") {
			t.Fatal("link admin script missing")
		}
	}
	cfg.Enabled = false
	write(cookie, p, linkpage.Command{Action: "configure", Settings: &cfg}, 200)
	res = serveContentAPI(h, nil, "GET", "https://example.test/links/friends/", "", nil)
	if res.Code != 404 {
		t.Fatal("hidden page exposed", res.Code)
	}
	res = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/admin/link-metadata", `{"url":"https://example.com"}`, nil)
	if res.Code != 401 && res.Code != 403 {
		t.Fatal("anonymous extraction", res.Code)
	}
	raw, _ := json.Marshal(map[string]any{"url": "https://127.0.0.1", "expected_user_id": p.UserID})
	res = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/link-metadata", string(raw), authenticatedAPIHeaders(p))
	if res.Code != 422 {
		t.Fatal("SSRF accepted", res.Code, res.Body.String())
	}
}
func TestPluginBusinessHTTPAuthAndEscaping(t *testing.T) {
	s, h, users, repo, cookie, p := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, p, "plugin-reader", "plugin-reader@example.test", "读者", "subscriber")
	// Use an official installation fixture and the distributed real WASM binary.
	root := t.TempDir()
	s.plugins = plugin.NewManager(root)
	dir := filepath.Join(root, "content-age")
	os.MkdirAll(dir, 0700)
	for _, file := range []string{"plugin.json", "plugin.wasm"} {
		b, e := os.ReadFile(filepath.Join("..", "..", "examples", "plugins", "content-age", file))
		if e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(dir, file), b, 0600)
	}
	os.WriteFile(filepath.Join(dir, ".disabled"), []byte("disabled"), 0600)
	manifest, config, _, e := s.plugins.Configuration("content-age")
	if e != nil {
		t.Fatal(e)
	}
	res := serveContentAPI(h, cookie, "GET", "https://example.test/admin/plugins/content-age/", "", nil)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	config.Grants = manifest.Capabilities
	config.Values["title"] = "<script>alert('plugin-xss')</script>"
	raw, _ := json.Marshal(map[string]any{"configuration": config, "expected_user_id": reader.UserID})
	res = serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/admin/plugins/content-age/configuration", string(raw), authenticatedAPIHeaders(reader))
	if res.Code != 403 {
		t.Fatal("reader configured plugin", res.Code)
	}
	raw, _ = json.Marshal(map[string]any{"configuration": config, "expected_user_id": p.UserID})
	res = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/plugins/content-age/configuration", string(raw), authenticatedAPIHeaders(p))
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	res = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/plugins/content-age/configuration", string(raw), authenticatedAPIHeaders(p))
	if res.Code != 409 {
		t.Fatal("config conflict", res.Code)
	}
	manifestBytes, _ := json.Marshal(manifest)
	wasmBytes, e := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if e != nil {
		t.Fatal(e)
	}
	manifestHash, wasmHash := sha256.Sum256(manifestBytes), sha256.Sum256(wasmBytes)
	if e = plugin.WritePrivate(filepath.Join(dir, ".official.json"), plugin.Release{Manifest: manifest}); e != nil {
		t.Fatal(e)
	}
	if e = plugin.WritePrivate(filepath.Join(dir, ".integrity.json"), map[string]string{"manifest": hex.EncodeToString(manifestHash[:]), "module": hex.EncodeToString(wasmHash[:])}); e != nil {
		t.Fatal(e)
	}
	if e = s.plugins.SetEnabled("content-age", true); e != nil {
		t.Fatal(e)
	}
	// Core exposes only public summaries, never drafts or authentication fields.
	posts, _ := repo.SearchPublished(t.Context(), content.PublishedQuery{Limit: 1})
	if len(posts.Items) > 0 {
		value, e := s.pluginReadContent(t.Context(), posts.Items[0].ID)
		b, _ := json.Marshal(value)
		if e != nil || strings.Contains(string(b), "content_source") {
			t.Fatal(e, string(b))
		}
		post := posts.Items[0]
		post.UpdatedAt = time.Now().Add(-200 * 24 * time.Hour)
		w := httptest.NewRecorder()
		s.renderPublicPost(w, httptest.NewRequest("GET", "https://example.test/posts/"+post.Slug, nil), 200, post, 1, publicCommentForm{}, nil, adminNotice{})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "plugin-article-note") || !strings.Contains(w.Body.String(), "&lt;script&gt;") || strings.Contains(w.Body.String(), "<script>alert('plugin-xss')") {
			t.Fatal("unsafe or missing plugin fragment", w.Code)
		}
		if e = s.plugins.SetEnabled("content-age", false); e != nil {
			t.Fatal(e)
		}
		if len(s.pluginRender(t.Context(), post)) != 0 {
			t.Fatal("disabled fragment remains")
		}
	}
}
