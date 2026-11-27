package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

func TestAdminNavigationLifecycleSafetyAndPublicProjection(t *testing.T) {
	server, handler, store, cookie, principal := newNavigationHTTPTestServer(t)

	page := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/", "", "", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "导航菜单") || !strings.Contains(page.Body.String(), "添加项目") {
		t.Fatalf("navigation page = %d %s", page.Code, page.Body.String())
	}

	create := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"create"}, "name": {"站点主导航"}, "slug": {"primary"},
		"description": {"公开站点入口"}, "locations": {navigation.LocationPrimary, navigation.LocationMobile},
	}
	created := serveNavigationForm(handler, cookie, create, "https://example.test", "application/x-www-form-urlencoded")
	if created.Code != http.StatusSeeOther || !strings.Contains(created.Header().Get("Location"), "notice=created") {
		t.Fatalf("create navigation = %d %#v %s", created.Code, created.Header(), created.Body.String())
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || snapshot.MenuCount != 1 || snapshot.Locations[navigation.LocationPrimary] == 0 {
		t.Fatalf("created snapshot = %#v, %v", snapshot, err)
	}
	menu := snapshot.Menus[0]

	items := []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/", Icon: "house", Accent: "#16A89A", Presentation: navigation.PresentationStandard},
		{ID: -2, Type: navigation.TypeCustom, Label: "<script>alert(1)</script>", URL: "/explore/", Icon: "compass", Accent: "#4E7CF6", Presentation: navigation.PresentationDropdown},
		{ID: -3, ParentID: -2, Type: navigation.TypeCategory, ObjectID: 1, Label: "工程分类", URL: "/categories/engineering/", Icon: "tags", Presentation: navigation.PresentationStandard},
		{ID: -4, ParentID: -3, Type: navigation.TypeCustom, Label: "外部指南", URL: "https://docs.example.test/guide", Target: navigation.TargetBlank, Rel: "ugc", Icon: "book-open", Presentation: navigation.PresentationStandard},
	}
	payload, _ := json.Marshal(items)
	save := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save_items"}, "id": {strconv.FormatInt(menu.ID, 10)},
		"version": {strconv.FormatInt(menu.Version, 10)}, "items_json": {string(payload)},
	}
	saved := serveNavigationForm(handler, cookie, save, "https://example.test", "application/x-www-form-urlencoded; charset=utf-8")
	if saved.Code != http.StatusSeeOther || !strings.Contains(saved.Header().Get("Location"), "items-saved") {
		t.Fatalf("save navigation items = %d %s", saved.Code, saved.Body.String())
	}
	menu, err = store.Get(t.Context(), menu.ID)
	if err != nil || menu.Version != 2 || len(menu.Items) != 4 || menu.Items[2].ParentID != menu.Items[1].ID || menu.Items[3].ParentID != menu.Items[2].ID {
		t.Fatalf("stored navigation = %#v, %v", menu, err)
	}

	dashboard := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/", "", "", "")
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), "导航菜单") ||
		!strings.Contains(dashboard.Body.String(), "/admin/navigation/") {
		t.Fatalf("dashboard navigation summary = %d %s", dashboard.Code, dashboard.Body.String())
	}
	system := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/api/v1/system", "", "", "")
	if system.Code != http.StatusOK {
		t.Fatalf("system navigation metrics = %d %s", system.Code, system.Body.String())
	}
	var systemPayload map[string]any
	if err := json.Unmarshal(system.Body.Bytes(), &systemPayload); err != nil {
		t.Fatal(err)
	}
	if systemPayload["navigation_ready"] != true || systemPayload["navigation_menu_count"] != float64(1) ||
		systemPayload["navigation_item_count"] != float64(4) || systemPayload["navigation_location_count"] != float64(2) {
		t.Fatalf("system navigation metrics = %#v", systemPayload)
	}

	home := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	body := home.Body.String()
	if home.Code != http.StatusOK || !strings.Contains(body, `class="primary-menu-entry nav-item-`) || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") ||
		!strings.Contains(body, "外部指南") || !strings.Contains(body, `target="_blank" rel="ugc noopener noreferrer"`) || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("public navigation projection = %d %s", home.Code, body)
	}
	css := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/navigation.css", "", "", "")
	if css.Code != http.StatusOK || css.Header().Get("Content-Type") != "text/css; charset=utf-8" || !strings.Contains(css.Body.String(), "--menu-accent:#16A89A") || css.Header().Get("ETag") == "" {
		t.Fatalf("navigation css = %d %#v %s", css.Code, css.Header(), css.Body.String())
	}
	notModifiedRequest := httptest.NewRequest(http.MethodGet, "https://example.test/navigation.css", nil)
	notModifiedRequest.Header.Set("If-None-Match", css.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, notModifiedRequest)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("navigation css conditional = %d", notModified.Code)
	}

	unsafeItems := `[ {"id":-1,"parent_id":0,"type":"custom","object_id":0,"label":"bad","url":"javascript:alert(1)","description":"","target":"self","rel":"","icon":"","accent":"#4E7CF6","presentation":"standard"} ]`
	unsafe := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save_items"}, "id": {strconv.FormatInt(menu.ID, 10)},
		"version": {strconv.FormatInt(menu.Version, 10)}, "items_json": {unsafeItems},
	}
	unsafeResponse := serveNavigationForm(handler, cookie, unsafe, "https://example.test", "application/x-www-form-urlencoded")
	if unsafeResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(unsafeResponse.Body.String(), "没有通过服务端校验") {
		t.Fatalf("unsafe navigation = %d %s", unsafeResponse.Code, unsafeResponse.Body.String())
	}
	current, _ := store.Get(t.Context(), menu.ID)
	if current.Version != menu.Version || len(current.Items) != 4 {
		t.Fatalf("unsafe write changed menu = %#v", current)
	}

	duplicateJSON := strings.Replace(unsafeItems, `"label":"bad"`, `"label":"bad","label":"again"`, 1)
	unsafe.Set("items_json", duplicateJSON)
	duplicate := serveNavigationForm(handler, cookie, unsafe, "https://example.test", "application/x-www-form-urlencoded")
	if duplicate.Code != http.StatusUnprocessableEntity || !strings.Contains(duplicate.Body.String(), "严格 JSON") {
		t.Fatalf("duplicate item JSON = %d %s", duplicate.Code, duplicate.Body.String())
	}

	conflictingCreate := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"create"}, "name": {"另一个菜单"}, "slug": {"secondary"},
		"description": {""}, "locations": {navigation.LocationPrimary},
	}
	conflict := serveNavigationForm(handler, cookie, conflictingCreate, "https://example.test", "application/x-www-form-urlencoded")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "不能同时分配") {
		t.Fatalf("location conflict = %d %s", conflict.Code, conflict.Body.String())
	}

	stale := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"update"}, "id": {strconv.FormatInt(menu.ID, 10)}, "version": {"1"},
		"name": {menu.Name}, "slug": {menu.Slug}, "description": {menu.Description}, "locations": menu.Locations,
	}
	staleResponse := serveNavigationForm(handler, cookie, stale, "https://example.test", "application/x-www-form-urlencoded")
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "其他请求修改") {
		t.Fatalf("stale navigation = %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	deleteForm := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"delete"}, "id": {strconv.FormatInt(menu.ID, 10)}, "version": {strconv.FormatInt(menu.Version, 10)},
	}
	deleted := serveNavigationForm(handler, cookie, deleteForm, "https://example.test", "application/x-www-form-urlencoded")
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/admin/navigation/?notice=deleted" {
		t.Fatalf("delete navigation = %d %#v", deleted.Code, deleted.Header())
	}
	emptyCreate := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"create"}, "name": {"显式空菜单"}, "slug": {"empty-primary"},
		"description": {"用于验证配置态不会意外回退"}, "locations": {navigation.LocationPrimary},
	}
	emptyCreated := serveNavigationForm(handler, cookie, emptyCreate, "https://example.test", "application/x-www-form-urlencoded")
	if emptyCreated.Code != http.StatusSeeOther {
		t.Fatalf("create empty navigation = %d %s", emptyCreated.Code, emptyCreated.Body.String())
	}
	emptySnapshot, err := store.Snapshot(t.Context())
	if err != nil || emptySnapshot.MenuCount != 1 || len(emptySnapshot.Menus[0].Items) != 0 {
		t.Fatalf("empty navigation snapshot = %#v, %v", emptySnapshot, err)
	}
	emptyProjection := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if emptyProjection.Code != http.StatusOK || strings.Contains(emptyProjection.Body.String(), `class="nav-home`) || strings.Contains(emptyProjection.Body.String(), "primary-menu-entry") {
		t.Fatalf("configured empty navigation fell back = %d %s", emptyProjection.Code, emptyProjection.Body.String())
	}
	emptyMenu := emptySnapshot.Menus[0]
	emptyDelete := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"delete"}, "id": {strconv.FormatInt(emptyMenu.ID, 10)}, "version": {strconv.FormatInt(emptyMenu.Version, 10)},
	}
	if response := serveNavigationForm(handler, cookie, emptyDelete, "https://example.test", "application/x-www-form-urlencoded"); response.Code != http.StatusSeeOther {
		t.Fatalf("delete empty navigation = %d %s", response.Code, response.Body.String())
	}
	fallback := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if fallback.Code != http.StatusOK || !strings.Contains(fallback.Body.String(), `class="nav-home`) {
		t.Fatalf("navigation fallback = %d %s", fallback.Code, fallback.Body.String())
	}
	_ = server
}

func TestAdminNavigationStrictWriteBoundaryAndCapability(t *testing.T) {
	server, handler, _, cookie, principal := newNavigationHTTPTestServer(t)
	base := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"create"}, "name": {"Menu"}, "slug": {"menu"}, "description": {""}}

	for name, response := range map[string]*httptest.ResponseRecorder{
		"cross-site": serveNavigationForm(handler, cookie, base, "https://attacker.test", "application/x-www-form-urlencoded"),
		"wrong-type": serveNavigationForm(handler, cookie, base, "https://example.test", "application/json"),
	} {
		if response.Code != http.StatusForbidden && response.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s boundary = %d %s", name, response.Code, response.Body.String())
		}
	}
	badCSRF := cloneURLValues(base)
	badCSRF.Set("csrf_token", "wrong")
	if response := serveNavigationForm(handler, cookie, badCSRF, "https://example.test", "application/x-www-form-urlencoded"); response.Code != http.StatusForbidden {
		t.Fatalf("bad csrf = %d", response.Code)
	}
	unknown := cloneURLValues(base)
	unknown.Set("unexpected", "true")
	if response := serveNavigationForm(handler, cookie, unknown, "https://example.test", "application/x-www-form-urlencoded"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", response.Code)
	}
	repeated := cloneURLValues(base)
	repeated["name"] = []string{"one", "two"}
	if response := serveNavigationForm(handler, cookie, repeated, "https://example.test", "application/x-www-form-urlencoded"); response.Code != http.StatusBadRequest {
		t.Fatalf("repeated field = %d", response.Code)
	}
	oversized := cloneURLValues(base)
	oversized.Set("description", strings.Repeat("x", 300<<10))
	if response := serveNavigationForm(handler, cookie, oversized, "https://example.test", "application/x-www-form-urlencoded"); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form = %d", response.Code)
	}

	_, persistentHandler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, _ := createAndLoginTestUser(t, manager, owner, "nav-editor", "nav-editor@example.test", "导航编辑", "editor")
	if response := serveCookieRequest(persistentHandler, http.MethodGet, "https://example.test/admin/navigation/", editorCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("editor navigation access = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(persistentHandler, http.MethodGet, "https://example.test/admin/navigation/", ownerCookie, nil); response.Code != http.StatusOK {
		t.Fatalf("administrator navigation access = %d", response.Code)
	}

	unconfigured, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server.auth = unconfigured
	previewRequest := httptest.NewRequest(http.MethodGet, "http://localhost/admin/navigation/", nil)
	previewRequest.RemoteAddr = "127.0.0.1:41234"
	preview := httptest.NewRecorder()
	handler.ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "当前为只读导航预览") {
		t.Fatalf("loopback navigation preview = %d %s", preview.Code, preview.Body.String())
	}
	previewWrite := httptest.NewRequest(http.MethodPost, "http://localhost/admin/navigation/", strings.NewReader(base.Encode()))
	previewWrite.RemoteAddr = "[::1]:41234"
	previewWrite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	previewWriteResponse := httptest.NewRecorder()
	handler.ServeHTTP(previewWriteResponse, previewWrite)
	if previewWriteResponse.Code != http.StatusForbidden || !strings.Contains(previewWriteResponse.Body.String(), "admin_credentials_not_configured") {
		t.Fatalf("loopback navigation write = %d %s", previewWriteResponse.Code, previewWriteResponse.Body.String())
	}
}

func newNavigationHTTPTestServer(t *testing.T) (*Server, http.Handler, *navigation.FileStore, *http.Cookie, auth.Principal) {
	t.Helper()
	password := "navigation owner password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	adminAuth, err := auth.New(auth.Config{Username: "navigator", PasswordHash: hash, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := adminAuth.Authenticate("navigation-tests", "navigator", password)
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager("", site.DefaultSettings("NewPress", "Navigation tests"))
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
	navigationStore, err := navigation.NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	repository := content.NewMemoryRepository(true)
	server := NewWithSiteSettingsMediaCommentsAndNavigation(config.Config{
		SiteTitle: "NewPress", SiteTagline: "Navigation tests", PublicURL: "https://example.test", ActiveTheme: "chunnel",
		AdminSessionTTL: time.Hour,
	}, repository, themes, plugin.NewManager(t.TempDir()), settings, library, comments, navigationStore, adminAuth)
	return server, server.routes(), navigationStore, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, principal
}

func serveNavigationForm(handler http.Handler, cookie *http.Cookie, values url.Values, origin, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/navigation/", strings.NewReader(values.Encode()))
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

func serveNavigationRequest(handler http.Handler, cookie *http.Cookie, method, target, body, origin, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func cloneURLValues(source url.Values) url.Values {
	result := make(url.Values, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}
