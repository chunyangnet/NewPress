package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/version"
)

func TestAdminToolsVisibilityAndCapabilityBoundaries(t *testing.T) {
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, _ := createAndLoginTestUser(t, manager, owner, "tools-editor", "tools-editor@example.test", "工具编辑", "editor")
	_, authorCookie, _ := createAndLoginTestUser(t, manager, owner, "tools-author", "tools-author@example.test", "工具作者", "author")

	unauthenticated := httptest.NewRequest(http.MethodGet, "https://example.test/admin/tools/", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusSeeOther || !strings.HasPrefix(unauthenticatedResponse.Header().Get("Location"), "/admin/login/") {
		t.Fatalf("unauthenticated tools page = %d %q", unauthenticatedResponse.Code, unauthenticatedResponse.Header().Get("Location"))
	}

	editorResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/", editorCookie, nil)
	editorBody := editorResponse.Body.String()
	if editorResponse.Code != http.StatusOK || !strings.Contains(editorBody, "<h1>工具与备份</h1>") ||
		!strings.Contains(editorBody, "/admin/tools/support-report.json") || !strings.Contains(editorBody, "/admin/tools/audit-events.json") ||
		!strings.Contains(editorBody, "完整性检查") || !strings.Contains(editorBody, "脱敏审计事件导出") {
		t.Fatalf("editor tools page = %d %s", editorResponse.Code, editorBody)
	}
	for _, forbidden := range []string{
		"/admin/settings/export/",
		"/admin/appearance/export/",
		"/admin/tools/navigation-export.json",
		"/admin/tools/device-mockup/",
	} {
		if strings.Contains(editorBody, forbidden) {
			t.Fatalf("editor tools page exposed %s", forbidden)
		}
	}

	authorResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/", authorCookie, nil)
	if authorResponse.Code != http.StatusForbidden {
		t.Fatalf("author tools page = %d %s", authorResponse.Code, authorResponse.Body.String())
	}

	ownerResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/", ownerCookie, nil)
	ownerBody := ownerResponse.Body.String()
	if ownerResponse.Code != http.StatusOK || !strings.Contains(ownerBody, `data-release="NP / `+version.Version+`"`) {
		t.Fatalf("owner tools page = %d %s", ownerResponse.Code, ownerBody)
	}
	for _, required := range []string{
		`data-tools-workspace`,
		`id="tools-downloads"`,
		`id="tools-recovery"`,
		`id="tools-diagnostics"`,
		`class="tools-download-row`,
		"/admin/tools/support-report.json",
		"/admin/tools/audit-events.json",
		"/admin/settings/export/",
		"/admin/appearance/export/",
		"/admin/tools/navigation-export.json",
		"/admin/tools/device-mockup/",
		"${NEWPRESS_DATA_DIR}/audit.jsonl",
	} {
		if !strings.Contains(ownerBody, required) {
			t.Fatalf("owner tools page missing %s", required)
		}
	}
	if !strings.Contains(ownerBody, "href=\"/admin/tools/\"") || !strings.Contains(ownerBody, "工具与备份") {
		t.Fatal("admin shell did not expose the tools navigation entry")
	}

	invalidQuery := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/?unexpected=1", ownerCookie, nil)
	if invalidQuery.Code != http.StatusBadRequest || !strings.Contains(invalidQuery.Body.String(), "request.invalid_query") {
		t.Fatalf("tools invalid query = %d %s", invalidQuery.Code, invalidQuery.Body.String())
	}
}

func TestAdminToolsUnconfiguredLoopbackPreviewAndExternalHiding(t *testing.T) {
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", PublicURL: "http://localhost:8080", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(false),
		themes,
		plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	loopback := httptest.NewRequest(http.MethodGet, "http://localhost/admin/tools/", nil)
	loopback.RemoteAddr = "127.0.0.1:42123"
	loopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackResponse, loopback)
	if loopbackResponse.Code != http.StatusOK || !strings.Contains(loopbackResponse.Body.String(), "本机只读预览") ||
		!strings.Contains(loopbackResponse.Body.String(), "/admin/tools/navigation-export.json") {
		t.Fatalf("loopback tools preview = %d %s", loopbackResponse.Code, loopbackResponse.Body.String())
	}

	external := httptest.NewRequest(http.MethodGet, "http://example.test/admin/tools/", nil)
	external.RemoteAddr = "198.51.100.24:42123"
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusNotFound {
		t.Fatalf("unconfigured external tools page = %d", externalResponse.Code)
	}
}

func TestAdminToolsSupportReportIsPrettyNoStoreAndSecretFree(t *testing.T) {
	server, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, editor := createAndLoginTestUser(
		t, manager, owner, "private-editor", "private-editor@example.test", "私密编辑", "editor",
	)
	server.config.DatabaseURL = "postgres://db-user:db-password-very-secret@db.example.test/private?sslmode=require"
	server.config.DataDir = "D:\\private-data\\owner-private@example.test\\newpress"
	server.config.AdminUsername = "private-bootstrap-user"
	server.config.AdminEmail = "private-bootstrap@example.test"
	server.config.MaxUploadBytes = 23 << 20
	server.config.AutoMigrate = true
	server.config.SeedDemo = false

	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/support-report.json", ownerCookie, nil)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Content-Disposition") != "attachment; filename=\"newpress-support-report.json\"" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("support report response = %d %#v %s", response.Code, response.Header(), body)
	}
	if !strings.Contains(body, "\n  \"kind\":") {
		t.Fatalf("support report was not pretty printed: %s", body)
	}
	var report supportReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	themeSettings := server.themes.SettingsSnapshot()
	visibleSettings := make(map[string]bool)
	for _, group := range themeSettings.Groups {
		for _, field := range group.Fields {
			visibleSettings[field.Key] = true
		}
	}
	if report.Schema != 1 || report.Kind != "newpress.support_report" || report.Storage.Mode != "postgresql" ||
		report.Software.Theme.Version != server.themes.Active().Version || report.Configuration.MaxUploadBytes != 23<<20 ||
		!report.Configuration.AutoMigrate || report.Configuration.SeedDemo ||
		report.Counts.Users < 2 || report.Counts.Sessions < 2 ||
		report.Revisions.SiteSettings.Revision == "" || len(visibleSettings) == 0 ||
		report.Revisions.ThemeSettings.FieldCount != len(visibleSettings) || report.Revisions.ThemeSettings.Revision != themeSettings.Revision ||
		len(report.Checks) < 8 {
		t.Fatalf("support report payload = %#v", report)
	}
	for _, secret := range []string{
		server.config.DatabaseURL,
		"db-password-very-secret",
		server.config.DataDir,
		"private-data",
		server.config.AdminUsername,
		server.config.AdminEmail,
		"private-editor",
		"private-editor@example.test",
		ownerCookie.Value,
		owner.CSRFToken,
		editor.CSRFToken,
	} {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatalf("support report leaked %q: %s", secret, body)
		}
	}

	editorResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/support-report.json", editorCookie, nil)
	if editorResponse.Code != http.StatusOK || editorResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("editor support report = %d %#v %s", editorResponse.Code, editorResponse.Header(), editorResponse.Body.String())
	}
	invalid := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/support-report.json?unexpected=1", ownerCookie, nil)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "request.invalid_query") {
		t.Fatalf("support report invalid query = %d %s", invalid.Code, invalid.Body.String())
	}
	for _, target := range []string{
		"https://example.test/admin/settings/export/?unexpected=1",
		"https://example.test/admin/appearance/export/?unexpected=1",
	} {
		exportResponse := serveCookieRequest(handler, http.MethodGet, target, ownerCookie, nil)
		if exportResponse.Code != http.StatusBadRequest || !strings.Contains(exportResponse.Body.String(), "request.invalid_query") {
			t.Fatalf("strict settings export %s = %d %s", target, exportResponse.Code, exportResponse.Body.String())
		}
	}
}

func TestAdminToolsPortableNavigationExportOmitsPersistentIdentity(t *testing.T) {
	server, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, _ := createAndLoginTestUser(t, manager, owner, "navigation-editor", "navigation-editor@example.test", "导航编辑", "editor")
	menu, err := server.navigation.Create(t.Context(), navigation.MenuInput{
		Name: "迁移主导航", Slug: "portable-primary", Description: "跨环境恢复测试",
		Locations: []string{navigation.LocationPrimary, navigation.LocationMobile},
	}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	menu, err = server.navigation.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{
		ExpectedVersion: menu.Version, Actor: owner.Username,
	}, []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/", Icon: "house", Accent: "#16A89A", Presentation: navigation.PresentationDropdown},
		{ID: -2, ParentID: -1, Type: navigation.TypeCategory, ObjectID: 42, Label: "工程", URL: "/categories/engineering/", Description: "工程分类", Presentation: navigation.PresentationStandard},
		{ID: -3, ParentID: -2, Type: navigation.TypeCustom, Label: "归档", URL: "/explore/", Target: navigation.TargetBlank, Presentation: navigation.PresentationStandard},
	})
	if err != nil {
		t.Fatal(err)
	}

	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/navigation-export.json", ownerCookie, nil)
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Disposition") != "attachment; filename=\"newpress-navigation-export.json\"" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("navigation export response = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	var document navigationExportDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != 1 || document.Kind != "newpress.navigation_export" ||
		document.Source.ThemeVersion != server.themes.Active().Version || document.Revision == "" ||
		len(document.LocationSlots) != len(navigation.Locations()) || len(document.Menus) != 1 || len(document.Menus[0].Items) != 3 {
		t.Fatalf("navigation export = %#v", document)
	}
	items := document.Menus[0].Items
	if document.Menus[0].Slug != "portable-primary" || items[0].Key != "item-001" || items[0].ParentKey != "" ||
		items[1].ParentKey != "item-001" || items[2].ParentKey != "item-002" || items[1].ObjectID != 42 ||
		items[0].Position != 0 || items[1].Position != 0 || items[2].Position != 0 {
		t.Fatalf("portable navigation keys = %#v", items)
	}
	var generic any
	if err := json.Unmarshal(response.Body.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysAbsent(t, generic, map[string]bool{
		"id": true, "menu_id": true, "parent_id": true, "created_at": true, "updated_at": true,
	})
	roundTripPreview := serveNavigationImportPreview(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
	}, "newpress-navigation-export.json", response.Body.Bytes(), "https://example.test")
	if roundTripPreview.Code != http.StatusOK || !strings.Contains(roundTripPreview.Body.String(), "导入预览") {
		t.Fatalf("export was not accepted by import preview = %d %s", roundTripPreview.Code, roundTripPreview.Body.String())
	}

	editorResponse := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/navigation-export.json", editorCookie, nil)
	if editorResponse.Code != http.StatusForbidden {
		t.Fatalf("editor navigation export = %d %s", editorResponse.Code, editorResponse.Body.String())
	}
	invalid := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/navigation-export.json?menu=1", ownerCookie, nil)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "request.invalid_query") {
		t.Fatalf("navigation export invalid query = %d %s", invalid.Code, invalid.Body.String())
	}
	_ = menu
}

func assertJSONKeysAbsent(t *testing.T, value any, forbidden map[string]bool) {
	t.Helper()
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if forbidden[key] {
				t.Fatalf("JSON export contains forbidden persistent field %q", key)
			}
			assertJSONKeysAbsent(t, child, forbidden)
		}
	case []any:
		for _, child := range current {
			assertJSONKeysAbsent(t, child, forbidden)
		}
	}
}
