package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestAdminSessionsHTMLScopesAndSafeProjection(t *testing.T) {
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	ownerOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "192.0.2.31", "owner", "Persistent owner password 2026", auth.SessionMetadata{
		IPAddress: "192.0.2.31",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, authorCookie, authorPrincipal := createAndLoginTestUser(t, manager, owner, "session-author", "session-author@example.test", "会话作者", "author")
	authorOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "198.51.100.42", "session-author", "Account password for session-author 2026", auth.SessionMetadata{
		IPAddress: "198.51.100.42", UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 Version/18.5 Mobile/15E148 Safari/604.1",
	})
	if err != nil {
		t.Fatal(err)
	}

	ownerResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/sessions/", ownerCookie, nil)
	ownerBody := ownerResponse.Body.String()
	if ownerResponse.Code != http.StatusOK || !strings.Contains(ownerBody, "登录与设备") || !strings.Contains(ownerBody, "Microsoft Edge 138") || !strings.Contains(ownerBody, "192.0.2.31") || !strings.Contains(ownerBody, "会话作者") {
		t.Fatalf("owner sessions page = %d %s", ownerResponse.Code, ownerBody)
	}
	for _, secret := range []string{ownerCookie.Value, ownerOtherToken, authorCookie.Value, authorOtherToken, authorPrincipal.CSRFToken} {
		if secret != "" && strings.Contains(ownerBody, secret) {
			t.Fatalf("sessions page exposed secret material %q", secret)
		}
	}

	authorResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/sessions/", authorCookie, nil)
	authorBody := authorResponse.Body.String()
	if authorResponse.Code != http.StatusOK || !strings.Contains(authorBody, "会话作者") || !strings.Contains(authorBody, "198.51.100.42") || strings.Contains(authorBody, "站点所有者") || strings.Contains(authorBody, "192.0.2.31") {
		t.Fatalf("author sessions scope = %d %s", authorResponse.Code, authorBody)
	}
	authorAll := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/sessions/?scope=all", authorCookie, nil)
	if authorAll.Code != http.StatusForbidden || !strings.Contains(authorAll.Body.String(), "只能查看并管理自己的登录设备") {
		t.Fatalf("author all scope = %d %s", authorAll.Code, authorAll.Body.String())
	}

	_, subscriberCookie, _ := createAndLoginTestUser(t, manager, owner, "session-reader", "session-reader@example.test", "会话订阅者", "subscriber")
	subscriberResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/sessions/", subscriberCookie, nil)
	if subscriberResponse.Code != http.StatusForbidden {
		t.Fatalf("subscriber sessions page = %d", subscriberResponse.Code)
	}

	previewHandler := newUnconfiguredSessionTestHandler(t)
	previewRequest := httptest.NewRequest(http.MethodGet, "http://localhost/admin/sessions/", nil)
	previewRequest.RemoteAddr = "127.0.0.1:41234"
	previewResponse := httptest.NewRecorder()
	previewHandler.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK || !strings.Contains(previewResponse.Body.String(), "当前是本机只读预览") || !strings.Contains(previewResponse.Body.String(), "当前范围没有活动会话") {
		t.Fatalf("loopback session preview = %d %s", previewResponse.Code, previewResponse.Body.String())
	}
}

func TestAdminSessionsHTMLRevocationAndStrictWriteBoundary(t *testing.T) {
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	ownerOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "192.0.2.51", "owner", "Persistent owner password 2026", auth.SessionMetadata{IPAddress: "192.0.2.51"})
	if err != nil {
		t.Fatal(err)
	}
	_, authorCookie, authorPrincipal := createAndLoginTestUser(t, manager, owner, "boundary-author", "boundary-author@example.test", "边界作者", "author")
	authorOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "198.51.100.51", "boundary-author", "Account password for boundary-author 2026", auth.SessionMetadata{IPAddress: "198.51.100.51"})
	if err != nil {
		t.Fatal(err)
	}
	ownerOtherID := currentSessionID(t, manager, owner, ownerOtherToken, true)
	authorCurrentID := currentSessionID(t, manager, authorPrincipal, authorCookie.Value, false)
	authorOtherID := currentSessionID(t, manager, authorPrincipal, authorOtherToken, false)

	forbidden := sessionForm(authorPrincipal.CSRFToken, "revoke", "mine", ownerOtherID)
	forbiddenResponse := serveSessionForm(handler, authorCookie, forbidden, "application/x-www-form-urlencoded", "https://example.test")
	if forbiddenResponse.Code != http.StatusForbidden || !strings.Contains(forbiddenResponse.Body.String(), "不能撤销其他账号") {
		t.Fatalf("author revoke owner = %d %s", forbiddenResponse.Code, forbiddenResponse.Body.String())
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), ownerOtherToken); !ok {
		t.Fatal("forbidden operation revoked the owner's session")
	}

	current := sessionForm(authorPrincipal.CSRFToken, "revoke", "mine", authorCurrentID)
	currentResponse := serveSessionForm(handler, authorCookie, current, "application/x-www-form-urlencoded", "https://example.test")
	if currentResponse.Code != http.StatusConflict || !strings.Contains(currentResponse.Body.String(), "必须使用“退出登录”结束") {
		t.Fatalf("current session revoke = %d %s", currentResponse.Code, currentResponse.Body.String())
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), authorCookie.Value); !ok {
		t.Fatal("current-session conflict ended the active cookie")
	}

	revoke := sessionForm(authorPrincipal.CSRFToken, "revoke", "mine", authorOtherID)
	revokeResponse := serveSessionForm(handler, authorCookie, revoke, "application/x-www-form-urlencoded", "https://example.test")
	if revokeResponse.Code != http.StatusSeeOther || revokeResponse.Header().Get("Location") != "/admin/sessions/?notice=revoked&scope=mine" {
		t.Fatalf("own session revoke = %d %q %s", revokeResponse.Code, revokeResponse.Header().Get("Location"), revokeResponse.Body.String())
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), authorOtherToken); ok {
		t.Fatal("revoked author session remained valid")
	}

	authorExtraOne, _, err := manager.AuthenticateContext(t.Context(), "author-extra-one", "boundary-author", "Account password for boundary-author 2026")
	if err != nil {
		t.Fatal(err)
	}
	authorExtraTwo, _, err := manager.AuthenticateContext(t.Context(), "author-extra-two", "boundary-author", "Account password for boundary-author 2026")
	if err != nil {
		t.Fatal(err)
	}
	others := sessionForm(authorPrincipal.CSRFToken, "revoke_others", "mine", "")
	othersResponse := serveSessionForm(handler, authorCookie, others, "application/x-www-form-urlencoded", "https://example.test")
	if othersResponse.Code != http.StatusSeeOther || othersResponse.Header().Get("Location") != "/admin/sessions/?notice=others&scope=mine" {
		t.Fatalf("revoke others = %d %q %s", othersResponse.Code, othersResponse.Header().Get("Location"), othersResponse.Body.String())
	}
	for _, token := range []string{authorExtraOne, authorExtraTwo} {
		if _, ok := manager.ValidateSessionContext(t.Context(), token); ok {
			t.Fatal("revoke others left an author device active")
		}
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), authorCookie.Value); !ok {
		t.Fatal("revoke others ended the current device")
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), ownerOtherToken); !ok {
		t.Fatal("revoke others crossed the identity boundary")
	}

	unknown := sessionForm(authorPrincipal.CSRFToken, "revoke_others", "mine", "")
	unknown.Set("is_super_admin", "true")
	assertJSONError(t, serveSessionForm(handler, authorCookie, unknown, "application/x-www-form-urlencoded", "https://example.test"), http.StatusBadRequest, "invalid_session_form_shape")
	duplicate := sessionForm(authorPrincipal.CSRFToken, "revoke_others", "mine", "")
	duplicate["csrf_token"] = append(duplicate["csrf_token"], authorPrincipal.CSRFToken)
	assertJSONError(t, serveSessionForm(handler, authorCookie, duplicate, "application/x-www-form-urlencoded", "https://example.test"), http.StatusBadRequest, "invalid_session_form_shape")
	assertJSONError(t, serveSessionForm(handler, authorCookie, others, "text/plain", "https://example.test"), http.StatusUnsupportedMediaType, "form_content_type_required")
	assertJSONError(t, serveSessionForm(handler, authorCookie, others, "application/x-www-form-urlencoded", "https://attacker.test"), http.StatusForbidden, "cross_site_request_rejected")
	badCSRF := sessionForm("invalid", "revoke_others", "mine", "")
	assertJSONError(t, serveSessionForm(handler, authorCookie, badCSRF, "application/x-www-form-urlencoded", "https://example.test"), http.StatusForbidden, "csrf_validation_failed")
	oversized := sessionForm(authorPrincipal.CSRFToken, "revoke", "mine", strings.Repeat("A", adminSessionsFormMaxBytes+1))
	assertJSONError(t, serveSessionForm(handler, authorCookie, oversized, "application/x-www-form-urlencoded", "https://example.test"), http.StatusRequestEntityTooLarge, "session_form_too_large")

	_ = ownerCookie
}

func TestAdminSessionsAPIReadWriteRBACAndSafeDTO(t *testing.T) {
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	ownerOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "203.0.113.61", "owner", "Persistent owner password 2026", auth.SessionMetadata{
		IPAddress: "203.0.113.61", UserAgent: "curl/8.14.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, authorCookie, authorPrincipal := createAndLoginTestUser(t, manager, owner, "api-session-author", "api-session-author@example.test", "API 会话作者", "author")
	authorOtherToken, _, err := manager.AuthenticateWithMetadataContext(t.Context(), "198.51.100.61", "api-session-author", "Account password for api-session-author 2026", auth.SessionMetadata{
		IPAddress: "198.51.100.61", UserAgent: "Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 Chrome/138.0.0.0 Mobile Safari/537.36",
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerOtherID := currentSessionID(t, manager, owner, ownerOtherToken, true)
	authorCurrentID := currentSessionID(t, manager, authorPrincipal, authorCookie.Value, false)
	authorOtherID := currentSessionID(t, manager, authorPrincipal, authorOtherToken, false)

	ownerList := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/sessions", ownerCookie, nil)
	if ownerList.Code != http.StatusOK || ownerList.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("owner session API = %d %s", ownerList.Code, ownerList.Body.String())
	}
	var ownerPayload adminSessionsAPIResponse
	decodeSessionAPIResponse(t, ownerList, &ownerPayload)
	if ownerPayload.Scope != "all" || ownerPayload.Count < 4 || ownerPayload.UserCount < 2 || ownerPayload.ReadOnly {
		t.Fatalf("owner session payload = %#v", ownerPayload)
	}
	ownerJSON := ownerList.Body.String()
	for _, forbidden := range []string{`"csrf_token"`, `"email"`, `"password"`, ownerCookie.Value, ownerOtherToken, authorCookie.Value, authorOtherToken} {
		if strings.Contains(ownerJSON, forbidden) {
			t.Fatalf("session API exposed forbidden material %q: %s", forbidden, ownerJSON)
		}
	}

	authorList := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/sessions", authorCookie, nil)
	var authorPayload adminSessionsAPIResponse
	decodeSessionAPIResponse(t, authorList, &authorPayload)
	if authorList.Code != http.StatusOK || authorPayload.Scope != "mine" || authorPayload.Count != 2 {
		t.Fatalf("author session payload = %d %#v", authorList.Code, authorPayload)
	}
	for _, item := range authorPayload.Items {
		if item.Username != "api-session-author" {
			t.Fatalf("author API leaked another identity: %#v", item)
		}
	}
	authorAll := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/sessions?scope=all", authorCookie, nil)
	assertSessionAPIError(t, authorAll, http.StatusForbidden, "sessions.scope_forbidden")

	unauthenticated := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/sessions", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	assertSessionAPIError(t, unauthenticatedResponse, http.StatusUnauthorized, "auth.authentication_required")
	_, subscriberCookie, _ := createAndLoginTestUser(t, manager, owner, "api-session-reader", "api-session-reader@example.test", "API 订阅者", "subscriber")
	subscriber := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/sessions", subscriberCookie, nil)
	assertSessionAPIError(t, subscriber, http.StatusForbidden, "auth.capability_required")

	currentDelete := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/"+authorCurrentID, "", "https://example.test")
	assertSessionAPIError(t, currentDelete, http.StatusConflict, "sessions.current_session")
	foreignDelete := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/"+ownerOtherID, "", "https://example.test")
	assertSessionAPIError(t, foreignDelete, http.StatusForbidden, "sessions.owner_required")
	validDelete := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/"+authorOtherID, "", "https://example.test")
	if validDelete.Code != http.StatusNoContent || validDelete.Body.Len() != 0 {
		t.Fatalf("delete own session = %d %s", validDelete.Code, validDelete.Body.String())
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), authorOtherToken); ok {
		t.Fatal("API delete left the target session valid")
	}
	invalidID := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/not-base64", "", "https://example.test")
	assertSessionAPIError(t, invalidID, http.StatusNotFound, "sessions.not_found")
	bodyRejected := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/"+ownerOtherID, "x", "https://example.test")
	assertSessionAPIError(t, bodyRejected, http.StatusBadRequest, "request.body_not_allowed")
	badCSRF := serveSessionAPIWrite(handler, authorCookie, "invalid", http.MethodDelete, "/api/v1/admin/sessions/"+ownerOtherID, "", "https://example.test")
	assertSessionAPIError(t, badCSRF, http.StatusForbidden, "auth.csrf_validation_failed")
	crossSite := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodDelete, "/api/v1/admin/sessions/"+ownerOtherID, "", "https://attacker.test")
	assertSessionAPIError(t, crossSite, http.StatusForbidden, "auth.cross_site_request_rejected")

	authorExtraOne, _, err := manager.AuthenticateContext(t.Context(), "api-extra-one", "api-session-author", "Account password for api-session-author 2026")
	if err != nil {
		t.Fatal(err)
	}
	authorExtraTwo, _, err := manager.AuthenticateContext(t.Context(), "api-extra-two", "api-session-author", "Account password for api-session-author 2026")
	if err != nil {
		t.Fatal(err)
	}
	revokeOthers := serveSessionAPIWrite(handler, authorCookie, authorPrincipal.CSRFToken, http.MethodPost, "/api/v1/admin/sessions/revoke-others", "", "https://example.test")
	if revokeOthers.Code != http.StatusOK || !strings.Contains(revokeOthers.Body.String(), `"revoked":2`) {
		t.Fatalf("API revoke others = %d %s", revokeOthers.Code, revokeOthers.Body.String())
	}
	for _, token := range []string{authorExtraOne, authorExtraTwo} {
		if _, ok := manager.ValidateSessionContext(t.Context(), token); ok {
			t.Fatal("API revoke others left another device active")
		}
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), ownerOtherToken); !ok {
		t.Fatal("API revoke others crossed into the owner identity")
	}

	previewHandler := newUnconfiguredSessionTestHandler(t)
	previewRequest := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/admin/sessions?scope=all", nil)
	previewRequest.RemoteAddr = "127.0.0.1:41234"
	previewResponse := httptest.NewRecorder()
	previewHandler.ServeHTTP(previewResponse, previewRequest)
	var preview adminSessionsAPIResponse
	decodeSessionAPIResponse(t, previewResponse, &preview)
	if previewResponse.Code != http.StatusOK || !preview.ReadOnly || preview.Count != 0 || preview.Scope != "all" {
		t.Fatalf("preview session API = %d %#v", previewResponse.Code, preview)
	}
	previewWrite := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/admin/sessions/revoke-others", nil)
	previewWrite.RemoteAddr = "127.0.0.1:41234"
	previewWriteResponse := httptest.NewRecorder()
	previewHandler.ServeHTTP(previewWriteResponse, previewWrite)
	assertSessionAPIError(t, previewWriteResponse, http.StatusForbidden, "auth.write_disabled")
}

func TestAdminLoginRecordsBoundedSessionMetadata(t *testing.T) {
	server, _, manager, _, _, _ := newPersistentAdminTestServer(t)
	server.clientIPs = newClientIPResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.2/32")})
	handler := server.routes()
	form := url.Values{"username": {"owner"}, "password": {"Persistent owner password 2026"}, "next": {"/admin/sessions/"}}
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", strings.NewReader(form.Encode()))
	request.RemoteAddr = "172.30.0.2:54231"
	request.Header.Set("X-Forwarded-For", "203.0.113.77")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("User-Agent", "NewPress QA Browser/2026.8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/sessions/" {
		t.Fatalf("metadata login = %d %q %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == auth.CookieName && cookie.Path == "/" && cookie.MaxAge > 0 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("metadata login did not set the root session cookie")
	}
	principal, ok := manager.ValidateSessionContext(t.Context(), sessionCookie.Value)
	if !ok {
		t.Fatal("metadata login session was not valid")
	}
	items := manager.ListSessions(principal, sessionCookie.Value, false)
	var current *auth.SessionInfo
	for index := range items {
		if items[index].Current {
			current = &items[index]
			break
		}
	}
	if current == nil || current.IPAddress != "203.0.113.77" || current.UserAgent != "NewPress QA Browser/2026.8" {
		t.Fatalf("recorded session metadata = %#v", current)
	}
}

func sessionForm(csrf, action, scope, sessionID string) url.Values {
	values := url.Values{"csrf_token": {csrf}, "action": {action}, "scope": {scope}}
	if sessionID != "" {
		values.Set("session_id", sessionID)
	}
	return values
}

func serveSessionForm(handler http.Handler, cookie *http.Cookie, values url.Values, contentType, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/sessions/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveSessionAPIWrite(handler http.Handler, cookie *http.Cookie, csrf, method, path, body, origin string) *httptest.ResponseRecorder {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, "https://example.test"+path, nil)
	} else {
		request = httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func currentSessionID(t *testing.T, manager *auth.Manager, principal auth.Principal, token string, includeAll bool) string {
	t.Helper()
	for _, item := range manager.ListSessions(principal, token, includeAll) {
		if item.Current {
			return item.ID
		}
	}
	t.Fatal("current session ID was not found")
	return ""
}

func newUnconfiguredSessionTestHandler(t *testing.T) http.Handler {
	t.Helper()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", PublicURL: "http://localhost:8080", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(false), themes, plugin.NewManager(t.TempDir()),
	)
	return server.routes()
}

func decodeSessionAPIResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode session API response: %v; body=%s", err, response.Body.String())
	}
}

func assertSessionAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("session API error = %d %s, want %d %s", response.Code, response.Body.String(), status, code)
	}
}

func TestSessionClientDescription(t *testing.T) {
	tests := []struct {
		userAgent string
		client    string
		platform  string
		kind      string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0", "Microsoft Edge 138", "Windows", "desktop"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) Version/18.5 Mobile Safari/604.1", "Safari 18", "iPhone / iOS", "mobile"},
		{"curl/8.14.1", "curl 8", "未知平台", "terminal"},
		{"", "未记录客户端", "未知平台", "unknown"},
	}
	for _, test := range tests {
		client, platform, kind := describeSessionClient(test.userAgent)
		if client != test.client || platform != test.platform || kind != test.kind {
			t.Fatalf("describe %q = %q %q %q", test.userAgent, client, platform, kind)
		}
	}
}

func TestSessionDurationLabels(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	if got := sessionUntil(now, now.Add(90*time.Minute)); got != "还剩 1 小时 30 分钟" {
		t.Fatalf("sessionUntil = %q", got)
	}
	if got := sessionAgo(now, now.Add(-26*time.Hour)); got != "1 天 2 小时前" {
		t.Fatalf("sessionAgo = %q", got)
	}
}
