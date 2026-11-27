package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

func TestPublicCommentSubmissionModerationReplyAndPrivacy(t *testing.T) {
	server, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	article := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.10:4000")
	if article.Code != http.StatusOK {
		t.Fatalf("article status = %d, body=%s", article.Code, article.Body.String())
	}
	token := extractCommentToken(t, article.Body.String())
	payload := url.Values{
		"form_token": {token}, "parent_id": {"0"}, "author_name": {"访客甲"},
		"email": {"visitor@example.test"}, "content": {"<script>alert(1)</script>\n\nPENDING-SECRET-8f42。"}, "website": {""},
	}
	created := servePublicCommentForm(handler, "welcome-to-newpress", payload, "https://example.test", "198.51.100.10:4000")
	if created.Code != http.StatusSeeOther || !strings.Contains(created.Header().Get("Location"), "comment=pending") {
		t.Fatalf("create status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	pending, err := comments.ListAdmin(t.Context(), comment.Query{Status: comment.StatusPending, Limit: 20})
	if err != nil || pending.Total != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	stored := pending.Items[0]
	if stored.AuthorIPHash == "" || stored.AuthorIPHash == "198.51.100.10" || len(stored.AuthorIPHash) != 64 || stored.AuthorEmail != "visitor@example.test" {
		t.Fatalf("stored comment boundary = %#v", stored)
	}

	publicBefore := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.11:4000")
	if strings.Contains(publicBefore.Body.String(), "visitor@example.test") || strings.Contains(publicBefore.Body.String(), "PENDING-SECRET-8f42") {
		t.Fatalf("pending comment leaked publicly: %s", publicBefore.Body.String())
	}

	adminPage := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/comments/", "", "", "198.51.100.12:4000")
	if adminPage.Code != http.StatusOK || !strings.Contains(adminPage.Body.String(), "visitor@example.test") || !strings.Contains(adminPage.Body.String(), "PENDING-SECRET-8f42") {
		t.Fatalf("admin queue status=%d body=%s", adminPage.Code, adminPage.Body.String())
	}
	moderation := adminCommentFormValues(principal, stored, "approved")
	approved := serveCommentAdminForm(handler, cookie, moderation, "https://example.test")
	if approved.Code != http.StatusSeeOther {
		t.Fatalf("approve status=%d body=%s", approved.Code, approved.Body.String())
	}
	current, err := comments.Get(t.Context(), stored.ID)
	if err != nil || current.Status != comment.StatusApproved || current.Version != 2 {
		t.Fatalf("approved=%#v err=%v", current, err)
	}

	publicAfter := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.13:4000")
	body := publicAfter.Body.String()
	if publicAfter.Code != http.StatusOK || !strings.Contains(body, "PENDING-SECRET-8f42") || strings.Contains(body, "visitor@example.test") || strings.Contains(body, "<script>") {
		t.Fatalf("public approved boundary status=%d body=%s", publicAfter.Code, body)
	}
	replyToken := extractCommentToken(t, body)
	reply := url.Values{
		"form_token": {replyToken}, "parent_id": {strconv.FormatInt(current.ID, 10)}, "author_name": {"回复者"},
		"email": {"reply@example.test"}, "content": {"这是同文回复。"}, "website": {""},
	}
	replied := servePublicCommentForm(handler, "welcome-to-newpress", reply, "https://example.test", "198.51.100.14:4000")
	if replied.Code != http.StatusSeeOther {
		t.Fatalf("reply status=%d body=%s", replied.Code, replied.Body.String())
	}

	api := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/api/v1/posts/welcome-to-newpress/comments", "", "", "198.51.100.15:4000")
	if api.Code != http.StatusOK || strings.Contains(api.Body.String(), "author_email") || strings.Contains(api.Body.String(), "visitor@example.test") || strings.Contains(api.Body.String(), "author_ip_hash") {
		t.Fatalf("public API privacy status=%d body=%s", api.Code, api.Body.String())
	}

	crossSite := servePublicCommentForm(handler, "welcome-to-newpress", payload, "https://attacker.test", "198.51.100.16:4000")
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site status=%d", crossSite.Code)
	}
	tampered := cloneValues(payload)
	tampered.Set("form_token", token+"x")
	badToken := servePublicCommentForm(handler, "welcome-to-newpress", tampered, "https://example.test", "198.51.100.17:4000")
	if badToken.Code != http.StatusForbidden {
		t.Fatalf("bad token status=%d", badToken.Code)
	}

	_ = server
}

func TestPublicCommentAPIStrictBoundaryHoneypotAndRateLimit(t *testing.T) {
	server, handler, comments, _, _ := newCommentHTTPTestServer(t)
	settings := server.siteSettings.Snapshot()
	updated := settings.Settings
	updated.Discussion.RateLimitPerTenMinutes = 1
	if _, fields, err := server.siteSettings.Save(settings.Revision, updated); err != nil || len(fields) != 0 {
		t.Fatalf("save rate settings fields=%v err=%v", fields, err)
	}
	get := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/api/v1/posts/welcome-to-newpress/comments", "", "", "203.0.113.10:4100")
	var projection publicCommentsAPIResponse
	if err := json.Unmarshal(get.Body.Bytes(), &projection); err != nil || projection.Submission.FormToken == "" {
		t.Fatalf("projection=%#v err=%v body=%s", projection, err, get.Body.String())
	}
	body := `{"form_token":"` + projection.Submission.FormToken + `","author_name":"API 访客","email":"api@example.test","content":"来自 JSON API","website":""}`
	created := servePublicCommentAPI(handler, "welcome-to-newpress", body, "https://example.test", "203.0.113.10:4100")
	if created.Code != http.StatusAccepted || strings.Contains(created.Body.String(), "api@example.test") {
		t.Fatalf("API create status=%d body=%s", created.Code, created.Body.String())
	}
	limited := servePublicCommentAPI(handler, "welcome-to-newpress", body, "https://example.test", "203.0.113.10:4100")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("rate status=%d retry=%q body=%s", limited.Code, limited.Header().Get("Retry-After"), limited.Body.String())
	}
	duplicate := strings.Replace(body, `"content":"来自 JSON API"`, `"content":"一","content":"二"`, 1)
	duplicateResponse := servePublicCommentAPI(handler, "welcome-to-newpress", duplicate, "https://example.test", "203.0.113.11:4100")
	assertAPIError(t, duplicateResponse, http.StatusBadRequest, "request.invalid_json")
	unknown := strings.TrimSuffix(body, "}") + `,"is_admin":true}`
	unknownResponse := servePublicCommentAPI(handler, "welcome-to-newpress", unknown, "https://example.test", "203.0.113.12:4100")
	assertAPIError(t, unknownResponse, http.StatusBadRequest, "request.invalid_json")
	crossSite := servePublicCommentAPI(handler, "welcome-to-newpress", body, "https://attacker.test", "203.0.113.13:4100")
	assertAPIError(t, crossSite, http.StatusForbidden, "comments.origin_rejected")

	honeypot := strings.Replace(body, `"website":""`, `"website":"https://spam.invalid"`, 1)
	honeypotResponse := servePublicCommentAPI(handler, "welcome-to-newpress", honeypot, "https://example.test", "203.0.113.14:4100")
	if honeypotResponse.Code != http.StatusAccepted {
		t.Fatalf("honeypot status=%d body=%s", honeypotResponse.Code, honeypotResponse.Body.String())
	}
	snapshot, err := comments.Snapshot(t.Context())
	if err != nil || snapshot.Total != 1 {
		t.Fatalf("honeypot persisted unexpectedly snapshot=%#v err=%v", snapshot, err)
	}

	oversized := `{"form_token":"` + projection.Submission.FormToken + `","author_name":"A","email":"a@example.test","content":"` + strings.Repeat("x", publicCommentRequestMaxBytes) + `"}`
	oversizedResponse := servePublicCommentAPI(handler, "welcome-to-newpress", oversized, "https://example.test", "203.0.113.15:4100")
	assertAPIError(t, oversizedResponse, http.StatusRequestEntityTooLarge, "request.body_too_large")
}

func TestPublicCommentRateLimitSeparatesTrustedProxyClients(t *testing.T) {
	server, _, _, _, _ := newCommentHTTPTestServer(t)
	server.clientIPs = newClientIPResolver([]netip.Prefix{netip.MustParsePrefix("172.30.0.2/32")})
	handler := server.routes()
	settings := server.siteSettings.Snapshot()
	updated := settings.Settings
	updated.Discussion.RateLimitPerTenMinutes = 1
	if _, fields, err := server.siteSettings.Save(settings.Revision, updated); err != nil || len(fields) != 0 {
		t.Fatalf("save rate settings fields=%v err=%v", fields, err)
	}

	get := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/api/v1/posts/welcome-to-newpress/comments", "", "", "172.30.0.2:4100")
	var projection publicCommentsAPIResponse
	if err := json.Unmarshal(get.Body.Bytes(), &projection); err != nil || projection.Submission.FormToken == "" {
		t.Fatalf("projection=%#v err=%v body=%s", projection, err, get.Body.String())
	}
	body := `{"form_token":"` + projection.Submission.FormToken + `","author_name":"代理访客","email":"proxy@example.test","content":"来自代理链","website":""}`
	first := servePublicCommentAPIForwarded(handler, "welcome-to-newpress", body, "172.30.0.2:4100", "198.51.100.10")
	second := servePublicCommentAPIForwarded(handler, "welcome-to-newpress", body, "172.30.0.2:4100", "198.51.100.11")
	limited := servePublicCommentAPIForwarded(handler, "welcome-to-newpress", body, "172.30.0.2:4100", "198.51.100.10")
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted || limited.Code != http.StatusTooManyRequests {
		t.Fatalf("trusted proxy rate statuses = %d %d %d", first.Code, second.Code, limited.Code)
	}

	forgedServer, forgedHandler, _, _, _ := newCommentHTTPTestServer(t)
	forgedSettings := forgedServer.siteSettings.Snapshot()
	forgedUpdated := forgedSettings.Settings
	forgedUpdated.Discussion.RateLimitPerTenMinutes = 1
	if _, fields, err := forgedServer.siteSettings.Save(forgedSettings.Revision, forgedUpdated); err != nil || len(fields) != 0 {
		t.Fatalf("save forged rate settings fields=%v err=%v", fields, err)
	}
	forgedGet := serveCommentRequest(forgedHandler, nil, http.MethodGet, "https://example.test/api/v1/posts/welcome-to-newpress/comments", "", "", "203.0.113.40:4100")
	if err := json.Unmarshal(forgedGet.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	body = `{"form_token":"` + projection.Submission.FormToken + `","author_name":"伪造访客","email":"forge@example.test","content":"伪造链","website":""}`
	forgedFirst := servePublicCommentAPIForwarded(forgedHandler, "welcome-to-newpress", body, "203.0.113.40:4100", "198.51.100.20")
	forgedLimited := servePublicCommentAPIForwarded(forgedHandler, "welcome-to-newpress", body, "203.0.113.40:4100", "198.51.100.21")
	if forgedFirst.Code != http.StatusAccepted || forgedLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted XFF rate statuses = %d %d", forgedFirst.Code, forgedLimited.Code)
	}
}

func TestAdminCommentAPIOptimisticModerationAndPermanentDelete(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	seed, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "待审 API", AuthorEmail: "moderate@example.test", Status: comment.StatusPending,
		ContentSource: "需要审核", AuthorIPHash: strings.Repeat("a", 64), UserAgent: "test-agent/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/api/v1/admin/comments", "", "", "198.51.100.20:4200")
	assertAPIError(t, unauthenticated, http.StatusUnauthorized, "auth.authentication_required")

	list := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/api/v1/admin/comments", "", "", "198.51.100.20:4200")
	body := list.Body.String()
	if list.Code != http.StatusOK || !strings.Contains(body, "moderate@example.test") || !strings.Contains(body, "source_fingerprint") || strings.Contains(body, "author_ip_hash") || strings.Contains(body, "user_agent") || strings.Contains(body, "metadata") {
		t.Fatalf("admin DTO status=%d body=%s", list.Code, body)
	}
	target := "https://example.test/api/v1/admin/comments/" + strconv.FormatInt(seed.ID, 10)
	approved := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPatch, target, `{"status":"approved","version":1}`, "", "https://example.test")
	if approved.Code != http.StatusOK || approved.Header().Get("ETag") == "" {
		t.Fatalf("approve API status=%d body=%s", approved.Code, approved.Body.String())
	}
	stale := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPatch, target, `{"status":"spam","version":1}`, "", "https://example.test")
	assertAPIError(t, stale, http.StatusConflict, "comments.version_conflict")
	crossSite := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPatch, target, `{"status":"spam","version":2}`, "", "https://attacker.test")
	assertAPIError(t, crossSite, http.StatusForbidden, "auth.cross_site_request_rejected")
	badCSRF := serveAdminCommentAPI(handler, cookie, "invalid", http.MethodPatch, target, `{"status":"spam","version":2}`, "", "https://example.test")
	assertAPIError(t, badCSRF, http.StatusForbidden, "auth.csrf_validation_failed")

	deleteApproved := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodDelete, target, "", approved.Header().Get("ETag"), "https://example.test")
	assertAPIError(t, deleteApproved, http.StatusConflict, "comments.trash_required")
	trashed := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPatch, target, `{"status":"trash","version":2}`, "", "https://example.test")
	if trashed.Code != http.StatusOK {
		t.Fatalf("trash API status=%d body=%s", trashed.Code, trashed.Body.String())
	}
	missingPrecondition := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodDelete, target, "", "", "https://example.test")
	assertAPIError(t, missingPrecondition, http.StatusPreconditionRequired, "comments.precondition_required")
	staleDelete := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodDelete, target, "", `"comment-`+strconv.FormatInt(seed.ID, 10)+`-v2"`, "https://example.test")
	assertAPIError(t, staleDelete, http.StatusConflict, "comments.version_conflict")
	deleted := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodDelete, target, "", trashed.Header().Get("ETag"), "https://example.test")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete API status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := comments.Get(t.Context(), seed.ID); !errors.Is(err, comment.ErrNotFound) {
		t.Fatalf("deleted comment error=%v", err)
	}

	repeatedQuery := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/comments/?status=pending&status=spam", "", "", "198.51.100.20:4200")
	if repeatedQuery.Code != http.StatusBadRequest {
		t.Fatalf("repeated query status=%d", repeatedQuery.Code)
	}
}

func newCommentHTTPTestServer(t *testing.T) (*Server, http.Handler, *comment.FileStore, *http.Cookie, auth.Principal) {
	t.Helper()
	password := "comment moderation password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	adminAuth, err := auth.New(auth.Config{Username: "moderator", PasswordHash: hash, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := adminAuth.Authenticate("comment-tests", "moderator", password)
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager("", site.DefaultSettings("NewPress", "Comment tests"))
	if err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	comments, err := comment.NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(true)
	server := NewWithSiteSettingsMediaAndComments(config.Config{
		SiteTitle: "NewPress", SiteTagline: "Comment tests", PublicURL: "https://example.test", ActiveTheme: "chunnel",
		AdminSessionTTL: time.Hour,
	}, repository, themes, plugin.NewManager(t.TempDir()), settings, library, comments, adminAuth)
	return server, server.routes(), comments, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, principal
}

func servePublicCommentForm(handler http.Handler, slug string, values url.Values, origin, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/posts/"+slug+"/comments", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.RemoteAddr = remote
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func servePublicCommentAPI(handler http.Handler, slug, body, origin, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/posts/"+slug+"/comments", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.RemoteAddr = remote
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func servePublicCommentAPIForwarded(handler http.Handler, slug, body, remote, forwarded string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/posts/"+slug+"/comments", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-Forwarded-For", forwarded)
	request.RemoteAddr = remote
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveCommentAdminForm(handler http.Handler, cookie *http.Cookie, values url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/comments/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveAdminCommentAPI(handler http.Handler, cookie *http.Cookie, csrf, method, target, body, ifMatch, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", csrf)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveCommentRequest(handler http.Handler, cookie *http.Cookie, method, target, body, origin, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.RemoteAddr = remote
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func adminCommentFormValues(principal auth.Principal, item comment.Comment, action string) url.Values {
	return url.Values{
		"csrf_token": {principal.CSRFToken}, "id": {strconv.FormatInt(item.ID, 10)}, "version": {strconv.FormatInt(item.Version, 10)},
		"action": {action}, "return_status": {"pending"}, "return_q": {""}, "return_post": {"0"}, "return_page": {"1"},
	}
}

func extractCommentToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="form_token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("comment form token not found: %s", body)
	}
	return match[1]
}
