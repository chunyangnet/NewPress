package database

import (
	"testing"

	"newpress.local/newpress/internal/virtualcurrency"
)

func TestPostgreSQLSystemCreditReceiptIsIsolated(t *testing.T) {
	store, cfg, ids := currencyPostgres(t)
	in := virtualcurrency.CreditInput{UserID: ids[0], Amount: 80, ExpectedVersion: cfg.Version, RequestID: "shop-system-refund-receipt", Source: "商城退款 so_test"}
	credit, err := store.Credit(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	entries, fp, err := store.Receipt(t.Context(), 0, in.RequestID)
	if err != nil || len(entries) != 1 || entries[0].ID != credit.ID || entries[0].Delta != in.Amount || fp != virtualcurrency.CreditFingerprint(in.UserID, in.Amount, in.Source) {
		t.Fatalf("system credit receipt could not be recovered: %+v/%s/%v", entries, fp, err)
	}
	entries, fp, err = store.Receipt(t.Context(), in.UserID, in.RequestID)
	if err != nil || len(entries) != 0 || fp != "" {
		t.Fatalf("system credit leaked into the user's request namespace: %+v/%s/%v", entries, fp, err)
	}
}
