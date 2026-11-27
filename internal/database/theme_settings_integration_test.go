package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/theme"
)

func TestPostgreSQLThemeSettingsRepositoryCAS(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("theme-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM np_theme_settings WHERE theme_slug=$1`, slug); err != nil {
			t.Errorf("clean theme settings fixture: %v", err)
		}
	})
	repository := NewThemeSettingsRepository(pool)
	if _, version, found, err := repository.Load(ctx, slug); err != nil || found || version != 0 {
		t.Fatal("empty repository", version, found, err)
	}
	first := []byte(fmt.Sprintf(`{"schema":1,"theme":%q,"theme_version":"1.0.0","values":{"headline":"first"}}`, slug))
	var created atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			ok, err := repository.Create(ctx, slug, first)
			if err != nil {
				t.Errorf("concurrent create: %v", err)
			}
			if ok {
				created.Add(1)
			}
		})
	}
	group.Wait()
	if created.Load() != 1 {
		t.Fatalf("initialization winners = %d", created.Load())
	}
	payload, version, found, err := repository.Load(ctx, slug)
	if err != nil || !found || version != 1 || !sameJSON(payload, first) {
		t.Fatal("initial settings", string(payload), version, found, err)
	}
	second := []byte(fmt.Sprintf(`{"schema":1,"theme":%q,"theme_version":"1.0.0","values":{"headline":"second"}}`, slug))
	var updated atomic.Int32
	for range 8 {
		group.Go(func() {
			ok, err := repository.CompareAndSwap(ctx, slug, 1, second)
			if err != nil {
				t.Errorf("concurrent save: %v", err)
			}
			if ok {
				updated.Add(1)
			}
		})
	}
	group.Wait()
	if updated.Load() != 1 {
		t.Fatalf("save winners = %d", updated.Load())
	}
	payload, version, found, err = repository.Load(ctx, slug)
	if err != nil || !found || version != 2 || !sameJSON(payload, second) {
		t.Fatal("updated settings", string(payload), version, found, err)
	}
	if ok, err := repository.CompareAndSwap(ctx, slug, 1, first); err != nil || ok {
		t.Fatal("stale version overwrote settings", ok, err)
	}
	for _, invalid := range []string{
		`{}`,
		fmt.Sprintf(`{"schema":1,"theme":%q}`, slug),
		fmt.Sprintf(`{"schema":"1","theme":%q,"values":{}}`, slug),
		`{"schema":1,"theme":"wrong-theme","values":{}}`,
	} {
		if ok, err := repository.CompareAndSwap(ctx, slug, 2, []byte(invalid)); err == nil || ok {
			t.Fatalf("invalid settings metadata accepted: %s, saved=%v, err=%v", invalid, ok, err)
		}
	}
	payload, version, _, err = repository.Load(ctx, slug)
	if err != nil || version != 2 || !sameJSON(payload, second) {
		t.Fatal("rejected document changed committed settings", version, err)
	}
}

func TestPostgreSQLThemeSettingsSizeBoundarySurvivesRestart(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("theme-size-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM np_theme_settings WHERE theme_slug=$1`, slug); err != nil {
			t.Errorf("clean theme settings fixture: %v", err)
		}
	})
	values := map[string]any{"large_number": float64(1e300)}
	properties := map[string]any{"large_number": map[string]any{"type": "number", "default": 0}}
	for i := range 64 {
		key := fmt.Sprintf("field_%02d", i)
		values[key] = ""
		properties[key] = map[string]any{"type": "string", "default": ""}
	}
	document := map[string]any{"schema": 1, "theme": slug, "theme_version": "1.0.0", "updated_at": "2026-11-19T00:00:00Z", "values": values}
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 256 << 10
	remaining := limit - 128 - len(payload)
	for i := range 64 {
		size := min(remaining, 4096)
		values[fmt.Sprintf("field_%02d", i)] = strings.Repeat("x", size)
		remaining -= size
	}
	if remaining != 0 {
		t.Fatal("fixture could not fill the document size budget")
	}
	payload, _ = json.Marshal(document)
	repository := NewThemeSettingsRepository(pool)
	if created, err := repository.Create(ctx, slug, payload); err != nil || !created {
		t.Fatal(created, err)
	}
	var databaseBytes, expandedBytes int
	if err := pool.QueryRow(ctx, `SELECT octet_length(document::text),octet_length(document::jsonb::text) FROM np_theme_settings WHERE theme_slug=$1`, slug).Scan(&databaseBytes, &expandedBytes); err != nil {
		t.Fatal(err)
	}
	if len(payload) >= limit || databaseBytes != len(payload) || expandedBytes <= limit {
		t.Fatalf("encoding boundary: original=%d database=%d jsonb=%d limit=%d", len(payload), databaseBytes, expandedBytes, limit)
	}
	manifest := theme.Manifest{Schema: 1, Slug: slug, Name: "Size boundary", Version: "1.0.0", Engine: "go-template/v1", View: theme.PublicViewV1, NewPress: ">=0.1.0", Templates: map[string]string{"home": "home.html", "single": "single.html", "page": "page.html"}, SettingsSchema: "settings.schema.json"}
	if err := theme.ValidateManifest(manifest); err != nil {
		t.Fatal("invalid size-boundary theme fixture", err)
	}
	manifestJSON, _ := json.Marshal(manifest)
	schemaJSON, _ := json.Marshal(map[string]any{"type": "object", "properties": properties})
	root := t.TempDir()
	for path, contents := range map[string][]byte{
		"theme.json":            manifestJSON,
		"settings.schema.json":  schemaJSON,
		"templates/base.html":   []byte(`{{define "base"}}{{template "content" .}}{{end}}`),
		"templates/home.html":   []byte(`{{define "content"}}home{{end}}`),
		"templates/single.html": []byte(`{{define "content"}}single{{end}}`),
		"templates/page.html":   []byte(`{{define "content"}}page{{end}}`),
		"assets/style.css":      []byte("body{}"),
	} {
		fullPath := filepath.Join(root, slug, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := theme.NewManager(root, slug, theme.WithSettingsRepository(repository))
	if err := manager.Load(); err != nil {
		t.Fatal("valid near-limit settings could not load from PostgreSQL", err)
	}
	saved, fields, err := manager.SaveSettings(manager.SettingsSnapshot().Values)
	if err != nil || len(fields) > 0 {
		t.Fatal("near-limit settings could not save", fields, err)
	}
	restarted := theme.NewManager(root, slug, theme.WithSettingsRepository(repository))
	if err := restarted.Load(); err != nil {
		t.Fatal("near-limit settings could not restart after saving", err)
	}
	got := restarted.SettingsSnapshot()
	if got.Revision != saved.Revision || got.Values["large_number"] != float64(1e300) {
		t.Fatal("restart changed settings values")
	}
}
