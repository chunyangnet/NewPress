package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
)

func TestPostgreSQLAppSettingsSurviveFreshDirectories(t *testing.T) {
	databaseURL, _, _ := isolatedAppDatabase(t)
	cfg := postgresAppConfig(t, databaseURL)
	first, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	if got := first.pool.Config().MaxConns; got != cfg.DBMaxConns {
		t.Fatalf("application connection limit = %d, want %d", got, cfg.DBMaxConns)
	}
	if got := first.pool.Config().MinConns; got != cfg.DBMinConns {
		t.Fatalf("application connection minimum = %d, want %d", got, cfg.DBMinConns)
	}
	if got := first.pool.Stat().IdleConns(); got < cfg.DBMinConns {
		t.Fatalf("application returned before its configured connections were ready: %d", got)
	}
	siteSnapshot := first.settings.Snapshot()
	siteSnapshot.Settings.Identity.Title = "Persisted site identity"
	siteSaved, fields, err := first.settings.Save(siteSnapshot.Revision, siteSnapshot.Settings)
	if err != nil || len(fields) > 0 {
		t.Fatalf("save site settings: fields=%v err=%v", fields, err)
	}
	themeSnapshot := first.themes.SettingsSnapshot()
	themeSnapshot.Values["accent"] = "#ABCDEF"
	themeSaved, fields, err := first.themes.SaveSettingsRevision(themeSnapshot.Revision, themeSnapshot.Values)
	if err != nil || len(fields) > 0 {
		t.Fatalf("save theme settings: fields=%v err=%v", fields, err)
	}
	for _, name := range []string{"site-settings.json", filepath.Join("theme-settings", "chunnel.json")} {
		if _, err := os.Stat(filepath.Join(cfg.DataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("database-backed settings unexpectedly need local file %s: %v", name, err)
		}
	}
	first.Close()

	// Only the database is retained. Theme packages are supplied afresh, as
	// they would be from an image; other local application state is empty.
	cfg = postgresAppConfig(t, databaseURL)
	cfg.SiteTitle = "Replacement deployment defaults"
	second, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal("start with fresh directories", err)
	}
	t.Cleanup(second.Close)
	if got := second.settings.Snapshot(); got.Revision != siteSaved.Revision || got.Settings.Identity.Title != siteSaved.Settings.Identity.Title {
		t.Fatalf("site settings were lost on restart: title=%q revision=%q", got.Settings.Identity.Title, got.Revision)
	}
	if got := second.themes.SettingsSnapshot(); got.Revision != themeSaved.Revision || got.Values["accent"] != "#ABCDEF" {
		t.Fatalf("theme settings were lost on restart: accent=%v revision=%q", got.Values["accent"], got.Revision)
	}
	if err := second.serverReadyForTest(t.Context()); err != nil {
		t.Fatal("restarted application stores", err)
	}
}

func TestPostgreSQLAppFailedStartupReleasesResources(t *testing.T) {
	databaseURL, adminPool, applicationName := isolatedAppDatabase(t)
	cfg := postgresAppConfig(t, databaseURL)
	cfg.ActiveTheme = "missing-theme"
	if application, err := New(t.Context(), cfg); err == nil {
		application.Close()
		t.Fatal("missing theme unexpectedly loaded")
	}
	var connections int
	if err := adminPool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`, applicationName).Scan(&connections); err != nil {
		t.Fatal(err)
	}
	if connections != 0 {
		t.Fatalf("failed startup retained %d database connections", connections)
	}
	// Reusing these exact directories also verifies failure released dirlock.
	cfg.ActiveTheme = "chunnel"
	application, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal("retry startup after correcting theme", err)
	}
	application.Close()
}

func postgresAppConfig(t *testing.T, databaseURL string) config.Config {
	t.Helper()
	return config.Config{
		Address: "127.0.0.1:0", DataDir: t.TempDir(), ThemesDir: isolatedThemes(t), PluginsDir: t.TempDir(),
		ActiveTheme: "chunnel", SiteTitle: "Database startup test", SiteTagline: "Test settings persistence",
		PublicURL: "http://127.0.0.1:8080", DatabaseURL: databaseURL, DBMaxConns: 2, DBMinConns: 2, AutoMigrate: true,
		AdminUsername: "admin", AdminEmail: "admin@example.test", AdminDisplayName: "Admin", AdminLocale: "zh-CN",
	}
}

func isolatedAppDatabase(t *testing.T) (string, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	adminPool, err := database.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("np_app_settings_it_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("clean up application settings test: %v", err)
		}
	})
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		parsed, err := url.Parse(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("application_name", schema)
		parsed.RawQuery = query.Encode()
		databaseURL = parsed.String()
	} else {
		// The generated schema contains only ASCII letters, digits and '_'.
		databaseURL += " search_path=" + schema + " application_name=" + schema
	}
	return databaseURL, adminPool, schema
}
