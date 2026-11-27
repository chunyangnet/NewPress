package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/theme"
)

func TestNavigationBadgesSaveRenderExportAndInvalidateCSS(t *testing.T) {
	s, handler, store, cookie, owner := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "角标测试", Slug: "badges", Locations: navigation.Locations()}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	badge := navigation.Badge{Text: "<新>", Style: "gradient", Background: "#123456", Color: "#FFFFFF", Radius: 7, OffsetX: 2, OffsetY: -3, HideOnMobile: true}
	items := []navigation.ItemInput{{ID: -1, Label: "星球", URL: "/explore/", Icon: "compass", Badge: badge}}
	raw, _ := json.Marshal(items)
	form := url.Values{"csrf_token": {owner.CSRFToken}, "action": {"save_items"}, "id": {strconv.FormatInt(menu.ID, 10)}, "version": {strconv.FormatInt(menu.Version, 10)}, "items_json": {string(raw)}}
	saved := serveNavigationForm(handler, cookie, form, "https://example.test", "application/x-www-form-urlencoded")
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	menu, _ = store.Get(t.Context(), menu.ID)
	want := menu.Items[0].Badge
	if want.Text != badge.Text || want.Background != badge.Background || !want.HideOnMobile {
		t.Fatal("form lost badge", want)
	}
	admin := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/", "", "", "")
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), `data-nav-field="badge_text" value="&lt;新&gt;"`) {
		t.Fatalf("editor did not restore badge: %d", admin.Code)
	}
	home := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if home.Code != 200 || strings.Count(home.Body.String(), `data-style="gradient" data-placement="corner">&lt;新&gt;`) < 3 || strings.Contains(home.Body.String(), "><新></span>") {
		t.Fatalf("public render: %d", home.Code)
	}
	links := publicLinks(s.publicNavigation("/").Primary)
	if len(links) != 1 || links[0].Badge.Background != want.Background || links[0].Badge.Text != want.Text || !links[0].Badge.HideOnMobile {
		t.Fatal("public/v1 lost badge", links)
	}
	api := publicNavigationItems(menu)
	if api[0].Badge != want {
		t.Fatal("public API lost badge", api)
	}
	css := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/navigation.css", "", "", "")
	if css.Code != 200 || !strings.Contains(css.Body.String(), "--nav-badge-bg:#123456") || strings.Contains(css.Body.String(), badge.Text) {
		t.Fatal("invalid CSS", css.Body.String())
	}
	snapshot, _ := store.Snapshot(t.Context())
	exported, _ := json.Marshal(s.navigationExport(snapshot, time.Now().UTC()))
	_, imported, _, _, err := prepareNavigationImport(exported, navigationImportDetachObjects)
	if err != nil || len(imported) != 1 || imported[0].Items[0].Badge != want {
		t.Fatal("export/import lost badge", err)
	}
	before := navigationRegistryMenuComparableFromCurrent(menu)
	if before != navigationRegistryMenuComparableFromInput(imported[0]) {
		t.Fatal("unchanged import reported a difference")
	}
	imported[0].Items[0].Badge.Color = "#EEEEEE"
	if before == navigationRegistryMenuComparableFromInput(imported[0]) {
		t.Fatal("badge-only import change was missed")
	}
	headers := authenticatedAPIHeaders(owner)
	get := serveContentAPI(handler, cookie, http.MethodGet, fmt.Sprintf("https://example.test/api/v1/admin/navigation/menus/%d", menu.ID), "", nil)
	headers["If-Match"] = get.Header().Get("ETag")
	items[0].ID = menu.Items[0].ID
	items[0].Badge.Color = "#EEEEEE"
	items[0].Badge.HideOnMobile = false
	updatedJSON, _ := json.Marshal(map[string]any{"items": items})
	updated := serveContentAPI(handler, cookie, http.MethodPut, fmt.Sprintf("https://example.test/api/v1/admin/navigation/menus/%d/items", menu.ID), string(updatedJSON), headers)
	if updated.Code != 200 {
		t.Fatalf("API update: %d %s", updated.Code, updated.Body.String())
	}
	newCSS := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/navigation.css", "", "", "")
	if newCSS.Header().Get("ETag") == css.Header().Get("ETag") || !strings.Contains(newCSS.Body.String(), "--nav-badge-color:#EEEEEE") {
		t.Fatal("badge CSS did not invalidate")
	}
	menu, _ = store.Get(t.Context(), menu.ID)
	if menu.Items[0].Badge.HideOnMobile {
		t.Fatal("API did not restore mobile visibility")
	}
	items[0].Badge.Text, items[0].Badge.OffsetX = "保留输入", 99
	invalidJSON, _ := json.Marshal(items)
	form.Set("version", strconv.FormatInt(menu.Version, 10))
	form.Set("items_json", string(invalidJSON))
	rejected := serveNavigationForm(handler, cookie, form, "https://example.test", "application/x-www-form-urlencoded")
	if rejected.Code != 422 || !strings.Contains(rejected.Body.String(), `data-nav-field="badge_text" value="保留输入"`) || !strings.Contains(rejected.Body.String(), "数值必须在 -12 到 12 之间") {
		t.Fatal("invalid badge did not preserve editable values", rejected.Code)
	}
	unchanged, _ := store.Get(t.Context(), menu.ID)
	if unchanged.Version != menu.Version || unchanged.Items[0].Badge.OffsetX == 99 {
		t.Fatal("invalid badge changed persisted state")
	}
	// An independent public/v1 theme can use text fallback, and its missing
	// account page still receives the embedded core renderer's badges.
	// Copy only this theme so a local saved selection cannot choose Chunnel.
	themeRoot := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "starter"), filepath.Join(themeRoot, "starter")); err != nil {
		t.Fatal(err)
	}
	s.themes = theme.NewManager(themeRoot, "starter", theme.WithSettingsDir(t.TempDir()))
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	starter := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if starter.Code != 200 || !strings.Contains(starter.Body.String(), "(&lt;新&gt;)") {
		t.Fatal("independent theme lost badge", starter.Code)
	}
	fallback := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/account/", "", "", "")
	if fallback.Code != 200 || !strings.Contains(fallback.Body.String(), `data-style="gradient"`) || !strings.Contains(fallback.Body.String(), "/css/chunnel-navigation-badges.css") {
		t.Fatal("core fallback lost badge", fallback.Code)
	}
}
