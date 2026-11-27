package httpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

func TestAdminUserDialogCompleteAccountAndValidation(t *testing.T) {
	_, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	u, _, _ := createAndLoginTestUser(t, manager, owner, "modal-reader", "modal-reader@example.test", "弹窗用户", "subscriber")
	id := strconv.FormatInt(u.ID, 10)
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/users/?edit="+id, cookie, nil)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	for _, text := range []string{`<dialog`, `data-user-edit`, `data-auto-open="true"`, `name="bio"`, `name="phone"`, `name="avatar_media_id"`, `name="phone_verified"`, `会员与经验`, `积分与余额`, `头衔与认证`} {
		if !strings.Contains(page.Body.String(), text) {
			t.Errorf("editor missing %s", text)
		}
	}
	values := url.Values{"csrf_token": {owner.CSRFToken}, "action": {"update"}, "id": {id}, "version": {strconv.FormatInt(u.Version, 10)}, "username": {u.Username}, "email": {u.Email}, "display_name": {"完整账户"}, "status": {auth.UserStatusSuspended}, "locale": {"zh-CN"}, "role": {"author", "subscriber"}, "account_details": {"1"}, "bio": {"简介\n第二行"}, "phone": {"+8613800138000"}, "avatar_media_id": {"0"}, "email_verified": {"true"}, "phone_verified": {"true"}}
	response := serveUserForm(handler, cookie, values, "application/x-www-form-urlencoded", "https://example.test")
	if response.Code != 303 {
		t.Fatal(response.Code, response.Body.String())
	}
	fresh, err := manager.GetUser(t.Context(), u.ID)
	if err != nil || fresh.Bio != "简介\n第二行" || fresh.Phone != values.Get("phone") || fresh.EmailVerifiedAt == nil || fresh.PhoneVerifiedAt == nil || !fresh.HasRole("author") || !fresh.HasRole("subscriber") || fresh.Status != auth.UserStatusSuspended {
		t.Fatalf("account fields: %+v %v", fresh, err)
	}
	values.Set("version", strconv.FormatInt(fresh.Version, 10))
	values.Set("bio", strings.Repeat("字", 281))
	invalid := serveUserForm(handler, cookie, values, "application/x-www-form-urlencoded", "https://example.test")
	if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `data-auto-open="true"`) || !strings.Contains(invalid.Body.String(), "简介不能超过") {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
	values.Set("bio", "不应保存")
	values.Set("avatar_media_id", "9999999")
	invalid = serveUserForm(handler, cookie, values, "application/x-www-form-urlencoded", "https://example.test")
	if invalid.Code != 422 {
		t.Fatal("missing avatar accepted", invalid.Code)
	}
	after, _ := manager.GetUser(t.Context(), u.ID)
	if after.Version != fresh.Version || after.Bio != fresh.Bio {
		t.Fatal("invalid edit changed account")
	}
	values.Del("avatar_media_id")
	if response = serveUserForm(handler, cookie, values, "application/x-www-form-urlencoded", "https://example.test"); response.Code != 422 {
		t.Fatal("partial account group accepted", response.Code)
	}
}

func TestAdminUserMembershipDatedEditAPI(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, _ := progressionHTTPStore(t, s)
	u, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "membership-editor", "member-edit@example.test", "会员编辑", "subscriber")
	now := time.Now().UTC().Truncate(time.Second)
	input := map[string]any{"user_id": strconv.FormatInt(u.ID, 10), "version": strconv.FormatInt(cfg.Version, 10), "member_version": "0", "action": "set", "plan": "member", "days": 0, "since": now, "until": now.AddDate(0, 1, 0), "request_id": "http-membership-editor", "reason": "用户弹窗调整"}
	raw, _ := json.Marshal(input)
	target := "https://example.test/api/v1/admin/progression/membership"
	if response := serveContentAPI(handler, readerCookie, "POST", target, string(raw), authenticatedAPIHeaders(reader)); response.Code != 403 {
		t.Fatal("subscriber edited membership", response.Code)
	}
	for i := 0; i < 2; i++ {
		if response := serveContentAPI(handler, cookie, "POST", target, string(raw), authenticatedAPIHeaders(owner)); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	snapshot, err := store.Snapshot(t.Context(), u.ID, 0)
	if err != nil || len(snapshot.Entries) != 1 || !snapshot.Account.Membership.Until.Equal(now.AddDate(0, 1, 0)) {
		t.Fatal(snapshot, err)
	}
}
