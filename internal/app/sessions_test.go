package app

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
)

func TestAppSessionsSurviveRestartButNotRevocation(t *testing.T) {
	cfg := config.Config{
		Address: "127.0.0.1:0", DataDir: t.TempDir(), ThemesDir: isolatedThemes(t), PluginsDir: t.TempDir(),
		ActiveTheme: "chunnel", SiteTitle: "Session restart", PublicURL: "http://localhost:8080",
	}
	app, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
	now := time.Now().UTC().Truncate(time.Second)
	r := auth.SessionRecord{ID: key("opaque token"), Username: "test-owner", DisplayName: "test-owner",
		CredentialDigest: key("credential"), CSRFToken: key("csrf"),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	p := auth.SessionPolicy{Now: now, IdleTTL: time.Hour, MaxActive: 3, MaxPerIdentity: 2}
	if err := app.sessions.Create(t.Context(), r, p); err != nil {
		app.Close()
		t.Fatal(err)
	}
	if err := app.sessions.Touch(t.Context(), r.ID, now.Add(time.Second), time.Hour); err != nil {
		app.Close()
		t.Fatal(err)
	}
	app.Close()
	app, err = New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := app.sessions.Get(t.Context(), r.ID)
	if err != nil || !loaded.LastSeenAt.Equal(now.Add(time.Second)) || loaded.CSRFToken != r.CSRFToken {
		app.Close()
		t.Fatalf("app restart lost the flushed session: %#v, %v", loaded, err)
	}
	if err := app.sessions.Delete(t.Context(), r.ID); err != nil {
		app.Close()
		t.Fatal(err)
	}
	app.Close()
	app, err = New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := app.sessions.Get(t.Context(), r.ID); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("app restart resurrected a revoked login: %v", err)
	}
	if _, err := auth.NewFileSessionStore(filepath.Join(cfg.DataDir, auth.SessionDirectory, "sessions.json")); err != nil {
		t.Fatalf("private session snapshot cannot be reopened: %v", err)
	}
}
