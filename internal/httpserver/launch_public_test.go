package httpserver

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestLaunchPublicSurfacesAndRetiredRoutes(t *testing.T) {
	t.Parallel()
	server, handler, _, cookie, _ := newNavigationHTTPTestServer(t)
	posts, err := server.repository.ListPublished(t.Context(), 1)
	if err != nil || len(posts) == 0 {
		t.Fatalf("fixture posts: %v", err)
	}
	paths := []string{"/", "/explore/", "/community/", "/authors/newpress-team/", "/posts/" + posts[0].Slug, "/categories/" + posts[0].Categories[0].Slug + "/"}
	adminLinks := regexp.MustCompile(`<a\b[^>]*href="/admin[^>]*>`)
	for _, path := range paths {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", path, response.Code)
		}
		body := response.Body.String()
		for _, retired := range []string{"/components/", "/market/", "/health/ready", "组件实验室", "示例价格", "样例展示", "会员组件", "赞赏作者", "暂无资源包", "planet-tile", "taxonomy-admin-bridge"} {
			if strings.Contains(body, retired) {
				t.Errorf("%s still renders %q", path, retired)
			}
		}
		for _, link := range adminLinks.FindAllString(body, -1) {
			if !strings.Contains(link, " hidden") || (!strings.Contains(link, "data-account-admin") && !strings.Contains(link, "data-account-publish")) {
				t.Errorf("%s has public operational link: %s", path, link)
			}
		}
	}
	for _, path := range []string{"/components", "/components/", "/components/buttons", "/market", "/market/", "/market/?resource=all"} {
		for _, session := range []*http.Cookie{nil, cookie} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				response := serveCookieRequest(handler, method, "https://example.test"+path, session, nil)
				if response.Code != http.StatusNotFound {
					t.Errorf("%s %s = %d, want 404", method, path, response.Code)
				}
			}
		}
	}
	appearance := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/appearance/", cookie, nil)
	for _, field := range []string{"download_module"} {
		if strings.Contains(appearance.Body.String(), `name="`+field+`"`) {
			t.Errorf("retired appearance control %s remains visible", field)
		}
	}
	for _, field := range []string{"market_nav", "market_module"} {
		if !strings.Contains(appearance.Body.String(), `name="`+field+`"`) {
			t.Errorf("shop appearance control %s missing", field)
		}
	}
	settings := server.siteSettings.Snapshot()
	settings.Settings.Operations.MaintenanceEnabled = true
	if _, fields, err := server.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) != 0 {
		t.Fatalf("enable maintenance: %v %v", fields, err)
	}
	maintenance := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
	if maintenance.Code != http.StatusServiceUnavailable || !strings.Contains(maintenance.Body.String(), "刷新重试") {
		t.Fatalf("maintenance homepage: HTTP %d", maintenance.Code)
	}
	if strings.Contains(maintenance.Body.String(), `href="/health/`) {
		t.Error("maintenance homepage exposes operational links")
	}
	for _, link := range adminLinks.FindAllString(maintenance.Body.String(), -1) {
		if !strings.Contains(link, " hidden") {
			t.Errorf("maintenance homepage exposes management link: %s", link)
		}
	}
}

func TestLaunchNavigationFiltersStoredMenusWithoutMutatingThem(t *testing.T) {
	t.Parallel()
	_, handler, store, _, _ := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{
		Name: "Existing navigation", Slug: "existing", Locations: []string{navigation.LocationPrimary, navigation.LocationUtility, navigation.LocationMobile, navigation.LocationFooter},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	items := []navigation.ItemInput{}
	for index, target := range []string{"/components/", "/market/?resource=all", "/admin/", "https://example.test/admin/content/", "/%61dmin/", "/health/ready", "/api/v1/system"} {
		items = append(items, navigation.ItemInput{ID: -int64(index + 1), Type: navigation.TypeCustom, Label: "Retired link", URL: target})
	}
	items = append(items,
		navigation.ItemInput{ID: -8, ParentID: -1, Type: navigation.TypeCustom, Label: "Retired subtree", URL: "/explore/"},
		navigation.ItemInput{ID: -9, Type: navigation.TypeCustom, Label: "Visible content", URL: "/explore/"},
		navigation.ItemInput{ID: -10, ParentID: -9, Type: navigation.TypeCustom, Label: "External guide", URL: "https://other.example/components/"},
		navigation.ItemInput{ID: -11, Type: navigation.TypeCustom, Label: "Visible community", URL: "/community/"},
	)
	menu, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: "test"}, items)
	if err != nil {
		t.Fatal(err)
	}
	home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
	if home.Code != http.StatusOK || strings.Contains(home.Body.String(), "Retired") || !strings.Contains(home.Body.String(), "External guide") {
		t.Fatalf("stored navigation not filtered: HTTP %d", home.Code)
	}
	for _, location := range []string{navigation.LocationPrimary, navigation.LocationUtility, navigation.LocationMobile, navigation.LocationFooter} {
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/navigation/"+location, nil, nil)
		var public publicNavigationMenuAPIResponse
		decodeAPIResponse(t, response, &public)
		if response.Code != http.StatusOK || len(public.Items) != 2 || public.Items[0].Label != "Visible content" || len(public.Items[0].Children) != 1 || strings.Contains(response.Body.String(), "Retired") {
			t.Errorf("public %s = %s", location, response.Body.String())
		}
		request := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/navigation/"+location, nil)
		request.Header.Set("If-None-Match", response.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		handler.ServeHTTP(cached, request)
		if cached.Code != http.StatusNotModified {
			t.Errorf("conditional %s = %d", location, cached.Code)
		}
	}
	stored, err := store.Get(t.Context(), menu.ID)
	if err != nil || len(stored.Items) != len(items) || stored.Version != menu.Version {
		t.Fatal("public reads modified stored navigation")
	}
}

func TestLaunchAccountCapabilitiesAndEmptySite(t *testing.T) {
	t.Parallel()
	_, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "launch-reader", "launch-reader@example.test", "读者", "subscriber")
	_, authorCookie, _ := createAndLoginTestUser(t, manager, owner, "launch-author", "launch-author@example.test", "作者", "author")
	for _, test := range []struct {
		name                    string
		cookie                  *http.Cookie
		authenticated, canAdmin bool
	}{
		{"guest", nil, false, false}, {"reader", readerCookie, true, false}, {"author", authorCookie, true, true}, {"owner", ownerCookie, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/social/session", test.cookie, nil)
			var session struct {
				Authenticated bool `json:"authenticated"`
				CanAdmin      bool `json:"can_admin"`
			}
			decodeAPIResponse(t, response, &session)
			if response.Code != http.StatusOK || session.Authenticated != test.authenticated || session.CanAdmin != test.canAdmin || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("account capabilities: %d %+v", response.Code, session)
			}
		})
	}
	home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "还没有公开内容") || strings.Contains(home.Body.String(), `id="community"`) {
		t.Fatal("empty site renders showcase discussions")
	}
	denied := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/", readerCookie, nil)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("reader admin access = %d", denied.Code)
	}
}
