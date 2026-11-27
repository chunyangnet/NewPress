package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxSnippetFileQuarantines   = 1000
	MaxSnippetFileEvidenceBytes = 64 << 20
	MaxSnippetFileRepairBytes   = 48 << 20
	snippetFileRepairReserve    = 8192
)

var (
	ErrSnippetFileMaintenanceInput      = errors.New("invalid file snippet maintenance request")
	ErrSnippetFileMaintenanceConflict   = errors.New("file snippet maintenance identity or original evidence changed")
	ErrSnippetFileMaintenanceEvidence   = errors.New("file snippet maintenance evidence cannot be verified")
	ErrSnippetFileMaintenanceIncomplete = errors.New("content file still has invalid canonical records; quarantine remains unresolved")
)

type SnippetFileTarget struct {
	SnippetID    int64  `json:"snippet_id,omitempty"`
	EditorialKey string `json:"editorial_key,omitempty"`
}

func (t SnippetFileTarget) valid() bool {
	return t.SnippetID > 0 && t.SnippetID < math.MaxInt64 && t.EditorialKey == "" || t.SnippetID == 0 && validSubmissionDigest(t.EditorialKey)
}

func (t SnippetFileTarget) scope() string {
	if t.SnippetID != 0 {
		return "snippet"
	}
	return "editorial_receipt"
}

type SnippetFileCaptureRequest struct {
	Target     SnippetFileTarget `json:"target"`
	Reference  string            `json:"reference"`
	RequestKey string            `json:"request_key"`
	Operator   string            `json:"operator"`
	Reason     string            `json:"reason"`
}

func fileMaintenanceText(s string, limit int) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (r SnippetFileCaptureRequest) valid() bool {
	return r.Target.valid() && validSubmissionDigest(r.Reference) && validSubmissionDigest(r.RequestKey) && fileMaintenanceText(r.Operator, 160) && fileMaintenanceText(r.Reason, 1000)
}

func fileSnippetDigest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func (r SnippetFileCaptureRequest) id() string {
	return fileSnippetDigest([]byte("newpress-snippet-file-quarantine\x00" + r.RequestKey))
}

type SnippetFileRepairRequest struct {
	ID                string
	Operator, Reason  string
	Replacement       []byte
	ReplacementSHA256 string
}

func (r SnippetFileRepairRequest) valid() bool {
	return validSubmissionDigest(r.ID) && fileMaintenanceText(r.Operator, 160) && fileMaintenanceText(r.Reason, 1000) &&
		len(r.Replacement) > 0 && len(r.Replacement) <= MaxSnippetFileRepairBytes && r.ReplacementSHA256 == fileSnippetDigest(r.Replacement)
}

type SnippetFileRepairDocument struct {
	Schema    int                `json:"schema"`
	Kind      string             `json:"kind"`
	Snippet   *SnippetRecord     `json:"snippet,omitempty"`
	Receipts  []SnippetReceipt   `json:"update_receipts,omitempty"`
	Editorial *SubmissionReceipt `json:"editorial_receipt,omitempty"`
}

type SnippetFileRepairAudit struct {
	Operator          string    `json:"operator"`
	Reason            string    `json:"reason"`
	At                time.Time `json:"at"`
	ReplacementSHA256 string    `json:"replacement_sha256"`
	ResultReference   string    `json:"result_reference"`
	SnippetVersion    int64     `json:"snippet_version,omitempty"`
}

// Byte slices deliberately preserve the original JSON, including whitespace and
// invalid field types. The outer content document must still be unambiguous JSON.
type snippetFileBundle struct {
	Target    SnippetFileTarget `json:"target"`
	Snippet   []byte            `json:"snippet,omitempty"`
	Receipts  [][]byte          `json:"update_receipts,omitempty"`
	Editorial []byte            `json:"editorial_receipt,omitempty"`
}

func (b snippetFileBundle) reference() string {
	raw, _ := json.Marshal(b)
	return fileSnippetDigest(append([]byte("newpress-snippet-file-records-v1\x00"), raw...))
}

type SnippetFileQuarantine struct {
	ID         string                    `json:"id"`
	Capture    SnippetFileCaptureRequest `json:"capture"`
	CapturedAt time.Time                 `json:"captured_at"`
	Original   snippetFileBundle         `json:"original"`
	Repair     *SnippetFileRepairAudit   `json:"repair,omitempty"`
}

type SnippetFileQuarantineSummary struct {
	ID         string                  `json:"id"`
	Scope      string                  `json:"scope"`
	SnippetID  int64                   `json:"snippet_id,omitempty"`
	Reference  string                  `json:"reference"`
	Operator   string                  `json:"operator"`
	Reason     string                  `json:"reason"`
	CapturedAt time.Time               `json:"captured_at"`
	Repair     *SnippetFileRepairAudit `json:"repair,omitempty"`
}

func (q SnippetFileQuarantine) summary() SnippetFileQuarantineSummary {
	return SnippetFileQuarantineSummary{ID: q.ID, Scope: q.Capture.Target.scope(), SnippetID: q.Capture.Target.SnippetID, Reference: q.Capture.Reference,
		Operator: q.Capture.Operator, Reason: q.Capture.Reason, CapturedAt: q.CapturedAt, Repair: q.Repair}
}

type SnippetFileQuarantinePage struct {
	Backend        string                         `json:"backend"`
	SnapshotSHA256 string                         `json:"snapshot_sha256"`
	Items          []SnippetFileQuarantineSummary `json:"items"`
	Total          int                            `json:"total"`
	Unresolved     int                            `json:"unresolved"`
	NextOffset     int                            `json:"next_offset"`
}

type SnippetFileMaintenanceReview struct {
	Backend        string   `json:"backend"`
	Scope          string   `json:"scope"`
	SnippetID      int64    `json:"snippet_id,omitempty"`
	Reference      string   `json:"reference"`
	SnapshotSHA256 string   `json:"snapshot_sha256"`
	Healthy        bool     `json:"healthy"`
	Faults         []string `json:"faults"`
}

func snippetFileEvidenceBudget(all []SnippetFileQuarantine) (int, error) {
	if len(all) > MaxSnippetFileQuarantines {
		return 0, ErrSnippetCapacity
	}
	seen, active := map[string]bool{}, map[SnippetFileTarget]bool{}
	reserve := 0
	for _, q := range all {
		if !q.Capture.valid() || q.ID != q.Capture.id() || seen[q.ID] || q.CapturedAt.IsZero() || q.Original.Target != q.Capture.Target ||
			q.Original.reference() != q.Capture.Reference || q.Original.validate() != nil {
			return 0, ErrSnippetFileMaintenanceEvidence
		}
		seen[q.ID] = true
		encoded, err := json.Marshal(q)
		if err != nil {
			return 0, ErrSnippetFileMaintenanceEvidence
		}
		required := len(encoded)
		if a := q.Repair; a != nil {
			if !fileMaintenanceText(a.Operator, 160) || !fileMaintenanceText(a.Reason, 1000) || a.At.Before(q.CapturedAt) ||
				!validSubmissionDigest(a.ReplacementSHA256) || !validSubmissionDigest(a.ResultReference) ||
				(q.Capture.Target.SnippetID != 0 && a.SnippetVersion <= 0) || (q.Capture.Target.SnippetID == 0 && a.SnippetVersion != 0) {
				return 0, ErrSnippetFileMaintenanceEvidence
			}
		} else {
			if active[q.Capture.Target] {
				return 0, ErrSnippetFileMaintenanceEvidence
			}
			active[q.Capture.Target] = true
			reserve += snippetFileRepairReserve
			required += snippetFileRepairReserve
		}
		if required > MaxSnippetFileRepairBytes {
			return 0, ErrSnippetCapacity
		}
	}
	raw, err := json.Marshal(all)
	if err != nil || len(raw)+reserve > MaxSnippetFileEvidenceBytes {
		return 0, ErrSnippetCapacity
	}
	return reserve, nil
}

func decodeSnippetFileRepair(raw []byte) (SnippetFileRepairDocument, error) {
	doc, err := packagefs.DecodeJSON[SnippetFileRepairDocument](bytes.NewReader(raw), MaxSnippetFileRepairBytes, "file snippet repair")
	if err != nil || doc.Schema != 1 || doc.Kind != "newpress.snippet-file-repair" || (doc.Snippet == nil) == (doc.Editorial == nil) ||
		len(doc.Receipts) > MaxSnippetReceipts || doc.Editorial != nil && len(doc.Receipts) != 0 {
		return SnippetFileRepairDocument{}, ErrSnippetFileMaintenanceInput
	}
	return doc, nil
}
