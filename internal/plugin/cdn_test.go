package plugin

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCDNDistributionDescriptor(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "plugins", CDN)
	m, e := readManifest(filepath.Join(dir, "plugin.json"))
	if e != nil || !reflect.DeepEqual(m, CDNManifest()) {
		t.Fatal("CDN descriptor differs from host", e)
	}
	if e = probeEntry(t.Context(), filepath.Join(dir, "builtin.json"), m); e != nil {
		t.Fatal(e)
	}
}

func TestCDNRegisteredHostLifecycle(t *testing.T) {
	m := NewManager(t.TempDir())
	manifest := CDNManifest()
	if e := ValidateManifest(manifest); e != nil {
		t.Fatal(e)
	}
	altered := manifest
	altered.Runtime = "builtin/unreviewed"
	if ValidateManifest(altered) == nil {
		t.Fatal("unreviewed runtime accepted")
	}
	if e := m.InstallBundled(CDN); e != nil {
		t.Fatal(e)
	}
	if m.WithData(CDN, true, 0, func(string, uint64) error { return nil }) == nil {
		t.Fatal("install enabled by default")
	}
	if e := m.SetEnabled(CDN, true); e != nil {
		t.Fatal(e)
	}
	var g uint64
	_ = m.WithData(CDN, true, 0, func(_ string, v uint64) error { g = v; return nil })
	lease, e := m.ActiveContext(CDN, g)
	if e != nil {
		t.Fatal(e)
	}
	page, e := SettingsPageFor(manifest)
	if e != nil || page.Adapter != CDN || len(page.Groups) != 6 {
		t.Fatal(page, e)
	}
	if e = m.Uninstall(CDN); e != nil {
		t.Fatal(e)
	}
	if lease.Err() != context.Canceled {
		t.Fatal("lifecycle lease not revoked")
	}
}

func TestCDNRejectsRetiredDescriptor(t *testing.T) {
	old := CDNManifest()
	old.Version, old.NewPress, old.Runtime = "1.0.0", "builtin/cdn-v1", "builtin/cdn-v1"
	if ValidateManifest(old) == nil {
		t.Fatal("retired CDN descriptor accepted")
	}
}
