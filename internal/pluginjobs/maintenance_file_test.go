package pluginjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMaintenanceFixture(t *testing.T, records ...StoredRecord) (*FileMaintenance, string) {
	t.Helper()
	fields := map[string]json.RawMessage{"schema": []byte("3"), "kind": []byte(`"newpress.pluginjobs"`)}
	for kind, name := range maintenanceMapNames {
		values := map[string]json.RawMessage{}
		for _, r := range records {
			if r.Type == kind {
				values[r.Key] = r.Data
			}
		}
		fields[name] = writeRawObject(values)
	}
	path := filepath.Join(t.TempDir(), "plugin-jobs.json")
	if err := os.WriteFile(path, writeRawObject(fields), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := NewFileMaintenance(path)
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func fileDiagnostic(t *testing.T, m *FileMaintenance, reference string) RecordDiagnostic {
	t.Helper()
	page, err := m.InspectRecords(t.Context(), DiagnosticQuery{})
	if err != nil || page.Fatal != "" {
		t.Fatal(page, err)
	}
	for _, item := range page.Items {
		if item.Reference == reference {
			return item
		}
	}
	t.Fatal("record reference was not diagnosed", reference)
	return RecordDiagnostic{}
}

func TestFileMaintenancePartialIsolationPreservesOpaqueRecordsAndConfirmations(t *testing.T) {
	original, in, now := maintenanceTask(t)
	bad := CloneStoredRecord(original)
	bad.Data = append(append([]byte{}, original.Data[:len(original.Data)-1]...), []byte(`, "unexpected":"<private>"}`)...)
	second := CloneStoredRecord(original)
	second.Key = strings.Repeat("b", 64)
	second.Data = bytes.Replace(original.Data, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	second.Data = bytes.Replace(second.Data, []byte(`"id":"`+original.Key+`"`), []byte(`"id":"`+second.Key+`"`), 1)
	// This second identity cannot be proved; it must not enable healthy claims.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, second.Data, "", "  "); err != nil {
		t.Fatal(err)
	}
	second.Data = pretty.Bytes()
	m, path := writeMaintenanceFixture(t, bad, second)
	before, _ := os.ReadFile(path)
	page, err := m.InspectRecords(t.Context(), DiagnosticQuery{Limit: 1})
	if err != nil || page.Total != 2 || page.NextOffset != 1 || page.SnapshotDigest != hash(before) {
		t.Fatal(page, err)
	}
	request := QuarantineRequest{Reference: RecordReference(bad), Type: bad.Type, Key: "isolation-command-1", Operator: "operator", Reason: "unknown field checked", SnapshotDigest: page.SnapshotDigest}
	first, err := m.QuarantineRecord(t.Context(), request, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	e, err := decodeMaintenanceEnvelope(body)
	if err != nil || !bytes.Equal(e.maps[RecordTask][second.Key], second.Data) || !bytes.Equal(e.quarantines[first.ID].Original.Data, bad.Data) {
		t.Fatal("untouched record/evidence was normalized or lost", err)
	}
	if _, err = NewFileBackend(path); err == nil {
		t.Fatal("runtime opened a file with an unselected damaged record")
	}
	confirmed, err := m.QuarantineRecord(t.Context(), request, now.Add(2*time.Second))
	afterRetry, _ := os.ReadFile(path)
	if err != nil || confirmed.ID != first.ID || !bytes.Equal(body, afterRetry) {
		t.Fatal("lost-response confirmation rewrote evidence", confirmed, err)
	}
	changed := request
	changed.Reason = "different audit reason"
	if _, err = m.QuarantineRecord(t.Context(), changed, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("request key was reused with different audit input", err)
	}
	secondRequest := QuarantineRequest{Reference: RecordReference(second), Type: RecordTask, Key: "isolation-command-2", Operator: "operator", Reason: "unprovable ID requires backup", SnapshotDigest: hash(before)}
	if _, err = m.QuarantineRecord(t.Context(), secondRequest, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale file snapshot was overwritten", err)
	}
	secondRequest.SnapshotDigest = hash(body)
	if _, err = m.QuarantineRecord(t.Context(), secondRequest, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	backend, err := NewFileBackend(path)
	if err != nil {
		t.Fatal("fully isolated file could not reopen", err)
	}
	queue := &Queue{Backend: backend}
	if _, err = queue.Enqueue(t.Context(), in, now.Add(3*time.Second)); !errors.Is(err, ErrQuarantined) {
		t.Fatal("isolated original was accepted as a new task", err)
	}
	if _, _, err = queue.Claim(t.Context(), now.Add(3*time.Second)); !errors.Is(err, ErrQuarantined) {
		t.Fatal("unprovable task identity did not fence claims", err)
	}
	repair := RepairRequest{ID: first.ID, Operator: "operator", Reason: "matching record backup", Replacement: original.Data, ReplacementDigest: hash(original.Data)}
	result, err := m.RepairRecord(t.Context(), repair, now.Add(4*time.Second))
	if err != nil || result.Repair == nil || result.Repair.Disposition != "cancelled" {
		t.Fatal(result, err)
	}
	body, _ = os.ReadFile(path)
	repair.SnapshotDigest = hash(before) // accepted confirmations precede new CAS
	if _, err = m.RepairRecord(t.Context(), repair, now.Add(5*time.Second)); err != nil {
		t.Fatal("repair confirmation lost after snapshot changed", err)
	}
	afterRetry, _ = os.ReadFile(path)
	if !bytes.Equal(body, afterRetry) {
		t.Fatal("repeat repair changed state or audit")
	}
	backend, err = NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := backend.Read(t.Context())
	if state.Schema != 4 || state.Jobs[original.Key].Status != "cancelled" || !bytes.Equal(state.Quarantines[first.ID].Original.Data, bad.Data) {
		t.Fatal("repair lost original evidence or resumed work")
	}
}

func TestFileMaintenanceDuplicateTerminalReconciliationFencesOldClaim(t *testing.T) {
	_, in, now := maintenanceTask(t)
	b, _ := NewFileBackend("")
	q := &Queue{Backend: b}
	original, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := q.Claim(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(claim)
	live := StoredRecord{Type: RecordTask, Key: original.ID, Data: raw}
	alias := StoredRecord{Type: RecordTask, Key: strings.Repeat("c", 64), Data: raw}
	m, path := writeMaintenanceFixture(t, live, alias)
	if diagnostic := fileDiagnostic(t, m, RecordReference(alias)); diagnostic.Fault != "duplicate_identity" {
		t.Fatal(diagnostic)
	}
	isolated, err := m.QuarantineRecord(t.Context(), QuarantineRequest{Reference: RecordReference(alias), Type: RecordTask, Key: "duplicate-1", Operator: "operator", Reason: "duplicate running identity"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	b, err = NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q = &Queue{Backend: b}
	if err = q.CheckClaim(t.Context(), claim, now.Add(time.Second)); !errors.Is(err, ErrAuthorization) {
		t.Fatal("old claim was authorized through the intact duplicate", err)
	}
	if err = q.Finish(t.Context(), claim, "", now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("quarantined identity was marked successful", err)
	}
	if _, found, err := q.Claim(t.Context(), now.Add(LeaseDuration)); err != nil || found {
		t.Fatal("quarantined alias was reclaimed", found, err)
	}
	repair := RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "explicit terminal survivor"}
	if _, err = m.RepairRecord(t.Context(), repair, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("repair overwrote existing identity", err)
	}
	if err = q.Change(t.Context(), in.Plugin, claim.ID, claim.Version, "cancel", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := m.InspectRecords(t.Context(), DiagnosticQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Task.Status != "cancelled" {
		t.Fatal(page, err)
	}
	repair.KeepReference = page.Items[0].Reference
	result, err := m.RepairRecord(t.Context(), repair, now.Add(3*time.Second))
	if err != nil || result.Repair == nil || result.Repair.Disposition != "reconciled_terminal" {
		t.Fatal(result, err)
	}
	body, _ := os.ReadFile(path)
	if _, err = m.RepairRecord(t.Context(), repair, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(body, after) {
		t.Fatal("terminal reconciliation was repeated")
	}
	b, err = NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q = &Queue{Backend: b}
	in.Key = "independent-new-task"
	if _, err = q.Enqueue(t.Context(), in, now.Add(4*time.Second)); err != nil {
		t.Fatal("reconciled isolation kept admission blocked", err)
	}
	if err = q.CheckClaim(t.Context(), claim, now.Add(4*time.Second)); !errors.Is(err, ErrAuthorization) {
		t.Fatal("repair reauthorized the old claim", err)
	}
}

func TestFileMaintenanceRejectsAmbiguousEnvelopeAndHealthyIsolation(t *testing.T) {
	original, _, now := maintenanceTask(t)
	m, path := writeMaintenanceFixture(t, original)
	request := QuarantineRequest{Reference: RecordReference(original), Type: original.Type, Key: "command", Operator: "operator", Reason: "checked"}
	if _, err := m.QuarantineRecord(t.Context(), request, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("healthy record was isolated without a diagnostic fault", err)
	}
	for _, body := range []string{
		`{"schema":4,"schema":3,"kind":"newpress.pluginjobs","jobs":{}}`,
		`{"schema":5,"kind":"newpress.pluginjobs","jobs":{}}`,
		`{"schema":4,"kind":"newpress.pluginjobs","jobs":{"\ud800":{}}}`,
		`{"schema":4,"kind":"newpress.pluginjobs","jobs":null}`,
		`{"schema":4,"kind":"newpress.pluginjobs","jobs":{},"unexpected":1}`,
		`{"schema":4,"kind":"newpress.pluginjobs","jobs":{"a":{}}`,
	} {
		t.Run(hash([]byte(body))[:8], func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			page, err := m.InspectRecords(t.Context(), DiagnosticQuery{})
			if err != nil || page.Fatal != "invalid_envelope" || len(page.Items) != 0 {
				t.Fatal(page, err)
			}
			if _, err = m.QuarantineRecord(t.Context(), request, now); !errors.Is(err, ErrMaintenanceEnvelope) {
				t.Fatal("ambiguous envelope was mutated", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != body {
				t.Fatal("fatal envelope was rewritten")
			}
		})
	}
}

func TestFileMaintenanceRepeatedCorruptionUsesNewCommandAndPreservesBothAudits(t *testing.T) {
	original, _, now := maintenanceTask(t)
	bad := CloneStoredRecord(original)
	bad.Key = strings.Repeat("c", 64)
	m, path := writeMaintenanceFixture(t, bad)
	request := QuarantineRequest{Reference: RecordReference(bad), Type: RecordTask, Key: "first-event", Operator: "operator", Reason: "wrong map key"}
	first, err := m.QuarantineRecord(t.Context(), request, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.RepairRecord(t.Context(), RepairRequest{ID: first.ID, Operator: "operator", Reason: "index rebuilt from JSON"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	e, _ := decodeMaintenanceEnvelope(body)
	// Recreate the exact original damaged bytes while retaining the repaired
	// terminal record. This is a distinct corruption event with its own audit.
	e.maps[RecordTask][bad.Key] = bad.Data
	body, err = e.encode()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	confirmed, err := m.QuarantineRecord(t.Context(), request, now.Add(3*time.Second))
	if err != nil || confirmed.ID != first.ID || confirmed.Repair == nil {
		t.Fatal("old command was applied to a new corruption occurrence", confirmed, err)
	}
	request.Key = "second-event"
	second, err := m.QuarantineRecord(t.Context(), request, now.Add(3*time.Second))
	if err != nil || second.ID == first.ID {
		t.Fatal("new corruption overwrote earlier evidence", second, err)
	}
	page, err := m.ListQuarantines(t.Context(), DiagnosticQuery{Limit: 1})
	if err != nil || page.Total != 2 || page.NextOffset != 1 {
		t.Fatal(page, err)
	}
}

func TestMaintenanceFileWhitespaceDoesNotBecomePostgreSQLRowDamage(t *testing.T) {
	original, _, _ := maintenanceTask(t)
	original.Data = append([]byte(strings.Repeat(" ", 70<<10)), original.Data...)
	if d := DiagnoseStoredRecord(original); d.Fault != "" {
		t.Fatal("file whitespace diagnosed against SQL row limit", d)
	}
	original.Metadata = []byte(`{}`)
	if d := DiagnoseStoredRecord(original); d.Fault != "invalid_data" {
		t.Fatal("SQL raw byte limit was silently compacted away", d)
	}
}

func TestFileMaintenanceRepairsAllRecordClassesWithoutResumingWork(t *testing.T) {
	for _, kind := range []RecordType{RecordTask, RecordTaskHistory, RecordFanout, RecordFanoutHistory} {
		t.Run(string(kind), func(t *testing.T) {
			original, _, now := maintenanceTask(t)
			record := StoredRecord{Type: kind, Key: "incorrect-map-key"}
			var expectedHistory ArchivedJob
			switch kind {
			case RecordTask:
				record.Data = original.Data
			case RecordTaskHistory:
				value, _ := decodeRecord(RecordTask, original.Data)
				j := *value.task
				j.Status, j.Version = "cancelled", j.Version+1
				var err error
				expectedHistory, err = ArchiveJob(j, now.Add(time.Second))
				if err != nil {
					t.Fatal(err)
				}
				record.Data, _ = json.Marshal(expectedHistory)
			default:
				f := FanoutReceipt{Event: Event{Key: "empty-cohort-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}, Subscriptions: []Subscription{}}
				if kind == RecordFanoutHistory {
					f.Delivered = true
					record.Data, _ = json.Marshal(ArchivedFanout{Receipt: f, ArchivedAt: now.Add(time.Second)})
				} else {
					record.Data, _ = json.Marshal(f)
				}
			}
			m, path := writeMaintenanceFixture(t, record)
			d := fileDiagnostic(t, m, RecordReference(record))
			if d.Fault != "index_mismatch" {
				t.Fatal(d)
			}
			isolated, err := m.QuarantineRecord(t.Context(), QuarantineRequest{Reference: d.Reference, Type: kind, Key: "isolation", Operator: "operator", Reason: "map key checked"}, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.RepairRecord(t.Context(), RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "JSON authority retained"}, now.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			b, err := NewFileBackend(path)
			if err != nil {
				t.Fatal(err)
			}
			state, _ := b.Read(t.Context())
			if !bytes.Equal(state.Quarantines[isolated.ID].Original.Data, record.Data) || state.Quarantines[isolated.ID].Repair == nil {
				t.Fatal("original evidence or repair audit was lost")
			}
			switch kind {
			case RecordTask:
				if state.Jobs[original.Key].Status != "cancelled" {
					t.Fatal("task index repair resumed work")
				}
			case RecordTaskHistory:
				actual, _ := json.Marshal(state.ArchivedJobs[original.Key])
				expected, _ := json.Marshal(expectedHistory)
				if !bytes.Equal(actual, expected) {
					t.Fatal("history index repair rewrote terminal facts")
				}
			case RecordFanout:
				f := state.Fanouts["empty-cohort-event"]
				if !f.Cancelled || f.Delivered || len(f.Subscriptions) != 0 {
					t.Fatal("empty frozen receipt was resumed or marked delivered")
				}
			case RecordFanoutHistory:
				f := state.ArchivedFanouts["empty-cohort-event"].Receipt
				if !f.Cancelled || !f.Delivered || len(f.Subscriptions) != 0 {
					t.Fatal("completed empty cohort fact was changed")
				}
			}
		})
	}
}

func TestFileMaintenancePreservesDuplicatePayloadAndInvalidUnicodeEvidence(t *testing.T) {
	original, _, now := maintenanceTask(t)
	for _, payload := range [][]byte{[]byte(`{"duplicate":1,"duplicate":2}`), append([]byte(`{"private":"`), 0xff, '"', '}')} {
		t.Run(hash(payload)[:8], func(t *testing.T) {
			bad := CloneStoredRecord(original)
			bad.Data = bytes.Replace(original.Data, []byte(`{"private":"do not expose"}`), payload, 1)
			m, path := writeMaintenanceFixture(t, bad)
			d := fileDiagnostic(t, m, RecordReference(bad))
			if d.Fault != "invalid_data" {
				t.Fatal(d)
			}
			isolated, err := m.QuarantineRecord(t.Context(), QuarantineRequest{Reference: d.Reference, Type: RecordTask, Key: "opaque-evidence", Operator: "operator", Reason: "damaged payload encoding"}, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			b, err := NewFileBackend(path)
			if err != nil {
				t.Fatal("isolated binary evidence was rejected as executable JSON", err)
			}
			state, _ := b.Read(t.Context())
			if !bytes.Equal(state.Quarantines[isolated.ID].Original.Data, bad.Data) {
				t.Fatal("damaged duplicate/Unicode bytes were repaired during capture")
			}
			if _, err = m.RepairRecord(t.Context(), RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "checksum-confirmed original parameters", Replacement: original.Data, ReplacementDigest: hash(original.Data)}, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaintenanceDiagnosticKeepsRawPayloadLimitAndQuarantineAuditReservation(t *testing.T) {
	original, _, now := maintenanceTask(t)
	oversizedPayload := append([]byte(`{"private":`), []byte(strings.Repeat(" ", MaxPayloadBytes))...)
	oversizedPayload = append(oversizedPayload, []byte(`"do not expose"}`)...)
	oversized := bytes.Replace(original.Data, []byte(`{"private":"do not expose"}`), oversizedPayload, 1)
	if d := DiagnoseStoredRecord(StoredRecord{Type: RecordTask, Key: original.Key, Data: oversized}); d.Fault != "invalid_data" {
		t.Fatal("diagnostic compacted away an oversized raw payload", d)
	}
	records := map[string]QuarantinedRecord{}
	for i := range 3 {
		r := StoredRecord{Type: RecordFanout, Key: "private", Data: bytes.Repeat([]byte("x"), 16<<20)}
		q, err := CaptureQuarantine(r, "invalid_data", QuarantineRequest{Reference: RecordReference(r), Type: r.Type, Key: string(rune('a' + i)), Operator: "operator", Reason: "evidence budget"}, now)
		if err != nil {
			t.Fatal(err)
		}
		records[q.ID] = q
		if size, err := QuarantineRequiredBytes(records); err != nil || (i < 2) != (size <= MaxQuarantineBytes) {
			t.Fatal("evidence byte boundary or audit reservation was not enforced", i, size, err)
		}
	}
	state := InitialState()
	state.Quarantines = records
	if err := checkStorageSize(state, false); !errors.Is(err, ErrCapacity) {
		t.Fatal("oversized evidence entered normal runtime state", err)
	}
}
