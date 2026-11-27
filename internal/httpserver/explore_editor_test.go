package httpserver

import (
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestExploreEditorSourceStates(t *testing.T) {
	s, handler, store, cookie, _ := newNavigationHTTPTestServer(t)
	primary := navigation.Menu{ID: 1, Name: "主菜单", Items: []navigation.Item{{ID: 1, Label: "任意名称", URL: "/explore/"}, {ID: 2, ParentID: 1, URL: "/guide/"}}}
	dedicated := navigation.Menu{ID: 2, Name: "专属", Items: []navigation.Item{{ID: 3, URL: "/guide/"}}}
	for _, tc := range []struct {
		name      string
		menus     []navigation.Menu
		locations map[string]int64
		title     string
	}{
		{"none", nil, nil, "尚无发现页入口"},
		{"fallback", []navigation.Menu{primary}, map[string]int64{"primary": 1}, "复用主导航子项"},
		{"dedicated", []navigation.Menu{primary, dedicated}, map[string]int64{"primary": 1, "explore": 2}, "使用发现页专属菜单"},
		{"empty", []navigation.Menu{primary, {ID: 2, Name: "空菜单"}}, map[string]int64{"primary": 1, "explore": 2}, "发现页专属菜单为空"},
		{"filtered", []navigation.Menu{{ID: 2, Items: []navigation.Item{{ID: 3, URL: "/admin/"}}}}, map[string]int64{"explore": 2}, "专属菜单暂无可公开入口"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.adminExploreSource(navigation.Snapshot{Menus: tc.menus, Locations: tc.locations})
			if got.Title != tc.title {
				t.Fatalf("%+v", got)
			}
			if tc.name == "fallback" && got.EditURL != navigationAdminURL(1, "") {
				t.Fatal(got)
			}
		})
	}
	before, _ := store.Snapshot(t.Context())
	page := serveNavigationRequest(handler, cookie, "GET", "https://example.test/admin/navigation/?location=explore", "", "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "发现页已发布内容来源") || !strings.Contains(page.Body.String(), `value="explore" checked`) || !strings.Contains(page.Body.String(), "未保存的编辑不会影响前台") {
		t.Fatalf("editor: %d %s", page.Code, page.Body.String())
	}
	after, _ := store.Snapshot(t.Context())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("viewing editor mutated menus")
	}
}
