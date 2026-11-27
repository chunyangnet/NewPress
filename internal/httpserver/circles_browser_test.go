package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"newpress.local/newpress/internal/community"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in fixture uses temporary file stores and test accounts only.
func TestCirclesBrowserFixture(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_CIRCLES_BROWSER_FIXTURE")
	if artifacts == "" {
		t.Skip("isolated browser fixture")
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "circle-reader", "circle-reader@example.test", "圈内读者", "subscriber")
	_, outsideCookie, _ := createAndLoginTestUser(t, users, owner, "circle-outsider", "circle-outsider@example.test", "圈外读者", "subscriber")
	root := t.TempDir()
	store, err := community.NewFileStore(filepath.Join(root, "community.json"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := community.NewFiles(filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	s.config.CommunityEnabled = true
	s.SetCommunityStore(store)
	s.SetCommunityFiles(files)
	for _, slug := range []string{"makers", "private"} {
		visibility := "public"
		if slug == "private" {
			visibility = "members"
		}
		created, err := store.Apply(t.Context(), slug, community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "create", Key: "browser-create-" + slug, Name: map[string]string{"makers": "创作与实践", "private": "成员的工作手记"}[slug], Description: "一起记录过程，交流经验，让问题得到回应。", Rules: "尊重他人，说明来源，分享真实的经验。", JoinPolicy: "open", Visibility: visibility, OwnerID: owner.UserID}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Apply(t.Context(), slug, community.Actor{ID: reader.UserID}, community.Command{Action: "join", Key: "browser-join-key-" + slug, Version: created.Version}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		switch r.PathValue("role") {
		case "reader":
			cookie = readerCookie
		case "outside":
			cookie = outsideCookie
		case "guest":
			expired := *ownerCookie
			expired.Value = ""
			expired.MaxAge = -1
			cookie = &expired
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/community/", 303)
	})
	mux.HandleFunc("POST /fixture/tick", func(w http.ResponseWriter, r *http.Request) {
		if err := s.notifications.Tick(r.Context(), time.Now()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
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
			http.Error(w, "style save failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	data, _ := json.Marshal(map[string]string{"url": host.URL, "owner": host.URL + "/fixture/login/owner", "reader": host.URL + "/fixture/login/reader", "outside": host.URL + "/fixture/login/outside", "guest": host.URL + "/fixture/login/guest"})
	if err = os.WriteFile(filepath.Join(artifacts, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(artifacts, "stop-browser")); err == nil {
				return
			}
		}
	}
}
