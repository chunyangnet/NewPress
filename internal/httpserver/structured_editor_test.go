package httpserver

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestStructuredEditorReuseUsesContentOwnership(t *testing.T) {
	s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	a, cookieA, _ := createAndLoginTestUser(t, manager, owner, "blocks-author-a", "blocks-a@example.test", "作者 A", "author")
	b, _, _ := createAndLoginTestUser(t, manager, owner, "blocks-author-b", "blocks-b@example.test", "作者 B", "author")
	_, reader, _ := createAndLoginTestUser(t, manager, owner, "blocks-reader", "blocks-reader@example.test", "读者", "subscriber")
	markup := `<aside data-callout="note"><p>常用片段</p></aside><script>secretScript()</script>`
	var ownID, otherID int64
	for _, target := range []struct {
		id          int64
		title, slug string
	}{{a.ID, "自用模板", "own-template"}, {b.ID, "他人私密模板", "other-template"}} {
		post, err := s.repository.Create(t.Context(), content.PostInput{AuthorID: target.id, Title: target.title, Slug: target.slug, Status: content.StatusDraft, ContentSource: markup})
		if err != nil {
			t.Fatal(err)
		}
		if target.id == a.ID {
			ownID = post.ID
		} else {
			otherID = post.ID
		}
	}
	editor := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/new/", cookieA, nil)
	for _, control := range []string{"data-composer", "data-editor-reuse-open", "data-block-add=\"table\"", "newpress-document-editor.js", "newpress-editor-page.js", "data-editor-tools-loader"} {
		if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), control) {
			t.Fatalf("editor missing %s: %d", control, editor.Code)
		}
	}
	if strings.Contains(editor.Body.String(), "js/chunnel-composer.js") {
		t.Fatal("article editor still loads the shop block renderer")
	}
	tools := regexp.MustCompile(`data-editor-tools-script="([^"]+)"`).FindStringSubmatch(editor.Body.String())
	if len(tools) != 2 || !strings.Contains(tools[1], "/newpress-editor-insert-tools.js") || strings.Contains(editor.Body.String(), "js/chunnel-editor-reuse.js") {
		t.Fatal("reuse must be available through the declared insertion tools bundle")
	}
	toolSource := serveCookieRequest(handler, http.MethodGet, "https://example.test"+html.UnescapeString(tools[1]), cookieA, nil)
	if toolSource.Code != http.StatusOK || !strings.Contains(toolSource.Body.String(), "data-editor-reuse-open") {
		t.Fatal("declared insertion tools do not provide the reuse entry", toolSource.Code)
	}
	list := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/contents?status=draft", cookieA, nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "他人私密模板") {
		t.Fatal("reuse listing leaked another author's draft", list.Code)
	}
	detail := serveCookieRequest(handler, http.MethodGet, fmt.Sprintf("https://example.test/api/v1/admin/contents/%d", ownID), cookieA, nil)
	var body adminContentAPIResponse
	if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil || detail.Code != http.StatusOK || !strings.Contains(body.ContentHTML, `data-callout="note"`) || strings.Contains(body.ContentHTML, "secretScript") {
		t.Fatal("unsafe or unavailable reusable body", err)
	}
	if !strings.Contains(detail.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private reusable source can be cached")
	}
	if denied := serveCookieRequest(handler, http.MethodGet, fmt.Sprintf("https://example.test/api/v1/admin/contents/%d", otherID), cookieA, nil); denied.Code != http.StatusForbidden {
		t.Fatal("cross-author reuse read allowed", denied.Code)
	}
	if denied := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/contents", reader, nil); denied.Code != http.StatusForbidden {
		t.Fatal("subscriber content listing allowed", denied.Code)
	}
}

func TestStructuredEditorSessionRecoveryIndependentOfContentAPI(t *testing.T) {
	s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
	author, cookie, principal := createAndLoginTestUser(t, manager, owner, "session-writer", "session-writer@example.test", "恢复写作", "author")
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.API.AdminContentEnabled = false
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("disable content API: %v %v", err, fields)
	}
	session := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/session", cookie, nil)
	var body adminSessionAPIResponse
	if err := json.Unmarshal(session.Body.Bytes(), &body); err != nil || session.Code != http.StatusOK {
		t.Fatalf("session unavailable with content API disabled: %d %v", session.Code, err)
	}
	if !body.Authenticated || body.ReadOnly || body.UserID != author.ID || body.CSRFToken != principal.CSRFToken || len(body.RoleCapabilities) == 0 {
		t.Fatal("session recovery lost its identity or capability contract")
	}
	editor := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/new/", cookie, nil)
	for _, expected := range []string{fmt.Sprintf(`data-editor-user-id="%d"`, author.ID), `data-editor-login="session-writer"`, "data-editor-retry", "data-editor-login-link"} {
		if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), expected) {
			t.Fatalf("editor missing recovery contract %q: %d", expected, editor.Code)
		}
	}
	denied := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/contents", cookie, nil)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("disabled content API remains exposed: %d", denied.Code)
	}
}
