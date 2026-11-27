package plugin

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskEpochLifecycle(t *testing.T) {
	m, manifest, _ := businessFixture(t)
	slug := manifest.Slug
	first, err := m.CurrentTaskEpoch(slug)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(m.root)
	same, err := restarted.CurrentTaskEpoch(slug)
	if err != nil || same != first {
		t.Fatal("restart lost authorization lifetime", err)
	}
	_, cfg, _, err := m.Configuration(slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Configure(slug, cfg); err != nil {
		t.Fatal(err)
	}
	changed, err := m.CurrentTaskEpoch(slug)
	if err != nil || changed.Token == first.Token {
		t.Fatal("configuration retained old epoch", err)
	}
	called := false
	if err = m.WithTaskEpoch(slug, first.Token, func() error { called = true; return nil }); !errors.Is(err, ErrInactive) || called {
		t.Fatal("stale epoch allowed a side effect", err)
	}
	if err = m.WithTaskEpoch(slug, changed.Token, func() error { called = true; return nil }); err != nil || !called {
		t.Fatal("current epoch rejected", err)
	}
	if err = m.Configure(slug, cfg); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatal(err)
	}
	afterConflict, err := m.CurrentTaskEpoch(slug)
	if err != nil || afterConflict != changed {
		t.Fatal("rejected configuration rotated epoch", err)
	}
	if err = m.SetEnabled(slug, false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CurrentTaskEpoch(slug); !errors.Is(err, ErrInactive) {
		t.Fatal("disabled epoch available", err)
	}
	if err = m.SetEnabled(slug, true); err != nil {
		t.Fatal(err)
	}
	enabled, err := m.CurrentTaskEpoch(slug)
	if err != nil || enabled.Token == changed.Token {
		t.Fatal("reenable revived old epoch", err)
	}
	if err = m.Uninstall(slug); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CurrentTaskEpoch(slug); !errors.Is(err, ErrInactive) {
		t.Fatal("uninstalled epoch available", err)
	}
}

func TestRejectedOversizedConfigurationKeepsTaskEpoch(t *testing.T) {
	m, manifest, _ := configurationLimitFixture(t, 17)
	before, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := configurationLimitSnapshot(t, m, manifest.Slug)
	candidate := configurationAtBytes(t, config, (64<<10)+1)
	expectConfigurationLimitError(t, m.Configure(manifest.Slug, candidate))
	after, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || before != after {
		t.Fatal("invalid configuration invalidated queued work", err)
	}
}

func TestTaskEpochPersistRotateAndRejectInvalid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := readTaskEpoch(dir); !errors.Is(err, ErrInactive) {
		t.Fatalf("missing epoch: %v", err)
	}
	before := time.Now().UTC()
	if err := rotateTaskEpoch(dir); err != nil {
		t.Fatal(err)
	}
	first, err := readTaskEpoch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Token) != 64 || first.StartedAt.Before(before) {
		t.Fatal("invalid epoch")
	}
	again, err := readTaskEpoch(dir)
	if err != nil || again != first {
		t.Fatal("epoch not durable")
	}
	if err = rotateTaskEpoch(dir); err != nil {
		t.Fatal(err)
	}
	next, err := readTaskEpoch(dir)
	if err != nil || next.Token == first.Token {
		t.Fatal("epoch was not rotated")
	}
	for _, bad := range []TaskEpoch{{Schema: 2, Token: first.Token, StartedAt: first.StartedAt}, {Schema: 1, Token: "bad", StartedAt: first.StartedAt}, {Schema: 1, Token: first.Token}} {
		if err = WritePrivate(filepath.Join(dir, "task-epoch.json"), bad); err != nil {
			t.Fatal(err)
		}
		if _, err = readTaskEpoch(dir); !errors.Is(err, ErrInactive) {
			t.Fatalf("accepted invalid epoch: %v", err)
		}
	}
}
