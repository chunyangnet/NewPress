package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func snippetRepairFixture(t *testing.T) (snippetRawBundle, SnippetRepairDocument) {
	t.Helper()
	a := content.SnippetAccess{Identity: "legacy:private-maintenance-identity", Actor: "private-maintenance-actor", Read: true, Manage: true}
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var s content.SnippetRecord
	var err error
	for i := range 3 {
		s, err = content.NextSnippetRecord(s, 7, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "private-maintenance-title", ContentSource: "<p>private-maintenance-body " + strings.Repeat("x", i+1) + "</p>", Visibility: content.SnippetShared}, a, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt := content.SnippetReceipt{Identity: a.Identity, RequestKey: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), SnippetID: s.ID, SnippetVersion: s.Version, TargetVersion: s.CurrentVersion, CreatedAt: now.Add(3 * time.Second), Items: []content.SnippetSelection{{ID: 1, Version: 2}}}
	doc := SnippetRepairDocument{Schema: 1, Kind: "newpress.snippet-repair", Snippet: &s, Receipts: []content.SnippetReceipt{receipt}}
	b := snippetRawBundle{Target: SnippetMaintenanceTarget{SnippetID: s.ID}}
	raw, _ := json.Marshal(s.Snippet)
	b.Metadata = &snippetRawRow{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Version: s.Version, Visibility: s.Visibility}, Data: raw}
	for _, rev := range s.Revisions {
		raw, _ = json.Marshal(rev)
		b.Revisions = append(b.Revisions, snippetRawRow{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Version: rev.Version}, Data: raw})
	}
	raw, _ = json.Marshal(receipt)
	b.Receipts = []snippetRawRow{{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Identity: receipt.Identity, Key: receipt.RequestKey}, Data: raw}}
	return b, doc
}

func TestSnippetRepairPreservesHistoryReceiptsAndForcesWithdrawal(t *testing.T) {
	b, doc := snippetRepairFixture(t)
	b.Revisions[0].Data = []byte(`{`)
	before := b.reference()
	review, err := reviewSnippetBundle(b, 0, false)
	if err != nil || review.Healthy {
		t.Fatal(review, err)
	}
	result, err := buildSnippetRepair(b, doc, time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if b.reference() != before || result.Metadata.Index.Version != 4 || result.Metadata.Index.Visibility != content.SnippetWithdrawn {
		t.Fatal("repair altered evidence or revived sharing")
	}
	var meta content.Snippet
	if err = json.Unmarshal(result.Metadata.Data, &meta); err != nil || meta.SharedSince != 0 || meta.CurrentVersion != 3 {
		t.Fatal(meta, err)
	}
	if len(result.Revisions) != 3 || len(result.Receipts) != 1 || !bytes.Equal(b.Revisions[1].Data, result.Revisions[1].Data) || !bytes.Equal(b.Receipts[0].Data, result.Receipts[0].Data) {
		t.Fatal("repair changed valid historical bytes")
	}
	encoded, _ := json.Marshal(review)
	for _, secret := range []string{"private-maintenance-title", "private-maintenance-body", "private-maintenance-actor", doc.Receipts[0].Identity, doc.Receipts[0].RequestKey} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("review leaked private record data")
		}
	}
}

func TestSnippetRepairRejectsHistoryAndReceiptErasureOrChanges(t *testing.T) {
	for _, kind := range []string{"drop history", "change history", "drop receipt", "change receipt", "wrong snippet", "version exhausted"} {
		t.Run(kind, func(t *testing.T) {
			b, doc := snippetRepairFixture(t)
			switch kind {
			case "drop history":
				doc.Snippet.Revisions = doc.Snippet.Revisions[1:]
				doc.Snippet.SharedSince = 2
			case "change history":
				doc.Snippet.Revisions[0].ContentSource = "<p>changed</p>"
				doc.Snippet.Revisions[0].ContentHTML = "<p>changed</p>"
			case "drop receipt":
				doc.Receipts = nil
			case "change receipt":
				doc.Receipts[0].Fingerprint = strings.Repeat("c", 64)
			case "wrong snippet":
				doc.Snippet.ID++
			case "version exhausted":
				b.Metadata.Index.Version = math.MaxInt64
			}
			if _, err := buildSnippetRepair(b, doc, time.Now().UTC()); err == nil {
				t.Fatal("unsafe replacement accepted")
			}
		})
	}
	b, doc := snippetRepairFixture(t)
	var meta content.Snippet
	if err := json.Unmarshal(b.Metadata.Data, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Version = 1000
	b.Metadata.Data, _ = json.Marshal(meta)
	b.Metadata.Index.Version = 900
	result, err := buildSnippetRepair(b, doc, time.Now().UTC())
	if err != nil || result.Metadata.Index.Version != 1001 {
		t.Fatal("management fence regressed", err)
	}
}

func TestSnippetRepairMissingHistoryAndStrictInput(t *testing.T) {
	b, doc := snippetRepairFixture(t)
	b.Revisions = b.Revisions[1:]
	review, err := reviewSnippetBundle(b, 0, false)
	if err != nil || review.Healthy {
		t.Fatal(review, err)
	}
	result, err := buildSnippetRepair(b, doc, time.Now().UTC())
	if err != nil || len(result.Revisions) != 3 {
		t.Fatal("missing history was not restored", err)
	}
	for _, raw := range []string{`{"schema":1,"schema":1}`, `{"Schema":1}`, `{"schema":1,"kind":"wrong"}`, `{"schema":1,"kind":"newpress.snippet-repair","unknown":true}`} {
		if _, err := decodeSnippetRepair([]byte(raw)); !errors.Is(err, ErrSnippetMaintenanceInput) {
			t.Fatal(raw, err)
		}
	}
	if _, err = buildSnippetRepair(b, doc, time.Time{}); !errors.Is(err, ErrSnippetMaintenanceInput) {
		t.Fatal(err)
	}
}

func TestSnippetRepairAuditReserveCoversEscapedDeclarations(t *testing.T) {
	b, _ := snippetRepairFixture(t)
	q := snippetQuarantine{Capture: SnippetCaptureRequest{Target: b.Target, Reference: b.reference(), RequestKey: strings.Repeat("a", 64), Operator: "operator", Reason: "captured"}, Original: b, CapturedAt: time.Now().UTC()}
	q.ID = q.Capture.id()
	before, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	q.Repair = &snippetRepairAudit{Operator: strings.Repeat("<", 160), Reason: strings.Repeat("&", 1000), At: q.CapturedAt,
		ReplacementSHA256: strings.Repeat("b", 64), ResultReference: strings.Repeat("c", 64), SnippetVersion: math.MaxInt64}
	if err := q.validate(); err != nil {
		t.Fatal("maximum accepted declarations rejected", err)
	}
	after, err := json.Marshal(q)
	if err != nil || len(after)-len(before) > snippetRepairReserve {
		t.Fatal("valid repair audit exceeds the capacity reserved at capture", len(after)-len(before), err)
	}
}

func TestSnippetRepairEditorialTimestampPrecision(t *testing.T) {
	stamp := time.Date(2026, 11, 26, 10, 0, 0, 123456000, time.FixedZone("offset", 8*3600))
	doc := SnippetRepairDocument{Schema: 1, Kind: "newpress.snippet-repair", Editorial: &content.SubmissionReceipt{CreatedAt: stamp}}
	raw, _ := json.Marshal(doc)
	decoded, err := decodeSnippetRepair(raw)
	if err != nil || !decoded.Editorial.CreatedAt.Equal(stamp) || decoded.Editorial.CreatedAt.Location() != time.UTC {
		t.Fatal("equivalent typed timestamp was not normalized", err)
	}
	doc.Editorial.CreatedAt = stamp.Add(time.Nanosecond)
	raw, _ = json.Marshal(doc)
	if _, err := decodeSnippetRepair(raw); !errors.Is(err, ErrSnippetMaintenanceInput) {
		t.Fatal("timestamp that would lose precision was accepted", err)
	}
}
