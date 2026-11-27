package httpserver

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/theme"
)

func TestMobileCentralActionLifecycle(t *testing.T) {
	s, handler, store, _, owner := newNavigationHTTPTestServer(t)
	dockPattern := regexp.MustCompile(`(?s)<nav class="mobile-bottom-nav[^"]*"[^>]*>.*?</nav>`)
	dock := func(path string) string {
		t.Helper()
		response := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test"+path, "", "", "")
		body := dockPattern.FindString(response.Body.String())
		if response.Code != 200 || body == "" {
			t.Fatalf("dock at %s: status %d", path, response.Code)
		}
		return body
	}
	if body := dock("/"); !strings.Contains(body, `data-modal-open="quick-actions"`) || !strings.Contains(body, "has-create") {
		t.Fatal("unconfigured central action lost legacy quick panel", body)
	}
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "中央入口", Slug: "central-action", Locations: []string{navigation.LocationMobileAction}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	if body := dock("/"); strings.Contains(body, "mobile-create") || strings.Contains(body, "has-create") {
		t.Fatal("assigned empty menu must hide central action", body)
	}
	menu, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, []navigation.ItemInput{
		{ID: -1, Label: "内部管理", URL: "/admin/"},
		{ID: -2, ParentID: -1, Label: "隐藏子项目", URL: "/explore/"},
		{ID: -3, Type: navigation.TypePage, ObjectID: 77, Label: "读<书>", URL: "https://docs.example.test/guide", Icon: "book-open", Target: navigation.TargetBlank, Rel: "ugc", Badge: navigation.Badge{Text: "新", HideOnMobile: true}},
		{ID: -4, Label: "第二入口", URL: "/community/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, style := range []string{"classic", "liquid-glass"} {
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
			t.Fatal(fields, err)
		}
		body := dock("/")
		for _, want := range []string{`mobile-create-custom`, `href="https://docs.example.test/guide"`, `aria-label="读&lt;书&gt;"`, `>读&lt;书&gt;</span>`, `target="_blank"`, `rel="ugc noopener noreferrer"`, "book-open", "nav-badge-mobile-hidden"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s central action missing %s: %s", style, want, body)
			}
		}
		central := strings.Index(body, `class="mobile-create`)
		if strings.Count(body, "<a ") != 5 || strings.Count(body[:central], "</a>") != 2 || strings.Contains(body, "第二入口") || strings.Contains(body, "内部管理") || strings.Contains(body, "隐藏子项目") || strings.Contains(body, "data-modal-open") {
			t.Fatal("central action position/filter/projection", body)
		}
	}
	public := publicPage(pageView{Navigation: s.publicNavigation("/")}).Navigation[navigation.LocationMobileAction]
	if !public.Configured || len(public.Items) != 1 || public.Items[0].Label != "读<书>" || !public.Items[0].Badge.HideOnMobile {
		t.Fatal("public/v1 central action lost configuration", public)
	}
	values := s.themes.SettingsSnapshot().Values
	values["mobile_quick_action"] = false
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	if body := dock("/"); strings.Contains(body, "mobile-create") || strings.Contains(body, "has-create") {
		t.Fatal("disabled central slot still rendered", body)
	}
	// An independent theme uses the embedded core account page and its defaults.
	s.themes = theme.NewManager("../../themes", "starter")
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	if body := dock("/account/"); !strings.Contains(body, "mobile-create-custom") || strings.Count(body, "<a ") != 5 {
		t.Fatal("core fallback lost custom mobile navigation", body)
	}
	menu, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, []navigation.ItemInput{{ID: -1, Label: "被过滤", URL: "/api/v1/system"}})
	if err != nil {
		t.Fatal(err)
	}
	if body := dock("/account/"); strings.Contains(body, "mobile-create") || strings.Contains(body, "has-create") {
		t.Fatal("filtered empty action must not restore quick panel", body)
	}
	if err := store.Delete(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}); err != nil {
		t.Fatal(err)
	}
	if body := dock("/account/"); !strings.Contains(body, `data-modal-open="quick-actions"`) {
		t.Fatal("released slot did not restore quick panel", body)
	}
}

func TestAdminNavigationLocationShortcuts(t *testing.T) {
	_, handler, store, cookie, owner := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "主导航", Slug: "main", Locations: []string{navigation.LocationPrimary}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{navigation.LocationMobile, navigation.LocationMobileAction} {
		response := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/?location="+location, "", "", "")
		body := response.Body.String()
		if response.Code != 200 || !strings.Contains(body, `class="admin-navigation-create" open`) || !strings.Contains(body, `value="`+location+`" checked`) || strings.Contains(body, `data-navigation-items-form>`) {
			t.Fatal("unassigned location should start a preselected creation form", location, response.Code)
		}
	}
	menu, err = store.Update(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, navigation.MenuInput{Name: menu.Name, Slug: menu.Slug, Locations: []string{navigation.LocationPrimary, navigation.LocationMobileAction}})
	if err != nil {
		t.Fatal(err)
	}
	response := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/?location=mobile_action", "", "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `data-navigation-items-form>`) || strings.Contains(response.Body.String(), `class="admin-navigation-create" open`) {
		t.Fatal("assigned shortcut did not open menu", response.Code)
	}
	for _, query := range []string{"location=unknown", "location=", "location=mobile&menu=1", "location=mobile&location=mobile_action"} {
		response := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test/admin/navigation/?"+query, "", "", "")
		if response.Code != 400 {
			t.Fatal("accepted ambiguous/invalid location", query, response.Code)
		}
	}
}

func TestNavigationImportRequiresAllCurrentSlots(t *testing.T) {
	document := validNavigationImportDocument()
	document.LocationSlots[4].MenuSlug = document.Menus[0].Slug
	document.Menus[0].Locations = append(document.Menus[0].Locations, navigation.LocationMobileAction)
	document.Menus[0].Items[0].Badge = navigation.Badge{Text: "新", HideOnMobile: true}
	_, inputs, _, _, err := prepareNavigationImport(marshalNavigationImportDocument(t, document), navigationImportPreserveObjects)
	if err != nil || !stringListContains(inputs[0].Menu.Locations, navigation.LocationMobileAction) || !inputs[0].Items[0].Badge.HideOnMobile {
		t.Fatal("current slots and visibility failed to import", err)
	}
	for index, slot := range document.LocationSlots {
		incomplete := validNavigationImportDocument()
		incomplete.LocationSlots = append(incomplete.LocationSlots[:index], incomplete.LocationSlots[index+1:]...)
		if _, _, _, _, err := prepareNavigationImport(marshalNavigationImportDocument(t, incomplete), navigationImportPreserveObjects); err == nil {
			t.Fatal("missing slot accepted", slot.Key)
		}
	}
	retired := validNavigationImportDocument()
	retired.LocationSlots = retired.LocationSlots[:4]
	if _, _, _, _, err := prepareNavigationImport(marshalNavigationImportDocument(t, retired), navigationImportPreserveObjects); err == nil {
		t.Fatal("retired four-slot export accepted")
	}
}
