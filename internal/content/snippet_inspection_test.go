package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetInspectionFileHistoryAndPermanentReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a, s, p, u := draftFixture(t, r)
	if _, err = r.Submit(t.Context(), draftConfirm(t, r, u, a)); err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: "private-diagnostic-title", ContentSource: "<p>private-diagnostic-body</p>", Visibility: SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	update := SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("c", 64)}
	plan, err := r.PreviewSnippetUpdate(t.Context(), update, a)
	if err != nil {
		t.Fatal(err)
	}
	update.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(t.Context(), update, a); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	q := SnippetInspectionQuery{Limit: 100}
	clean, err := InspectSnippetFile(t.Context(), path, q)
	if err != nil || !clean.Complete || !clean.Healthy || clean.Totals.Snippets != 1 || clean.Totals.Revisions != 2 || clean.Totals.UpdateReceipts != 1 || clean.Totals.EditorialReceipts != 1 || clean.Checked != clean.Totals {
		t.Fatal(clean, err)
	}
	if clean.SnapshotSHA256 != fmt.Sprintf("%x", sha256.Sum256(original)) {
		t.Fatal("file digest is not the original bytes")
	}
	for _, fault := range []string{"history", "next id", "duplicate snippet", "duplicate receipt", "receipt target", "editorial receipt", "capacity", "invalid JSON"} {
		t.Run(fault, func(t *testing.T) {
			var doc contentDocument
			if err := json.Unmarshal(original, &doc); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "history":
				doc.Snippets[0].Revisions[0].ContentHTML = "wrong historical body"
			case "next id":
				doc.NextSnippetID = 0
			case "duplicate snippet":
				doc.Snippets = append(doc.Snippets, doc.Snippets[0])
			case "duplicate receipt":
				doc.SnippetReceipts = append(doc.SnippetReceipts, doc.SnippetReceipts[0])
			case "receipt target":
				doc.SnippetReceipts[0].SnippetVersion, doc.SnippetReceipts[0].TargetVersion = 99, 99
			case "editorial receipt":
				doc.Submissions[0].Fingerprint = "invalid"
			case "capacity":
				for len(doc.Snippets) <= MaxSnippets {
					doc.Snippets = append(doc.Snippets, doc.Snippets[0])
				}
			}
			raw, err := json.Marshal(doc)
			if fault == "invalid JSON" {
				raw = []byte(`{"schema":2,"schema":2,"private":"private-diagnostic-body"}`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path+".bak", original, 0600); err != nil {
				t.Fatal(err)
			}
			report, err := InspectSnippetFile(t.Context(), path, q)
			if err != nil || report.Healthy || report.SnapshotSHA256 == clean.SnapshotSHA256 {
				t.Fatal(report, err)
			}
			if fault == "capacity" || fault == "invalid JSON" {
				if report.Complete || report.Fatal == "" {
					t.Fatal("incomplete scan reported complete", report)
				}
			} else if !report.Complete || report.IssueCount == 0 {
				t.Fatal("bad records not identified", report)
			}
			encoded, _ := json.Marshal(report)
			for _, private := range []string{"private-diagnostic-title", "private-diagnostic-body", a.Identity, update.RequestKey} {
				if bytes.Contains(encoded, []byte(private)) {
					t.Fatal("diagnostic leaked private record data")
				}
			}
			var items []SnippetInspectionIssue
			for offset := 0; report.Complete; {
				page, err := InspectSnippetFile(t.Context(), path, SnippetInspectionQuery{Limit: 1, Offset: offset})
				if err != nil || page.SnapshotSHA256 != report.SnapshotSHA256 || page.IssueCount != report.IssueCount {
					t.Fatal(page, err)
				}
				items = append(items, page.Items...)
				if page.NextOffset < 0 {
					break
				}
				offset = page.NextOffset
			}
			if report.Complete && len(items) != report.IssueCount {
				t.Fatal("issue pagination lost entries")
			}
			after, _ := os.ReadFile(path)
			backup, _ := os.ReadFile(path + ".bak")
			if !bytes.Equal(raw, after) || !bytes.Equal(original, backup) {
				t.Fatal("inspection changed primary or rollback file")
			}
		})
	}
}

func TestSnippetInspectionMissingFileCancellationAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "content.json")
	for _, q := range []SnippetInspectionQuery{{Limit: 0}, {Limit: 101}, {Limit: 1, Offset: -1}, {Limit: 1, Offset: 1_000_001}} {
		if _, err := InspectSnippetFile(t.Context(), path, q); err == nil {
			t.Fatal(q)
		}
	}
	if _, err := InspectSnippetFile(t.Context(), path, SnippetInspectionQuery{Limit: 1}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("missing file created storage")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InspectSnippetFile(ctx, path, SnippetInspectionQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSnippetDraftRejectsInvalidPinnedRevision(t *testing.T) {
	r := NewMemoryRepository(false)
	a, s, p, u := draftFixture(t, r)
	record, err := r.GetSnippet(t.Context(), s.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	key := contenthtml.SnippetKey{ID: s.ID, Version: 1}
	rev := record.Revisions[0]
	rev.Title = "not the current title"
	if _, err = BuildSnippetDraft(p, u, a, map[contenthtml.SnippetKey]SnippetDraftBody{key: {Snippet: s, Revision: rev}}); !errors.Is(err, ErrSnippetCorrupt) {
		t.Fatal(err)
	}
}

func TestSnippetInspectionEmptyLegacyDocumentAndIssueOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	raw := []byte(`{"schema":1,"kind":"newpress.content","next_id":1}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := InspectSnippetFile(t.Context(), path, SnippetInspectionQuery{Limit: 1, Offset: 1_000_000})
	if err != nil || !report.Complete || !report.Healthy || report.Checked != (SnippetInspectionTotals{}) || len(report.Items) != 0 || report.NextOffset != -1 {
		t.Fatal(report, err)
	}
	if report.SnapshotSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal("empty legacy document lost original-byte digest")
	}
}
