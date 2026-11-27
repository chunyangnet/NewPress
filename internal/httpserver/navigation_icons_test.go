package httpserver

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestNavigationIconCatalogHasLocalSymbols(t *testing.T) {
	for _, path := range []string{"../coreui/templates/partials/icons.html", "../../themes/chunnel/templates/partials/icons.html"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]bool)
		for _, option := range navigationIconOptions() {
			if seen[option.Value] {
				t.Fatalf("duplicate icon option %q", option.Value)
			}
			seen[option.Value] = true
			if option.Value != "" && !strings.Contains(string(data), `id="icon-`+option.Value+`"`) {
				t.Errorf("icon %q has no local symbol in %s", option.Value, path)
			}
		}
	}
}

func TestNavigationEditorRetainsImportedThemeIcons(t *testing.T) {
	_, handler, store, cookie, principal := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "图标菜单", Slug: "icon-menu", Locations: []string{navigation.LocationPrimary}}, principal.Username)
	if err != nil {
		t.Fatal(err)
	}
	menu, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: principal.Username}, []navigation.ItemInput{
		{ID: -1, Label: "主题图标", URL: "/", Icon: "theme-custom-icon"},
		{ID: -2, Label: "另一个入口", URL: "/explore/", Icon: "theme-custom-icon"},
	})
	if err != nil {
		t.Fatal(err)
	}
	options := navigationIconsForMenu(menu)
	if len(options) != len(navigationIconOptions())+1 || options[len(options)-1].Value != "theme-custom-icon" {
		t.Fatalf("custom icon catalog = %#v", options)
	}
	page := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/?menu="+strconv.FormatInt(menu.ID, 10), "", "", "")
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "data-icon-select") || strings.Count(body, `<option value="theme-custom-icon" selected>`) != 2 {
		t.Fatalf("editor did not retain custom icon values: status %d", page.Code)
	}
}

func TestNavigationIconsRenderInUtilityAndFooter(t *testing.T) {
	_, handler, store, _, principal := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "图标入口", Slug: "icon-locations", Locations: []string{navigation.LocationUtility, navigation.LocationFooter}}, principal.Username)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: principal.Username}, []navigation.ItemInput{
		{ID: -1, Label: "圈子入口", URL: "/community/", Icon: "orbit"},
		{ID: -2, ParentID: -1, Label: "资料入口", URL: "/explore/", Icon: "library"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	body := page.Body.String()
	start := strings.Index(body, `<nav class="utility-nav"`)
	if page.Code != http.StatusOK || start < 0 {
		t.Fatalf("public navigation status %d", page.Code)
	}
	utility, _, _ := strings.Cut(body[start:], "</nav>")
	if !strings.Contains(utility, `href="#icon-orbit"`) {
		t.Error("utility navigation omitted its selected icon")
	}
	start = strings.Index(body, `<div class="footer-column nav-item-`)
	if start < 0 {
		t.Fatal("footer navigation is missing")
	}
	footer, _, _ := strings.Cut(body[start:], "</div>")
	for _, name := range []string{"orbit", "library"} {
		if !strings.Contains(footer, `href="#icon-`+name+`"`) {
			t.Errorf("footer navigation omitted %q", name)
		}
	}
}
