package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/payment"
)

func TestCommerceBackupRestoresEncryptedConfigAndRequiresOriginalKey(t *testing.T) {
	f := fixtureSite(t, false)
	keyPath := filepath.Join(f.cfg.DataDir, "payment-secrets.key")
	vault, err := commerce.OpenVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	provider := payment.Config{Provider: "epay", Endpoint: "https://payments.example.test", MerchantID: "fixture-merchant", Secret: "commerce-backup-fixture-secret"}
	sealed, err := vault.Seal(provider)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := commerce.NewFileBackend(filepath.Join(f.cfg.DataDir, "commerce.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.Update(t.Context(), func(s *commerce.State) error {
		s.Settings.Channels = []commerce.Channel{{Key: "fixture", Name: "备份渠道", Provider: provider.Provider, Currency: "CNY", SealedConfig: sealed}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(f.cfg.DataDir, "commerce.json"))
	if err != nil || bytes.Contains(body, []byte(provider.Secret)) {
		t.Fatal("merchant secret must only exist in encrypted configuration", err)
	}
	originalKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "commerce.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-commerce"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	data := restored.Environment["NEWPRESS_DATA_DIR"]
	restoredKey := filepath.Join(data, "payment-secrets.key")
	key, err := os.ReadFile(restoredKey)
	if err != nil || !bytes.Equal(key, originalKey) {
		t.Fatal("backup must restore the original payment encryption key", err)
	}
	backend, err = commerce.NewFileBackend(filepath.Join(data, "commerce.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Settings.Channels) != 1 {
		t.Fatal("payment configuration was not restored", err)
	}
	vault, err = commerce.OpenVault(restoredKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := vault.Open(state.Settings.Channels[0].SealedConfig)
	if err != nil || got != provider {
		t.Fatal("restored channel must decrypt with the restored key", err)
	}

	// Losing a key must stop startup, including for a disabled historical
	// channel. A new random key would make its existing ciphertext unrecoverable.
	if err = os.Rename(restoredKey, restoredKey+".saved"); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.DataDir = data
	cfg.ThemesDir = restored.Environment["NEWPRESS_THEMES_DIR"]
	cfg.PluginsDir = restored.Environment["NEWPRESS_PLUGINS_DIR"]
	application, err := app.New(t.Context(), cfg)
	if application != nil {
		application.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "payment encryption key is missing") {
		t.Fatalf("startup with historical payment config but no original key: %v", err)
	}
	if _, err = os.Stat(restoredKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup must not silently generate a replacement key", err)
	}
	if err = os.Rename(restoredKey+".saved", restoredKey); err != nil {
		t.Fatal(err)
	}
}
