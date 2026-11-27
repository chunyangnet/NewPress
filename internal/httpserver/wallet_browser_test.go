package httpserver

import (
	"fmt"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

// File-backed stores let the browser fixture compare every account, ledger,
// receipt and order before and after intercepted browser mutations.
func walletBrowserStores(t *testing.T, s *Server, owner int64) []string {
	t.Helper()
	directory := t.TempDir()
	coinPath, progressionPath := filepath.Join(directory, "currency.json"), filepath.Join(directory, "progression.json")
	coin, err := virtualcurrency.NewFileStore(coinPath)
	if err != nil {
		t.Fatal(err)
	}
	coinSettings := virtualcurrency.DefaultSettings()
	coinSettings.Enabled, coinSettings.AllowTransfers, coinSettings.CheckInEnabled = true, true, true
	coinSettings.RequireVerifiedEmail, coinSettings.Precision = false, 2
	coinSettings, err = coin.UpdateSettings(t.Context(), 1, coinSettings)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCurrencyStore(coin)
	store, err := progression.NewFileStore(progressionPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := progression.DefaultSettings()
	settings.Experience.Enabled, settings.CheckIn.Enabled, settings.Levels.Enabled, settings.Membership.Enabled = true, true, true, true
	settings.Experience.RequireVerifiedEmail = false
	settings.Membership.Plans[0].PurchaseEnabled, settings.Membership.Plans[0].Price = true, "1.25"
	settings, err = store.UpdateSettings(t.Context(), 1, settings)
	if err != nil {
		t.Fatal(err)
	}
	s.SetProgressionService(&progression.Service{Store: store, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog})
	for i := 0; i < 35; i++ {
		if _, err := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner, UserID: owner, Delta: 100, ExpectedVersion: coinSettings.Version, RequestID: fmt.Sprintf("wallet-browser-credit-%04d", i), Reason: fmt.Sprintf("钱包验收流水 %02d", i)}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Adjust(t.Context(), progression.Adjustment{ActorID: owner, UserID: owner, Delta: 5, ExpectedVersion: settings.Version, RequestID: fmt.Sprintf("wallet-browser-experience-%04d", i), Reason: fmt.Sprintf("成长验收流水 %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return []string{coinPath, progressionPath}
}
