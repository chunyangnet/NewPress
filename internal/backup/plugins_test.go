package backup

import (
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/plugin"
)

func TestBackupRestoresOfficialPluginSettingsAndPrivateRecords(t *testing.T) {
	f := fixtureSite(t, false)
	m := plugin.NewManager(f.cfg.PluginsDir)
	if err := m.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	origin := commentorigin.New(m)
	settings, _ := origin.Settings()
	settings.Provider = "baidu"
	settings.Key = "backup-test-secret"
	if _, err := origin.Save(settings, false); err != nil {
		t.Fatal(err)
	}
	if err := origin.Capture(123, "8.8.8.8", "Windows NT 10.0 Chrome/135"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "official-plugin.zip")
	if _, err := Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-official-plugin"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	manager := plugin.NewManager(restored.Environment["NEWPRESS_PLUGINS_DIR"])
	service := commentorigin.New(manager)
	config, err := service.Settings()
	if err != nil || config.Key != settings.Key || config.Provider != "baidu" {
		t.Fatal("plugin settings not restored", err)
	}
	record, err := service.PrivateRecord(123)
	if err != nil || record.IP != "8.8.8.8" || record.Device.OS != "Windows" {
		t.Fatal("plugin record not restored", err)
	}
	if err = manager.Uninstall(plugin.CommentOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PrivateRecord(123); err == nil {
		t.Fatal("private source survived uninstall")
	}
	if original, err := origin.PrivateRecord(123); err != nil || original.IP != "8.8.8.8" {
		t.Fatal("restored uninstall changed source backup", err)
	}
}
