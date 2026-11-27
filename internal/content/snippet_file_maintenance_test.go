package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

type snippetFileFixture struct {
	path        string
	r           *FileRepository
	a           SnippetAccess
	s           Snippet
	p           Post
	submission  Submission
	created     SubmissionResult
	update      SnippetUpdate
	replacement []byte
	before      []byte
}

func newSnippetFileFixture(t *testing.T) snippetFileFixture {
	t.Helper()
	f := snippetFileFixture{path: filepath.Join(t.TempDir(), "content.json")}
	var err error
	f.r, err = NewFileRepository(f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	var draft SnippetDraftRequest
	f.a, f.s, f.p, draft = draftFixture(t, f.r)
	f.submission = draftConfirm(t, f.r, draft, f.a)
	f.created, err = f.r.Submit(t.Context(), f.submission)
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = f.r.SaveSnippet(t.Context(), f.s.ID, Mutation{ExpectedVersion: f.s.Version}, SnippetInput{Title: "latest", ContentSource: "<p>latest shared</p>", Visibility: SnippetShared}, f.a)
	if err != nil {
		t.Fatal(err)
	}
	f.update = SnippetUpdate{SnippetID: f.s.ID, SnippetVersion: f.s.Version, Items: []SnippetSelection{{ID: f.p.ID, Version: f.p.Version}}, RequestKey: strings.Repeat("c", 64)}
	plan, err := f.r.PreviewSnippetUpdate(t.Context(), f.update, f.a)
	if err != nil {
		t.Fatal(err)
	}
	f.update.Digest = plan.Digest
	if _, err = f.r.ApplySnippetUpdate(t.Context(), f.update, f.a); err != nil {
		t.Fatal(err)
	}
	f.p, err = f.r.GetByID(t.Context(), f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.r.Create(t.Context(), PostInput{Title: "unrelated", ContentSource: "<p>unchanged unrelated</p>"}); err != nil {
		t.Fatal(err)
	}
	f.replacement, err = ExportSnippetFileRepair(t.Context(), f.path, SnippetFileTarget{SnippetID: f.s.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.before, err = os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func changeSnippetFile(t *testing.T, path string, change func(*contentDocument)) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc contentDocument
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	change(&doc)
	raw, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func captureSnippetFile(t *testing.T, f snippetFileFixture, target SnippetFileTarget, key string) SnippetFileQuarantineSummary {
	t.Helper()
	review, err := ReviewSnippetFileMaintenance(t.Context(), f.path, target)
	if err != nil || review.Healthy {
		t.Fatal("bad record review", review, err)
	}
	for _, secret := range []string{"latest shared", "unchanged unrelated", "user:7"} {
		raw, _ := json.Marshal(review)
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("review exposed private record")
		}
	}
	q, err := QuarantineSnippetFile(t.Context(), f.path, SnippetFileCaptureRequest{Target: target, Reference: review.Reference, RequestKey: key, Operator: "operator", Reason: "retain original damaged records"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func fileRepairRequest(q SnippetFileQuarantineSummary, raw []byte) SnippetFileRepairRequest {
	return SnippetFileRepairRequest{ID: q.ID, Operator: "operator", Reason: "independently checked backup", Replacement: raw, ReplacementSHA256: fileSnippetDigest(raw)}
}

func assertSnippetFileUnchanged(t *testing.T, path string, raw []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("failed operation changed source bytes", err)
	}
}

func TestSnippetFileMaintenanceRepairPreservesFactsAndReplays(t *testing.T) {
	f := newSnippetFileFixture(t)
	bad := changeSnippetFile(t, f.path, func(d *contentDocument) { d.Snippets[0].Revisions[0].ContentHTML = "damaged history" })
	original, err := decodeSnippetFileSnapshot(bad)
	if err != nil {
		t.Fatal(err)
	}
	q := captureSnippetFile(t, f, SnippetFileTarget{SnippetID: f.s.ID}, strings.Repeat("d", 64))
	quarantined, err := readSnippetFileSnapshot(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range original.fields {
		if key != "schema" && !bytes.Equal(value, quarantined.fields[key]) {
			t.Fatal("quarantine rewrote canonical field", key)
		}
	}
	bundle, err := original.bundle(SnippetFileTarget{SnippetID: f.s.ID})
	if err != nil || !reflect.DeepEqual(bundle, quarantined.doc.SnippetQuarantines[0].Original) {
		t.Fatal("quarantine changed original bytes", err)
	}
	if _, err = NewFileRepository(f.path, true); !errors.Is(err, ErrSnippetMaintenance) {
		t.Fatal("unresolved file opened", err)
	}
	if _, err = f.r.Create(t.Context(), PostInput{Title: "stale writer"}); err == nil {
		t.Fatal("stale repository bypassed quarantine")
	}
	if _, err = ExportSnippetFileRepair(t.Context(), f.path, SnippetFileTarget{SnippetID: f.s.ID}); !errors.Is(err, ErrSnippetMaintenance) {
		t.Fatal("unresolved export allowed", err)
	}
	page, err := ListSnippetFileQuarantines(t.Context(), f.path, SnippetInspectionQuery{Limit: 1})
	if err != nil || page.Unresolved != 1 || page.Total != 1 {
		t.Fatal(page, err)
	}
	request := quarantined.doc.SnippetQuarantines[0].Capture
	repeated, err := QuarantineSnippetFile(t.Context(), f.path, request, time.Now().Add(time.Hour))
	if err != nil || repeated.ID != q.ID {
		t.Fatal("capture replay", err)
	}
	assertSnippetFileUnchanged(t, f.path, quarantined.raw)
	repaired, err := RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now().UTC())
	if err != nil || repaired.Repair == nil {
		t.Fatal(err)
	}
	after, err := readSnippetFileSnapshot(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range original.fields {
		if key != "schema" && key != "snippets" && key != "snippet_receipts" && !bytes.Equal(value, after.fields[key]) {
			t.Fatal("repair rewrote unrelated field", key)
		}
	}
	if !reflect.DeepEqual(after.doc.SnippetQuarantines[0].Original, bundle) {
		t.Fatal("repair mutated permanent evidence")
	}
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertSnippetFileUnchanged(t, f.path, after.raw)
	r, err := NewFileRepository(f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.GetSnippet(t.Context(), f.s.ID, f.a)
	if err != nil || s.Visibility != SnippetWithdrawn || s.SharedSince != 0 || s.Version != 3 || len(s.Revisions) != 2 {
		t.Fatal(s, err)
	}
	resolved, err := r.ResolveSnippets(t.Context(), []contenthtml.SnippetKey{{ID: s.ID, Version: 1}, {ID: s.ID, Version: 2}})
	if err != nil || len(resolved) != 0 {
		t.Fatal("repair revived sharing", err)
	}
	draft, err := r.Submit(t.Context(), f.submission)
	if err != nil || !draft.Replayed || draft.Receipt != f.created.Receipt {
		t.Fatal("draft replay lost", err)
	}
	update, err := r.ApplySnippetUpdate(t.Context(), f.update, f.a)
	if err != nil || !update.Replayed || update.Receipt.Items[0].Version != f.p.Version {
		t.Fatal("update replay lost", err)
	}
	if _, err = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: "reshared", ContentSource: "<p>new shared period</p>", Visibility: SnippetShared}, f.a); err != nil {
		t.Fatal(err)
	}
	state, err := readSnippetFileSnapshot(t.Context(), f.path)
	if err != nil || state.doc.Schema != 3 || !reflect.DeepEqual(state.doc.SnippetQuarantines, after.doc.SnippetQuarantines) {
		t.Fatal("ordinary write downgraded schema or evidence", err)
	}
	resolved, err = r.ResolveSnippets(t.Context(), []contenthtml.SnippetKey{{ID: s.ID, Version: 1}})
	if err != nil || len(resolved) != 0 {
		t.Fatal("new sharing revived old reference", err)
	}
	current := state.raw
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnippetFileUnchanged(t, f.path, current)
	report, err := InspectSnippetFile(t.Context(), f.path, SnippetInspectionQuery{Limit: 100})
	if err != nil || !report.Complete || !report.Healthy {
		t.Fatal(report, err)
	}
}

func TestSnippetFileMaintenanceLastRepairAndEditorialFacts(t *testing.T) {
	f := newSnippetFileFixture(t)
	editorial, err := ExportSnippetFileRepair(t.Context(), f.path, SnippetFileTarget{EditorialKey: f.created.Receipt.Key})
	if err != nil {
		t.Fatal(err)
	}
	changeSnippetFile(t, f.path, func(d *contentDocument) {
		d.Snippets[0].Revisions[0].ContentHTML = "damaged"
		d.Submissions[0].CreatedAt = time.Time{}
	})
	q := captureSnippetFile(t, f, SnippetFileTarget{SnippetID: f.s.ID}, strings.Repeat("e", 64))
	raw, _ := os.ReadFile(f.path)
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now()); !errors.Is(err, ErrSnippetFileMaintenanceIncomplete) {
		t.Fatal("last repair ignored another fault", err)
	}
	assertSnippetFileUnchanged(t, f.path, raw)
	eq := captureSnippetFile(t, f, SnippetFileTarget{EditorialKey: f.created.Receipt.Key}, strings.Repeat("f", 64))
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileRepository(f.path, false); !errors.Is(err, ErrSnippetMaintenance) {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(f.path)
	for _, field := range []string{"fingerprint", "post", "version", "action"} {
		t.Run(field, func(t *testing.T) {
			doc, err := decodeSnippetFileRepair(editorial)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "fingerprint":
				doc.Editorial.Fingerprint = strings.Repeat("0", 64)
			case "post":
				doc.Editorial.PostID = f.p.ID
			case "version":
				doc.Editorial.Version = 2
			case "action":
				doc.Editorial.Action = "save"
			}
			bad, _ := json.Marshal(doc)
			if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(eq, bad), time.Now()); err == nil {
				t.Fatal("rewrite valid editorial fact accepted")
			}
			assertSnippetFileUnchanged(t, f.path, current)
		})
	}
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(eq, editorial), time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := NewFileRepository(f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.Submit(t.Context(), f.submission)
	if err != nil || !replayed.Replayed || replayed.Receipt != f.created.Receipt {
		t.Fatal("original editorial fact lost", err)
	}
}

func TestSnippetFileMaintenanceRefusesLossConflictsAndWriteFailures(t *testing.T) {
	f := newSnippetFileFixture(t)
	changeSnippetFile(t, f.path, func(d *contentDocument) { d.Snippets[0].Revisions[0].ContentHTML = "damaged" })
	q := captureSnippetFile(t, f, SnippetFileTarget{SnippetID: f.s.ID}, strings.Repeat("1", 64))
	current, _ := os.ReadFile(f.path)
	for _, kind := range []string{"history loss", "valid history edit", "receipt loss", "receipt edit", "wrong identity", "duplicate field", "other kind", "checksum", "cancelled", "external change", "write failure"} {
		t.Run(kind, func(t *testing.T) {
			doc, err := decodeSnippetFileRepair(f.replacement)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "history loss":
				doc.Snippet.Revisions = doc.Snippet.Revisions[1:]
				doc.Snippet.SharedSince = 2
			case "valid history edit":
				doc.Snippet.Revisions[1].ContentSource = "<p>changed valid</p>"
				doc.Snippet.Revisions[1].ContentHTML = doc.Snippet.Revisions[1].ContentSource
			case "receipt loss":
				doc.Receipts = nil
			case "receipt edit":
				doc.Receipts[0].Fingerprint = strings.Repeat("b", 64)
			case "wrong identity":
				doc.Snippet.ID++
			case "other kind":
				doc.Kind = "newpress.snippet-repair"
			}
			raw, _ := json.Marshal(doc)
			if kind == "duplicate field" {
				raw = bytes.Replace(raw, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
			}
			request := fileRepairRequest(q, raw)
			ctx := t.Context()
			if kind == "checksum" {
				request.ReplacementSHA256 = strings.Repeat("0", 64)
			}
			if kind == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var modified []byte
			if kind == "external change" {
				modified = changeSnippetFile(t, f.path, func(d *contentDocument) { d.Snippets[0].Title = "externally changed" })
			}
			if kind == "write failure" {
				if err = os.Mkdir(f.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(f.path + ".bak")
			}
			if _, err = RepairSnippetFile(ctx, f.path, request, time.Now()); err == nil {
				t.Fatal("invalid repair accepted", kind)
			}
			if modified != nil {
				assertSnippetFileUnchanged(t, f.path, modified)
				if err = os.WriteFile(f.path, current, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				assertSnippetFileUnchanged(t, f.path, current)
			}
		})
	}
}

func TestSnippetFileMaintenanceMissingHistoryAndParent(t *testing.T) {
	for _, kind := range []string{"missing history", "missing snippet", "unsafe next ID"} {
		t.Run(kind, func(t *testing.T) {
			f := newSnippetFileFixture(t)
			changeSnippetFile(t, f.path, func(d *contentDocument) {
				switch kind {
				case "missing history":
					d.Snippets[0].Revisions = d.Snippets[0].Revisions[1:]
				case "missing snippet":
					d.Snippets = nil
				case "unsafe next ID":
					d.NextSnippetID = 1
					d.Snippets[0].Revisions[0].ContentHTML = "bad"
				}
			})
			q := captureSnippetFile(t, f, SnippetFileTarget{SnippetID: f.s.ID}, strings.Repeat("2", 64))
			raw, _ := os.ReadFile(f.path)
			_, err := RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now())
			if kind == "unsafe next ID" {
				if err == nil {
					t.Fatal("unsafe identity counter accepted")
				}
				assertSnippetFileUnchanged(t, f.path, raw)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewFileRepository(f.path, false)
			if err != nil {
				t.Fatal(err)
			}
			s, err := r.GetSnippet(t.Context(), f.s.ID, f.a)
			if err != nil || len(s.Revisions) != 2 || s.Visibility != SnippetWithdrawn {
				t.Fatal(s, err)
			}
		})
	}
}

func TestSnippetFileMaintenanceRejectsAmbiguousOrUnrelatedCorruption(t *testing.T) {
	for _, kind := range []string{"duplicate snippet", "duplicate receipt", "invalid revision identity", "bad article", "unknown field", "alias", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			f := newSnippetFileFixture(t)
			raw := changeSnippetFile(t, f.path, func(d *contentDocument) {
				d.Snippets[0].Revisions[0].ContentHTML = "bad"
				switch kind {
				case "duplicate snippet":
					d.Snippets = append(d.Snippets, d.Snippets[0])
				case "duplicate receipt":
					d.SnippetReceipts = append(d.SnippetReceipts, d.SnippetReceipts[0])
				case "invalid revision identity":
					d.Snippets[0].Revisions[0].Version = 0
				case "bad article":
					d.Posts[0].Title = ""
				}
			})
			switch kind {
			case "unknown field":
				raw = bytes.Replace(raw, []byte(`"schema": 2`), []byte(`"schema": 2,"future":true`), 1)
			case "alias":
				raw = bytes.Replace(raw, []byte(`"schema": 2`), []byte(`"schema": 2,"Schema":2`), 1)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			}
			if err := os.WriteFile(f.path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReviewSnippetFileMaintenance(t.Context(), f.path, SnippetFileTarget{SnippetID: f.s.ID}); err == nil {
				t.Fatal("ambiguous or unrelated damage accepted", kind)
			}
			assertSnippetFileUnchanged(t, f.path, raw)
		})
	}
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, err := ListSnippetFileQuarantines(t.Context(), path, SnippetInspectionQuery{Limit: 1}); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("maintenance initialized missing file")
	}
}

func TestSnippetFileMaintenanceEvidenceCapacityAndAuditReserve(t *testing.T) {
	f := newSnippetFileFixture(t)
	changeSnippetFile(t, f.path, func(d *contentDocument) { d.Snippets[0].Revisions[0].ContentHTML = "bad" })
	q := captureSnippetFile(t, f, SnippetFileTarget{SnippetID: f.s.ID}, strings.Repeat("3", 64))
	s, err := readSnippetFileSnapshot(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	base := s.doc.SnippetQuarantines[0]
	base.Capture.Operator = strings.Repeat("<", 160)
	base.Capture.Reason = strings.Repeat("&", 1000)
	before, _ := json.Marshal(base)
	base.Repair = &SnippetFileRepairAudit{Operator: base.Capture.Operator, Reason: base.Capture.Reason, At: base.CapturedAt, ReplacementSHA256: strings.Repeat("a", 64), ResultReference: strings.Repeat("b", 64), SnippetVersion: 3}
	after, _ := json.Marshal(base)
	if len(after)-len(before) > snippetFileRepairReserve {
		t.Fatal("insufficient worst JSON escaping reserve")
	}
	all := make([]SnippetFileQuarantine, MaxSnippetFileQuarantines)
	for i := range all {
		all[i] = base
		all[i].Capture.RequestKey = fmt.Sprintf("%064x", i)
		all[i].ID = all[i].Capture.id()
	}
	if _, err = snippetFileEvidenceBudget(all); err != nil {
		t.Fatal("exact count limit rejected", err)
	}
	if _, err = snippetFileEvidenceBudget(append(all, base)); !errors.Is(err, ErrSnippetCapacity) {
		t.Fatal("count overflow accepted", err)
	}
	all[0].Original.Snippet = []byte("changed evidence")
	if _, err = snippetFileEvidenceBudget(all); !errors.Is(err, ErrSnippetFileMaintenanceEvidence) {
		t.Fatal("evidence tamper accepted", err)
	}
	if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, f.replacement), time.Now()); err != nil {
		t.Fatal(err)
	}
	changeSnippetFile(t, f.path, func(d *contentDocument) { d.Schema = 2 })
	if _, err = NewFileRepository(f.path, false); err == nil {
		t.Fatal("schema downgrade ignored evidence")
	}
}

func TestSnippetFileMaintenancePreservesWithdrawalHeadroom(t *testing.T) {
	f := newSnippetFileFixture(t)
	s, err := readSnippetFileSnapshot(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	base, err := prepareSnippetFileWrite(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The writer preserves unrelated raw JSON whitespace. A valid, nearly full
	// snapshot must not let quarantine evidence consume another snippet's final
	// 64-byte withdrawal reserve. This tests the real encoded-file limit.
	posts := s.fields["posts"]
	s.fields["posts"] = append(bytes.Clone(posts), bytes.Repeat([]byte(" "), maxContentFileBytes-len(base)-63)...)
	if _, err = prepareSnippetFileWrite(s, nil); !errors.Is(err, ErrSnippetCapacity) {
		t.Fatal("maintenance consumed withdrawal reserve", err)
	}
	s.fields["posts"] = s.fields["posts"][:len(s.fields["posts"])-1]
	accepted, err := prepareSnippetFileWrite(s, nil)
	if err != nil || len(accepted)+64 != maxContentFileBytes {
		t.Fatal("exact withdrawal headroom rejected", len(accepted), err)
	}
}

func TestSnippetFileMaintenanceMalformedTypesAndDeletedReceipt(t *testing.T) {
	for _, kind := range []string{"history timestamp", "receipt timestamp", "deleted editorial"} {
		t.Run(kind, func(t *testing.T) {
			f := newSnippetFileFixture(t)
			target := SnippetFileTarget{SnippetID: f.s.ID}
			replacement := f.replacement
			if kind == "deleted editorial" {
				trashed, err := f.r.ChangeStatus(t.Context(), f.created.Receipt.PostID, Mutation{ExpectedVersion: 1}, StatusTrash)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: trashed.ID, ExpectedVersion: trashed.Version}}}); err != nil {
					t.Fatal(err)
				}
				target = SnippetFileTarget{EditorialKey: f.created.Receipt.Key}
				replacement, err = ExportSnippetFileRepair(t.Context(), f.path, target)
				if err != nil {
					t.Fatal(err)
				}
			}
			s, err := readSnippetFileSnapshot(t.Context(), f.path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "history timestamp":
				fields, rows, err := snippetFileHistory(s.doc.Snippets[0])
				if err != nil {
					t.Fatal(err)
				}
				row, err := snippetFileObject(rows[0], fileSnippetRevisionFields)
				if err != nil {
					t.Fatal(err)
				}
				row["created_at"] = json.RawMessage(`"unreadable timestamp"`)
				rows[0] = encodeSnippetFileFields(row)
				fields["revisions"] = encodeSnippetFileArray(rows)
				s.doc.Snippets[0] = encodeSnippetFileFields(fields)
				s.fields["snippets"] = encodeSnippetFileArray(s.doc.Snippets)
			case "receipt timestamp":
				row, err := snippetFileObject(s.doc.SnippetReceipts[0], fileSnippetReceiptFields)
				if err != nil {
					t.Fatal(err)
				}
				row["created_at"] = json.RawMessage(`"unreadable timestamp"`)
				s.doc.SnippetReceipts[0] = encodeSnippetFileFields(row)
				s.fields["snippet_receipts"] = encodeSnippetFileArray(s.doc.SnippetReceipts)
			case "deleted editorial":
				row, err := snippetFileObject(s.doc.Submissions[0], fileEditorialFields)
				if err != nil {
					t.Fatal(err)
				}
				row["fingerprint"] = json.RawMessage(`"invalid"`)
				s.doc.Submissions[0] = encodeSnippetFileFields(row)
				s.fields["submissions"] = encodeSnippetFileArray(s.doc.Submissions)
			}
			if err = os.WriteFile(f.path, encodeSnippetFileFields(s.fields), 0600); err != nil {
				t.Fatal(err)
			}
			q := captureSnippetFile(t, f, target, strings.Repeat("4", 64))
			if _, err = NewFileRepository(f.path, false); err == nil {
				t.Fatal("unresolved malformed file opened")
			}
			if kind == "receipt timestamp" {
				doc, err := decodeSnippetFileRepair(replacement)
				if err != nil {
					t.Fatal(err)
				}
				doc.Receipts[0].Fingerprint = strings.Repeat("f", 64)
				raw, _ := json.Marshal(doc)
				if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, raw), time.Now()); !errors.Is(err, ErrSnippetFileMaintenanceConflict) {
					t.Fatal("valid receipt identity changed with broken time", err)
				}
			}
			if _, err = RepairSnippetFile(t.Context(), f.path, fileRepairRequest(q, replacement), time.Now()); err != nil {
				t.Fatal(err)
			}
			r, err := NewFileRepository(f.path, false)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "deleted editorial" {
				replay, err := r.FindSubmission(t.Context(), f.submission.Key, f.submission.Fingerprint)
				if err != nil || !replay.Receipt.Deleted || replay.Receipt.PostID != f.created.Receipt.PostID {
					t.Fatal("deleted confirmation changed", err)
				}
			}
		})
	}
}

func TestSnippetFileMaintenanceSchemaOneEditorialUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	request := Submission{Key: strings.Repeat("5", 64), Fingerprint: strings.Repeat("6", 64), Action: "create", Input: PostInput{Title: "schema one draft"}}
	created, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	target := SnippetFileTarget{EditorialKey: created.Receipt.Key}
	replacement, err := ExportSnippetFileRepair(t.Context(), path, target)
	if err != nil {
		t.Fatal(err)
	}
	changeSnippetFile(t, path, func(d *contentDocument) {
		if d.Schema != 1 {
			t.Fatal("fixture not schema 1")
		}
		d.Submissions[0].CreatedAt = time.Time{}
	})
	q := captureSnippetFile(t, snippetFileFixture{path: path}, target, strings.Repeat("7", 64))
	if _, err = RepairSnippetFile(t.Context(), path, fileRepairRequest(q, replacement), time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.state.contentSchema != 3 || r.state.nextSnippetID != 1 {
		t.Fatal("schema-one upgrade lost snippet counter")
	}
	result, err := r.Submit(t.Context(), request)
	if err != nil || !result.Replayed || result.Receipt != created.Receipt {
		t.Fatal(result, err)
	}
}
