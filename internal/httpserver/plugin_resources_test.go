package httpserver

import (
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/commentorigin"
)

func TestPluginAdminScriptScopes(t *testing.T) {
	s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	installTaskFixture(t, s)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	s.commentOrigin = commentorigin.New(s.plugins)
	s.SetPluginTaskStore(newSharedReviewPreviewStore(t))
	s.SetPluginWebhookStore(&migrationWebhookStore{})
	for _, page := range []struct{ path, script string }{
		{"/admin/plugins/", "chunnel-plugins.js"},
		{"/admin/plugins/comment-origin/", "chunnel-plugins.js"},
		{"/admin/plugins/task-fixture/", "chunnel-plugin-runtime.js"},
		{"/admin/plugin-records/", "newpress-plugin-records.js"},
		{"/admin/plugin-webhooks/", "newpress-plugin-webhooks.js"},
	} {
		t.Run(page.path, func(t *testing.T) {
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, cookie, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("page status %d", response.Code)
			}
			for _, script := range []string{"chunnel-plugins.js", "chunnel-plugin-runtime.js", "newpress-plugin-records.js", "newpress-plugin-webhooks.js"} {
				want := 0
				if script == page.script {
					want = 1
				}
				if count := strings.Count(response.Body.String(), "/js/"+script); count != want {
					t.Errorf("%s reference count %d on %s", script, count, page.path)
				}
			}
		})
	}
}
