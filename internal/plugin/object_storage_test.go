package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestObjectStorageDistributionMatchesRegisteredRuntime(t *testing.T) {
	source := filepath.Join("..", "..", "examples", "plugins", ObjectStorage)
	manifest, err := readManifest(filepath.Join(source, "plugin.json"))
	if err != nil || !reflect.DeepEqual(manifest, ObjectStorageManifest()) {
		t.Fatalf("distribution descriptor differs from registered host plugin: %v", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := probeEntry(t.Context(), filepath.Join(source, manifest.Entry), manifest); err != nil {
		t.Fatal(err)
	}
	altered := manifest
	altered.Capabilities = []string{"media.storage"}
	if ValidateManifest(altered) == nil {
		t.Fatal("unregistered host descriptor accepted")
	}
	m := NewManager(t.TempDir())
	if err := m.InstallBundled(ObjectStorage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, ObjectStorage, ".disabled")); err != nil {
		t.Fatal("new install must be disabled", err)
	}
	if err := m.SetEnabled(ObjectStorage, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(ObjectStorage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, ObjectStorage)); !os.IsNotExist(err) {
		t.Fatal("uninstall left active plugin directory", err)
	}
}
