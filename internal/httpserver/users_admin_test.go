package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestAdminUsersLifecycleSecurityAndSessionRotation(t *testing.T) {
	server, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_ = server

	listRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/users/", nil)
	listRequest.AddCookie(ownerCookie)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "<h1>用户与权限</h1>") || !strings.Contains(listResponse.Body.String(), "站点所有者") {
		t.Fatalf("users page = %d %s", listResponse.Code, listResponse.Body.String())
	}

	create := url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"create"}, "username": {"writer"},
		"email": {"writer@example.test"}, "display_name": {"特约作者"}, "status": {auth.UserStatusActive},
		"locale": {"zh-CN"}, "role": {"author"}, "password": {"Writer account password 2026"},
		"password_confirm": {"Writer account password 2026"},
	}
	createdResponse := serveUserForm(handler, ownerCookie, create, "application/x-www-form-urlencoded", "https://example.test")
	if createdResponse.Code != http.StatusSeeOther || createdResponse.Header().Get("Location") != "/admin/users/?notice=created" {
		t.Fatalf("create user = %d %q %s", createdResponse.Code, createdResponse.Header().Get("Location"), createdResponse.Body.String())
	}
	writer, err := manager.ListUsers(t.Context(), auth.UserQuery{Search: "writer", Limit: 10})
	if err != nil || writer.Total != 1 || writer.Items[0].PrimaryRoleName() != "作者" {
		t.Fatalf("created writer = %#v, err=%v", writer, err)
	}
	user := writer.Items[0]

	mismatch := cloneValues(create)
	mismatch.Set("username", "mismatch")
	mismatch.Set("email", "mismatch@example.test")
	mismatch.Set("password_confirm", "Different account password 2026")
	mismatchResponse := serveUserForm(handler, ownerCookie, mismatch, "application/x-www-form-urlencoded", "https://example.test")
	if mismatchResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(mismatchResponse.Body.String(), "两次输入的密码不一致") || !strings.Contains(mismatchResponse.Body.String(), "mismatch@example.test") {
		t.Fatalf("password mismatch = %d %s", mismatchResponse.Code, mismatchResponse.Body.String())
	}

	duplicate := cloneValues(create)
	duplicate.Set("username", "another-writer")
	duplicate.Set("display_name", "保留的冲突输入")
	duplicateResponse := serveUserForm(handler, ownerCookie, duplicate, "application/x-www-form-urlencoded", "https://example.test")
	if duplicateResponse.Code != http.StatusConflict || !strings.Contains(duplicateResponse.Body.String(), "邮箱冲突") || !strings.Contains(duplicateResponse.Body.String(), "保留的冲突输入") {
		t.Fatalf("duplicate email = %d %s", duplicateResponse.Code, duplicateResponse.Body.String())
	}

	update := url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"update"}, "id": {strconv.FormatInt(user.ID, 10)},
		"version": {strconv.FormatInt(user.Version, 10)}, "username": {user.Username}, "email": {user.Email},
		"display_name": {"高级编辑"}, "status": {auth.UserStatusActive}, "locale": {"en-US"}, "role": {"editor"},
		"password": {""}, "password_confirm": {""},
	}
	updateResponse := serveUserForm(handler, ownerCookie, update, "application/x-www-form-urlencoded", "https://example.test")
	if updateResponse.Code != http.StatusSeeOther || !strings.Contains(updateResponse.Header().Get("Location"), "notice=updated") {
		t.Fatalf("update user = %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	user, err = manager.GetUser(t.Context(), user.ID)
	if err != nil || user.Version != 2 || user.Locale != "en-US" || !user.Can(auth.CapabilityContentWrite) {
		t.Fatalf("updated user = %#v, err=%v", user, err)
	}

	oldPassword := "Writer account password 2026"
	oldToken, _, err := manager.AuthenticateContext(t.Context(), "writer-before-rotation", user.Email, oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	password := url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"password"}, "id": {strconv.FormatInt(user.ID, 10)},
		"version": {strconv.FormatInt(user.Version, 10)}, "username": {user.Username}, "email": {user.Email},
		"display_name": {user.DisplayName}, "status": {user.Status}, "locale": {user.Locale}, "role": {"editor"},
		"password": {"Rotated writer password 2026"}, "password_confirm": {"Rotated writer password 2026"},
	}
	passwordResponse := serveUserForm(handler, ownerCookie, password, "application/x-www-form-urlencoded", "https://example.test")
	if passwordResponse.Code != http.StatusSeeOther || !strings.Contains(passwordResponse.Header().Get("Location"), "notice=password") {
		t.Fatalf("rotate password = %d %s", passwordResponse.Code, passwordResponse.Body.String())
	}
	if _, ok := manager.ValidateSessionContext(t.Context(), oldToken); ok {
		t.Fatal("password rotation left an old session valid")
	}
	if _, _, err := manager.AuthenticateContext(t.Context(), "old-password", user.Username, oldPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password error = %v", err)
	}
	if _, _, err := manager.AuthenticateContext(t.Context(), "new-password", user.Email, "Rotated writer password 2026"); err != nil {
		t.Fatalf("new password login = %v", err)
	}

	ownerUser, err := manager.GetUser(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	selfSuspend := url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"update"}, "id": {strconv.FormatInt(ownerUser.ID, 10)},
		"version": {strconv.FormatInt(ownerUser.Version, 10)}, "username": {ownerUser.Username}, "email": {ownerUser.Email},
		"display_name": {ownerUser.DisplayName}, "status": {auth.UserStatusSuspended}, "locale": {ownerUser.Locale}, "role": {"subscriber"},
		"password": {""}, "password_confirm": {""},
	}
	selfResponse := serveUserForm(handler, ownerCookie, selfSuspend, "application/x-www-form-urlencoded", "https://example.test")
	if selfResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(selfResponse.Body.String(), "不能停用自己的当前会话") {
		t.Fatalf("self suspension = %d %s", selfResponse.Code, selfResponse.Body.String())
	}
	ownerUser, _ = manager.GetUser(t.Context(), owner.UserID)
	if ownerUser.Status != auth.UserStatusActive || !ownerUser.HasRole("administrator") {
		t.Fatalf("self-protection failed: %#v", ownerUser)
	}

	unknown := cloneValues(update)
	unknown.Set("is_super_admin", "true")
	unknownResponse := serveUserForm(handler, ownerCookie, unknown, "application/x-www-form-urlencoded", "https://example.test")
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", unknownResponse.Code)
	}
	wrongTypeResponse := serveUserForm(handler, ownerCookie, update, "text/plain", "https://example.test")
	if wrongTypeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type = %d", wrongTypeResponse.Code)
	}
	crossSiteResponse := serveUserForm(handler, ownerCookie, update, "application/x-www-form-urlencoded", "https://attacker.test")
	if crossSiteResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-site user mutation = %d", crossSiteResponse.Code)
	}
}

func TestPersistentRBACRoutesAndAuthorObjectScope(t *testing.T) {
	server, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)

	authorOne, authorOneCookie, authorOnePrincipal := createAndLoginTestUser(t, manager, owner, "author-one", "author1@example.test", "作者甲", "author")
	authorTwo, _, _ := createAndLoginTestUser(t, manager, owner, "author-two", "author2@example.test", "作者乙", "author")
	_, editorCookie, editorPrincipal := createAndLoginTestUser(t, manager, owner, "site-editor", "editor@example.test", "值班编辑", "editor")
	_, subscriberCookie, subscriberPrincipal := createAndLoginTestUser(t, manager, owner, "reader", "reader@example.test", "订阅者", "subscriber")

	initialThemeRevision := server.themes.SettingsSnapshot().Revision
	for _, actor := range []struct {
		role      string
		cookie    *http.Cookie
		principal auth.Principal
	}{
		{"author", authorOneCookie, authorOnePrincipal},
		{"editor", editorCookie, editorPrincipal},
		{"subscriber", subscriberCookie, subscriberPrincipal},
	} {
		form := url.Values{"action": {"save"}, "csrf_token": {actor.principal.CSRFToken}, "visual_style": {"liquid-glass"}}
		response := serveContentForm(handler, actor.cookie, "https://example.test/admin/appearance/", "https://example.test", form)
		if response.Code != http.StatusForbidden || server.themes.SettingsSnapshot().Revision != initialThemeRevision {
			t.Fatalf("%s must not change site appearance: HTTP %d", actor.role, response.Code)
		}
	}

	one := createOwnedTestPost(t, repository, authorOne.ID, authorOne.DisplayName, "author-one-post", "作者甲的草稿")
	two := createOwnedTestPost(t, repository, authorTwo.ID, authorTwo.DisplayName, "author-two-post", "作者乙的草稿")

	authorList := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/", authorOneCookie, nil)
	if authorList.Code != http.StatusOK || !strings.Contains(authorList.Body.String(), one.Title) || strings.Contains(authorList.Body.String(), two.Title) {
		t.Fatalf("author scoped list = %d %s", authorList.Code, authorList.Body.String())
	}
	authorDashboard := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", authorOneCookie, nil)
	if authorDashboard.Code != http.StatusOK || strings.Contains(authorDashboard.Body.String(), two.Title) || strings.Contains(authorDashboard.Body.String(), "用户与权限") || strings.Contains(authorDashboard.Body.String(), "站点设置") {
		t.Fatalf("author dashboard scope = %d %s", authorDashboard.Code, authorDashboard.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/"+strconv.FormatInt(two.ID, 10)+"/edit/", authorOneCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("author read other content = %d", response.Code)
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/"+strconv.FormatInt(one.ID, 10)+"/edit/", authorOneCookie, nil); response.Code != http.StatusOK {
		t.Fatalf("author read own content = %d %s", response.Code, response.Body.String())
	} else if body := response.Body.String(); strings.Contains(body, `aria-label="管理分类与标签"`) || strings.Contains(body, "先上传图片到安全媒体库") || !strings.Contains(body, "打开媒体库选择已有图片") {
		t.Fatalf("author editor leaked unavailable management actions: %s", body)
	}
	for _, target := range []string{"/admin/taxonomy/", "/admin/settings/", "/admin/appearance/", "/admin/users/"} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, authorOneCookie, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("author route %s = %d", target, response.Code)
		}
	}
	mediaResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/media/", authorOneCookie, nil)
	if mediaResponse.Code != http.StatusOK || !strings.Contains(mediaResponse.Body.String(), "当前媒体工作区为只读") {
		t.Fatalf("author media read = %d %s", mediaResponse.Code, mediaResponse.Body.String())
	}
	if body := mediaResponse.Body.String(); !strings.Contains(body, "暂无媒体") || strings.Contains(body, `href="#media-upload"`) || strings.Contains(body, `href="/admin/media/#media-upload"`) {
		t.Fatalf("author media empty state exposed upload CTA: %s", body)
	}
	for _, unavailable := range []string{`href="/admin/taxonomy/"`, `href="/admin/users/"`, `href="/admin/settings/"`, `href="/admin/appearance/"`, `href="/admin/system/"`} {
		if strings.Contains(mediaResponse.Body.String(), unavailable) {
			t.Fatalf("author media navigation leaked %s: %s", unavailable, mediaResponse.Body.String())
		}
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/system", authorOneCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("author system API = %d", response.Code)
	}

	create := url.Values{
		"csrf_token": {authorOnePrincipal.CSRFToken}, "action": {"save"}, "type": {content.TypePost},
		"status": {content.StatusDraft}, "title": {"作者甲的新内容"}, "slug": {"author-one-new"}, "excerpt": {""},
		"content_source": {"正文"}, "cover_url": {""}, "category": {"未分类"}, "comment_status": {content.CommentOpen}, "published_at": {""},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
	}
	created := serveContentForm(handler, authorOneCookie, "https://example.test/admin/content/new/", "https://example.test", create)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("author create = %d %s", created.Code, created.Body.String())
	}
	createdList, err := repository.ListAdmin(t.Context(), content.AdminQuery{AuthorID: authorOne.ID, Search: "作者甲的新内容", Limit: 10})
	if err != nil || createdList.Total != 1 || createdList.Items[0].AuthorID != authorOne.ID || createdList.Items[0].AuthorName != authorOne.DisplayName {
		t.Fatalf("author attribution = %#v, err=%v", createdList, err)
	}

	apiList := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/contents", authorOneCookie, nil)
	if apiList.Code != http.StatusOK || !strings.Contains(apiList.Body.String(), "author-one-post") || strings.Contains(apiList.Body.String(), "author-two-post") {
		t.Fatalf("author API list = %d %s", apiList.Code, apiList.Body.String())
	}
	apiOther := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/contents/"+strconv.FormatInt(two.ID, 10), authorOneCookie, nil)
	if apiOther.Code != http.StatusForbidden || !strings.Contains(apiOther.Body.String(), "content.owner_required") {
		t.Fatalf("author API other object = %d %s", apiOther.Code, apiOther.Body.String())
	}

	editorList := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/", editorCookie, nil)
	if editorList.Code != http.StatusOK || !strings.Contains(editorList.Body.String(), one.Title) || !strings.Contains(editorList.Body.String(), two.Title) {
		t.Fatalf("editor list = %d %s", editorList.Code, editorList.Body.String())
	}
	for _, target := range []string{"/admin/taxonomy/", "/admin/media/", "/api/v1/system"} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, editorCookie, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("editor route %s = %d %s", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{"/admin/users/", "/admin/settings/", "/admin/appearance/"} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, editorCookie, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("editor forbidden route %s = %d", target, response.Code)
		}
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", subscriberCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("subscriber admin = %d", response.Code)
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/session", subscriberCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("subscriber admin session = %d", response.Code)
	}
}

func newPersistentAdminTestServer(t *testing.T) (*Server, http.Handler, *auth.Manager, *content.MemoryRepository, *http.Cookie, auth.Principal) {
	t.Helper()
	store, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	password := "Persistent owner password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewWithContext(t.Context(), auth.Config{
		Username: "owner", PasswordHash: hash, Users: store, BootstrapEmail: "owner@example.test",
		BootstrapDisplayName: "站点所有者", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, owner, err := manager.AuthenticateContext(t.Context(), "owner-test", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(false)
	server := New(config.Config{
		SiteTitle: "NewPress", SiteTagline: "Test", PublicURL: "https://example.test", ActiveTheme: "chunnel", AdminSessionTTL: time.Hour,
	}, repository, themes, plugin.NewManager(t.TempDir()), manager)
	return server, server.routes(), manager, repository, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, owner
}

func createAndLoginTestUser(t *testing.T, manager *auth.Manager, owner auth.Principal, username, email, displayName, role string) (auth.User, *http.Cookie, auth.Principal) {
	t.Helper()
	password := "Account password for " + username + " 2026"
	user, err := manager.CreateUser(t.Context(), owner, auth.UserInput{
		Username: username, Email: email, DisplayName: displayName, Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{role},
	}, password)
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := manager.AuthenticateContext(t.Context(), username+"-login", username, password)
	if err != nil {
		t.Fatal(err)
	}
	return user, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, principal
}

func createOwnedTestPost(t *testing.T, repository *content.MemoryRepository, authorID int64, authorName, slug, title string) content.Post {
	t.Helper()
	post, err := repository.Create(t.Context(), content.PostInput{
		AuthorID: authorID, AuthorName: authorName, Actor: authorName, Type: content.TypePost,
		Status: content.StatusDraft, Slug: slug, Title: title, ContentSource: "测试正文", Category: "未分类", CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	return post
}

func serveUserForm(handler http.Handler, cookie *http.Cookie, values url.Values, contentType, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/users/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveCookieRequest(handler http.Handler, method, target string, cookie *http.Cookie, body *strings.Reader) *httptest.ResponseRecorder {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, body)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
