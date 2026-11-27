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
	for _, required := range []string{`data-browser-recovery-enabled="true"`, "data-browser-recovery-toggle", "chunnel-editor-recovery.js", "明文"} {
		if !strings.Contains(page, required) {
			t.Fatalf("editor missing %s", required)
		}
	}
	if strings.Index(page, "chunnel-editor-recovery.js") > strings.Index(page, "chunnel-admin.js") {
		t.Fatal("recovery module must load before editor initialization")
	}
	for _, path := range []string{"/", "/admin/content/", "/admin/settings/"} {
		if strings.Contains(read(path), "chunnel-editor-recovery.js") {
			t.Fatalf("recovery script loaded outside editor: %s", path)
		}
	}
}
