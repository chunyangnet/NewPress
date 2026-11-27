package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"newpress.local/newpress/internal/packagefs"
)

// InspectSnippetFile reads only the named primary file. It never creates a
// store, restores .bak or rewrites records. CLI callers hold its directory lock.
// Structural JSON errors cannot be safely attributed to individual records.
func InspectSnippetFile(ctx context.Context, path string, q SnippetInspectionQuery) (SnippetInspectionReport, error) {
	b, err := NewSnippetInspector(q)
	if err != nil {
		return SnippetInspectionReport{}, err
	}
	if err = ctx.Err(); err != nil {
		return SnippetInspectionReport{}, err
	}
	raw, err := readContentFile(path)
	if err != nil {
		return SnippetInspectionReport{}, err
	}
	digest := sha256.Sum256(raw)
	doc, err := packagefs.DecodeJSONWithNumbers[contentDocument](bytes.NewReader(raw), maxContentFileBytes, "content inspection")
	totals := SnippetInspectionTotals{}
	finish := func(fatal string) (SnippetInspectionReport, error) {
		if err := ctx.Err(); err != nil {
			return SnippetInspectionReport{}, err
		}
		report := b.Finish("file", fatal, totals)
		report.SnapshotSHA256 = hex.EncodeToString(digest[:])
		return report, nil
	}
	if err != nil {
		return finish("invalid_document_json")
	}
	if (doc.Schema != 1 && doc.Schema != 2 && doc.Schema != 3) || doc.Kind != "newpress.content" ||
		(doc.Schema == 1 && (len(doc.Snippets) != 0 || len(doc.SnippetReceipts) != 0 || doc.NextSnippetID != 0)) {
		return finish("invalid_snippet_document_schema")
	}
	if _, err := snippetFileEvidenceBudget(doc.SnippetQuarantines); err != nil || doc.Schema < 3 && len(doc.SnippetQuarantines) != 0 {
		return finish("invalid_snippet_quarantine_evidence")
	}
	totals.Snippets, totals.UpdateReceipts, totals.EditorialReceipts = int64(len(doc.Snippets)), int64(len(doc.SnippetReceipts)), int64(len(doc.Submissions))
	for _, s := range doc.Snippets {
		totals.Revisions += int64(len(s.Revisions))
		totals.SnippetBytes += int64(SnippetStorageSize(s))
	}
	for _, r := range doc.SnippetReceipts {
		totals.ReceiptBytes += int64(SnippetReceiptSize(r))
	}
	if !totals.WithinLimits() {
		return finish("capacity_exceeded")
	}
	for _, s := range doc.Snippets {
		encoded, _ := json.Marshal(s.Snippet)
		if err = b.Metadata(SnippetInspectionIndex{SnippetID: s.ID, Version: s.Version, Visibility: s.Visibility}, encoded); err != nil {
			return finish("record_limit_exceeded")
		}
	}
	for _, s := range doc.Snippets {
		for _, rev := range s.Revisions {
			if err = ctx.Err(); err != nil {
				return SnippetInspectionReport{}, err
			}
			encoded, _ := json.Marshal(rev)
			if err = b.Revision(SnippetInspectionIndex{SnippetID: s.ID, Version: rev.Version}, encoded); err != nil {
				return finish("record_limit_exceeded")
			}
		}
	}
	for _, r := range doc.SnippetReceipts {
		encoded, _ := json.Marshal(r)
		if err = b.UpdateReceipt(SnippetInspectionIndex{SnippetID: r.SnippetID, Identity: r.Identity, Key: r.RequestKey}, encoded); err != nil {
			return finish("record_limit_exceeded")
		}
	}
	posts := map[int64]int64{}
	for _, p := range doc.Posts {
		posts[p.ID] = p.Version
	}
	for _, r := range doc.Submissions {
		if err = ctx.Err(); err != nil {
			return SnippetInspectionReport{}, err
		}
		version, exists := posts[r.PostID]
		if err = b.EditorialReceipt(r, version, exists); err != nil {
			return finish("record_limit_exceeded")
		}
	}
	if doc.Schema >= 2 {
		b.CheckNextID(doc.NextSnippetID)
	}
	return finish("")
}
