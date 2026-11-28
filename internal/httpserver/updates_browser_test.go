package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/update"
)

// Keep the synthetic catalog fresh throughout the verifier. Automatic checking
// is covered separately; this read fixture never launches an upstream task.
type migrationFreshMarket struct {
	*httpExtensionMarket
	covers *extensionupdate.Service
}

func (m *migrationFreshMarket) View() plugin.MarketView {
	v := m.httpExtensionMarket.View()
	v.Checked = time.Now().UTC()
	return v
}
func (m *migrationFreshMarket) ThemeCover(ctx context.Context, slug, digest string) ([]byte, string, error) {
	return m.covers.ThemeCover(ctx, slug, digest)
}

// Render real administrative pages and JSON projections. No update, restart,
// installation or upstream download runs during browser verification.
func TestUpdatesMigrationBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_UPDATES_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated update migration browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, cookie, owner, _ := themeAdminFixture(t, "")
	catalog, err := s.extensions.Snapshot("theme", "stable")
	if err != nil {
		t.Fatal(err)
	}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "jobs.json"), &migrationFreshMarket{httpExtensionMarket: &httpExtensionMarket{view: catalog.Market}, covers: s.extensions}, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	s.SetExtensionUpdates(service)
	manager, err := update.New(t.TempDir(), "0.1.0", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	s.SetUpdates(manager)
	if err = manager.Configure(update.Source{Official: true}, false); err != nil {
		t.Fatal(err)
	}
	notices, err := s.announcements.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.announcements.Save(t.Context(), notices.Revision, "save", announcement.Item{Title: "更新验证公告", Body: "这是隔离验证数据。", Date: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC), Published: true}); err != nil {
		t.Fatal(err)
	}
	capture := func(path string) json.RawMessage {
		t.Helper()
		r := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
		if r.Code != 200 {
			t.Fatal(path, r.Code, r.Body.String())
		}
		return append(json.RawMessage{}, r.Body.Bytes()...)
	}
	snapshot := capture("/api/v1/admin/updates")
	system := capture("/api/v1/admin/system-status")
	job := capture("/api/v1/admin/extensions/theme/job")
	state := func() []byte {
		posts, e := s.repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		notices, e := s.announcements.Snapshot(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		updates, e := manager.Snapshot()
		if e != nil {
			t.Fatal(e)
		}
		files := map[string]string{}
		if e = filepath.WalkDir(s.config.ThemesDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			files[path] = hex.EncodeToString(sum[:])
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		data, e := json.Marshal(map[string]any{"posts": posts, "announcements": notices, "updates": updates, "themes": files, "settings": s.siteSettings.Snapshot(), "theme_settings": s.themes.SettingsSnapshot(), "media": s.media.Snapshot(), "job": s.extensions.Job("theme")})
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	write := func(name string, value any) {
		data, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(directory, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	before := state()
	var escaped atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/updates/", 303)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			escaped.Add(1)
			http.Error(w, "browser mutation not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	now := time.Now().UTC()
	write("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login", "owner_id": owner.UserID, "snapshot": snapshot, "system": system, "job": job, "progress": update.State{ID: "fixture-core-job", Phase: "observing", Target: "0.2.0", Previous: "0.1.0", Percent: 90, StartedAt: now, UpdatedAt: now, Logs: []update.Log{{Time: now, Level: "info", Message: "隔离进度样本"}}}, "extension_job": extensionupdate.Job{ID: "fixture-extension-job", Kind: "theme", Action: "install", Slug: "gallery", Target: "1.0.0", Phase: "downloading", Downloaded: 2048, Total: 4096, StartedAt: now, UpdatedAt: now, Message: "隔离任务样本"}, "notice": "Progress scenarios are Go-typed synthetic jobs; actual update, supervisor and downloads are not invoked."})
	t.Log("Update migration fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(45 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("fixture timeout")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(directory, "stop")); e == nil {
				after := state()
				if !bytes.Equal(before, after) || escaped.Load() != 0 {
					write("state-before.json", json.RawMessage(before))
					write("state-after.json", json.RawMessage(after))
					t.Fatalf("fixture changed; escaped writes %d", escaped.Load())
				}
				sum := sha256.Sum256(after)
				write("read-only-confirmation.json", map[string]any{"state_unchanged": true, "unintercepted_mutations": escaped.Load(), "state_sha256": hex.EncodeToString(sum[:])})
				return
			}
		}
	}
}
