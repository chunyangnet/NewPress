package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/pluginshared"
)

func TestPostgreSQLAppSharedQuarantineAllowsReviewAndRejectsCorruptEvidence(t *testing.T) {
	dsn, _, _ := isolatedAppDatabase(t)
	cfg := postgresAppConfig(t, dsn)
	first, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	pool, err := database.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	id := strings.Repeat("a", 64)
	if _, err = pool.Exec(t.Context(), `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$1,'unprovable',$2)`, id, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	store := database.NewPluginJobRepository(pool)
	page, err := store.InspectSharedRecords(t.Context(), pluginshared.Query{Type: pluginshared.ContentEffect})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	q, err := store.QuarantineSharedRecord(t.Context(), pluginshared.CaptureRequest{Type: pluginshared.ContentEffect, Reference: page.Items[0].Reference, Key: "startup-review", Operator: "offline-fixture", Reason: "retain undecodable evidence"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal("valid unresolved evidence prevented review startup", err)
	}
	second.Close()
	if _, _, err = store.Claim(t.Context(), time.Now().UTC()); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("startup cleared quarantine gate", err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE np_plugin_shared_quarantines SET repaired=true WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	third, err := New(t.Context(), cfg)
	if err == nil {
		third.Close()
		t.Fatal("corrupt quarantine evidence allowed startup")
	}
}
