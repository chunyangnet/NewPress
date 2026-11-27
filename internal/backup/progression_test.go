package backup

import (
	"context"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

type progressionBackupUser struct{}

func (progressionBackupUser) GetUser(_ context.Context, id int64) (auth.User, error) {
	return auth.User{ID: id, Status: auth.UserStatusActive}, nil
}
func TestBackupRestoresMembershipPaymentAndExperience(t *testing.T) {
	f := fixtureSite(t, true)
	catalog, err := virtualcurrency.NewFileCatalog(f.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := progression.NewFileStore(filepath.Join(f.cfg.DataDir, "progression.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := progression.DefaultSettings()
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "1.25"
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := &progression.Service{Store: store, Users: progressionBackupUser{}, Currencies: catalog}
	in := progression.PurchaseInput{UserID: 1, ExpectedVersion: cfg.Version, Plan: "member", Mode: "grant", RequestID: "backup-membership-payment"}
	order, err := service.Purchase(t.Context(), in)
	if err != nil || order.Status != "completed" {
		t.Fatal(order, err)
	}
	adjust := progression.Adjustment{ActorID: 1, UserID: 1, ExpectedVersion: cfg.Version, Delta: 321, RequestID: "backup-experience-adjust", Reason: "备份前调整经验"}
	if _, err = store.Adjust(t.Context(), adjust); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "progression.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-progression"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	data := restored.Environment["NEWPRESS_DATA_DIR"]
	catalog, err = virtualcurrency.NewFileCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	store, err = progression.NewFileStore(filepath.Join(data, "progression.json"))
	if err != nil {
		t.Fatal(err)
	}
	service.Store, service.Currencies = store, catalog
	if err = service.ValidatePayments(t.Context()); err != nil {
		t.Fatal(err)
	}
	replay, err := service.Purchase(t.Context(), in)
	if err != nil || replay.PaymentEntry != order.PaymentEntry || replay.EntryID != order.EntryID {
		t.Fatal(replay, err)
	}
	r, err := store.Adjust(t.Context(), adjust)
	if err != nil || !r.Replayed {
		t.Fatal(r, err)
	}
	v, err := store.Snapshot(t.Context(), 1, 0)
	if err != nil || v.Account.Experience != 321 || v.Account.Membership.Version != 1 {
		t.Fatal(v, err)
	}
}
