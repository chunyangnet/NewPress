package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/licensing"
)

func TestBundledInstallsKeepLicenseNotices(t *testing.T) {
	for _, manifest := range BundledManifests() {
		t.Run(manifest.Slug, func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(root)
			if err := manager.InstallBundled(manifest.Slug); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"LICENSE":                licensing.Apache20,
				"THIRD_PARTY_NOTICES.md": licensing.HostPluginNotices,
				"flag-icons-MIT.txt":     licensing.FlagIconsMIT,
			} {
				got, err := os.ReadFile(filepath.Join(root, manifest.Slug, name))
				if err != nil || string(got) != want {
					t.Fatalf("installed %s lacks its complete %s: %v", manifest.Slug, name, err)
				}
			}
			items, err := manager.List()
			if err != nil || len(items) != 1 || items[0].Enabled || items[0].Error != "" {
				t.Fatalf("license files broke disabled install: %+v, %v", items, err)
			}
		})
	}
}

func TestCommentOriginRejectsRetiredDescriptor(t *testing.T) {
	old := BuiltinManifest()
	old.Version = "1.0.0"
	if ValidateManifest(old) == nil {
		t.Fatal("retired host descriptor accepted")
	}
}
