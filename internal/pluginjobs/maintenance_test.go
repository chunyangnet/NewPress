package pluginjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func maintenanceTask(t *testing.T) (StoredRecord, Input, time.Time) {
	t.Helper()
	b, err := NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{Backend: b}
	now := time.Now().UTC().Add(-time.Minute)
	in := Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "maintenance-original", ActorID: 7, Payload: json.RawMessage(`{"private":"do not expose"}`)}
	j, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(j)
	return StoredRecord{Type: RecordTask, Key: j.ID, Data: raw}, in, now
}

func TestMaintenanceJSONObjectPreservesDamagedValuesAndRejectsAmbiguousKeys(t *testing.T) {
	for _, input := range []string{`{"jobs":{"a":{"payload":{"duplicate":1,"duplicate":2}}},"schema":3}`, `{"id":"abc", "kind":"task.execute.v1"}`} {
		fields, err := readJSONObject([]byte(input))
		if err != nil || len(fields) != 2 {
			t.Fatal("opaque record values rejected", len(fields), err)
		}
	}
	for _, input := range []string{`{"jobs":{},"jobs":{}}`, `{"\ud800":1}`, `{"a":1} {}`, `{"a":`, `[]`} {
		if _, err := readJSONObject([]byte(input)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ambiguous envelope accepted: %q, %v", input, err)
		}
	}
	fields, err := readJSONObject([]byte(`{"ID":"one","id":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readField[string](fields, "id", true); !errors.Is(err, ErrInvalid) {
		t.Fatal("folded identity aliases collapsed", err)
	}
}

func TestMaintenanceDiagnosticRedactionAndOpaqueEvidence(t *testing.T) {
	record, _, now := maintenanceTask(t)
	record.Data = bytes.Replace(record.Data, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	diagnostic := DiagnoseStoredRecord(record)
	if diagnostic.Fault != "invalid_data" || diagnostic.Reference != RecordReference(record) {
		t.Fatal(diagnostic)
	}
	request := QuarantineRequest{Reference: diagnostic.Reference, Type: record.Type, Operator: " local-admin ", Reason: " checked corruption "}
	quarantine, err := CaptureQuarantine(record, diagnostic.Fault, request, now.Add(time.Second))
	if err != nil || !bytes.Equal(quarantine.Original.Data, record.Data) || quarantine.Operator != "local-admin" {
		t.Fatal("original evidence lost", quarantine, err)
	}
	encoded, _ := json.Marshal(SummarizeQuarantine(quarantine))
	for _, private := range []string{"do not expose", "maintenance-original", "actor_id", "fingerprint", "metadata", "payload", "epoch"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("private evidence exposed by maintenance summary", private)
		}
	}
	clone := CloneQuarantine(quarantine)
	clone.Original.Data[0] = '!'
	if bytes.Equal(clone.Original.Data, quarantine.Original.Data) {
		t.Fatal("quarantine clone retained writable data alias")
	}
}

func TestMaintenanceRepairPreservesIdentityAndCancelsWork(t *testing.T) {
	original, in, now := maintenanceTask(t)
	damaged := CloneStoredRecord(original)
	damaged.Data = bytes.Replace(damaged.Data, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	request := QuarantineRequest{Reference: RecordReference(damaged), Type: damaged.Type, Operator: "operator", Reason: "isolated bad status"}
	quarantine, err := CaptureQuarantine(damaged, "invalid_data", request, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	repaired, audit, err := BuildRepairedRecord(quarantine, RepairRequest{ID: quarantine.ID, Operator: "operator", Reason: "checked matching backup",
		Replacement: original.Data, ReplacementDigest: hash(original.Data)}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRecord(repaired.Type, repaired.Data)
	if err != nil || decoded.task.ID != original.Key || decoded.task.Status != "cancelled" || decoded.task.Version != 2 || decoded.task.LeaseToken != "" || audit.Disposition != "cancelled" {
		t.Fatal("repair resumed or changed task identity", decoded, audit, err)
	}
	if decoded.task.ActorID != in.ActorID || !bytes.Equal(decoded.task.Payload, in.Payload) {
		t.Fatal("repair changed original parameters or execution account")
	}
	changed := *decoded.task
	changed.Payload = json.RawMessage(`{"private":"replacement parameters"}`)
	_, changed.Fingerprint, _ = identity(Input{Plugin: changed.Plugin, Epoch: changed.Epoch, Key: changed.Key, Kind: changed.Kind, ActorID: changed.ActorID, Payload: changed.Payload})
	changedData, _ := json.Marshal(changed)
	if _, _, err = BuildRepairedRecord(quarantine, RepairRequest{ID: quarantine.ID, Operator: "operator", Reason: "different input", Replacement: changedData, ReplacementDigest: hash(changedData)}, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("repair admitted different parameters under an existing identity", err)
	}
	if _, _, err = BuildRepairedRecord(quarantine, RepairRequest{ID: quarantine.ID, Operator: "operator", Reason: "incorrect checksum", Replacement: original.Data, ReplacementDigest: strings.Repeat("0", 64)}, now.Add(2*time.Second)); err == nil {
		t.Fatal("unchecked replacement accepted")
	}
}

func TestMaintenanceIndexRepairAndAdmissionGates(t *testing.T) {
	original, _, now := maintenanceTask(t)
	index, _ := json.Marshal(RecordIndex{Plugin: "wrong-reader", Status: "pending", AvailableAt: &now, UpdatedAt: now})
	original.Metadata = index
	diagnostic := DiagnoseStoredRecord(original)
	if diagnostic.Fault != "index_mismatch" {
		t.Fatal(diagnostic)
	}
	quarantine, err := CaptureQuarantine(original, diagnostic.Fault, QuarantineRequest{Reference: diagnostic.Reference, Type: original.Type, Operator: "operator", Reason: "incorrect index"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]QuarantinedRecord{quarantine.ID: quarantine}
	if !errors.Is(QuarantineAdmission(items, true), ErrQuarantined) || QuarantineAdmission(items, false) != nil {
		t.Fatal("admission gate did not preserve record class")
	}
	blocked, err := QuarantineWorkPolicy(items)
	if err != nil || !blocked[original.Key] {
		t.Fatal("original claim identity was not fenced", blocked, err)
	}
	_, audit, err := BuildRepairedRecord(quarantine, RepairRequest{ID: quarantine.ID, Operator: "operator", Reason: "JSON authority checked"}, now.Add(2*time.Second))
	if err != nil || audit.Disposition != "cancelled" {
		t.Fatal("intact JSON index repair failed", audit, err)
	}
}

func TestMaintenanceFanoutRepairPreservesFrozenCohortAndCompletion(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	original := FanoutReceipt{Event: Event{Key: "event-private-key", Kind: "content.updated", ObjectID: 1, Version: 2, At: now},
		Subscriptions: []Subscription{{Plugin: "reader", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 7}}, Delivered: true}
	raw, _ := json.Marshal(original)
	damaged := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":"private"}`)...)
	record := StoredRecord{Type: RecordFanout, Key: original.Event.Key, Data: damaged}
	q, err := CaptureQuarantine(record, "invalid_data", QuarantineRequest{Reference: RecordReference(record), Type: record.Type, Operator: "operator", Reason: "unknown field"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	out, audit, err := BuildRepairedRecord(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: "matching frozen backup", Replacement: raw, ReplacementDigest: hash(raw)}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRecord(out.Type, out.Data)
	if err != nil || !decoded.fanout.Cancelled || !decoded.fanout.Delivered || audit.Disposition != "fanout_cancelled" {
		t.Fatal("repair lost completion or resumed fanout", decoded, audit, err)
	}
	original.Subscriptions = []Subscription{{Plugin: "other-reader", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 8}}
	changed, _ := json.Marshal(original)
	if _, _, err = BuildRepairedRecord(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: "different recipients", Replacement: changed, ReplacementDigest: hash(changed)}, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("repair rediscovered recipients", err)
	}
}

func TestMaintenanceRepairPreservesReissueProvenanceAndRejectsAmbiguousReview(t *testing.T) {
	_, in, now := maintenanceTask(t)
	b, _ := NewFileBackend("")
	q := &Queue{Backend: b}
	source, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	details, _ := q.GetReview(t.Context(), in.Plugin, source.ID)
	in.Key = "reviewed-maintenance-command"
	in.Payload = json.RawMessage(`{"private":"explicit new parameters"}`)
	request := ReissueRequest{SourceID: source.ID, SourceVersion: details.Task.Version, Input: in, Reason: "original review reason", AllowNew: true}
	child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := b.Read(t.Context())
	original := state.Jobs[child.ID]
	raw, _ := json.Marshal(original)
	damaged := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":"damage"}`)...)
	record := StoredRecord{Type: RecordTask, Key: child.ID, Data: damaged}
	isolated, err := CaptureQuarantine(record, "invalid_data", QuarantineRequest{Reference: RecordReference(record), Type: record.Type, Key: "reviewed-isolation", Operator: "operator", Reason: "unexpected field"}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	newRecord, _, err := BuildRepairedRecord(isolated, RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "matching original reviewed command", Replacement: raw, ReplacementDigest: hash(raw)}, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := decodeRecord(newRecord.Type, newRecord.Data)
	if value.task.Status != "cancelled" || !reflect.DeepEqual(value.task.Review, original.Review) {
		t.Fatal("repair changed reviewed source facts or resumed child")
	}
	request.Reason = "different reviewed command"
	changed, err := BuildReissuedJob(request, details.Task, state.Jobs[source.ID].Fingerprint, original.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	changedRaw, _ := json.Marshal(changed)
	if _, _, err = BuildRepairedRecord(isolated, RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "changed review", Replacement: changedRaw, ReplacementDigest: hash(changedRaw)}, now.Add(4*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("repair changed private review under the same task identity", err)
	}
	ambiguous := bytes.Replace(damaged, []byte(`"reason":"original review reason"`), []byte(`"reason":"original review reason","REASON":"original review reason"`), 1)
	record.Data = ambiguous
	isolated, err = CaptureQuarantine(record, "invalid_data", QuarantineRequest{Reference: RecordReference(record), Type: record.Type, Key: "ambiguous-reviewed-isolation", Operator: "operator", Reason: "ambiguous review aliases"}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = BuildRepairedRecord(isolated, RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "cannot prove review", Replacement: raw, ReplacementDigest: hash(raw)}, now.Add(4*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous private review was silently collapsed", err)
	}
}

func TestMaintenanceIncompleteFanoutAliasRequiresReconciliationBeforeDelivery(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	f := FanoutReceipt{Event: Event{Key: "frozen-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}, Subscriptions: []Subscription{}}
	raw, _ := json.Marshal(f)
	r := StoredRecord{Type: RecordFanout, Key: "misindexed-event", Data: raw}
	isolated, err := CaptureQuarantine(r, "index_mismatch", QuarantineRequest{Reference: RecordReference(r), Type: r.Type, Key: "fanout-alias", Operator: "operator", Reason: "event alias checked"}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewFileBackend("")
	if err = b.Update(t.Context(), func(s *State) error {
		s.Fanouts = map[string]FanoutReceipt{f.Event.Key: f}
		s.Quarantines = map[string]QuarantinedRecord{isolated.ID: isolated}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	q := &Queue{Backend: b}
	if _, _, err = q.FindFanout(t.Context(), f.Event); !errors.Is(err, ErrQuarantined) {
		t.Fatal("incomplete receipt initiated work through a quarantined alias", err)
	}
	if _, err = q.FreezeFanout(t.Context(), f.Event, nil); !errors.Is(err, ErrQuarantined) {
		t.Fatal("freeze replay bypassed incomplete receipt fence", err)
	}
	if err = q.CompleteFanout(t.Context(), f.Event); !errors.Is(err, ErrQuarantined) {
		t.Fatal("completion was fabricated while evidence remained unresolved", err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if confirmed, err := q.FreezeFanout(t.Context(), f.Event, nil); err != nil || !confirmed.Cancelled || confirmed.Delivered {
		t.Fatal("completed/cancelled confirmation did not preserve frozen facts", confirmed, err)
	}
}
