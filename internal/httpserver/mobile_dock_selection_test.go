package httpserver

import (
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestMobileDockDeduplicatesWithoutChangingStoredOrder(t *testing.T) {
	items := []navigationItemView{
		{ID: 1, Label: "首页", URL: "/"},
		{ID: 2, Label: "重复首页", URL: "/"},
		{ID: 3, Label: "发现", URL: "/explore/"},
		{ID: 4, Label: "重复发现", URL: "/explore/"},
		{ID: 5, Label: "我的", URL: "/account/"},
	}
	original := append([]navigationItemView(nil), items...)
	dock := mobileDockItems(items, "/", false)
	if len(dock) != 4 || dock[0].ID != 1 || dock[1].ID != 3 || dock[2].ID != 5 || dock[3].URL != "/series/" {
		t.Fatalf("want first distinct destinations and fallback, got %+v", dock)
	}
	if !reflect.DeepEqual(items, original) {
		t.Fatal("public projection changed stored menu")
	}
	for _, item := range dock {
		if item.URL == "/community/" {
			t.Fatal("disabled community used as fallback")
		}
	}
}

func TestMobileDockSelectsOneMostSpecificDestination(t *testing.T) {
	for _, tc := range []struct {
		name, current, want string
		links               []string
	}{
		{"exact before ancestor", "/account/access/", "/account/access/", []string{"/account/", "/account/access/", "/", "/explore/"}},
		{"deepest ancestor", "/account/access/device/", "/account/access/", []string{"/account/", "/account/access/", "/", "/explore/"}},
		{"boundary", "/accountant/", "", []string{"/account/", "/account/access/", "/", "/explore/"}},
		{"external network path", "/account/access/", "/account/", []string{"//outside.test/account/access/", "/account/", "/", "/explore/"}},
		{"external absolute path", "/account/access/", "/account/", []string{"https://outside.test/account/access/", "/account/", "/", "/explore/"}},
		{"section is not whole page", "/", "/", []string{"/#latest", "/", "/account/", "/explore/"}},
		{"query view remains distinct", "/account/", "/account/", []string{"/account/?view=bookmark", "/account/", "/account/?view=like", "/"}},
		{"matching query", "/account/?view=bookmark", "/account/?view=bookmark", []string{"/account/", "/account/?view=bookmark", "/account/?view=like", "/"}},
		{"query order", "/explore/?sort=new&page=2", "/explore/?page=2&sort=new", []string{"/explore/", "/explore/?page=2&sort=new", "/account/", "/"}},
		{"escaped percent", "/topics/100%25/", "/topics/100%25/", []string{"/topics/", "/topics/100%25/", "/account/", "/"}},
		{"no inherited child activation", "/unrelated/", "", []string{"/collection/", "/account/", "/", "/explore/"}},
		{"no pathname", "", "", []string{"/account/", "/explore/", "/", "/series/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]navigationItemView, len(tc.links))
			for i, link := range tc.links {
				items[i] = navigationItemView{ID: int64(i + 1), URL: link, Active: true}
			}
			dock := mobileDockItems(items, tc.current, false)
			var selected []string
			for _, item := range dock {
				if item.Active {
					selected = append(selected, item.URL)
				}
			}
			if tc.want == "" && len(selected) != 0 || tc.want != "" && (len(selected) != 1 || selected[0] != tc.want) {
				t.Fatalf("selected %v; want %q", selected, tc.want)
			}
			for i := range items {
				if !items[i].Active {
					t.Fatal("input projection mutated")
				}
			}
		})
	}
}

func TestMobileDockCustomSelectionAndModuleControlsHTTP(t *testing.T) {
	s, handler, store, cookie, owner := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "移动入口", Slug: "mobile-unique", Locations: []string{navigation.LocationMobile}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "我的", URL: "/account/"},
		{ID: -2, Type: navigation.TypeCustom, Label: "登录与找回", URL: "/account/access/"},
		{ID: -3, Type: navigation.TypeCustom, Label: "重复账号入口", URL: "/account/"},
		{ID: -4, Type: navigation.TypeCustom, Label: "首页", URL: "/"},
		{ID: -5, Type: navigation.TypeCustom, Label: "重复首页", URL: "/"},
		{ID: -6, Type: navigation.TypeCustom, Label: "定制探索第六项", URL: "/explore/"},
	}
	if _, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, inputs); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dockPattern := regexp.MustCompile(`(?s)<nav class="mobile-bottom-nav[^"]*"[^>]*>(.*?)</nav>`)
	anchors := regexp.MustCompile(`<a\s+[^>]*>`)
	for _, style := range []string{"classic", "liquid-glass"} {
		for _, quick := range []bool{true, false} {
			values := s.themes.SettingsSnapshot().Values
			values["visual_style"], values["mobile_quick_action"], values["mobile_nav_enabled"] = style, quick, true
			if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
				t.Fatal(fields, err)
			}
			for _, path := range []string{"/account/access/", "/account/", "/", "/explore/"} {
				response := serveNavigationRequest(handler, cookie, http.MethodGet, "https://example.test"+path, "", "", "")
				match := dockPattern.FindStringSubmatch(response.Body.String())
				if response.Code != 200 || len(match) != 2 {
					t.Fatalf("%s %s: %d, mobile dock missing", style, path, response.Code)
				}
				dock := match[1]
				if strings.Contains(dock, "重复账号入口") || strings.Contains(dock, "重复首页") || !strings.Contains(dock, "定制探索第六项") || strings.Count(dock, `class="mobile-create"`) != map[bool]int{true: 1, false: 0}[quick] || len(anchors.FindAllString(dock, -1)) != 4 {
					t.Fatal("custom projection or quick-action switch", dock)
				}
				selected := 0
				for _, anchor := range anchors.FindAllString(dock, -1) {
					if strings.Contains(anchor, `aria-current="page"`) {
						selected++
						if !strings.Contains(anchor, `href="`+path+`"`) {
							t.Fatal("wrong current destination", path, anchor)
						}
					}
				}
				if selected != 1 {
					t.Fatal("multiple current destinations", path, selected)
				}
				if quick {
					plus := strings.Index(dock, `class="mobile-create"`)
					if len(anchors.FindAllString(dock[:plus], -1)) != 2 {
						t.Fatal("plus is not between two pairs of links")
					}
				}
			}
		}
	}
	values := s.themes.SettingsSnapshot().Values
	values["mobile_nav_enabled"] = false
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	response := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if dockPattern.MatchString(response.Body.String()) {
		t.Fatal("disabled dock still rendered")
	}
	after, err := store.Snapshot(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("render changed persisted menus", err)
	}
}

func TestMobileDockAccountViewSelectionHTTP(t *testing.T) {
	s, handler, store, _, owner := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "账号快捷入口", Slug: "account-views", Locations: []string{navigation.LocationMobile}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "我的", URL: "/account/"},
		{ID: -2, Type: navigation.TypeCustom, Label: "我的收藏", URL: "/account/?view=bookmark"},
		{ID: -3, Type: navigation.TypeCustom, Label: "我的点赞", URL: "/account/?view=like"},
		{ID: -4, Type: navigation.TypeCustom, Label: "账号找回", URL: "/account/access/?flow=recover"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dockPattern := regexp.MustCompile(`(?s)<nav class="mobile-bottom-nav[^"]*"[^>]*>(.*?)</nav>`)
	selectedPattern := regexp.MustCompile(`<a\s+[^>]*aria-current="page"[^>]*>`)
	for _, style := range []string{"classic", "liquid-glass"} {
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			t.Fatal(fields, err)
		}
		for _, path := range []string{"/account/", "/account/?view=bookmark", "/account/?view=like", "/account/access/?flow=recover"} {
			response := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test"+path, "", "", "")
			dock := dockPattern.FindStringSubmatch(response.Body.String())
			if response.Code != 200 || len(dock) != 2 {
				t.Fatal(path, response.Code)
			}
			selected := selectedPattern.FindAllString(dock[1], -1)
			if len(selected) != 1 || !strings.Contains(selected[0], `href="`+path+`"`) {
				t.Fatal("query-specific current link", path, selected)
			}
			if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), `rel="canonical"`) {
				t.Fatal("private account URL must remain uncached and unindexed")
			}
			if !strings.Contains(response.Body.String(), "js/chunnel-account-nav.js") {
				t.Fatal("account selection module missing")
			}
		}
	}
	home := serveNavigationRequest(handler, nil, http.MethodGet, "https://example.test/", "", "", "")
	if strings.Contains(home.Body.String(), "chunnel-account-nav") {
		t.Fatal("account selection module loaded on homepage")
	}
}
