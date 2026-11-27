package httpserver

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestAccountPagesKeepMobileNavigationSelected(t *testing.T) {
	s, handler, users, _, _, owner := newPersistentAdminTestServer(t)
	_, memberCookie, _ := createAndLoginTestUser(t, users, owner, "nav-reader", "nav-reader@example.test", "导航读者", "subscriber")
	dockPattern := regexp.MustCompile(`(?s)<nav class="mobile-bottom-nav[^"]*"[^>]*>(.*?)</nav>`)
	anchorPattern := regexp.MustCompile(`<a\s+[^>]*>`)
	for _, mode := range []string{"fallback", "configured"} {
		if mode == "configured" {
			menu, err := s.navigation.Create(t.Context(), navigation.MenuInput{
				Name: "移动导航", Slug: "account-navigation", Locations: []string{navigation.LocationMobile},
			}, owner.Username)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.navigation.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, []navigation.ItemInput{
				{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/"},
				{ID: -2, Type: navigation.TypeCustom, Label: "发现", URL: "/explore/"},
				{ID: -3, Type: navigation.TypeCustom, Label: "专题", URL: "/series/"},
				{ID: -4, Type: navigation.TypeCustom, Label: "我的", URL: "/account/"},
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, session := range []struct {
			name   string
			cookie *http.Cookie
		}{{"guest", nil}, {"member", memberCookie}} {
			for _, path := range []string{"/account/", "/account/?view=bookmarks", "/account/access/", "/account/access/?flow=recovery", "/", "/explore/"} {
				t.Run(mode+"/"+session.name+path, func(t *testing.T) {
					response := serveContentAPI(handler, session.cookie, http.MethodGet, "https://example.test"+path, "", nil)
					if response.Code != http.StatusOK {
						t.Fatalf("page status = %d", response.Code)
					}
					body := response.Body.String()
					dock := dockPattern.FindStringSubmatch(body)
					if len(dock) != 2 {
						t.Fatal("mobile navigation missing")
					}
					want := path
					if strings.HasPrefix(path, "/account/") {
						want = "/account/"
						if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Robots-Tag") != "noindex, nofollow, noarchive" || strings.Contains(body, `rel="canonical"`) || strings.Contains(body, `property="og:url"`) {
							t.Error("account page caching or indexing behavior changed")
						}
					}
					selected := 0
					for _, anchor := range anchorPattern.FindAllString(dock[1], -1) {
						if strings.Contains(anchor, `aria-current="page"`) {
							selected++
							if !strings.Contains(anchor, `href="`+want+`"`) || !strings.Contains(anchor, "is-current") {
								t.Errorf("incorrect selected item: %s", anchor)
							}
						}
					}
					if selected != 1 {
						t.Fatalf("got %d selected items, want exactly one for %s", selected, want)
					}
				})
			}
		}
	}
}
