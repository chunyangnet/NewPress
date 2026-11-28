package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEditorBrowserRecoveryPolicyAndPageIsolation(t *testing.T) {
	s, handler, _, cookie, _ := newAuthenticatedContentServer(t)
	read := func(path string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, response.Code)
		}
		return response.Body.String()
	}
	page := read("/admin/content/new/")
	if !strings.Contains(page, `data-browser-recovery-enabled="false"`) || strings.Contains(page, "data-browser-recovery-toggle") {
		t.Fatal("browser persistence must start disabled")
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Writing.BrowserRecoveryEnabled = true
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("save: %v %v", fields, err)
	}
	page = read("/admin/content/new/")
	for _, required := range []string{`data-browser-recovery-enabled="true"`, "data-browser-recovery-toggle", "newpress-editor-page.js", "明文"} {
		if !strings.Contains(page, required) {
			t.Fatalf("editor missing %s", required)
		}
	}
	// Recovery initializes inside the eager page bundle. The generated-bundle
	// browser checks verify that enabling it restores drafts across real tabs.
	pageEntry := strings.Index(page, "/newpress-editor-page.js")
	documentEntry := strings.Index(page, "/newpress-document-editor.js")
	if strings.Count(page, "/newpress-editor-page.js") != 1 || documentEntry < 0 || pageEntry > documentEntry {
		t.Fatal("the editor page bundle must load once before the document editor")
	}
	for _, path := range []string{"/", "/admin/content/", "/admin/settings/"} {
		body := read(path)
		if strings.Contains(body, "newpress-editor-page.js") || strings.Contains(body, "chunnel-editor-recovery.js") {
			t.Fatalf("recovery entry loaded outside editor: %s", path)
		}
	}
}
