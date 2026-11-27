package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDatabaseWithoutSiteInitialization(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "uncreated")
	secret := filepath.Join(root, "database-url")
	if err := os.WriteFile(secret, []byte("postgres://fixture/db\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", secret)
	t.Setenv("NEWPRESS_DB_MIN_CONNS", "1")
	t.Setenv("NEWPRESS_DB_MAX_CONNS", "16")
	t.Setenv("NEWPRESS_DATA_DIR", directory)
	t.Setenv("NEWPRESS_THEMES_DIR", filepath.Join(directory, "themes"))
	t.Setenv("NEWPRESS_PLUGINS_DIR", filepath.Join(directory, "plugins"))
	t.Setenv("NEWPRESS_SMTP_PASSWORD_FILE", filepath.Join(root, "absent-smtp-secret"))
	// An explicitly empty direct setting suppresses a secret file.
	if cfg, err := LoadDatabase(); err != nil || cfg.DatabaseURL != "" || cfg.DBMinConns != 1 || cfg.DBMaxConns != 16 {
		t.Fatal(cfg, err)
	}
	if err := os.Unsetenv("NEWPRESS_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadDatabase(); err != nil || cfg.DatabaseURL != "postgres://fixture/db" {
		t.Fatal(cfg, err)
	}
	t.Setenv("NEWPRESS_DATABASE_URL", "postgres://direct/db")
	if cfg, err := LoadDatabase(); err != nil || cfg.DatabaseURL != "postgres://direct/db" {
		t.Fatal(cfg, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("database configuration created a site directory", err)
	}
	for _, limits := range [][2]string{{"0", "16"}, {"2", "1"}, {"1", "0"}, {"1", "65"}, {"bad", "16"}, {"1", "bad"}} {
		t.Setenv("NEWPRESS_DB_MIN_CONNS", limits[0])
		t.Setenv("NEWPRESS_DB_MAX_CONNS", limits[1])
		if _, err := LoadDatabase(); err == nil {
			t.Fatal("invalid pool limits accepted", limits)
		}
	}
}
