package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/pluginshared"
)

func TestPostgreSQLAppWebhookQuarantineAllowsReviewAndRejectsCorruptEvidence(t *testing.T) {
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
	id := strings.Repeat("b", 64)
	if _, err = pool.Exec(t.Context(), `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$1,'unprovable',1,'pending',$2)`, id, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	store := database.NewPluginJobRepository(pool)
	page, err := store.InspectSharedRecords(t.Context(), pluginshared.Query{Type: pluginshared.WebhookIntent})
	if err != nil || len(page.Items) != 1 || page.Items[0].Fault != "invalid_data" {
		t.Fatal(page, err)
	}
	q, err := store.QuarantineSharedRecord(t.Context(), pluginshared.CaptureRequest{Type: pluginshared.WebhookIntent, Reference: page.Items[0].Reference, Key: "webhook-startup", Operator: "offline-fixture", Reason: "retain unprovable original"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal("valid webhook quarantine prevented review startup", err)
	}
	second.Close()
	if _, _, err = database.NewPluginWebhookRepository(pool, true).Claim(t.Context()); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("startup removed the send gate", err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE np_plugin_shared_quarantines SET repaired=true WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	third, err := New(t.Context(), cfg)
	if err == nil {
		third.Close()
		t.Fatal("repaired index hid corrupt webhook evidence at startup")
	}
}
