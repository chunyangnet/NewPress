package httpserver

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

func TestAdminUserOAuthSearchAndRemoval(t *testing.T) {
	_, h, m, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	u, cookie, _ := createAndLoginTestUser(t, m, owner, "oauth-admin-target", "oauth-target@example.test", "OAuth target", "subscriber")
	u, err := m.UpdateOAuthBinding(t.Context(), u, "github", secretDigest("admin binding"))
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"github", "bound", "unbound", "google"} {
		result, err := m.ListUsers(t.Context(), auth.UserQuery{Search: u.Username, OAuthProvider: filter, Limit: 10})
		want := 0
		if filter == "github" || filter == "bound" {
			want = 1
		}
		if err != nil || result.Total != want {
			t.Fatal(filter, result.Total, err)
		}
	}
	page := serveContentAPI(h, ownerCookie, "GET", "https://example.test/admin/users/?oauth=github&q=oauth-admin-target&edit="+strconv.FormatInt(u.ID, 10), "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "解除 GitHub 绑定") {
		t.Fatal(page.Code, page.Body.String())
	}
	form := url.Values{"csrf_token": {owner.CSRFToken}, "action": {"oauth-remove"}, "id": {strconv.FormatInt(u.ID, 10)}, "version": {strconv.FormatInt(u.Version, 10)}, "provider": {"github"}}
	denied := serveUserForm(h, cookie, form, "application/x-www-form-urlencoded", "https://example.test")
	if denied.Code == http.StatusSeeOther {
		t.Fatal("subscriber removed binding")
	}
	bad := cloneValues(form)
	bad.Set("csrf_token", "wrong")
	if w := serveUserForm(h, ownerCookie, bad, "application/x-www-form-urlencoded", "https://example.test"); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	bad = cloneValues(form)
	bad.Set("version", strconv.FormatInt(u.Version-1, 10))
	if w := serveUserForm(h, ownerCookie, bad, "application/x-www-form-urlencoded", "https://example.test"); w.Code != 409 {
		t.Fatal("stale", w.Code)
	}
	w := serveUserForm(h, ownerCookie, form, "application/x-www-form-urlencoded", "https://example.test")
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	fresh, err := m.GetUser(t.Context(), u.ID)
	if err != nil || len(fresh.OAuthBindings) != 0 {
		t.Fatal("removal", err)
	}
	session := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/session", "", nil)
	if !strings.Contains(session.Body.String(), `"authenticated":false`) {
		t.Fatal("sessions not revoked", session.Body.String())
	}
}
