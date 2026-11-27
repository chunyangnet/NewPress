package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatWorkspaceIdentityAndSessionRecovery(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	settings := s.siteSettings.Snapshot()
	settings.Settings.API.AdminContentEnabled = false
	if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Fatal(err)
	}
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/wechat/", cookie, nil)
	for _, token := range []string{fmt.Sprintf(`data-user-id="%d"`, owner.UserID), "data-wechat-recovery", "data-wechat-recover", "data-wechat-config-state", "data-wechat-discard"} {
		if page.Code != 200 || !strings.Contains(page.Body.String(), token) {
			t.Fatal("missing workspace contract", token, page.Code)
		}
	}
	base := "https://example.test/api/v1/admin/wechat"
	read := func(cookie *http.Cookie) (int64, wechat.Config) {
		t.Helper()
		r := serveContentAPI(handler, cookie, http.MethodGet, base, "", nil)
		var response struct {
			UserID int64         `json:"user_id"`
			Config wechat.Config `json:"config"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(r.Code, r.Body.String(), err)
		}
		if response.Config.Secret != "" {
			t.Fatal("credential leak")
		}
		return response.UserID, response.Config
	}
	actor, config := read(cookie)
	if actor != owner.UserID {
		t.Fatal(actor, owner.UserID)
	}
	user, newCookie, principal := createAndLoginTestUser(t, manager, owner, "wx-session-manager", "wx-session-manager@example.test", "微信管理员", "administrator")
	token, renewed, err := manager.AuthenticateContext(t.Context(), "wx-session-manager-new", "wx-session-manager", "Account password for wx-session-manager 2026")
	if err != nil {
		t.Fatal(err)
	}
	newCookie = &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	actor, _ = read(newCookie)
	if actor != user.ID || actor == owner.UserID {
		t.Fatal("identity mismatch", actor)
	}
	session := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/session", newCookie, nil)
	var current adminSessionAPIResponse
	if err := json.Unmarshal(session.Body.Bytes(), &current); err != nil || current.UserID != actor || current.CSRFToken != renewed.CSRFToken {
		t.Fatal(current, err)
	}
	config.Author = "恢复后保存"
	body := wechatJSON(t, map[string]any{"config": config})
	denied := serveContentAPI(handler, newCookie, http.MethodPost, base+"/config", body, authenticatedAPIHeaders(principal))
	if denied.Code != 403 {
		t.Fatal("old csrf accepted", denied.Code)
	}
	saved := serveContentAPI(handler, newCookie, http.MethodPost, base+"/config", body, authenticatedAPIHeaders(renewed))
	if saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	_, after := read(newCookie)
	if after.Version != config.Version+1 || after.Author != config.Author {
		t.Fatal(after)
	}
}
