package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func isolatedChildThemes(t *testing.T, parentSlug string) (*theme.Manager, string) {
	t.Helper()
	root := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", parentSlug), filepath.Join(root, parentSlug)); err != nil {
		t.Fatal(err)
	}
	childSlug := "starter-child"
	if parentSlug == "starter" {
		if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", childSlug), filepath.Join(root, childSlug)); err != nil {
			t.Fatal(err)
		}
	} else {
		childSlug = "chunnel-child"
		if err := os.Mkdir(filepath.Join(root, childSlug), 0700); err != nil {
			t.Fatal(err)
		}
		manifest := theme.Manifest{Schema: 2, Slug: childSlug, Name: "Chunnel Child", Version: "1.0.0", Engine: "go-template/v1", View: theme.ChunnelViewV1, Parent: &theme.ParentTheme{Slug: parentSlug, Version: "*"}}
		if err := extension.WriteJSON(filepath.Join(root, childSlug, "theme.json"), manifest); err != nil {
			t.Fatal(err)
		}
	}
	manager := theme.NewManager(root, childSlug, theme.WithSettingsDir(t.TempDir()))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	return manager, root
}

func TestChildThemeHTTP(t *testing.T) {
	for _, parent := range []string{"starter", "chunnel"} {
		t.Run(parent, func(t *testing.T) {
			s, h, _, _, _, _ := newPersistentAdminTestServer(t)
			s.themes, _ = isolatedChildThemes(t, parent)
			w := serveCookieRequest(h, "GET", "https://example.test/", nil, nil)
			if w.Code != 200 {
				t.Fatalf("home: %d %.300s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "/theme-assets/"+s.themes.Active().Slug+"/") {
				t.Fatal("parent resource namespace leaked")
			}
			if parent == "starter" && !strings.Contains(w.Body.String(), "仅维护差异文件") {
				t.Fatal("child footer missing")
			}
			asset := s.themes.AssetURL("css/starter.css")
			if parent == "chunnel" {
				asset = s.themes.AssetURL("css/chunnel.css")
			}
			w = serveCookieRequest(h, "GET", "https://example.test"+asset, nil, nil)
			if w.Code != 200 {
				t.Fatal("inherited asset", w.Code)
			}
			w = serveCookieRequest(h, "GET", "https://example.test/admin/login/", nil, nil)
			if strings.Contains(w.Body.String(), "仅维护差异文件") {
				t.Fatal("child overrode core admin")
			}
		})
	}
}

// Opt-in isolated fixture. All package and settings writes are in t.TempDir.
func TestChildThemeBrowserFixture(t *testing.T) {
	addr := os.Getenv("NEWPRESS_CHILD_BROWSER_ADDR")
	if addr == "" {
		t.Skip("isolated browser fixture")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatal("loopback required")
	}
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	var themeRoot string
	s.themes, themeRoot = isolatedChildThemes(t, "starter")
	// Extra definitions exist only in this isolated browser fixture.
	parentSchema := `{"type":"object","properties":{"accent":{"type":"string","title":"链接颜色","default":"#2563eb","pattern":"^#[0-9a-fA-F]{6}$","x-newpress":{"control":"color","group":"appearance","css_variable":"--accent"}},"parent_caption":{"type":"string","title":"父主题说明","default":"父默认值","x-newpress":{"group":"appearance"}}},"x-newpress":{"version":1,"groups":[{"id":"appearance","title":"外观","icon":"palette"}]}}`
	childSchema := `{"type":"object","properties":{"accent":{"default":"#0f766e"},"child_caption":{"type":"string","title":"子主题说明","default":"子默认值","x-newpress":{"group":"appearance"}}}}`
	for name, data := range map[string]string{"starter/settings.schema.json": parentSchema, "starter-child/settings.schema.json": childSchema} {
		if err := os.WriteFile(filepath.Join(themeRoot, filepath.FromSlash(name)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.themes.LoadExplicit("starter-child"); err != nil {
		t.Fatal(err)
	}
	s.config.PublicURL = "http://" + addr
	market := &httpExtensionMarket{view: plugin.MarketView{}}
	var parentInfo theme.Manifest
	parentBytes, err := os.ReadFile(filepath.Join(themeRoot, "starter", "theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(parentBytes, &parentInfo); err != nil {
		t.Fatal(err)
	}
	parentInfo.Version = "1.1.0"
	market.view.Checked = time.Now()
	market.view.Expires = time.Now().Add(time.Hour)
	market.view.Themes = []theme.Release{{Manifest: parentInfo, Release: &extension.Metadata{Channel: "stable", DataCompatible: true}}}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "jobs.json"), market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	s.SetExtensionUpdates(service)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: childLifecycleFixture(t, s.themes, themeRoot, h), ReadHeaderTimeout: 5 * time.Second}
	go func() { <-time.After(30 * time.Minute); _ = server.Shutdown(context.Background()) }()
	t.Log("child theme fixture ready", addr)
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
