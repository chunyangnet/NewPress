package pluginjobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxQuarantineRecords = 10000
	MaxQuarantineBytes   = 64 << 20
	MaxMaintenancePage   = 1000
	// Reserve an audit so a full evidence budget can still be reconciled.
	QuarantineRepairReserveBytes = 4096
	quarantineSchema             = 1
)

var ErrQuarantined = errors.New("extension records require quarantine maintenance")

type RecordType string

const (
	RecordTask          RecordType = "task"
	RecordTaskHistory   RecordType = "task_history"
	RecordFanout        RecordType = "fanout"
	RecordFanoutHistory RecordType = "fanout_history"
)

func (t RecordType) Valid() bool {
	return t == RecordTask || t == RecordTaskHistory || t == RecordFanout || t == RecordFanoutHistory
}

func (t RecordType) IsTask() bool { return t == RecordTask || t == RecordTaskHistory }

// StoredRecord is private evidence. Binary fields preserve damaged JSON without
// making it part of the executable state's JSON document. Metadata is the SQL
// index snapshot; it is nil for file records.
type StoredRecord struct {
	Type     RecordType `json:"type"`
	Key      string     `json:"key"`
	Data     []byte     `json:"data"`
	Metadata []byte     `json:"metadata,omitempty"`
}

func RecordReference(r StoredRecord) string {
	raw, _ := json.Marshal(r)
	return hash(raw)
}

type RepairAudit struct {
	At            time.Time `json:"at"`
	Operator      string    `json:"operator"`
	Reason        string    `json:"reason"`
	Replacement   string    `json:"replacement_sha256"`
	InputDigest   string    `json:"input_sha256,omitempty"`
	KeptReference string    `json:"kept_reference,omitempty"`
	Disposition   string    `json:"disposition"`
	RestoredKeyID string    `json:"restored_key_hash"`
}

type QuarantinedRecord struct {
	Schema     int          `json:"schema"`
	ID         string       `json:"id"`
	Reference  string       `json:"reference"`
	RequestKey string       `json:"request_key"`
	Original   StoredRecord `json:"original"`
	Fault      string       `json:"fault"`
	CapturedAt time.Time    `json:"captured_at"`
	Operator   string       `json:"operator"`
	Reason     string       `json:"reason"`
	Repair     *RepairAudit `json:"repair,omitempty"`
}

// Maintenance output carries references and fixed fault codes, never the raw
// record, original request keys, payload, epoch, fingerprint or SQL metadata.
type RecordDiagnostic struct {
	Reference     string     `json:"reference"`
	Type          RecordType `json:"type"`
	Bytes         int        `json:"bytes"`
	Fault         string     `json:"fault,omitempty"`
	IndexMismatch bool       `json:"index_mismatch,omitempty"`
	TaskID        string     `json:"task_id,omitempty"`
	Task          *Summary   `json:"task,omitempty"`
}

type QuarantineSummary struct {
	ID         string       `json:"id"`
	Reference  string       `json:"reference"`
	Type       RecordType   `json:"type"`
	Fault      string       `json:"fault"`
	Bytes      int          `json:"bytes"`
	CapturedAt time.Time    `json:"captured_at"`
	Operator   string       `json:"operator"`
	Reason     string       `json:"reason"`
	Repair     *RepairAudit `json:"repair,omitempty"`
}

type DiagnosticQuery struct {
	Type   RecordType
	Offset int
	Limit  int
}

type DiagnosticPage struct {
	Items          []RecordDiagnostic `json:"items"`
	Total          int                `json:"total"`
	NextOffset     int                `json:"next_offset"`
	SnapshotDigest string             `json:"snapshot_sha256,omitempty"`
	Fatal          string             `json:"fatal,omitempty"`
}

type QuarantinePage struct {
	Items      []QuarantineSummary `json:"items"`
	Total      int                 `json:"total"`
	NextOffset int                 `json:"next_offset"`
}

type QuarantineRequest struct {
	Reference      string
	Key            string
	Type           RecordType
	Operator       string
	Reason         string
	SnapshotDigest string
}

type RepairRequest struct {
	ID                string
	Operator          string
	Reason            string
	Replacement       []byte
	ReplacementDigest string
	KeepReference     string
	SnapshotDigest    string
}

type MaintenanceReader interface {
	InspectRecords(context.Context, DiagnosticQuery) (DiagnosticPage, error)
	ListQuarantines(context.Context, DiagnosticQuery) (QuarantinePage, error)
}

type MaintenanceStore interface {
	MaintenanceReader
	QuarantineRecord(context.Context, QuarantineRequest, time.Time) (QuarantineSummary, error)
	RepairRecord(context.Context, RepairRequest, time.Time) (QuarantineSummary, error)
}

func NormalizeDiagnosticQuery(q DiagnosticQuery) (DiagnosticQuery, error) {
	if q.Type != "" && !q.Type.Valid() || q.Offset < 0 || q.Offset > MaxJobs+MaxFanouts+MaxArchivedJobs+MaxArchivedFanouts+MaxQuarantineRecords {
		return DiagnosticQuery{}, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > MaxMaintenancePage {
		return DiagnosticQuery{}, ErrInvalid
	}
	return q, nil
}

func maintenanceOperator(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return "", ErrInvalid
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", ErrInvalid
		}
	}
	return value, nil
}

func PrepareQuarantineRequest(r QuarantineRequest) (QuarantineRequest, error) {
	if !digest.MatchString(r.Reference) || !r.Type.Valid() || r.SnapshotDigest != "" && !digest.MatchString(r.SnapshotDigest) {
		return QuarantineRequest{}, ErrInvalid
	}
	if r.Key == "" {
		r.Key = r.Reference
	}
	if len(r.Key) > 256 || !utf8.ValidString(r.Key) {
		return QuarantineRequest{}, ErrInvalid
	}
	var err error
	if r.Operator, err = maintenanceOperator(r.Operator); err != nil {
		return QuarantineRequest{}, err
	}
	if r.Reason, err = reviewReason(r.Reason); err != nil {
		return QuarantineRequest{}, err
	}
	return r, nil
}

func PrepareRepairRequest(r RepairRequest) (RepairRequest, error) {
	if !digest.MatchString(r.ID) || len(r.Replacement) > 512<<10 || r.SnapshotDigest != "" && !digest.MatchString(r.SnapshotDigest) {
		return RepairRequest{}, ErrInvalid
	}
	if r.KeepReference != "" && (!digest.MatchString(r.KeepReference) || len(r.Replacement) > 0 || r.ReplacementDigest != "") {
		return RepairRequest{}, ErrInvalid
	}
	var err error
	if r.Operator, err = maintenanceOperator(r.Operator); err != nil {
		return RepairRequest{}, err
	}
	if r.Reason, err = reviewReason(r.Reason); err != nil {
		return RepairRequest{}, err
	}
	if len(r.Replacement) == 0 {
		if r.ReplacementDigest != "" {
			return RepairRequest{}, ErrInvalid
		}
	} else if !digest.MatchString(r.ReplacementDigest) || hash(r.Replacement) != r.ReplacementDigest {
		return RepairRequest{}, ErrConflict
	}
	return r, nil
}

func CaptureQuarantine(r StoredRecord, fault string, request QuarantineRequest, now time.Time) (QuarantinedRecord, error) {
	request, err := PrepareQuarantineRequest(request)
	if err != nil || now.IsZero() || r.Type != request.Type || RecordReference(r) != request.Reference {
		return QuarantinedRecord{}, ErrInvalid
	}
	q := QuarantinedRecord{Schema: quarantineSchema, ID: QuarantineRequestID(request), Reference: request.Reference, RequestKey: request.Key, Original: CloneStoredRecord(r), Fault: fault,
		CapturedAt: now.UTC(), Operator: request.Operator, Reason: request.Reason}
	return q, ValidateQuarantine(q)
}

func QuarantineRequestID(r QuarantineRequest) string {
	if r.Key == "" {
		r.Key = r.Reference
	}
	raw, _ := json.Marshal([]string{string(r.Type), r.Key})
	return hash(raw)
}

func MatchQuarantineRequest(q QuarantinedRecord, r QuarantineRequest) error {
	if q.ID != QuarantineRequestID(r) || q.Reference != r.Reference || q.RequestKey != r.Key || q.Original.Type != r.Type ||
		q.Operator != r.Operator || q.Reason != r.Reason {
		return ErrConflict
	}
	return nil
}

func MatchRepairRequest(q QuarantinedRecord, r RepairRequest) error {
	if q.Repair == nil || q.ID != r.ID || q.Repair.Operator != r.Operator || q.Repair.Reason != r.Reason || q.Repair.InputDigest != r.ReplacementDigest || q.Repair.KeptReference != r.KeepReference {
		return ErrConflict
	}
	return nil
}

func CloneStoredRecord(r StoredRecord) StoredRecord {
	r.Data = slices.Clone(r.Data)
	r.Metadata = slices.Clone(r.Metadata)
	return r
}

func CloneQuarantine(r QuarantinedRecord) QuarantinedRecord {
	r.Original = CloneStoredRecord(r.Original)
	if r.Repair != nil {
		copy := *r.Repair
		r.Repair = &copy
	}
	return r
}

func SummarizeQuarantine(r QuarantinedRecord) QuarantineSummary {
	var repair *RepairAudit
	if r.Repair != nil {
		copy := *r.Repair
		repair = &copy
	}
	return QuarantineSummary{ID: r.ID, Reference: r.Reference, Type: r.Original.Type, Fault: r.Fault, Bytes: len(r.Original.Data),
		CapturedAt: r.CapturedAt, Operator: r.Operator, Reason: r.Reason, Repair: repair}
}

func ValidateQuarantine(r QuarantinedRecord) error {
	if r.Schema != quarantineSchema || !digest.MatchString(r.ID) || !r.Original.Type.Valid() ||
		!utf8.ValidString(r.Original.Key) || len(r.Original.Key) > 4096 ||
		len(r.Original.Data) > MaxStateBytes || len(r.Original.Metadata) > 4096 || r.CapturedAt.IsZero() ||
		!digest.MatchString(r.Reference) || RecordReference(r.Original) != r.Reference || r.RequestKey == "" ||
		len(r.RequestKey) > 256 || !utf8.ValidString(r.RequestKey) || QuarantineRequestID(QuarantineRequest{Type: r.Original.Type, Key: r.RequestKey}) != r.ID {
		return ErrInvalid
	}
	if r.Fault != "invalid_data" && r.Fault != "index_mismatch" && r.Fault != "duplicate_identity" {
		return ErrInvalid
	}
	operator, err := maintenanceOperator(r.Operator)
	if err != nil || operator != r.Operator {
		return ErrInvalid
	}
	reason, err := reviewReason(r.Reason)
	if err != nil || reason != r.Reason {
		return ErrInvalid
	}
	if repair := r.Repair; repair != nil {
		operator, e := maintenanceOperator(repair.Operator)
		reason, e2 := reviewReason(repair.Reason)
		if e != nil || e2 != nil || operator != repair.Operator || reason != repair.Reason ||
			repair.At.Before(r.CapturedAt) || !digest.MatchString(repair.Replacement) || !digest.MatchString(repair.RestoredKeyID) ||
			(repair.Disposition != "cancelled" && repair.Disposition != "terminal" && repair.Disposition != "fanout_cancelled" &&
				repair.Disposition != "reconciled_terminal" && repair.Disposition != "reconciled_fanout") {
			return ErrInvalid
		}
		if repair.InputDigest != "" && !digest.MatchString(repair.InputDigest) {
			return ErrInvalid
		}
		if strings.HasPrefix(repair.Disposition, "reconciled_") {
			if !digest.MatchString(repair.KeptReference) || repair.InputDigest != "" {
				return ErrInvalid
			}
		} else if repair.KeptReference != "" {
			return ErrInvalid
		}
	}
	return nil
}

func validateQuarantines(s State) error {
	if len(s.Quarantines) > MaxQuarantineRecords || len(s.Quarantines) > 0 && s.Schema < 4 {
		return ErrInvalid
	}
	for key, record := range s.Quarantines {
		if key != record.ID {
			return ErrInvalid
		}
		if err := ValidateQuarantine(record); err != nil {
			return err
		}
	}
	return nil
}

// Until every affected identity can be reconciled, new admissions of that class
// fail closed. Existing receipts may still be confirmed before this gate.
func QuarantineAdmission(records map[string]QuarantinedRecord, task bool) error {
	for _, record := range records {
		if err := ValidateQuarantine(record); err != nil {
			return err
		}
		if record.Repair == nil && record.Original.Type.IsTask() == task {
			return ErrQuarantined
		}
	}
	return nil
}

func QuarantineRequiredBytes(records map[string]QuarantinedRecord) (int64, error) {
	raw, err := json.Marshal(records)
	if err != nil {
		return 0, err
	}
	size := int64(len(raw))
	for _, q := range records {
		if q.Repair == nil {
			size += QuarantineRepairReserveBytes
		}
	}
	return size, nil
}

// Diagnostics are computed across all classes before filtering, so a history
// filter cannot conceal a duplicate identity on the active side of a move.
func PaginateDiagnostics(records []StoredRecord, query DiagnosticQuery) DiagnosticPage {
	sortRecords(records)
	return paginateRecordDiagnostics(DiagnoseRecordSet(records), query)
}

func paginateRecordDiagnostics(diagnostics []RecordDiagnostic, query DiagnosticQuery) DiagnosticPage {
	items := []RecordDiagnostic{}
	for _, item := range diagnostics {
		if query.Type == "" || item.Type == query.Type {
			items = append(items, item)
		}
	}
	page := DiagnosticPage{Items: []RecordDiagnostic{}, Total: len(items), NextOffset: -1}
	start, end := min(query.Offset, len(items)), min(query.Offset+query.Limit, len(items))
	page.Items = items[start:end]
	if end < len(items) {
		page.NextOffset = end
	}
	return page
}

func PaginateQuarantines(records map[string]QuarantinedRecord, query DiagnosticQuery) QuarantinePage {
	ids := []string{}
	for id, q := range records {
		if query.Type == "" || q.Original.Type == query.Type {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	page := QuarantinePage{Items: []QuarantineSummary{}, Total: len(ids), NextOffset: -1}
	end := min(query.Offset+query.Limit, len(ids))
	for i := min(query.Offset, len(ids)); i < end; i++ {
		page.Items = append(page.Items, SummarizeQuarantine(records[ids[i]]))
	}
	if end < len(ids) {
		page.NextOffset = end
	}
	return page
}
