package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"slices"

	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

type SnippetInspectionQuery struct{ Limit, Offset int }

func (q SnippetInspectionQuery) Validate() error {
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 1_000_000 {
		return errors.New("inspection requires limit 1-100 and offset 0-1000000")
	}
	return nil
}

type SnippetInspectionTotals struct {
	Snippets          int64 `json:"snippets"`
	Revisions         int64 `json:"revisions"`
	UpdateReceipts    int64 `json:"update_receipts"`
	EditorialReceipts int64 `json:"editorial_receipts"`
	SnippetBytes      int64 `json:"snippet_bytes"`
	ReceiptBytes      int64 `json:"update_receipt_bytes"`
}

func (t SnippetInspectionTotals) WithinLimits() bool {
	return t.Snippets <= MaxSnippets && t.Revisions <= MaxSnippetRevisions && t.UpdateReceipts <= MaxSnippetReceipts &&
		t.EditorialReceipts <= MaxSubmissionReceipts && t.SnippetBytes <= MaxSnippetStorageBytes && t.ReceiptBytes <= MaxSnippetReceiptBytes
}

type SnippetInspectionIssue struct {
	Type      string `json:"type"`
	Ordinal   int64  `json:"ordinal"`
	SnippetID int64  `json:"snippet_id,omitempty"`
	Version   int64  `json:"version,omitempty"`
	Reference string `json:"reference"`
	Fault     string `json:"fault"`
}

type SnippetInspectionReport struct {
	Storage        string                   `json:"storage"`
	RecordEncoding string                   `json:"record_encoding"`
	Complete       bool                     `json:"complete"`
	Healthy        bool                     `json:"healthy"`
	Fatal          string                   `json:"fatal,omitempty"`
	SnapshotSHA256 string                   `json:"snapshot_sha256,omitempty"`
	Totals         SnippetInspectionTotals  `json:"totals"`
	Checked        SnippetInspectionTotals  `json:"checked"`
	IssueCount     int                      `json:"issue_count"`
	Offset         int                      `json:"offset"`
	NextOffset     int                      `json:"next_offset"`
	Items          []SnippetInspectionIssue `json:"items"`
}

type SnippetInspectionIndex struct {
	SnippetID   int64  `json:"snippet_id,omitempty"`
	Version     int64  `json:"version,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	Identity    string `json:"identity,omitempty"`
	Key         string `json:"key,omitempty"`
	PostID      int64  `json:"post_id,omitempty"`
	PostVersion int64  `json:"post_version,omitempty"`
	PostExists  bool   `json:"post_exists,omitempty"`
}

type inspectedSnippet struct {
	meta          Snippet
	valid         bool
	history       *SnippetHistoryValidator
	historyBroken bool
	ref           SnippetInspectionIssue
}

// SnippetInspector accepts metadata, then ordered history, then update and
// editorial receipts. It keeps identities, validators and version membership,
// not old bodies. Reports contain opaque references, never titles or payloads.
type SnippetInspector struct {
	query      SnippetInspectionQuery
	report     SnippetInspectionReport
	digest     hash.Hash
	snippets   map[int64]*inspectedSnippet
	versions   map[contenthtml.SnippetKey]bool
	updateKeys map[string]bool
	editorKeys map[string]bool
}

func NewSnippetInspector(q SnippetInspectionQuery) (*SnippetInspector, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	b := &SnippetInspector{query: q, report: SnippetInspectionReport{Complete: true, Offset: q.Offset, NextOffset: -1, Items: []SnippetInspectionIssue{}}, digest: sha256.New(),
		snippets: map[int64]*inspectedSnippet{}, versions: map[contenthtml.SnippetKey]bool{}, updateKeys: map[string]bool{}, editorKeys: map[string]bool{}}
	_, _ = b.digest.Write([]byte("newpress-snippet-inspection-v1\x00"))
	return b, nil
}

func inspectionFrame(h hash.Hash, values ...[]byte) {
	for _, value := range values {
		_ = binary.Write(h, binary.BigEndian, int64(len(value)))
		_, _ = h.Write(value)
	}
}

func (b *SnippetInspector) reference(kind string, ordinal int64, index SnippetInspectionIndex, raw []byte) SnippetInspectionIssue {
	encoded, _ := json.Marshal(index)
	h := sha256.New()
	inspectionFrame(h, []byte(kind), encoded, raw)
	inspectionFrame(b.digest, []byte(kind), encoded, raw)
	return SnippetInspectionIssue{Type: kind, Ordinal: ordinal, SnippetID: index.SnippetID, Version: index.Version, Reference: hex.EncodeToString(h.Sum(nil))}
}

func (b *SnippetInspector) issue(ref SnippetInspectionIssue, fault string) {
	ref.Fault = fault
	if b.report.IssueCount >= b.query.Offset && len(b.report.Items) < b.query.Limit {
		b.report.Items = append(b.report.Items, ref)
	}
	b.report.IssueCount++
}

func (b *SnippetInspector) CheckNextID(next int64) {
	if next < 1 || next == 1<<63-1 {
		b.issue(SnippetInspectionIssue{Type: "file_metadata"}, "invalid_next_snippet_id")
		return
	}
	for id := range b.snippets {
		if id >= next {
			b.issue(SnippetInspectionIssue{Type: "file_metadata"}, "invalid_next_snippet_id")
			return
		}
	}
}

func (b *SnippetInspector) Metadata(index SnippetInspectionIndex, raw []byte) error {
	b.report.Checked.Snippets++
	b.report.Checked.SnippetBytes += 4096
	if !b.report.Checked.WithinLimits() || len(raw) > 4096 || len(index.Visibility) > 32 {
		return ErrSnippetCapacity
	}
	ref := b.reference("metadata", b.report.Checked.Snippets, index, raw)
	if _, exists := b.snippets[index.SnippetID]; exists {
		b.issue(ref, "duplicate_metadata")
		return nil
	}
	state := &inspectedSnippet{ref: ref}
	b.snippets[index.SnippetID] = state
	s, err := packagefs.DecodeJSON[Snippet](bytes.NewReader(raw), 4096, "snippet")
	if err != nil {
		b.issue(ref, "invalid_json")
		return nil
	}
	if ValidateSnippetMetadata(s) != nil {
		b.issue(ref, "invalid_metadata")
		return nil
	}
	if s.ID != index.SnippetID || s.Version != index.Version || s.Visibility != index.Visibility {
		b.issue(ref, "index_mismatch")
		return nil
	}
	state.meta, state.valid = s, true
	state.history, _ = NewSnippetHistoryValidator(s)
	return nil
}

func (b *SnippetInspector) Revision(index SnippetInspectionIndex, raw []byte) error {
	b.report.Checked.Revisions++
	b.report.Checked.SnippetBytes += int64(len(raw))
	if !b.report.Checked.WithinLimits() || len(raw) > 2<<20 {
		return ErrSnippetCapacity
	}
	ref := b.reference("revision", b.report.Checked.Revisions, index, raw)
	state := b.snippets[index.SnippetID]
	accepted := false
	defer func() {
		if state != nil && !accepted {
			state.historyBroken = true
		}
	}()
	key := contenthtml.SnippetKey{ID: index.SnippetID, Version: index.Version}
	if b.versions[key] {
		b.issue(ref, "duplicate_revision")
		return nil
	}
	b.versions[key] = true
	rev, err := packagefs.DecodeJSON[SnippetRevision](bytes.NewReader(raw), 2<<20, "snippet revision")
	if err != nil {
		b.issue(ref, "invalid_json")
		return nil
	}
	if rev.Version != index.Version {
		b.issue(ref, "index_mismatch")
		return nil
	}
	if state == nil {
		b.issue(ref, "orphan_revision")
	} else if !state.valid {
		b.issue(ref, "parent_unverifiable")
	} else if ValidateSnippetRevision(state.meta, rev) != nil {
		b.issue(ref, "invalid_revision")
	} else if state.history.Add(rev) != nil {
		b.issue(ref, "history_order_or_bounds")
	} else {
		accepted = true
	}
	return nil
}

func (b *SnippetInspector) UpdateReceipt(index SnippetInspectionIndex, raw []byte) error {
	b.report.Checked.UpdateReceipts++
	b.report.Checked.ReceiptBytes += int64(len(raw))
	if !b.report.Checked.WithinLimits() || len(raw) > 16384 || len(index.Identity) > 160 || len(index.Key) > 64 {
		return ErrSnippetCapacity
	}
	ref := b.reference("update_receipt", b.report.Checked.UpdateReceipts, index, raw)
	r, err := packagefs.DecodeJSON[SnippetReceipt](bytes.NewReader(raw), 16384, "snippet receipt")
	if err != nil {
		b.issue(ref, "invalid_json")
		return nil
	}
	if ValidateSnippetReceipt(r) != nil {
		b.issue(ref, "invalid_receipt")
		return nil
	}
	if r.Identity != index.Identity || r.RequestKey != index.Key || r.SnippetID != index.SnippetID {
		b.issue(ref, "index_mismatch")
		return nil
	}
	key := r.Identity + "\x00" + r.RequestKey
	if b.updateKeys[key] {
		b.issue(ref, "duplicate_receipt")
	}
	b.updateKeys[key] = true
	state, exists := b.snippets[r.SnippetID]
	if !exists {
		b.issue(ref, "orphan_receipt")
	} else if !state.valid {
		b.issue(ref, "parent_unverifiable")
	} else if r.SnippetVersion > state.meta.Version || r.TargetVersion > state.meta.CurrentVersion || r.CreatedAt.Before(state.meta.CreatedAt) {
		b.issue(ref, "receipt_relationship")
	}
	if !b.versions[contenthtml.SnippetKey{ID: r.SnippetID, Version: r.TargetVersion}] {
		b.issue(ref, "receipt_target_missing")
	}
	return nil
}

// The creation ledger does not record whether a draft came from snippets.
// Inspect the entire common editorial ledger instead of guessing provenance.
func (b *SnippetInspector) EditorialReceipt(r SubmissionReceipt, postVersion int64, exists bool) error {
	b.report.Checked.EditorialReceipts++
	raw, err := json.Marshal(r)
	if err != nil || !b.report.Checked.WithinLimits() || len(raw) > 2048 {
		return ErrSnippetCapacity
	}
	ref := b.reference("editorial_receipt", b.report.Checked.EditorialReceipts, SnippetInspectionIndex{Key: r.Key, PostID: r.PostID, PostVersion: postVersion, PostExists: exists}, raw)
	posts := map[int64]Post{}
	if exists {
		posts[r.PostID] = Post{ID: r.PostID, Version: postVersion}
	}
	if ValidateSubmissionReceipts([]SubmissionReceipt{r}, posts) != nil {
		b.issue(ref, "invalid_editorial_receipt")
	}
	if b.editorKeys[r.Key] {
		b.issue(ref, "duplicate_editorial_receipt")
	}
	b.editorKeys[r.Key] = true
	return nil
}

// Finish is called once after all source rows have been consumed. A fatal
// preflight/stream limit produces an incomplete report, never a clean result.
func (b *SnippetInspector) Finish(storage, fatal string, totals SnippetInspectionTotals) SnippetInspectionReport {
	if fatal == "" && b.report.Checked != totals {
		fatal = "incomplete_source_scan"
	}
	b.report.Storage, b.report.Fatal, b.report.Totals = storage, fatal, totals
	b.report.RecordEncoding = "raw_snippet_json_and_typed_editorial_rows"
	if storage == "file" {
		b.report.RecordEncoding = "canonical_decoded_json"
	}
	if fatal != "" {
		b.report.Complete = false
	} else {
		ids := make([]int64, 0, len(b.snippets))
		for id := range b.snippets {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			state := b.snippets[id]
			if state.valid && (state.historyBroken || state.history.Complete() != nil) {
				b.issue(state.ref, "incomplete_or_invalid_history")
			}
		}
		b.report.SnapshotSHA256 = hex.EncodeToString(b.digest.Sum(nil))
	}
	b.report.Healthy = b.report.Complete && b.report.IssueCount == 0
	if b.query.Offset+len(b.report.Items) < b.report.IssueCount {
		b.report.NextOffset = b.query.Offset + len(b.report.Items)
	}
	return b.report
}
