// Package pluginshared defines private maintenance for shared authorization
// and retained operation records. Repairs preserve provable facts, never consent.
package pluginshared

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

const (
	Authority                = "authority"
	ContentEffect            = "content_effect"
	WebhookIntent            = "webhook_intent"
	MaxQuarantines           = 10000
	MaxQuarantineBytes       = 64 << 20
	MaxQuarantineRecordBytes = 256 << 10
	RepairReserve            = 8 << 10
	MaxPage                  = 1000
	MaxRawBytes              = plugin.MaxTaskControlBytes
)

var (
	ErrInvalid     = pluginjobs.ErrInvalid
	ErrConflict    = pluginjobs.ErrConflict
	ErrCapacity    = pluginjobs.ErrCapacity
	ErrQuarantined = pluginjobs.ErrQuarantined
)

type Record struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Data  []byte `json:"data"`
	Index []byte `json:"index"`
}

type Index struct {
	Revision int64  `json:"revision,omitempty"`
	Epoch    string `json:"epoch,omitempty"`
	Enabled  bool   `json:"enabled,omitempty"`
	Retired  bool   `json:"retired,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	Slot     string `json:"slot,omitempty"`
	Version  int64  `json:"version,omitempty"`
	State    string `json:"state,omitempty"`
}

type Diagnostic struct {
	Type          string                `json:"type"`
	Reference     string                `json:"reference"`
	Bytes         int                   `json:"bytes"`
	Fault         string                `json:"fault,omitempty"`
	IndexMismatch bool                  `json:"index_mismatch,omitempty"`
	Plugin        string                `json:"plugin,omitempty"`
	Revision      int64                 `json:"revision,omitempty"`
	Enabled       bool                  `json:"enabled,omitempty"`
	Retired       bool                  `json:"retired,omitempty"`
	JobID         string                `json:"job_id,omitempty"`
	Result        *plugineffects.Result `json:"result,omitempty"`
	Webhook       *WebhookResult        `json:"webhook,omitempty"`
}

// Maintenance projections deliberately omit URLs, body, slot, fingerprint,
// actor, authority and attempt credentials. Human review has a separate page.
type WebhookResult struct {
	OperationID string                 `json:"operation_id"`
	Version     int64                  `json:"version"`
	State       string                 `json:"state"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	ResultAt    time.Time              `json:"result_at,omitzero"`
	Outcome     plugindispatch.Outcome `json:"outcome"`
}

type Query struct {
	Type, Plugin, JobID string
	Limit, Offset       int
}
type Page struct {
	Items      []Diagnostic `json:"items"`
	Total      int          `json:"total"`
	NextOffset int          `json:"next_offset"`
	Fatal      string       `json:"fatal,omitempty"`
}
type RepairAudit struct {
	At            time.Time `json:"at"`
	Operator      string    `json:"operator"`
	Reason        string    `json:"reason"`
	InputDigest   string    `json:"input_sha256,omitempty"`
	KeptReference string    `json:"kept_reference,omitempty"`
	OutputDigest  string    `json:"output_sha256"`
	Disposition   string    `json:"disposition"`
}
type Quarantine struct {
	Schema     int          `json:"schema"`
	ID         string       `json:"id"`
	Reference  string       `json:"reference"`
	RequestKey string       `json:"request_key"`
	Original   Record       `json:"original"`
	Fault      string       `json:"fault"`
	CapturedAt time.Time    `json:"captured_at"`
	Operator   string       `json:"operator"`
	Reason     string       `json:"reason"`
	Repair     *RepairAudit `json:"repair,omitempty"`
}
type QuarantineSummary struct {
	ID         string       `json:"id"`
	Type       string       `json:"type"`
	Reference  string       `json:"reference"`
	Fault      string       `json:"fault"`
	Bytes      int          `json:"bytes"`
	CapturedAt time.Time    `json:"captured_at"`
	Operator   string       `json:"operator"`
	Reason     string       `json:"reason"`
	Repair     *RepairAudit `json:"repair,omitempty"`
}
type QuarantinePage struct {
	Items      []QuarantineSummary `json:"items"`
	Total      int                 `json:"total"`
	NextOffset int                 `json:"next_offset"`
}
type CaptureRequest struct{ Type, Reference, Key, Operator, Reason string }
type RepairRequest struct {
	ID, Operator, Reason, ReplacementDigest, KeepReference string
	Replacement                                            []byte
}
type Reader interface {
	InspectSharedRecords(context.Context, Query) (Page, error)
	ListSharedQuarantines(context.Context, Query) (QuarantinePage, error)
}
type Store interface {
	Reader
	QuarantineSharedRecord(context.Context, CaptureRequest, time.Time) (QuarantineSummary, error)
	RepairSharedRecord(context.Context, RepairRequest, time.Time) (QuarantineSummary, error)
}

func ValidType(kind string) bool {
	return kind == Authority || kind == ContentEffect || kind == WebhookIntent
}
func Reference(r Record) string { b, _ := json.Marshal(r); return plugineffects.Digest(b) }
func cleanText(s string, limit int) bool {
	if s == "" || strings.TrimSpace(s) != s || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func NormalizeQuery(q Query) (Query, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Type != "" && !ValidType(q.Type) || q.Plugin != "" && packagefs.ValidateSlug(q.Plugin) != nil || q.JobID != "" && !plugineffects.IsDigest(q.JobID) || q.Limit < 1 || q.Limit > MaxPage || q.Offset < 0 || q.Offset > plugin.MaxTaskControls+plugineffects.MaxReceipts+plugindispatch.MaxRecords+MaxQuarantines {
		return Query{}, ErrInvalid
	}
	return q, nil
}
func ValidateCapture(r CaptureRequest) error {
	if !ValidType(r.Type) || !plugineffects.IsDigest(r.Reference) || !cleanText(r.Key, 256) || !cleanText(r.Operator, 128) || !cleanText(r.Reason, 1024) {
		return ErrInvalid
	}
	return nil
}
func CaptureID(r CaptureRequest) string {
	b, _ := json.Marshal([]string{"shared-record-quarantine-v1", r.Type, r.Key})
	return plugineffects.Digest(b)
}
func ValidateRepair(r RepairRequest) error {
	if !plugineffects.IsDigest(r.ID) || !cleanText(r.Operator, 128) || !cleanText(r.Reason, 1024) || len(r.Replacement) > MaxRawBytes {
		return ErrInvalid
	}
	if len(r.Replacement) == 0 && r.ReplacementDigest != "" || len(r.Replacement) > 0 && plugineffects.Digest(r.Replacement) != r.ReplacementDigest {
		return ErrInvalid
	}
	if r.KeepReference != "" && (!plugineffects.IsDigest(r.KeepReference) || len(r.Replacement) > 0 || r.ReplacementDigest != "") {
		return ErrInvalid
	}
	return nil
}

type decoded struct {
	control *plugin.TaskControl
	effect  *plugineffects.Receipt
	webhook *plugindispatch.Intent
}

func decode(kind string, raw []byte, loose bool) (decoded, error) {
	if len(raw) > MaxRawBytes {
		return decoded{}, ErrInvalid
	}
	var err error
	switch kind {
	case Authority:
		var c plugin.TaskControl
		if loose {
			err = packagefs.ValidateUniqueJSONFor(raw, &c)
			if err == nil {
				err = json.Unmarshal(raw, &c)
			}
		} else {
			c, err = packagefs.DecodeJSON[plugin.TaskControl](bytes.NewReader(raw), plugin.MaxTaskControlBytes, "shared authority")
		}
		if err != nil || plugin.ValidateTaskControl(c) != nil {
			return decoded{}, ErrInvalid
		}
		if _, err = plugin.TaskControlBudgetBytes(c); err != nil {
			return decoded{}, ErrInvalid
		}
		return decoded{control: &c}, nil
	case ContentEffect:
		var e plugineffects.Receipt
		if loose {
			err = packagefs.ValidateUniqueJSONFor(raw, &e)
			if err == nil {
				err = json.Unmarshal(raw, &e)
			}
		} else {
			e, err = packagefs.DecodeJSON[plugineffects.Receipt](bytes.NewReader(raw), plugineffects.MaxReceiptBytes, "content receipt")
		}
		if err != nil || plugineffects.ValidateReceipt(e) != nil {
			return decoded{}, ErrInvalid
		}
		return decoded{effect: &e}, nil
	case WebhookIntent:
		var i plugindispatch.Intent
		if loose {
			err = packagefs.ValidateUniqueJSONFor(raw, &i)
			if err == nil {
				err = json.Unmarshal(raw, &i)
			}
		} else {
			i, err = packagefs.DecodeJSON[plugindispatch.Intent](bytes.NewReader(raw), plugindispatch.MaxRecordBytes, "webhook intent")
		}
		if err != nil || plugindispatch.Validate(i) != nil {
			return decoded{}, ErrInvalid
		}
		return decoded{webhook: &i}, nil
	}
	return decoded{}, ErrInvalid
}
func (d decoded) key() string {
	if d.control != nil {
		return d.control.Slug
	}
	if d.webhook != nil {
		return d.webhook.ID
	}
	return d.effect.Result.OperationID
}
func (d decoded) plugin() string {
	if d.control != nil {
		return d.control.Slug
	}
	if d.webhook != nil {
		return d.webhook.Plugin
	}
	return d.effect.Plugin
}
func (d decoded) index() Index {
	if c := d.control; c != nil {
		return Index{Revision: c.Revision, Epoch: c.Epoch.Token, Enabled: c.Enabled, Retired: c.Retired}
	}
	if i := d.webhook; i != nil {
		return Index{JobID: i.JobID, Slot: i.Slot, Version: i.Version, State: i.State}
	}
	return Index{JobID: d.effect.JobID, Slot: d.effect.Slot}
}
func indexMatches(r Record, d decoded) bool {
	i, err := packagefs.DecodeJSON[Index](bytes.NewReader(r.Index), 4096, "shared record index")
	return err == nil && r.Key == d.key() && i == d.index()
}
func Diagnose(r Record) Diagnostic {
	out := Diagnostic{Type: r.Type, Reference: Reference(r), Bytes: len(r.Data)}
	d, err := decode(r.Type, r.Data, false)
	if err != nil {
		out.Fault = "invalid_data"
		return out
	}
	if !indexMatches(r, d) {
		out.Fault = "index_mismatch"
		out.IndexMismatch = true
	}
	out.Plugin = d.plugin()
	if c := d.control; c != nil {
		out.Revision, out.Enabled, out.Retired = c.Revision, c.Enabled, c.Retired
	} else if i := d.webhook; i != nil {
		out.JobID = i.JobID
		out.Webhook = &WebhookResult{i.ID, i.Version, i.State, i.CreatedAt, i.UpdatedAt, i.ResultAt, i.Outcome}
	} else {
		result := d.effect.Result
		out.Result = &result
		out.JobID = d.effect.JobID
	}
	return out
}
func DiagnoseSet(records []Record) []Diagnostic {
	out := make([]Diagnostic, len(records))
	identities := map[string][]int{}
	for i, r := range records {
		out[i] = Diagnose(r)
		if d, e := decode(r.Type, r.Data, false); e == nil {
			key := r.Type + ":" + d.key()
			identities[key] = append(identities[key], i)
		}
	}
	for _, indexes := range identities {
		if len(indexes) > 1 {
			for _, i := range indexes {
				out[i].Fault = "duplicate_identity"
			}
		}
	}
	return out
}
func DiagnoseWithQuarantines(records []Record, quarantines []Quarantine) []Diagnostic {
	all := slices.Clone(records)
	for _, q := range quarantines {
		if q.Repair == nil {
			all = append(all, q.Original)
		}
	}
	return DiagnoseSet(all)[:len(records)]
}
func Paginate(records []Record, q Query) Page {
	return PaginateDiagnostics(DiagnoseSet(records), q)
}
func PaginateDiagnostics(all []Diagnostic, q Query) Page {
	items := []Diagnostic{}
	for _, d := range all {
		if q.Type != "" && q.Type != d.Type || q.Plugin != "" && q.Plugin != d.Plugin || q.JobID != "" && q.JobID != d.JobID {
			continue
		}
		items = append(items, d)
	}
	slices.SortFunc(items, func(a, b Diagnostic) int { return strings.Compare(a.Type+":"+a.Reference, b.Type+":"+b.Reference) })
	p := Page{Items: []Diagnostic{}, Total: len(items), NextOffset: -1}
	start := min(q.Offset, len(items))
	end := min(start+q.Limit, len(items))
	p.Items = items[start:end]
	if end < len(items) {
		p.NextOffset = end
	}
	return p
}
func Capture(r Record, fault string, request CaptureRequest, at time.Time) (Quarantine, error) {
	if ValidateCapture(request) != nil || r.Type != request.Type || Reference(r) != request.Reference {
		return Quarantine{}, ErrInvalid
	}
	r.Data = slices.Clone(r.Data)
	r.Index = slices.Clone(r.Index)
	q := Quarantine{Schema: 1, ID: CaptureID(request), Reference: request.Reference, RequestKey: request.Key, Original: r, Fault: fault, CapturedAt: at.UTC(), Operator: request.Operator, Reason: request.Reason}
	return q, ValidateQuarantine(q)
}
func MatchCapture(q Quarantine, r CaptureRequest) error {
	if ValidateCapture(r) != nil || q.ID != CaptureID(r) || q.Reference != r.Reference || q.RequestKey != r.Key || q.Original.Type != r.Type || q.Operator != r.Operator || q.Reason != r.Reason {
		return ErrConflict
	}
	return nil
}
func MatchRepair(q Quarantine, r RepairRequest) error {
	a := q.Repair
	if ValidateRepair(r) != nil || a == nil || q.ID != r.ID || a.Operator != r.Operator || a.Reason != r.Reason || a.InputDigest != r.ReplacementDigest || a.KeptReference != r.KeepReference {
		return ErrConflict
	}
	return nil
}
func ValidateQuarantine(q Quarantine) error {
	if q.Schema != 1 || !ValidType(q.Original.Type) || len(q.Original.Data) > MaxRawBytes || len(q.Original.Index) > 4096 || len(q.Original.Key) > 4096 || !utf8.ValidString(q.Original.Key) || q.CapturedAt.IsZero() || q.Reference != Reference(q.Original) || q.ID != CaptureID(CaptureRequest{Type: q.Original.Type, Key: q.RequestKey}) || !cleanText(q.RequestKey, 256) || !cleanText(q.Operator, 128) || !cleanText(q.Reason, 1024) {
		return ErrInvalid
	}
	if q.Fault != "invalid_data" && q.Fault != "index_mismatch" && q.Fault != "duplicate_identity" {
		return ErrInvalid
	}
	if a := q.Repair; a != nil {
		if a.At.Before(q.CapturedAt) || !cleanText(a.Operator, 128) || !cleanText(a.Reason, 1024) || !plugineffects.IsDigest(a.OutputDigest) || a.InputDigest != "" && !plugineffects.IsDigest(a.InputDigest) {
			return ErrInvalid
		}
		allowed := map[string][]string{
			Authority:     {"authority_retired", "duplicate_authority_retired"},
			ContentEffect: {"receipt_preserved", "duplicate_preserved"},
			WebhookIntent: {"webhook_cancelled", "webhook_uncertain", "webhook_preserved", "duplicate_webhook_preserved"},
		}
		if !slices.Contains(allowed[q.Original.Type], a.Disposition) {
			return ErrInvalid
		}
		if strings.HasPrefix(a.Disposition, "duplicate_") {
			if !plugineffects.IsDigest(a.KeptReference) || a.InputDigest != "" || a.OutputDigest != a.KeptReference {
				return ErrInvalid
			}
		} else if a.KeptReference != "" {
			return ErrInvalid
		}
	}
	return nil
}
func Summarize(q Quarantine) QuarantineSummary {
	var audit *RepairAudit
	if q.Repair != nil {
		copy := *q.Repair
		audit = &copy
	}
	return QuarantineSummary{ID: q.ID, Type: q.Original.Type, Reference: q.Reference, Fault: q.Fault, Bytes: len(q.Original.Data), CapturedAt: q.CapturedAt, Operator: q.Operator, Reason: q.Reason, Repair: audit}
}
func PaginateQuarantines(records []Quarantine, q Query) QuarantinePage {
	items := []QuarantineSummary{}
	for _, r := range records {
		if q.Type != "" && q.Type != r.Original.Type {
			continue
		}
		if q.Plugin != "" || q.JobID != "" {
			d := Diagnose(r.Original)
			if q.Plugin != "" && q.Plugin != d.Plugin || q.JobID != "" && q.JobID != d.JobID {
				continue
			}
		}
		items = append(items, Summarize(r))
	}
	slices.SortFunc(items, func(a, b QuarantineSummary) int { return strings.Compare(a.ID, b.ID) })
	p := QuarantinePage{Items: []QuarantineSummary{}, Total: len(items), NextOffset: -1}
	start := min(q.Offset, len(items))
	end := min(start+q.Limit, len(items))
	p.Items = items[start:end]
	if end < len(items) {
		p.NextOffset = end
	}
	return p
}

// A replacement may remove unknown fields, but every decoded domain fact must
// still be valid and identical. Missing/ambiguous identity or receipt results
// require a complete verified backup; SQL keys alone cannot prove those facts.
func BuildRepair(q Quarantine, r RepairRequest, now time.Time) (Record, RepairAudit, error) {
	if ValidateQuarantine(q) != nil || ValidateRepair(r) != nil || q.ID != r.ID || now.IsZero() || now.Before(q.CapturedAt) || r.KeepReference != "" {
		return Record{}, RepairAudit{}, ErrInvalid
	}
	original, err := decode(q.Original.Type, q.Original.Data, true)
	if err != nil {
		return Record{}, RepairAudit{}, ErrConflict
	}
	raw := q.Original.Data
	if len(r.Replacement) > 0 {
		raw = r.Replacement
	}
	candidate, err := decode(q.Original.Type, raw, false)
	if err != nil || !reflect.DeepEqual(original, candidate) {
		return Record{}, RepairAudit{}, ErrConflict
	}
	disposition := "receipt_preserved"
	if c := candidate.control; c != nil {
		if c.Revision == math.MaxInt64 {
			return Record{}, RepairAudit{}, ErrConflict
		}
		c.Revision++
		c.Enabled, c.Retired, c.TransitionID, c.TransitionBytes = false, true, "", 0
		c.Epoch, err = plugin.NewTaskEpoch(0)
		if err != nil {
			return Record{}, RepairAudit{}, err
		}
		raw, err = json.Marshal(c)
		if err != nil {
			return Record{}, RepairAudit{}, err
		}
		disposition = "authority_retired"
	}
	if i := candidate.webhook; i != nil {
		disposition = "webhook_preserved"
		switch i.State {
		case "pending":
			disposition = "webhook_cancelled"
		case "sending":
			disposition = "webhook_uncertain"
		}
		if disposition != "webhook_preserved" {
			*i, err = plugindispatch.SuppressForMaintenance(*i, now)
			if err != nil {
				return Record{}, RepairAudit{}, ErrConflict
			}
			raw, err = json.Marshal(i)
			if err != nil {
				return Record{}, RepairAudit{}, err
			}
		}
	}
	index, _ := json.Marshal(candidate.index())
	out := Record{Type: q.Original.Type, Key: candidate.key(), Data: slices.Clone(raw), Index: index}
	if Diagnose(out).Fault != "" {
		return Record{}, RepairAudit{}, ErrInvalid
	}
	audit := RepairAudit{At: now.UTC(), Operator: r.Operator, Reason: r.Reason, InputDigest: r.ReplacementDigest, OutputDigest: Reference(out), Disposition: disposition}
	return out, audit, nil
}
func ValidateRepairTarget(out Record, records []Record) error {
	d, err := decode(out.Type, out.Data, false)
	if err != nil || Diagnose(out).Fault != "" {
		return ErrInvalid
	}
	for _, old := range records {
		if old.Type != out.Type {
			continue
		}
		other, e := decode(old.Type, old.Data, true)
		if old.Key == out.Key || e != nil || other.key() == d.key() {
			return ErrConflict
		}
		if d.effect != nil && other.effect.JobID == d.effect.JobID && other.effect.Slot == d.effect.Slot {
			return ErrConflict
		}
		if d.webhook != nil && other.webhook.JobID == d.webhook.JobID && other.webhook.Slot == d.webhook.Slot {
			return ErrConflict
		}
	}
	return nil
}
func Reconcile(q Quarantine, r RepairRequest, kept Record, now time.Time) (RepairAudit, error) {
	if ValidateQuarantine(q) != nil || ValidateRepair(r) != nil || q.ID != r.ID || q.Original.Type != kept.Type || r.KeepReference != Reference(kept) || Diagnose(kept).Fault != "" || now.IsZero() || now.Before(q.CapturedAt) {
		return RepairAudit{}, ErrConflict
	}
	a, ea := decode(q.Original.Type, q.Original.Data, true)
	b, eb := decode(kept.Type, kept.Data, false)
	if ea != nil || eb != nil {
		return RepairAudit{}, ErrConflict
	}
	disposition := "duplicate_preserved"
	if a.control != nil {
		old, current := *a.control, *b.control
		if current.Enabled || !current.Retired || current.TransitionID != "" || current.Epoch.ActorID != 0 || current.Revision <= old.Revision || current.Epoch.Token == old.Epoch.Token {
			return RepairAudit{}, ErrConflict
		}
		// Only host lifetime fields may differ from the original authority.
		old.Revision, old.Epoch, old.Enabled, old.Retired, old.TransitionID, old.TransitionBytes = current.Revision, current.Epoch, current.Enabled, current.Retired, current.TransitionID, current.TransitionBytes
		if !reflect.DeepEqual(old, current) {
			return RepairAudit{}, ErrConflict
		}
		disposition = "duplicate_authority_retired"
	} else if a.webhook != nil {
		// An active duplicate may only reconcile to the same operation already
		// suppressed by maintenance. Never keep a pending/sending candidate.
		expected, err := plugindispatch.SuppressForMaintenance(*a.webhook, b.webhook.UpdatedAt)
		if err != nil || !reflect.DeepEqual(expected, *b.webhook) {
			return RepairAudit{}, ErrConflict
		}
		disposition = "duplicate_webhook_preserved"
	} else if !reflect.DeepEqual(a, b) {
		return RepairAudit{}, ErrConflict
	}
	return RepairAudit{At: now.UTC(), Operator: r.Operator, Reason: r.Reason, KeptReference: r.KeepReference, OutputDigest: Reference(kept), Disposition: disposition}, nil
}
