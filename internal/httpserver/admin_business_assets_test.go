package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminBusinessScriptScopes(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, page := range []struct {
		path    string
		entries []string
	}{
		{"/admin/", nil},
		{"/admin/content/", []string{"lists"}},
		{"/admin/content/new/", nil},
		{"/admin/taxonomy/", []string{"lists"}},
		{"/admin/comments/", []string{"lists", "comments"}},
		{"/admin/settings/", []string{"settings"}},
		{"/admin/appearance/", []string{"settings"}},
		{"/admin/appearance/layout/", []string{"settings"}},
		{"/admin/tools/", []string{"settings"}},
		{"/admin/media/", []string{"media-upload"}},
		{"/admin/navigation/", nil},
		{"/admin/plugins/", nil},
		{"/admin/analytics/", nil},
		{"/admin/login/", []string{"login"}},
	} {
		t.Run(page.path, func(t *testing.T) {
			session := cookie
			if page.path == "/admin/login/" {
				session = nil
			}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, session, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("page status = %d", response.Code)
			}
			body := response.Body.String()
			for _, name := range []string{"lists", "comments", "settings", "media-upload", "login"} {
				want := false
				for _, entry := range page.entries {
					want = want || entry == name
				}
				resource := "js/chunnel-admin-" + name + ".js"
				count := strings.Count(body, resource)
				if (want && count != 1) || (!want && count != 0) {
					t.Errorf("%s count = %d, expected present = %t", resource, count, want)
				}
				if want && name != "login" && strings.Index(body, resource) < strings.Index(body, "js/chunnel-admin.js") {
					t.Errorf("%s loaded before its common helpers", resource)
				}
			}
		})
	}
}
