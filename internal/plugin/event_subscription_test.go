package plugin

import (
	"path/filepath"
	"testing"
)

func TestEventSubscriptionRequiresExplicitHostConsent(t *testing.T) {
	_, manifest, code := businessFixture(t)
	manifest.Hooks["event.deliver.v1"] = []string{"np_handle"}
	m := NewManager(t.TempDir())
	if _, err := m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(manifest.Slug, defaultConfiguration(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	epoch, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || epoch.ActorID != 0 {
		t.Fatal("enabled without consent", err)
	}
	_, cfg, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ConfigureAs(manifest.Slug, cfg, 42); err != nil {
		t.Fatal(err)
	}
	subscribed, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || subscribed.ActorID != 42 || subscribed.Token == epoch.Token {
		t.Fatal("consent not bound", err)
	}
	if err = m.ConfigureAs(manifest.Slug, cfg, 99); err != ErrConfigurationConflict {
		t.Fatal("stale consent", err)
	}
	after, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || after != subscribed {
		t.Fatal("failed save changed consent", err)
	}
	_, cfg, _, err = m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	after, err = m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || after.ActorID != 0 {
		t.Fatal("consent not revoked", err)
	}
	_, cfg, _, err = m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ConfigureAs(manifest.Slug, cfg, 42); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(manifest.Slug, false); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	after, err = m.CurrentTaskEpoch(manifest.Slug)
	if err != nil || after.ActorID != 0 {
		t.Fatal("reenable revived consent", err)
	}
}
