package pluginshared

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

func webhookRecordFixture(t *testing.T, state string) (Record, plugindispatch.Intent) {
	t.Helper()
	r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("a", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)}, Slot: "secret-slot", URL: "https://example.com/private?token=hidden", Body: json.RawMessage(`{"private":"retained body","id":9007199254740993}`)}
	id, fp, body, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	i := plugindispatch.Intent{Schema: 1, ID: id, JobID: r.Authorization.Claim.ID, SourceFingerprint: strings.Repeat("d", 64), Plugin: "test-webhook", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: 7, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: r.Slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: "pending", CreatedAt: at, UpdatedAt: at}
	if state == "cancelled" {
		i.Version, i.State, i.Outcome = 2, state, plugindispatch.Outcome{Code: "source_cancelled"}
		i.UpdatedAt = at.Add(time.Second)
	} else if state != "pending" {
		i.Version, i.State = 2, "sending"
		i.AttemptAt, i.UpdatedAt = at.Add(time.Second), at.Add(time.Second)
		i.AttemptUntil, i.AttemptToken = i.AttemptAt.Add(plugindispatch.AttemptDuration), strings.Repeat("f", 64)
		if state != "sending" {
			i.Version, i.State = 3, state
			i.UpdatedAt, i.ResultAt = at.Add(2*time.Second), at.Add(2*time.Second)
			i.Outcome = plugindispatch.Outcome{Code: "transport_unconfirmed"}
			if state == "http_confirmed" {
				i.Outcome = plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}
			}
			if strings.HasPrefix(state, "reviewed_") {
				i.Version, i.UpdatedAt = 4, at.Add(3*time.Second)
				i.Review = &plugindispatch.Review{Decision: strings.TrimPrefix(state, "reviewed_"), ActorID: 7, Reason: "verified receiver record", At: i.UpdatedAt}
			}
		}
	}
	if err = plugindispatch.Validate(i); err != nil {
		t.Fatal(state, err)
	}
	raw, _ := json.Marshal(i)
	index, _ := json.Marshal(Index{JobID: i.JobID, Slot: i.Slot, Version: i.Version, State: i.State})
	return Record{Type: WebhookIntent, Key: i.ID, Data: raw, Index: index}, i
}

func TestSharedWebhookRepairNeverRevivesAndRetainsTerminalBytes(t *testing.T) {
	for _, state := range []string{"pending", "sending", "cancelled", "uncertain", "http_confirmed", "reviewed_received", "reviewed_not_received"} {
		t.Run(state, func(t *testing.T) {
			r, old := webhookRecordFixture(t, state)
			q := isolateFixture(t, r)
			request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "verified complete request and attempt"}
			out, audit, err := BuildRepair(q, request, time.Now().UTC())
			if err != nil || Diagnose(out).Fault != "" {
				t.Fatal(audit, err)
			}
			var current plugindispatch.Intent
			if json.Unmarshal(out.Data, &current) != nil || plugindispatch.Validate(current) != nil {
				t.Fatal("invalid repair")
			}
			if current.ID != old.ID || current.Fingerprint != old.Fingerprint || current.URL != old.URL || !bytes.Equal(current.Body, old.Body) || current.AttemptToken != old.AttemptToken || current.Accepted() != old.Accepted() {
				t.Fatal("request, acceptance or attempt facts changed")
			}
			if state == "pending" {
				if current.State != "cancelled" || current.Outcome.Code != "maintenance" || audit.Disposition != "webhook_cancelled" {
					t.Fatal(current.State, audit)
				}
			} else if state == "sending" {
				if current.State != "uncertain" || current.Outcome.Code != "maintenance" || audit.Disposition != "webhook_uncertain" {
					t.Fatal(current.State, audit)
				}
			} else if !bytes.Equal(out.Data, r.Data) || audit.Disposition != "webhook_preserved" {
				t.Fatal("terminal result or original bytes changed")
			}
			q.Repair = &audit
			if ValidateQuarantine(q) != nil || MatchRepair(q, request) != nil {
				t.Fatal("repair audit cannot be confirmed")
			}
			q.Repair.Disposition = "receipt_preserved"
			if ValidateQuarantine(q) == nil {
				t.Fatal("content audit accepted for a webhook")
			}
		})
	}
}

func TestSharedWebhookDiagnosticPrivacyAndDuplicateEvidence(t *testing.T) {
	r, _ := webhookRecordFixture(t, "reviewed_not_received")
	q := isolateFixture(t, r)
	d := DiagnoseWithQuarantines([]Record{r}, []Quarantine{q})
	if len(d) != 1 || d[0].Fault != "duplicate_identity" || d[0].Webhook == nil || d[0].Webhook.State != "reviewed_not_received" || d[0].Webhook.Outcome.Code != "transport_unconfirmed" {
		t.Fatal(d)
	}
	raw, _ := json.Marshal(PaginateDiagnostics(d, Query{Type: WebhookIntent, Limit: 10}))
	for _, secret := range []string{"private", "secret-slot", "retained body", "verified receiver record", "fingerprint", "attempt_token", "actor_id", "epoch", "manifest_digest", "module_digest", "url", "slot"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private field exposed", secret)
		}
	}
	if bad := Diagnose(Record{Type: WebhookIntent, Key: r.Key, Index: r.Index, Data: []byte(`{`)}); bad.Fault != "invalid_data" || bad.Plugin != "" || bad.JobID != "" || bad.Webhook != nil {
		t.Fatal("guessed domain facts from SQL", bad)
	}
}

func TestSharedWebhookRepairCannotInventPayloadOrResult(t *testing.T) {
	r, original := webhookRecordFixture(t, "uncertain")
	r.Data = bytes.Replace(r.Data, []byte(`{"schema":1`), []byte(`{"unknown":true,"schema":1`), 1)
	q := isolateFixture(t, r)
	clean, _ := json.Marshal(original)
	request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "remove unknown field only", Replacement: clean, ReplacementDigest: plugineffects.Digest(clean)}
	if _, _, err := BuildRepair(q, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*plugindispatch.Intent){
		func(i *plugindispatch.Intent) {
			i.Outcome = plugindispatch.Outcome{Code: "worker_expired"}
			i.ResultAt = i.AttemptUntil
			i.UpdatedAt = i.ResultAt
		},
		func(i *plugindispatch.Intent) { i.ActorID++ },
		func(i *plugindispatch.Intent) { i.AttemptToken = strings.Repeat("1", 64) },
		func(i *plugindispatch.Intent) {
			i.URL = "https://example.com/different"
			request := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: i.JobID}, ManifestDigest: i.ManifestDigest, ModuleDigest: i.ModuleDigest}, Slot: i.Slot, URL: i.URL, Body: i.Body}
			_, i.Fingerprint, _, _ = request.Identity()
		},
	} {
		candidate := original
		change(&candidate)
		if err := plugindispatch.Validate(candidate); err != nil {
			t.Fatal("fixture must be internally valid", err)
		}
		request.Replacement, _ = json.Marshal(candidate)
		request.ReplacementDigest = plugineffects.Digest(request.Replacement)
		if _, _, err := BuildRepair(q, request, time.Now().UTC()); err == nil {
			t.Fatal("different provable facts accepted")
		}
	}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`{"schema":1,"schema":1}`), bytes.Replace(clean, []byte(`"state":"uncertain"`), []byte(`"state":"uncertain","STATE":"uncertain"`), 1)} {
		bad := r
		bad.Data = raw
		isolated := isolateFixture(t, bad)
		request.ID, request.Replacement, request.ReplacementDigest = isolated.ID, clean, plugineffects.Digest(clean)
		if _, _, err := BuildRepair(isolated, request, time.Now().UTC()); err == nil {
			t.Fatal("ambiguous/missing source facts invented")
		}
	}
}

func TestSharedWebhookDuplicateRequiresSuppressedSurvivor(t *testing.T) {
	for _, state := range []string{"pending", "sending", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			r, _ := webhookRecordFixture(t, state)
			q := isolateFixture(t, r)
			request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "same operation checked", KeepReference: Reference(r)}
			if state != "uncertain" {
				if _, err := Reconcile(q, request, r, time.Now().UTC()); err == nil {
					t.Fatal("active survivor permitted")
				}
			}
			fixed, _, err := BuildRepair(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: "stop old operation"}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			request.KeepReference = Reference(fixed)
			audit, err := Reconcile(q, request, fixed, time.Now().UTC())
			if err != nil || audit.Disposition != "duplicate_webhook_preserved" {
				t.Fatal(audit, err)
			}
			q.Repair = &audit
			if ValidateQuarantine(q) != nil {
				t.Fatal("duplicate repair audit invalid")
			}
			if err := ValidateRepairTarget(fixed, []Record{r}); err == nil {
				t.Fatal("existing logical identity overwritten")
			}
			if !reflect.DeepEqual(q.Original.Data, r.Data) {
				t.Fatal("quarantine changed original")
			}
		})
	}
}
