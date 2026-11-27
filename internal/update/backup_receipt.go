package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

// DatabaseBackup binds an operator's restore receipt to the actual deployment.
func DatabaseBackup(required bool, site string) func(*Manager) {
	return func(m *Manager) { m.databaseBackup = required; m.siteURL = site }
}

type backupReceipt struct {
	Schema   int       `json:"schema"`
	Site     string    `json:"site"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Captured time.Time `json:"captured_at"`
	Verified time.Time `json:"restore_verified_at"`
	Database string    `json:"database_snapshot"`
	Files    string    `json:"site_files_snapshot"`
}

func verifyBackupReceipt(site, from, to string) error {
	invalid := errors.New("请由运维核对 PostgreSQL 和站点文件的恢复演练，并提供 24 小时内与本次升级匹配的备份回执")
	name := os.Getenv("NEWPRESS_UPDATE_BACKUP_RECEIPT_FILE")
	if !filepath.IsAbs(name) {
		return invalid
	}
	f, _, err := packagefs.OpenRegularNoSymlink(name)
	if err != nil {
		return invalid
	}
	defer f.Close()
	r, err := packagefs.DecodeJSON[backupReceipt](f, 4096, "backup receipt")
	now := time.Now()
	if err != nil || r.Schema != 1 || site == "" || r.Site != site || r.From != from || r.To != to || strings.TrimSpace(r.Database) == "" || strings.TrimSpace(r.Files) == "" || r.Verified.Before(r.Captured) || r.Verified.After(now) || r.Captured.Before(now.Add(-24*time.Hour)) {
		return invalid
	}
	return nil
}
