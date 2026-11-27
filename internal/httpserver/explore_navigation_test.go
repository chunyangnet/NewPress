package httpserver

import (
	"net/http/httptest"
	"newpress.local/newpress/internal/navigation"
	"strings"
	"testing"
)

func TestNavigationQueryCurrentAndAncestors(t *testing.T) {
	menu := navigation.Menu{ID: 1, Items: []navigation.Item{
		{ID: 1, URL: "/explore/"},
		{ID: 2, ParentID: 1, URL: "/explore/?category=项目日志", Position: 1},
		{ID: 3, ParentID: 1, URL: "/explore/?category=主题设计", Position: 2},
		{ID: 4, ParentID: 1, URL: "/explore/?category=扩展架构", Position: 3},
		{ID: 5, ParentID: 1, URL: "/explore/?category=主题设计", Position: 4},
	}}
	for _, tc := range []struct {
		url string
		id  int64
	}{
		{"/explore/", 1}, {"/explore/?category=项目日志", 2},
		{"/explore/?view=list&page=2&category=%E4%B8%BB%E9%A2%98%E8%AE%BE%E8%AE%A1&sort=updated", 3},
		{"/explore/?category=扩展架构", 4},
	} {
		tree := buildNavigationTree(menu, tc.url)
		if !tree[0].Active || tree[0].Current != (tc.id == 1) {
			t.Fatalf("parent state: %s %#v", tc.url, tree[0])
		}
		for _, child := range tree[0].Children {
			if child.Current != (child.ID == tc.id) || child.Active != child.Current {
				t.Fatalf("child state: %s %#v", tc.url, child)
			}
		}
	}
	for _, tc := range []struct {
		target, current string
		want            bool
	}{
		{"/a/", "/ab/", false}, {"/a/", "/a/b", true}, {"/", "/?q=x", true},
		{"//other.test/explore/", "/explore/", false}, {"https://other.test/explore/", "/explore/", false},
		{"/explore/#x", "/explore/", false}, {"/explore/?q=a&category=b", "/explore/?category=b&q=a&page=2", true},
		{"/explore/?category=b", "/explore/", false},
	} {
		if got := navigationURLActive(tc.target, tc.current); got != tc.want {
			t.Errorf("%s on %s = %v", tc.target, tc.current, got)
		}
	}
}

func TestExploreNavigationImportSixSlots(t *testing.T) {
	document := validNavigationImportDocument()
	document.LocationSlots[5].MenuSlug = document.Menus[0].Slug
	document.Menus[0].Locations = append(document.Menus[0].Locations, navigation.LocationExplore)
	_, inputs, _, _, err := prepareNavigationImport(marshalNavigationImportDocument(t, document), navigationImportPreserveObjects)
	if err != nil || !stringListContains(inputs[0].Menu.Locations, navigation.LocationExplore) {
		t.Fatal("six-slot import", err)
	}
	if navigationMatch("/explore/?category=主题设计", "/explore/?category=design&page=2", map[string]string{"主题设计": "design", "design": "design"}) == 0 {
		t.Fatal("category name and slug should identify the same destination")
	}
}

func TestExploreDirectoryConfigurationAndEmptyMenu(t *testing.T) {
	server, handler, store, _, principal := newNavigationHTTPTestServer(t)
	ctx := t.Context()
	main, err := store.Create(ctx, navigation.MenuInput{Name: "Primary", Slug: "primary", Locations: []string{navigation.LocationPrimary}}, principal.Username)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceItems(ctx, main.ID, navigation.Mutation{ExpectedVersion: main.Version}, []navigation.ItemInput{
		{ID: -1, Label: "任意名称", URL: "/explore/"}, {ID: -2, ParentID: -1, Label: "旧栏目", URL: "/explore/?category=old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.publicNavigation("/explore/"); len(got.Explore) != 1 || got.Explore[0].Label != "旧栏目" {
		t.Fatal(got)
	}
	menu, err := store.Create(ctx, navigation.MenuInput{Name: "站点指南", Slug: "explore", Description: "选择一个入口", Locations: []string{navigation.LocationExplore}}, principal.Username)
	if err != nil {
		t.Fatal(err)
	}
	if got := server.publicNavigation("/explore/"); !got.ExploreConfigured || len(got.Explore) != 0 {
		t.Fatal(got)
	}
	menu, err = store.ReplaceItems(ctx, menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, []navigation.ItemInput{
		{ID: -1, Label: "阅读", URL: "/explore/?sort=updated", Icon: "library"},
		{ID: -2, ParentID: -1, Label: "指南", URL: "/guide/", Description: "开始阅读"},
		{ID: -3, Label: "后台不可公开", URL: "/admin/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ url, mode string }{{"/explore/", "directory"}, {"/explore/?sort=updated", "results"}, {"/explore/?category=old", "results"}, {"/explore/?q=hello", "results"}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `data-discovery-mode="`+tc.mode+`"`) {
			t.Fatalf("%s = %d", tc.url, w.Code)
		}
		if tc.mode == "directory" && (!strings.Contains(w.Body.String(), "站点指南") || !strings.Contains(w.Body.String(), "开始阅读") || strings.Contains(w.Body.String(), "后台不可公开")) {
			t.Fatal("directory projection")
		}
	}
}
