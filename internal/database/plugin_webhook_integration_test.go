package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

type webhookFixture struct {
	*contentEffectFixture
	webhooks *PluginWebhookRepository
	req      plugindispatch.Request
}

func newWebhookFixture(t *testing.T) *webhookFixture {
	t.Helper()
	f := newContentEffectFixture(t, false)
	f.control.Revision++
	f.control.Configuration.Version++
	f.control.Manifest.Capabilities = append(f.control.Manifest.Capabilities, "http.send")
	f.control.Manifest.OutboundHosts = []string{"example.com"}
	f.control.Configuration.Grants = append(f.control.Configuration.Grants, "http.send")
	if err := f.controls.SaveTaskControl(f.ctx, f.control.Revision-1, f.control); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(f.control.Manifest)
	r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: f.request.Authorization.Claim, ManifestDigest: plugineffects.Digest(manifest), ModuleDigest: f.control.ModuleDigest}, Slot: "notify", URL: "https://example.com/private-path?secret=hidden", Body: json.RawMessage(`{"message":"secret body","id":9007199254740993}`)}
	w := NewPluginWebhookRepository(f.pool, true)
	if err := w.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	return &webhookFixture{f, w, r}
}

func (f *webhookFixture) success(t *testing.T) {
	t.Helper()
	if err := f.jobs.Finish(f.ctx, f.req.Authorization.Claim, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
func (f *webhookFixture) accept(t *testing.T, slot string) plugindispatch.Accepted {
	t.Helper()
	r := f.req
	r.Slot = slot
	a, err := f.webhooks.Enqueue(f.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func (f *webhookFixture) claim(t *testing.T) plugindispatch.Intent {
	t.Helper()
	i, ok, err := f.webhooks.Claim(f.ctx)
	if err != nil || !ok {
		t.Fatal("claim", ok, err)
	}
	return i
}
func (f *webhookFixture) items(t *testing.T) []plugindispatch.Summary {
	t.Helper()
	p, err := f.webhooks.List(f.ctx, plugindispatch.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}
func (f *webhookFixture) assertNoClaim(t *testing.T) {
	t.Helper()
	_, ok, err := f.webhooks.Claim(f.ctx)
	if err != nil || ok {
		t.Fatal("unexpected send claim", ok, err)
	}
}

func TestPostgreSQLWebhookAdmissionReplayAndSourceSuccess(t *testing.T) {
	f := newWebhookFixture(t)
	if _, err := NewPluginWebhookRepository(f.pool, false).Enqueue(f.ctx, f.req); !errors.Is(err, plugindispatch.ErrUnavailable) {
		t.Fatal("disabled admission", err)
	}
	first := f.accept(t, "notify")
	f.assertNoClaim(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			a, err := f.webhooks.Enqueue(f.ctx, f.req)
			if err != nil || a != first {
				t.Error("concurrent replay", a, err)
			}
		})
	}
	wg.Wait()
	changed := f.req
	changed.Body = json.RawMessage(`{"changed":true}`)
	if _, err := f.webhooks.Enqueue(f.ctx, changed); !errors.Is(err, plugindispatch.ErrConflict) {
		t.Fatal("slot request conflict", err)
	}
	f.success(t)
	// A task archive retains the permanent source fingerprint required to send.
	result, err := f.jobs.Archive(f.ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: time.Now().UTC(), Limit: 100}, time.Now().UTC())
	if err != nil || result.Tasks != 1 {
		t.Fatal("archive", result, err)
	}
	i := f.claim(t)
	if i.ID != first.OperationID || i.State != "sending" {
		t.Fatal("identity changed")
	}
	f.assertNoClaim(t)
	out := plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 202, ResponseDigest: plugineffects.Digest([]byte("accepted")), ResponseBytes: 8}
	if err = f.webhooks.Finish(f.ctx, i, out); err != nil {
		t.Fatal(err)
	}
	if err = f.webhooks.Finish(f.ctx, i, out); err != nil {
		t.Fatal("finish response replay", err)
	}
	f.assertNoClaim(t)
	items := f.items(t)
	raw, _ := json.Marshal(items)
	if len(items) != 1 || items[0].State != "http_confirmed" || strings.Contains(string(raw), "private-path") || strings.Contains(string(raw), "hidden") || strings.Contains(string(raw), "secret body") || strings.Contains(string(raw), i.AttemptToken) || strings.Contains(string(raw), i.Epoch) {
		t.Fatal("public projection leaked private request")
	}
}

func TestPostgreSQLWebhookRetryRetainsSlotWithoutEarlySend(t *testing.T) {
	f := newWebhookFixture(t)
	first := f.accept(t, "notify")
	if err := f.jobs.Finish(f.ctx, f.req.Authorization.Claim, "execution_failed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.assertNoClaim(t)
	time.Sleep(2100 * time.Millisecond)
	claim, ok, err := f.jobs.Claim(f.ctx, time.Now().UTC())
	if err != nil || !ok {
		t.Fatal("reclaim", ok, err)
	}
	// Retry uses the real database clock after the task backoff.
	f.req.Authorization.Claim = claim
	a, err := f.webhooks.Enqueue(f.ctx, f.req)
	if err != nil || a != first {
		t.Fatal("retry changed accepted fact", a, err)
	}
	f.success(t)
	i := f.claim(t)
	if i.ID != first.OperationID {
		t.Fatal("retry changed operation")
	}
}

func TestPostgreSQLWebhookCancellationAuthorityAccountAndHost(t *testing.T) {
	for _, mode := range []string{"cancel", "disable", "epoch", "role", "account", "unlisted", "no_grant"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebhookFixture(t)
			if mode == "unlisted" {
				f.req.URL = "https://different.example/path"
				if _, err := f.webhooks.Enqueue(f.ctx, f.req); !errors.Is(err, pluginjobs.ErrAuthorization) {
					t.Fatal(err)
				}
				return
			}
			f.accept(t, "notify")
			if mode == "cancel" {
				if err := f.jobs.Change(f.ctx, f.control.Slug, f.req.Authorization.Claim.ID, f.req.Authorization.Claim.Version, "cancel", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else {
				f.success(t)
			}
			switch mode {
			case "disable", "epoch", "no_grant":
				c := f.control
				c.Revision++
				if mode == "disable" {
					c.Enabled = false
				}
				if mode == "epoch" {
					c.Epoch, _ = plugin.NewTaskEpoch(f.user.ID)
				}
				if mode == "no_grant" {
					c.Configuration.Grants = []string{"settings.read"}
					c.Configuration.Version++
				}
				if err := f.controls.SaveTaskControl(f.ctx, c.Revision-1, c); err != nil {
					t.Fatal(err)
				}
			case "role":
				f.exec(t, `UPDATE np_roles SET capabilities='["admin:access"]'::jsonb WHERE slug='plugin-editor'`)
			case "account":
				f.exec(t, `UPDATE np_users SET status='suspended' WHERE id=$1`, f.user.ID)
			}
			f.assertNoClaim(t)
			items := f.items(t)
			if len(items) != 1 || items[0].State != "cancelled" {
				t.Fatal(items)
			}
		})
	}
}

func TestPostgreSQLWebhookUncertainReviewAndRevokedFinish(t *testing.T) {
	f := newWebhookFixture(t)
	f.accept(t, "notify")
	f.success(t)
	i := f.claim(t)
	c := f.control
	c.Revision++
	c.Enabled = false
	if err := f.controls.SaveTaskControl(f.ctx, c.Revision-1, c); err != nil {
		t.Fatal(err)
	}
	if err := NewPluginWebhookRepository(f.pool, false).Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err != nil {
		t.Fatal("facts lost after revocation", err)
	}
	r := plugindispatch.ReviewRequest{ID: i.ID, Version: 3, ActorID: f.user.ID, Decision: "not_received", Reason: "接收方核对操作 ID 后未找到记录"}
	w, err := f.webhooks.Review(f.ctx, r)
	if err != nil || w.State != "reviewed_not_received" || w.Outcome.Code != "transport_unconfirmed" {
		t.Fatal(w, err)
	}
	if _, err = f.webhooks.Review(f.ctx, r); err != nil {
		t.Fatal("review acknowledgement replay", err)
	}
	r.Decision = "received"
	if _, err = f.webhooks.Review(f.ctx, r); !errors.Is(err, plugindispatch.ErrConflict) {
		t.Fatal("conflicting review", err)
	}
	f.assertNoClaim(t)
	var audits int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_audit_events WHERE action='plugin.webhook.reviewed'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
	f.exec(t, `UPDATE np_roles SET capabilities='[]'::jsonb WHERE slug='plugin-editor'`)
	r.Decision = "not_received"
	if _, err = f.webhooks.Review(f.ctx, r); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("revoked replay", err)
	}
}

func TestPostgreSQLWebhookActorDoesNotRequireContentWrite(t *testing.T) {
	f := newWebhookFixture(t)
	f.exec(t, `UPDATE np_roles SET capabilities=$1::jsonb WHERE slug='plugin-editor'`, []string{auth.CapabilityAdminAccess, auth.CapabilityPluginsManage})
	f.accept(t, "notify")
	f.success(t)
	f.claim(t)
}

func TestPostgreSQLWebhookAuditFailureRollsBackAndInvalidRowsBlock(t *testing.T) {
	f := newWebhookFixture(t)
	f.exec(t, `CREATE FUNCTION fail_webhook_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'plugin.webhook.%' THEN RAISE EXCEPTION 'injected webhook audit failure'; END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE TRIGGER fail_webhook_audit BEFORE INSERT ON np_audit_events FOR EACH ROW EXECUTE FUNCTION fail_webhook_audit()`)
	if _, err := f.webhooks.Enqueue(f.ctx, f.req); err == nil {
		t.Fatal("admission ignored audit failure")
	}
	f.exec(t, `DROP TRIGGER fail_webhook_audit ON np_audit_events`)
	f.accept(t, "notify")
	f.success(t)
	f.exec(t, `CREATE TRIGGER fail_webhook_audit BEFORE INSERT ON np_audit_events FOR EACH ROW EXECUTE FUNCTION fail_webhook_audit()`)
	if _, _, err := f.webhooks.Claim(f.ctx); err == nil {
		t.Fatal("claim ignored audit failure")
	}
	if got := f.items(t)[0].State; got != "pending" {
		t.Fatal(got)
	}
	f.exec(t, `DROP TRIGGER fail_webhook_audit ON np_audit_events`)
	i := f.claim(t)
	f.exec(t, `CREATE TRIGGER fail_webhook_audit BEFORE INSERT ON np_audit_events FOR EACH ROW EXECUTE FUNCTION fail_webhook_audit()`)
	if err := f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err == nil {
		t.Fatal("finish ignored audit failure")
	}
	if got := f.items(t)[0].State; got != "sending" {
		t.Fatal(got)
	}
	f.exec(t, `DROP TRIGGER fail_webhook_audit ON np_audit_events`)
	if err := f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE np_plugin_webhook_intents SET version=4 WHERE id=$1`, i.ID)
	if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("bad index accepted", err)
	}
	if _, _, err := f.webhooks.Claim(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("bad index not fenced", err)
	}
	if err := f.jobs.CancelRestored(f.ctx, time.Now().UTC()); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("restore accepted corruption", err)
	}
	c, ok, err := f.controls.LoadTaskControl(f.ctx, f.control.Slug)
	if err != nil || !ok || c.Retired {
		t.Fatal("failed restore partially retired authority", err)
	}
}

func TestPostgreSQLWebhookRestorePreservesTerminalAndSuppressesPending(t *testing.T) {
	f := newWebhookFixture(t)
	for _, slot := range []string{"one", "two", "three"} {
		f.accept(t, slot)
	}
	f.success(t)
	confirmed := f.claim(t)
	if err := f.webhooks.Finish(f.ctx, confirmed, plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}); err != nil {
		t.Fatal(err)
	}
	f.claim(t)
	var before []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, confirmed.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, i := range f.items(t) {
		counts[i.State]++
	}
	if counts["cancelled"] != 1 || counts["uncertain"] != 1 || counts["http_confirmed"] != 1 {
		t.Fatal(counts)
	}
	var after []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, confirmed.ID).Scan(&after); err != nil || string(after) != string(before) {
		t.Fatal("terminal bytes changed", err)
	}
	f.assertNoClaim(t)
}

func TestPostgreSQLWebhookClaimSerializesWithRevocation(t *testing.T) {
	f := newWebhookFixture(t)
	f.accept(t, "notify")
	f.success(t)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.ctx, `SELECT id FROM np_users WHERE id=$1 FOR UPDATE`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		found bool
		err   error
	}
	ch := make(chan result, 1)
	go func() { _, found, err := f.webhooks.Claim(f.ctx); ch <- result{found, err} }()
	select {
	case v := <-ch:
		t.Fatal("claim passed locked account", v)
	case <-time.After(120 * time.Millisecond):
	}
	if _, err = tx.Exec(f.ctx, `UPDATE np_users SET status='suspended' WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-ch:
		if v.err != nil || v.found {
			t.Fatal(v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not resume")
	}
	if f.items(t)[0].State != "cancelled" {
		t.Fatal("revoke failed")
	}
}

func TestPostgreSQLWebhookPermanentSlotBudgetAndStrictRows(t *testing.T) {
	f := newWebhookFixture(t)
	for n := 0; n < plugindispatch.MaxSlotsPerTask; n++ {
		f.accept(t, "slot"+fmt.Sprint(n))
	}
	r := f.req
	r.Slot = "overflow"
	if _, err := f.webhooks.Enqueue(f.ctx, r); !errors.Is(err, plugindispatch.ErrCapacity) {
		t.Fatal("slot budget", err)
	}
	r.Slot = "slot0"
	if _, err := f.webhooks.Enqueue(f.ctx, r); err != nil {
		t.Fatal("full budget denied old fact", err)
	}
	var id string
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT id,data FROM np_plugin_webhook_intents ORDER BY id LIMIT 1`).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append([]byte(`{"schema":1,`), raw[1:]...), bytes.Replace(raw, []byte(`"state":"pending"`), []byte(`"state":"uncertain"`), 1)} {
		f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, bad)
		if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
			t.Fatal("bad JSON accepted", err)
		}
		f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, raw)
	}
}

func TestPostgreSQLWebhookSharedQuarantineBlocksNewSendButKeepsResult(t *testing.T) {
	f := newWebhookFixture(t)
	f.accept(t, "first")
	f.accept(t, "second")
	f.success(t)
	i := f.claim(t)
	request, _ := sharedDamage(t, f.contentEffectFixture, pluginshared.Authority)
	if _, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.webhooks.Claim(f.ctx); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("quarantine bypass", err)
	}
	if err := f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err != nil {
		t.Fatal("lost result under quarantine", err)
	}
	if _, err := f.webhooks.Review(f.ctx, plugindispatch.ReviewRequest{ID: i.ID, Version: 3, ActorID: f.user.ID, Decision: "received", Reason: "接收方确认该操作 ID"}); err != nil {
		t.Fatal("review blocked by unrelated damaged authority", err)
	}
}
