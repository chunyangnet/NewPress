package httpserver

import (
	"strings"
	"testing"
)

func TestFloatingSearchWithoutHeaderSearch(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	values := s.themes.SettingsSnapshot().Values
	values["search_enabled"] = false
	values["floating_tools"] = true
	values["floating_search"] = true
	values["floating_top"] = false
	values["floating_notifications"] = false
	if _, _, err := s.themes.SaveSettings(values); err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(h, nil, "GET", "https://example.test/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `href="/explore/" aria-label="打开搜索"`) {
		t.Fatal("floating search requires header search", r.Code)
	}
}
