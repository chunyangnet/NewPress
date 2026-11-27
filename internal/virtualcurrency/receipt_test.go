package virtualcurrency

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSystemCreditReceiptIsDurableAndIsolated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "currency.json")
	store, cfg := testCurrency(t, path)
	in := CreditInput{UserID: 12, Amount: 80, ExpectedVersion: cfg.Version, RequestID: "system-refund-receipt-test", Source: "商城退款 so_test"}
	credit, err := store.Credit(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, fp, err := store.Receipt(t.Context(), 0, in.RequestID)
	if err != nil || len(entries) != 1 || entries[0].ID != credit.ID || entries[0].UserID != in.UserID || entries[0].Delta != in.Amount || fp != CreditFingerprint(in.UserID, in.Amount, in.Source) {
		t.Fatalf("system credit receipt cannot be recovered: %+v/%s/%v", entries, fp, err)
	}
	entries, fp, err = store.Receipt(t.Context(), in.UserID, in.RequestID)
	if err != nil || len(entries) != 0 || fp != "" {
		t.Fatalf("system receipt leaked into the user request namespace: %+v/%s/%v", entries, fp, err)
	}
	if _, _, err = store.Receipt(t.Context(), -1, in.RequestID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative receipt actor was accepted: %v", err)
	}
}
