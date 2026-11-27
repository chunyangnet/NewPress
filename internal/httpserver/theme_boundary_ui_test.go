package httpserver

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

// Opt-in local browser fixture. Only a temporary starter installation and
// in-memory test accounts/content are used; no production data is opened.
func TestThemeBoundaryInteractiveUI(t *testing.T) {
	if os.Getenv("NEWPRESS_THEME_UI") != "1" {
		t.Skip("interactive browser fixture only")
	}
	root := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "starter"), filepath.Join(root, "starter")); err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(root, "starter", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Boundary fixture 2026!")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, BootstrapEmail: "owner@example.test", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{SiteTitle: "Independent theme QA", SiteTagline: "公共主题与核心界面", PublicURL: "http://127.0.0.1:19087", ActiveTheme: "starter"}, content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()), identity)
	listener, err := net.Listen("tcp", "127.0.0.1:19087")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	t.Log("Isolated starter fixture: http://127.0.0.1:19087/; owner / Boundary fixture 2026!")
	go server.Serve(listener)
	defer server.Close()
	// A test-only environment-selected stop file permits a clean browser handoff.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if stop := os.Getenv("NEWPRESS_THEME_UI_STOP"); stop != "" {
				if _, err := os.Stat(stop); err == nil {
					return
				}
			}
		}
	}
}
