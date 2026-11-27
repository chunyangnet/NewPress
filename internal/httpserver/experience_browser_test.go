package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/navigation"
)

// Opt-in browser QA starts with empty circles and temporary settings only.
// It exercises the same UI save paths as an installed site without its data.
func TestExperienceBrowserFixture(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_EXPERIENCE_BROWSER_FIXTURE")
	if artifacts == "" {
		t.Skip("isolated browser fixture")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store, err := community.NewFileStore(filepath.Join(t.TempDir(), "community.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.config.CommunityEnabled = true
	s.SetCommunityStore(store)
	menu, err := s.navigation.Create(t.Context(), navigation.MenuInput{Name: "主导航", Slug: "primary", Locations: []string{navigation.LocationPrimary, navigation.LocationMobile}}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.navigation.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, []navigation.ItemInput{
		{ID: -1, Label: "首页", URL: "/", Icon: "house"},
		{ID: -2, Label: "发现", URL: "/explore/", Icon: "compass"},
		{ID: -3, Label: "圈子", URL: "/community/", Icon: "users"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/circles/", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	data, _ := json.Marshal(map[string]string{"url": host.URL, "login": host.URL + "/fixture/login"})
	if err = os.WriteFile(filepath.Join(artifacts, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("experience browser fixture ready", host.URL)
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
