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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/shop"
)

// Explicit opt-in fixture: temporary accounts/settings and in-memory content.
// The production handler is unchanged; test control paths only use loopback.
func TestAdminResourcesBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_ADMIN_RESOURCES_BROWSER") != "1" {
		t.Skip("opt-in admin resource browser fixture")
	}
	dir := os.Getenv("NEWPRESS_ADMIN_RESOURCES_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("browser evidence directory must be explicit and absolute")
	}
	s, handler, accounts, repository, cookie, owner := newPersistentAdminTestServer(t)
	readOnlyEditor := os.Getenv("NEWPRESS_EDITOR_WORKSPACE_BROWSER") == "1"
	var editorSettingsBefore []byte
	if readOnlyEditor {
		snapshot := s.siteSettings.Snapshot()
		snapshot.Settings.Writing.BrowserRecoveryEnabled = true
		if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
			t.Fatalf("editor recovery fixture policy: %v %v", fields, err)
		}
		editorSettingsBefore, _ = json.Marshal(s.siteSettings.Snapshot())
	}
	var navigationBefore *navigation.Snapshot
	navigationPrimary, navigationExplore := "/admin/navigation/", "/admin/navigation/?location=explore"
	if os.Getenv("NEWPRESS_ADMIN_NAVIGATION_BROWSER") == "1" {
		for _, location := range []string{navigation.LocationPrimary, navigation.LocationExplore} {
			menu, err := s.navigation.Create(t.Context(), navigation.MenuInput{Name: "导航交互验收 " + location, Slug: "browser-" + location, Locations: []string{location}}, owner.Username)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.navigation.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, []navigation.ItemInput{
				{ID: -1, Label: "第一组", URL: "/", Icon: "house", Presentation: navigation.PresentationPanel, Badge: navigation.Badge{Text: "新", Radius: 4}},
				{ID: -2, ParentID: -1, Label: "第一组子项", URL: "/explore/", Icon: "compass"},
				{ID: -3, ParentID: -2, Label: "第三层", URL: "/search/", Icon: "search"},
				{ID: -4, Label: "第二组", URL: "/community/", Icon: "users"},
				{ID: -5, ParentID: -4, Label: "第二组子项", URL: "/links/", Icon: "link"},
				{ID: -6, Label: "独立入口", URL: "/archive/", Icon: "archive"},
			})
			if err != nil {
				t.Fatal(err)
			}
			path := "/admin/navigation/?menu=" + strconv.FormatInt(menu.ID, 10)
			if location == navigation.LocationPrimary {
				navigationPrimary = path
			} else {
				navigationExplore = "/admin/navigation/?location=explore"
			}
		}
		snapshot, err := s.navigation.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		navigationBefore = &snapshot
	}
	var appearanceBefore []byte
	if os.Getenv("NEWPRESS_ADMIN_LAYOUT_BROWSER") == "1" {
		var err error
		appearanceBefore, err = json.Marshal(s.themes.SettingsSnapshot())
		if err != nil {
			t.Fatal(err)
		}
	}
	var composerShop shop.Backend
	var shopBefore shop.State
	if os.Getenv("NEWPRESS_ADMIN_COMPOSER_BROWSER") == "1" {
		backend, err := shop.NewFileBackend("")
		if err != nil {
			t.Fatal(err)
		}
		composerShop = backend
		s.SetShopService(&shop.Service{Backend: backend})
		shopBefore, err = backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
	}
	input := content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "后台资源核对",
		Slug: "admin-resource-fixture", AuthorID: owner.UserID, AuthorName: "核对人员", Actor: "fixture",
		ContentSource: "<h2>布局与源码核对</h2><p>第一份正文。</p>"}
	post, err := repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.ContentSource = "<h2>布局与源码核对</h2><p>第二份正文。</p>"
	post, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "fixture"}, input)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 2 {
		t.Fatal("fixture revisions unavailable", err)
	}
	var businessComment *comment.Comment
	var businessPost *content.Post
	commentEditorPath := ""
	if os.Getenv("NEWPRESS_ADMIN_BUSINESS_BROWSER") == "1" {
		article := createOwnedTestPost(t, repository, owner.UserID, owner.DisplayName, "admin-business-comments", "后台评论交互核对")
		businessPost = &article
		item, createErr := s.comments.Create(t.Context(), comment.CreateInput{PostID: article.ID, AuthorName: "验收读者", AuthorEmail: "acceptance@example.test", Status: comment.StatusApproved, ContentSource: "原始评论。"})
		if createErr != nil {
			t.Fatal(createErr)
		}
		businessComment = &item
		commentEditorPath = "/admin/comments/" + strconv.FormatInt(item.ID, 10) + "/edit/"
	}
	userBefore, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(random[:])
	login, stop := "/__admin_resource_login/"+nonce, "/__admin_resource_stop/"+nonce
	done, once := make(chan struct{}), sync.Once{}
	var uninterceptedWrites atomic.Int64
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == login && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
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
		if readOnlyEditor && r.Method != http.MethodGet && r.Method != http.MethodHead {
			uninterceptedWrites.Add(1)
			http.Error(w, "editor acceptance writes must be intercepted", http.StatusForbidden)
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
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr := json.NewEncoder(file).Encode(value)
		closeErr := file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("write evidence: %v / %v", encodeErr, closeErr)
		}
	}
	response := httptest.NewRecorder()
	s.writeEditorSubmissionResult(response, "editor-acceptance", content.SubmissionResult{Receipt: content.SubmissionReceipt{
		PostID: post.ID, Version: post.Version + 1, Action: "save",
	}})
	now := time.Now().UTC()
	autosaveProjection := s.autosaveResponse(content.Autosave{PostID: post.ID, SourceVersion: post.Version,
		UpdatedAt: now, ExpiresAt: now.Add(time.Hour), Snapshot: post}, "editor-acceptance")
	writeEvidence("fixture.json", map[string]any{"base": server.URL, "login": login, "stop": stop,
		"editor_receipt": json.RawMessage(response.Body.Bytes()), "autosave_receipt": autosaveProjection,
		"account_id": owner.UserID, "editor_post_id": post.ID, "editor_version": post.Version, "comment_editor": commentEditorPath, "paths": map[string]string{
			"dashboard": "/admin/", "editor": "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/",
			"revision":   "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/?from=" + strconv.FormatInt(revisions[1].ID, 10) + "&to=current",
			"categories": "/admin/taxonomy/", "users": "/admin/users/", "sessions": "/admin/sessions/",
			"navigation": navigationPrimary, "navigation_explore": navigationExplore, "plugins": "/admin/plugins/", "login": "/admin/login/",
		}})
	t.Log("isolated admin resource browser fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("browser fixture was not explicitly stopped within 25 minutes")
	case <-t.Context().Done():
		t.Fatal("browser fixture interrupted before explicit stop")
	}
	after, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(after, post) {
		t.Fatal("resource checks changed current content", err)
	}
	afterRevisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || !reflect.DeepEqual(afterRevisions, revisions) {
		t.Fatal("resource checks changed revision history", err)
	}
	userAfter, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil || !reflect.DeepEqual(userAfter, userBefore) {
		t.Fatal("resource checks changed the account", err)
	}
	if businessComment != nil {
		current, readErr := s.comments.Get(t.Context(), businessComment.ID)
		if readErr != nil || !reflect.DeepEqual(current, *businessComment) {
			t.Fatal("business checks changed the comment", readErr)
		}
	}
	if businessPost != nil {
		current, readErr := repository.GetByID(t.Context(), businessPost.ID)
		if readErr != nil || !reflect.DeepEqual(current, *businessPost) {
			t.Fatal("business checks changed the published article", readErr)
		}
	}
	if composerShop != nil {
		current, readErr := composerShop.Read(t.Context())
		if readErr != nil || !reflect.DeepEqual(current, shopBefore) {
			t.Fatal("composer checks changed shop state", readErr)
		}
	}
	if appearanceBefore != nil {
		current, readErr := json.Marshal(s.themes.SettingsSnapshot())
		if readErr != nil || !reflect.DeepEqual(current, appearanceBefore) {
			t.Fatal("layout preview checks changed saved theme settings", readErr)
		}
	}
	if navigationBefore != nil {
		current, readErr := s.navigation.Snapshot(t.Context())
		if readErr != nil || !reflect.DeepEqual(current, *navigationBefore) {
			t.Fatal("navigation checks changed saved menus", readErr)
		}
	}
	if uninterceptedWrites.Load() != 0 {
		t.Fatalf("editor acceptance leaked %d mutation requests", uninterceptedWrites.Load())
	}
	if readOnlyEditor {
		current, err := json.Marshal(s.siteSettings.Snapshot())
		if err != nil || !reflect.DeepEqual(current, editorSettingsBefore) {
			t.Fatal("editor acceptance changed settings", err)
		}
	}
	writeEvidence("read-only-confirmation.json", map[string]any{"post_id": post.ID, "version": post.Version, "unintercepted_writes": uninterceptedWrites.Load(),
		"revisions": len(revisions), "content_unchanged": true, "revisions_unchanged": true, "account_unchanged": true, "comment_checked": businessComment != nil, "business_post_checked": businessPost != nil, "shop_checked": composerShop != nil, "theme_settings_checked": appearanceBefore != nil, "navigation_checked": navigationBefore != nil})
}
