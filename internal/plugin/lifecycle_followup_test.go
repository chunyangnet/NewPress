package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDamagedManifestKeepsInstallationIdentityForUninstall(t *testing.T) {
	for _, body := range []string{`{broken`, `{"schema":1,"slug":"wrong-directory"}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			m := NewManager(root)
			if err := m.InstallBuiltin(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, CommentOrigin, "plugin.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			items, err := m.List()
			if err != nil || len(items) != 1 || items[0].Slug != CommentOrigin || items[0].Error == "" || items[0].Enabled {
				t.Fatalf("damaged installation: %+v, %v", items, err)
			}
			if err := m.Uninstall(items[0].Slug); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, CommentOrigin)); !os.IsNotExist(err) {
				t.Fatal("damaged installation survived uninstall")
			}
		})
	}
}
