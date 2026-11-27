package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxSnippetQuarantines           = 1000
	MaxSnippetQuarantineBytes       = 128 << 20
	MaxSnippetQuarantineRecordBytes = 64 << 20
	MaxSnippetRepairBytes           = 48 << 20
	// Includes worst-case JSON escaping of the bounded operator and reason.
	snippetRepairReserve = 8192
)

var (
	ErrSnippetMaintenanceInput      = errors.New("invalid snippet maintenance request")
	ErrSnippetMaintenanceConflict   = errors.New("snippet maintenance evidence changed or request identity conflicts")
	ErrSnippetMaintenanceEvidence   = errors.New("snippet maintenance evidence cannot be verified")
	ErrSnippetMaintenanceIncomplete = errors.New("canonical snippet or editorial records still have faults; quarantine remains unresolved")
)

type SnippetMaintenanceTarget struct {
	SnippetID    int64  `json:"snippet_id,omitempty"`
	EditorialKey string `json:"editorial_key,omitempty"`
}

func snippetDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func (t SnippetMaintenanceTarget) Validate() error {
	if t.SnippetID > 0 && t.SnippetID < math.MaxInt64 && t.EditorialKey == "" || t.SnippetID == 0 && snippetDigest(t.EditorialKey) {
		return nil
	}
	return ErrSnippetMaintenanceInput
}

func (t SnippetMaintenanceTarget) scope() string {
	if t.SnippetID != 0 {
		return "snippet"
	}
	return "editorial_receipt"
}

type snippetRawRow struct {
	Index content.SnippetInspectionIndex `json:"index"`
	Data  []byte                         `json:"data"`
}

// Original bytes remain opaque even when they are invalid JSON. No row is
// removed from the canonical tables merely because it has been quarantined.
type snippetRawBundle struct {
	Target    SnippetMaintenanceTarget   `json:"target"`
	Metadata  *snippetRawRow             `json:"metadata,omitempty"`
	Revisions []snippetRawRow            `json:"revisions,omitempty"`
	Receipts  []snippetRawRow            `json:"update_receipts,omitempty"`
	Editorial *content.SubmissionReceipt `json:"editorial_receipt,omitempty"`
}

func (b snippetRawBundle) reference() string {
	raw, _ := json.Marshal(b)
	digest := sha256.Sum256(append([]byte("newpress-snippet-maintenance-v1\x00"), raw...))
	return hex.EncodeToString(digest[:])
}

func (b snippetRawBundle) validateEnvelope() error {
	if b.Target.Validate() != nil || len(b.Revisions) > content.MaxSnippetRevisions || len(b.Receipts) > content.MaxSnippetReceipts {
		return ErrSnippetMaintenanceEvidence
	}
	if b.Target.EditorialKey != "" {
		if b.Metadata != nil || len(b.Revisions)+len(b.Receipts) != 0 || b.Editorial == nil || b.Editorial.Key != b.Target.EditorialKey || b.Editorial.Deleted {
			return ErrSnippetMaintenanceEvidence
		}
		raw, err := json.Marshal(b.Editorial)
		if err != nil || len(raw) > 2048 {
			return ErrSnippetMaintenanceEvidence
		}
		return nil
	}
	if b.Editorial != nil || b.Metadata == nil && len(b.Revisions)+len(b.Receipts) == 0 {
		return ErrSnippetMaintenanceEvidence
	}
	if m := b.Metadata; m != nil && (m.Index.SnippetID != b.Target.SnippetID || len(m.Data) > 4096 || len(m.Index.Visibility) > 32 ||
		m.Index.Identity != "" || m.Index.Key != "" || m.Index.PostID != 0 || m.Index.PostVersion != 0 || m.Index.PostExists) {
		return ErrSnippetMaintenanceEvidence
	}
	total, receiptBytes := 4096, 0
	var previous int64
	for _, row := range b.Revisions {
		if row.Index != (content.SnippetInspectionIndex{SnippetID: b.Target.SnippetID, Version: row.Index.Version}) || row.Index.Version <= previous || len(row.Data) > 2<<20 {
			return ErrSnippetMaintenanceEvidence
		}
		previous = row.Index.Version
		total += len(row.Data)
	}
	last := ""
	for _, row := range b.Receipts {
		key := row.Index.Identity + "\x00" + row.Index.Key
		if row.Index != (content.SnippetInspectionIndex{SnippetID: b.Target.SnippetID, Identity: row.Index.Identity, Key: row.Index.Key}) ||
			len(row.Index.Identity) > 160 || len(row.Index.Key) > 64 || key <= last || len(row.Data) > 16384 {
			return ErrSnippetMaintenanceEvidence
		}
		last, receiptBytes = key, receiptBytes+len(row.Data)
	}
	if total > content.MaxSnippetStorageBytes || receiptBytes > content.MaxSnippetReceiptBytes {
		return ErrSnippetMaintenanceEvidence
	}
	return nil
}

type SnippetMaintenanceReview struct {
	Scope      string   `json:"scope"`
	SnippetID  int64    `json:"snippet_id,omitempty"`
	Reference  string   `json:"reference"`
	Healthy    bool     `json:"healthy"`
	IssueCount int      `json:"issue_count"`
	Faults     []string `json:"faults"`
}

func reviewSnippetBundle(b snippetRawBundle, postVersion int64, postExists bool) (SnippetMaintenanceReview, error) {
	if err := b.validateEnvelope(); err != nil {
		return SnippetMaintenanceReview{}, err
	}
	inspector, _ := content.NewSnippetInspector(content.SnippetInspectionQuery{Limit: 100})
	var totals content.SnippetInspectionTotals
	if b.Metadata != nil {
		totals.Snippets, totals.SnippetBytes = 1, 4096
		if err := inspector.Metadata(b.Metadata.Index, b.Metadata.Data); err != nil {
			return SnippetMaintenanceReview{}, err
		}
	}
	for _, row := range b.Revisions {
		totals.Revisions++
		totals.SnippetBytes += int64(len(row.Data))
		if err := inspector.Revision(row.Index, row.Data); err != nil {
			return SnippetMaintenanceReview{}, err
		}
	}
	for _, row := range b.Receipts {
		totals.UpdateReceipts++
		totals.ReceiptBytes += int64(len(row.Data))
		if err := inspector.UpdateReceipt(row.Index, row.Data); err != nil {
			return SnippetMaintenanceReview{}, err
		}
	}
	if b.Editorial != nil {
		r := *b.Editorial
		r.Deleted = !postExists
		totals.EditorialReceipts = 1
		if err := inspector.EditorialReceipt(r, postVersion, postExists); err != nil {
			return SnippetMaintenanceReview{}, err
		}
	}
	report := inspector.Finish("postgresql", "", totals)
	if !report.Complete {
		return SnippetMaintenanceReview{}, ErrSnippetMaintenanceEvidence
	}
	result := SnippetMaintenanceReview{Scope: b.Target.scope(), SnippetID: b.Target.SnippetID, Reference: b.reference(), Healthy: report.Healthy, IssueCount: report.IssueCount, Faults: []string{}}
	for _, issue := range report.Items {
		if !slices.Contains(result.Faults, issue.Fault) {
			result.Faults = append(result.Faults, issue.Fault)
		}
	}
	return result, nil
}

type SnippetRepairDocument struct {
	Schema    int                        `json:"schema"`
	Kind      string                     `json:"kind"`
	Snippet   *content.SnippetRecord     `json:"snippet,omitempty"`
	Receipts  []content.SnippetReceipt   `json:"update_receipts,omitempty"`
	Editorial *content.SubmissionReceipt `json:"editorial_receipt,omitempty"`
}

type SnippetCaptureRequest struct {
	Target     SnippetMaintenanceTarget `json:"target"`
	Reference  string                   `json:"reference"`
	RequestKey string                   `json:"request_key"`
	Operator   string                   `json:"operator"`
	Reason     string                   `json:"reason"`
}

func validMaintenanceText(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (r SnippetCaptureRequest) Validate() error {
	if r.Target.Validate() != nil || !snippetDigest(r.Reference) || !snippetDigest(r.RequestKey) || !validMaintenanceText(r.Operator, 160) || !validMaintenanceText(r.Reason, 1000) {
		return ErrSnippetMaintenanceInput
	}
	return nil
}
func (r SnippetCaptureRequest) id() string {
	h := sha256.Sum256([]byte("newpress-snippet-quarantine\x00" + r.RequestKey))
	return hex.EncodeToString(h[:])
}

type SnippetRepairRequest struct {
	ID                string
	Operator, Reason  string
	Replacement       []byte
	ReplacementSHA256 string
}

func (r SnippetRepairRequest) Validate() error {
	h := sha256.Sum256(r.Replacement)
	if !snippetDigest(r.ID) || !validMaintenanceText(r.Operator, 160) || !validMaintenanceText(r.Reason, 1000) ||
		len(r.Replacement) == 0 || len(r.Replacement) > MaxSnippetRepairBytes || r.ReplacementSHA256 != hex.EncodeToString(h[:]) {
		return ErrSnippetMaintenanceInput
	}
	return nil
}

type snippetRepairAudit struct {
	Operator          string    `json:"operator"`
	Reason            string    `json:"reason"`
	At                time.Time `json:"at"`
	ReplacementSHA256 string    `json:"replacement_sha256"`
	ResultReference   string    `json:"result_reference"`
	SnippetVersion    int64     `json:"snippet_version,omitempty"`
}

type snippetQuarantine struct {
	ID         string                `json:"id"`
	Capture    SnippetCaptureRequest `json:"capture"`
	CapturedAt time.Time             `json:"captured_at"`
	Original   snippetRawBundle      `json:"original"`
	Repair     *snippetRepairAudit   `json:"repair,omitempty"`
}

func (q snippetQuarantine) validate() error {
	if q.Capture.Validate() != nil || q.ID != q.Capture.id() || q.CapturedAt.IsZero() || q.Original.validateEnvelope() != nil ||
		q.Original.Target != q.Capture.Target || q.Original.reference() != q.Capture.Reference {
		return ErrSnippetMaintenanceEvidence
	}
	if r := q.Repair; r != nil && (!validMaintenanceText(r.Operator, 160) || !validMaintenanceText(r.Reason, 1000) ||
		r.At.Before(q.CapturedAt) || !snippetDigest(r.ReplacementSHA256) || !snippetDigest(r.ResultReference) ||
		(q.Capture.Target.SnippetID != 0 && r.SnippetVersion <= 0) || (q.Capture.Target.SnippetID == 0 && r.SnippetVersion != 0)) {
		return ErrSnippetMaintenanceEvidence
	}
	return nil
}

type SnippetQuarantineSummary struct {
	ID         string              `json:"id"`
	Scope      string              `json:"scope"`
	SnippetID  int64               `json:"snippet_id,omitempty"`
	Reference  string              `json:"reference"`
	Operator   string              `json:"operator"`
	Reason     string              `json:"reason"`
	CapturedAt time.Time           `json:"captured_at"`
	Repair     *snippetRepairAudit `json:"repair,omitempty"`
}

func (q snippetQuarantine) summary() SnippetQuarantineSummary {
	return SnippetQuarantineSummary{ID: q.ID, Scope: q.Capture.Target.scope(), SnippetID: q.Capture.Target.SnippetID, Reference: q.Capture.Reference,
		Operator: q.Capture.Operator, Reason: q.Capture.Reason, CapturedAt: q.CapturedAt, Repair: q.Repair}
}

func decodeSnippetRepair(raw []byte) (SnippetRepairDocument, error) {
	doc, err := packagefs.DecodeJSON[SnippetRepairDocument](bytes.NewReader(raw), MaxSnippetRepairBytes, "snippet repair")
	if err != nil || doc.Schema != 1 || doc.Kind != "newpress.snippet-repair" || (doc.Snippet == nil) == (doc.Editorial == nil) ||
		len(doc.Receipts) > content.MaxSnippetReceipts || doc.Editorial != nil && len(doc.Receipts) != 0 {
		return SnippetRepairDocument{}, ErrSnippetMaintenanceInput
	}
	if doc.Editorial != nil {
		// Typed PostgreSQL timestamps have microsecond precision. Reject lossy
		// replacements and normalize equivalent offsets before hashing stored facts.
		if doc.Editorial.CreatedAt.Nanosecond()%1000 != 0 {
			return SnippetRepairDocument{}, ErrSnippetMaintenanceInput
		}
		doc.Editorial.CreatedAt = doc.Editorial.CreatedAt.UTC()
	}
	return doc, nil
}

func originalRevisionValid(b snippetRawBundle, rev content.SnippetRevision) bool {
	var meta content.Snippet
	if b.Metadata != nil {
		meta, _ = packagefs.DecodeJSON[content.Snippet](bytes.NewReader(b.Metadata.Data), 4096, "snippet")
	}
	if content.ValidateSnippetMetadata(meta) != nil {
		// Without trustworthy metadata, preserve every self-consistent body.
		// This deliberately refuses ambiguous changes that require a whole-site restore.
		meta = content.Snippet{ID: b.Target.SnippetID, Version: rev.Version, CurrentVersion: rev.Version, Title: rev.Title,
			Visibility: content.SnippetPrivate, CreatedAt: rev.CreatedAt, UpdatedAt: rev.CreatedAt}
	}
	return content.ValidateSnippetMetadata(meta) == nil && content.ValidateSnippetRevision(meta, rev) == nil
}

func buildSnippetRepair(b snippetRawBundle, doc SnippetRepairDocument, now time.Time) (snippetRawBundle, error) {
	if b.validateEnvelope() != nil || doc.Snippet == nil || doc.Editorial != nil || now.IsZero() {
		return snippetRawBundle{}, ErrSnippetMaintenanceInput
	}
	s := *doc.Snippet
	if s.ID != b.Target.SnippetID || content.ValidateSnippetRecord(s) != nil {
		return snippetRawBundle{}, ErrSnippetMaintenanceInput
	}
	// Validation already establishes strictly ascending immutable versions.
	revisions := make(map[int64]content.SnippetRevision, len(s.Revisions))
	for _, rev := range s.Revisions {
		revisions[rev.Version] = rev
	}
	maxVersion := s.Version
	if b.Metadata != nil {
		maxVersion = max(maxVersion, b.Metadata.Index.Version)
		if old, err := packagefs.DecodeJSON[content.Snippet](bytes.NewReader(b.Metadata.Data), 4096, "snippet"); err == nil {
			maxVersion = max(maxVersion, old.Version)
		}
	}
	result := snippetRawBundle{Target: b.Target}
	preservedRevisions := make(map[int64][]byte, len(b.Revisions))
	for _, row := range b.Revisions {
		candidate, ok := revisions[row.Index.Version]
		if !ok {
			return snippetRawBundle{}, ErrSnippetMaintenanceConflict
		}
		old, err := packagefs.DecodeJSON[content.SnippetRevision](bytes.NewReader(row.Data), 2<<20, "snippet revision")
		if err == nil && old.Version == row.Index.Version && originalRevisionValid(b, old) && !reflect.DeepEqual(old, candidate) {
			return snippetRawBundle{}, ErrSnippetMaintenanceConflict
		}
		if err == nil && reflect.DeepEqual(old, candidate) {
			preservedRevisions[row.Index.Version] = row.Data
		}
	}
	for _, rev := range s.Revisions {
		raw := preservedRevisions[rev.Version]
		if raw == nil {
			var err error
			raw, err = json.Marshal(rev)
			if err != nil {
				return snippetRawBundle{}, ErrSnippetMaintenanceInput
			}
		}
		result.Revisions = append(result.Revisions, snippetRawRow{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Version: rev.Version}, Data: raw})
	}
	// Every existing receipt identity remains present, even when its body needs repair.
	byKey := map[string]content.SnippetReceipt{}
	for _, receipt := range doc.Receipts {
		key := receipt.Identity + "\x00" + receipt.RequestKey
		if _, exists := byKey[key]; exists || content.ValidateSnippetReceipt(receipt) != nil || receipt.SnippetID != s.ID ||
			receipt.TargetVersion > s.CurrentVersion || receipt.SnippetVersion > s.Version || receipt.CreatedAt.Before(s.CreatedAt) {
			return snippetRawBundle{}, ErrSnippetMaintenanceInput
		}
		if _, exists := revisions[receipt.TargetVersion]; !exists {
			return snippetRawBundle{}, ErrSnippetMaintenanceInput
		}
		byKey[key] = receipt
	}
	preservedReceipts := make(map[string][]byte, len(b.Receipts))
	for _, row := range b.Receipts {
		candidate, exists := byKey[row.Index.Identity+"\x00"+row.Index.Key]
		if !exists {
			return snippetRawBundle{}, ErrSnippetMaintenanceConflict
		}
		old, err := packagefs.DecodeJSON[content.SnippetReceipt](bytes.NewReader(row.Data), 16384, "snippet receipt")
		if err == nil && content.ValidateSnippetReceipt(old) == nil {
			if !reflect.DeepEqual(old, candidate) {
				return snippetRawBundle{}, ErrSnippetMaintenanceConflict
			}
			maxVersion = max(maxVersion, old.SnippetVersion)
		}
		if err == nil && reflect.DeepEqual(old, candidate) {
			preservedReceipts[row.Index.Identity+"\x00"+row.Index.Key] = row.Data
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		receipt := byKey[key]
		raw := preservedReceipts[key]
		if raw == nil {
			var err error
			raw, err = json.Marshal(receipt)
			if err != nil {
				return snippetRawBundle{}, ErrSnippetMaintenanceInput
			}
		}
		result.Receipts = append(result.Receipts, snippetRawRow{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Identity: receipt.Identity, Key: receipt.RequestKey}, Data: raw})
	}
	if maxVersion >= math.MaxInt64 {
		return snippetRawBundle{}, content.ErrSnippetCapacity
	}
	s.Version, s.Visibility, s.SharedSince = maxVersion+1, content.SnippetWithdrawn, 0
	if now.After(s.UpdatedAt) {
		s.UpdatedAt = now
	}
	raw, err := json.Marshal(s.Snippet)
	if err != nil {
		return snippetRawBundle{}, ErrSnippetMaintenanceInput
	}
	result.Metadata = &snippetRawRow{Index: content.SnippetInspectionIndex{SnippetID: s.ID, Version: s.Version, Visibility: s.Visibility}, Data: raw}
	review, err := reviewSnippetBundle(result, 0, false)
	if err != nil || !review.Healthy {
		return snippetRawBundle{}, ErrSnippetMaintenanceInput
	}
	return result, nil
}
