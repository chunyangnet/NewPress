package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

func TestLocalAppBootstrapsAndReopensAuditedFacts(t *testing.T) {
	themeDir := isolatedThemes(t)
	dataDir := t.TempDir()
	passwordHash, err := auth.HashPassword("App bootstrap password 2026")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Address: "127.0.0.1:0", DataDir: dataDir, ThemesDir: themeDir, PluginsDir: t.TempDir(), ActiveTheme: "chunnel",
		SiteTitle: "Audit test", SiteTagline: "Audit test", PublicURL: "http://127.0.0.1:8080", SeedDemo: true,
		AdminUsername: "admin", AdminPassHash: passwordHash, AdminEmail: "admin@example.test", AdminDisplayName: "Admin", AdminLocale: "zh-CN",
	}
	application, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.serverReadyForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	application.Close()
	contentPath := filepath.Join(dataDir, "content.json")
	if _, err := os.Stat(contentPath); err != nil {
		t.Fatalf("default app did not create durable content: %v", err)
	}
	posts, err := content.NewFileRepository(contentPath, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := posts.Create(context.Background(), content.PostInput{Title: "Survives application restart", Slug: "application-restart", Status: content.StatusPublish, AuthorID: 1, AuthorName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}

	ledgerPath := filepath.Join(dataDir, "audit.jsonl")
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Ready(); err != nil {
		t.Fatalf("ledger after first boot: %v", err)
	}
	actions := make(map[string]bool)
	for _, record := range ledger.Records() {
		if record.Phase == "commit" {
			actions[record.Action] = true
		}
	}
	for _, action := range []string{"comment.seeded", "navigation.seeded", "user.roles.ensure", "user.created"} {
		if !actions[action] {
			t.Fatalf("missing first-boot action %s in %#v", action, actions)
		}
	}
	for _, factKey := range []string{"users", "comments", "navigation"} {
		found := false
		for _, record := range ledger.Records() {
			if record.FactKey == factKey && record.Phase == "commit" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no committed audit record for %s", factKey)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "users.json")); err != nil {
		t.Fatal(err)
	}

	cfg.SiteTitle = strings.Repeat("obsolete bootstrap", 20)
	cfg.SiteTagline = "Changed deployment environment"
	second, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("reopen audited local app: %v", err)
	}
	if got := second.settings.Snapshot().Settings.Identity.Title; got != "Audit test" {
		t.Fatalf("restart applied bootstrap title over saved settings: %q", got)
	}
	second.Close()
	posts, err = content.NewFileRepository(contentPath, true)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := posts.GetPublishedBySlug(context.Background(), created.Slug)
	if err != nil || restored.ID != created.ID || restored.Title != created.Title {
		t.Fatalf("application restart replaced local content with seed: %#v, %v", restored, err)
	}
	reloaded, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Ready(); err != nil {
		t.Fatalf("ledger after reopen: %v", err)
	}
	commentsPath := filepath.Join(dataDir, "comments.json")
	payload, err := os.ReadFile(commentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commentsPath, append(payload, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "audit fact does not match") {
		t.Fatalf("tampered local fact startup error = %v", err)
	}
}

func TestLocalAppAnchorsValidatedLegacyFacts(t *testing.T) {
	dataDir := t.TempDir()
	usersPath := filepath.Join(dataDir, "users.json")
	users, err := auth.NewFileUserStore(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.EnsureRoles(context.Background(), auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	if _, err := comment.NewFileStore(filepath.Join(dataDir, "comments.json"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := navigation.NewFileStore(filepath.Join(dataDir, "navigation.json"), true); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Address: "127.0.0.1:0", DataDir: dataDir, ThemesDir: isolatedThemes(t),
		PluginsDir: t.TempDir(), ActiveTheme: "chunnel", SiteTitle: "Legacy audit test", SiteTagline: "Legacy audit test",
		PublicURL: "http://127.0.0.1:8080", AdminUsername: "admin", AdminEmail: "admin@example.test", AdminDisplayName: "Admin", AdminLocale: "zh-CN",
	}
	application, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	application.Close()
	ledger, err := audit.Open(filepath.Join(dataDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	baselines := make(map[string]bool)
	for _, record := range ledger.Records() {
		if record.Phase == "commit" && record.Action == "audit.fact.baseline" {
			baselines[record.FactKey] = true
		}
	}
	for _, factKey := range []string{"users", "comments", "navigation"} {
		if !baselines[factKey] {
			t.Fatalf("missing legacy baseline for %s: %#v", factKey, baselines)
		}
	}
}

func isolatedThemes(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve app test path")
	}
	directory := t.TempDir()
	source := filepath.Join(filepath.Dir(sourceFile), "..", "..", "themes", "chunnel")
	if err := os.CopyFS(filepath.Join(directory, "chunnel"), os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return directory
}

// serverReadyForTest keeps the app test independent of the HTTP handler while
// checking every durable store's readiness contract.
func (a *App) serverReadyForTest(ctx context.Context) error {
	if err := a.comments.Ready(ctx); err != nil {
		return err
	}
	if err := a.navigation.Ready(ctx); err != nil {
		return err
	}
	return nil
}
