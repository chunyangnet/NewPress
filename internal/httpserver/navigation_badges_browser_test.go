package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/theme"
)

// Opt-in QA fixture uses isolated test stores and never opens the site's data.
func TestNavigationBadgeBrowserFixture(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_NAV_BADGE_BROWSER_FIXTURE")
	if artifacts == "" {
		t.Skip("browser fixture only")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, store, cookie, owner := newNavigationHTTPTestServer(t)
	if _, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePage, Status: content.StatusPublish, Title: "阅读指南", Slug: "mobile-guide", ContentSource: "<p>自定义中央按钮的目标页面。</p>"}); err != nil {
		t.Fatal(err)
	}
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "主导航样式", Slug: "badge-styles", Locations: []string{navigation.LocationPrimary, navigation.LocationMobile, navigation.LocationFooter}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	items := []navigation.ItemInput{}
	for i, entry := range []struct{ style, label, color string }{{"bubble", "气泡", "#FFFFFF"}, {"solid", "实色", "#FFFFFF"}, {"pill", "胶囊", "#FFFFFF"}, {"outline", "描边", "#D14343"}, {"soft", "柔和", "#D14343"}, {"gradient", "渐变", "#FFFFFF"}, {"ribbon", "飘带", "#FFFFFF"}} {
		items = append(items, navigation.ItemInput{ID: -int64(i + 1), Label: entry.label, URL: "/explore/?style=" + entry.style, Icon: "compass", Badge: navigation.Badge{Text: "最新", Style: entry.style, Color: entry.color, Radius: 4}})
	}
	if _, err := store.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, items); err != nil {
		t.Fatal(err)
	}
	quick, err := store.Create(t.Context(), navigation.MenuInput{Name: "顶部快捷入口", Slug: "quick-badges", Locations: []string{navigation.LocationUtility}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceItems(t.Context(), quick.ID, navigation.Mutation{ExpectedVersion: quick.Version, Actor: owner.Username}, []navigation.ItemInput{{ID: -1, Label: "星球", URL: "/community/", Badge: navigation.Badge{Text: "交流", Radius: 4}}, {ID: -2, Label: "问答", URL: "/community/#questions", Badge: navigation.Badge{Text: "最新", Radius: 4}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/navigation/?menu="+strconv.FormatInt(quick.ID, 10), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, "invalid fixture style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", handler)
	mux.HandleFunc("POST /fixture/core", func(w http.ResponseWriter, r *http.Request) {
		s.themes = theme.NewManager("../../themes", "starter")
		if err := s.themes.Load(); err != nil {
			http.Error(w, "invalid fixture theme", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := s.themes.Load(); err != nil {
			http.Error(w, "invalid fixture reload", 500)
			return
		}
		w.WriteHeader(204)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	data, _ := json.Marshal(map[string]string{"url": host.URL, "login": host.URL + "/fixture/login"})
	if err := os.WriteFile(filepath.Join(artifacts, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("navigation badge fixture ready", host.URL)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(artifacts, "stop")); err == nil {
				return
			}
		}
	}
}
