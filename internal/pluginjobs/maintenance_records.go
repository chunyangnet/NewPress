package pluginjobs

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

// RecordIndex is a private snapshot of PostgreSQL selection metadata. File
// records have no second representation. Nullable fields remain distinguishable
// from timestamps or booleans with their zero value.
type RecordIndex struct {
	Plugin      string     `json:"plugin,omitempty"`
	Status      string     `json:"status,omitempty"`
	AvailableAt *time.Time `json:"available_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at,omitzero"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	ReissueID   *string    `json:"reissue_id,omitempty"`
	Cancelled   *bool      `json:"cancelled,omitempty"`
}

type decodedRecord struct {
	task          *Job
	taskHistory   *ArchivedJob
	fanout        *FanoutReceipt
	fanoutHistory *ArchivedFanout
}

func decodeRecord(t RecordType, raw []byte) (decodedRecord, error) {
	// File records inherit the snapshot limit; SQL limits the actual row bytes
	// separately. Decode original bytes so payload whitespace still participates
	// in the domain's raw 32 KiB bound, just as it does on normal file reopening.
	switch t {
	case RecordTask:
		job, err := packagefs.DecodeJSON[Job](bytes.NewReader(raw), MaxSnapshotBytes, "plugin task")
		if err != nil {
			return decodedRecord{}, ErrInvalid
		}
		state := InitialState()
		state.Jobs[job.ID] = job
		if ValidateState(state) != nil {
			return decodedRecord{}, ErrInvalid
		}
		return decodedRecord{task: &job}, nil
	case RecordTaskHistory:
		job, err := packagefs.DecodeJSON[ArchivedJob](bytes.NewReader(raw), MaxSnapshotBytes, "plugin task history")
		if err != nil || ValidateArchivedJob(job) != nil {
			return decodedRecord{}, ErrInvalid
		}
		return decodedRecord{taskHistory: &job}, nil
	case RecordFanout:
		r, err := packagefs.DecodeJSON[FanoutReceipt](bytes.NewReader(raw), MaxSnapshotBytes, "plugin fanout")
		if err != nil || ValidateFanout(r) != nil {
			return decodedRecord{}, ErrInvalid
		}
		return decodedRecord{fanout: &r}, nil
	case RecordFanoutHistory:
		r, err := packagefs.DecodeJSON[ArchivedFanout](bytes.NewReader(raw), MaxSnapshotBytes, "plugin fanout history")
		if err != nil || ValidateArchivedFanout(r) != nil {
			return decodedRecord{}, ErrInvalid
		}
		return decodedRecord{fanoutHistory: &r}, nil
	default:
		return decodedRecord{}, ErrInvalid
	}
}

func (r decodedRecord) key() string {
	if r.task != nil {
		return r.task.ID
	}
	if r.taskHistory != nil {
		return r.taskHistory.ID
	}
	if r.fanout != nil {
		return r.fanout.Event.Key
	}
	return r.fanoutHistory.Receipt.Event.Key
}

func (r decodedRecord) review() *ReissueProvenance {
	if r.task != nil {
		return r.task.Review
	}
	if r.taskHistory != nil {
		return r.taskHistory.Review
	}
	return nil
}

func sameRecordReview(id *string, r *ReissueProvenance) bool {
	if r == nil {
		return id == nil
	}
	return id != nil && *id == r.RequestID
}

func recordIndexMatches(record StoredRecord, value decodedRecord) bool {
	if record.Key != value.key() {
		return false
	}
	if len(record.Metadata) == 0 {
		return true
	}
	if _, err := readJSONObject(record.Metadata); err != nil {
		return false
	}
	index, err := packagefs.DecodeJSON[RecordIndex](bytes.NewReader(record.Metadata), 4096, "plugin record index")
	if err != nil {
		return false
	}
	switch record.Type {
	case RecordTask:
		j := value.task
		return index.Plugin == j.Plugin && index.Status == j.Status && index.AvailableAt != nil &&
			index.AvailableAt.Equal(j.AvailableAt.Truncate(time.Microsecond)) && index.UpdatedAt.Equal(j.UpdatedAt.Truncate(time.Microsecond)) &&
			(index.LeaseUntil == nil) == j.LeaseUntil.IsZero() && (index.LeaseUntil == nil || index.LeaseUntil.Equal(j.LeaseUntil.Truncate(time.Microsecond))) &&
			index.ArchivedAt == nil && index.Cancelled == nil && sameRecordReview(index.ReissueID, j.Review)
	case RecordTaskHistory:
		j := value.taskHistory
		return index.Plugin == j.Plugin && index.Status == j.Status && index.AvailableAt == nil && index.LeaseUntil == nil &&
			index.UpdatedAt.Equal(j.UpdatedAt.Truncate(time.Microsecond)) && index.ArchivedAt != nil &&
			index.ArchivedAt.Equal(j.ArchivedAt.Truncate(time.Microsecond)) && index.Cancelled == nil && sameRecordReview(index.ReissueID, j.Review)
	case RecordFanout:
		return index.Plugin == "" && index.Status == "" && index.AvailableAt == nil && index.UpdatedAt.IsZero() &&
			index.LeaseUntil == nil && index.ArchivedAt == nil && index.ReissueID == nil && index.Cancelled == nil
	case RecordFanoutHistory:
		f := value.fanoutHistory
		return index.Plugin == "" && index.Status == "" && index.AvailableAt == nil && index.UpdatedAt.IsZero() &&
			index.LeaseUntil == nil && index.ArchivedAt != nil && index.ArchivedAt.Equal(f.ArchivedAt.Truncate(time.Microsecond)) &&
			index.ReissueID == nil && index.Cancelled != nil && *index.Cancelled == f.Receipt.Cancelled
	}
	return false
}

func DiagnoseStoredRecord(record StoredRecord) RecordDiagnostic {
	d := RecordDiagnostic{Reference: RecordReference(record), Type: record.Type, Bytes: len(record.Data)}
	value, err := decodeRecord(record.Type, record.Data)
	if err != nil || len(record.Metadata) > 0 && len(record.Data) > RecordByteLimit(record.Type) {
		d.Fault = "invalid_data"
	} else if !recordIndexMatches(record, value) {
		d.Fault = "index_mismatch"
		d.IndexMismatch = true
	}
	if record.Type.IsTask() {
		if value.task != nil {
			summary := Summarize(*value.task)
			d.Task, d.TaskID = &summary, summary.ID
		} else if value.taskHistory != nil {
			summary := value.taskHistory.Summary
			d.Task, d.TaskID = &summary, summary.ID
		} else if anchor, _, err := taskAnchor(record); err == nil {
			d.TaskID = anchor.ID
		}
	}
	return d
}

// PostgreSQL limits the actual stored encoding, including whitespace. File
// records are bounded by their snapshot and validated canonical state instead.
func RecordByteLimit(t RecordType) int {
	switch t {
	case RecordTask:
		return 64 << 10
	case RecordTaskHistory:
		return 4 << 10
	case RecordFanout, RecordFanoutHistory:
		return 512 << 10
	}
	return 0
}

// ValidateStoredRecord checks both representations when SQL metadata exists.
func ValidateStoredRecord(record StoredRecord) error {
	value, err := decodeRecord(record.Type, record.Data)
	if err != nil || len(record.Metadata) > 0 && len(record.Data) > RecordByteLimit(record.Type) || !recordIndexMatches(record, value) {
		return ErrInvalid
	}
	return nil
}

// A maintenance reader checks its envelope and map keys while preserving each
// record as opaque bytes. Normal runtime decoding remains fully strict. Values
// may contain damage, but duplicate or invalid-Unicode envelope keys cannot be
// collapsed by encoding/json and then silently discarded during maintenance.
func readJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		start := d.InputOffset()
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrInvalid
		}
		literal := bytes.TrimSpace(raw[start:d.InputOffset()])
		if len(literal) > 0 && literal[0] == ',' {
			literal = bytes.TrimSpace(literal[1:])
		}
		if packagefs.ValidateUniqueJSON(literal) != nil {
			return nil, ErrInvalid
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, ErrInvalid
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, ErrInvalid
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return fields, nil
}

func rawField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool, error) {
	var out json.RawMessage
	found := false
	for key, value := range fields {
		if strings.EqualFold(key, name) {
			if found {
				return nil, false, ErrInvalid
			}
			out, found = value, true
		}
	}
	return out, found, nil
}

func readField[T any](fields map[string]json.RawMessage, name string, required bool) (T, error) {
	var result T
	raw, found, err := rawField(fields, name)
	if err != nil || !found && required {
		return result, ErrInvalid
	}
	if !found {
		return result, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || packagefs.ValidateUniqueJSON(raw) != nil || json.Unmarshal(raw, &result) != nil {
		return result, ErrInvalid
	}
	return result, nil
}

type taskIdentityAnchor struct {
	ID, Fingerprint, Plugin, Kind string
	Epoch, Key                    string
	ActorID                       int64
}

func taskAnchor(record StoredRecord) (taskIdentityAnchor, map[string]json.RawMessage, error) {
	var a taskIdentityAnchor
	fields, err := readJSONObject(record.Data)
	if err != nil || !record.Type.IsTask() {
		return a, nil, ErrInvalid
	}
	if value, err := decodeRecord(record.Type, record.Data); err == nil {
		if value.task != nil {
			j := value.task
			a = taskIdentityAnchor{ID: j.ID, Fingerprint: j.Fingerprint, Plugin: j.Plugin, Kind: j.Kind, Epoch: j.Epoch, Key: j.Key, ActorID: j.ActorID}
		} else {
			j := value.taskHistory
			a = taskIdentityAnchor{ID: j.ID, Fingerprint: j.Fingerprint, Plugin: j.Plugin, Kind: j.Kind}
		}
		return a, fields, nil
	}
	for key, target := range map[string]*string{"id": &a.ID, "fingerprint": &a.Fingerprint, "plugin": &a.Plugin, "kind": &a.Kind} {
		*target, err = readField[string](fields, key, true)
		if err != nil {
			return taskIdentityAnchor{}, nil, err
		}
	}
	if !digest.MatchString(a.ID) || !digest.MatchString(a.Fingerprint) || !name.MatchString(a.Plugin) || !name.MatchString(a.Kind) {
		return taskIdentityAnchor{}, nil, ErrInvalid
	}
	if record.Type == RecordTask {
		if a.Epoch, err = readField[string](fields, "epoch", true); err != nil {
			return taskIdentityAnchor{}, nil, err
		}
		if a.Key, err = readField[string](fields, "key", true); err != nil {
			return taskIdentityAnchor{}, nil, err
		}
		if a.ActorID, err = readField[int64](fields, "actor_id", true); err != nil {
			return taskIdentityAnchor{}, nil, err
		}
		encoded, _ := json.Marshal([]string{a.Plugin, a.Epoch, a.Key})
		if !digest.MatchString(a.Epoch) || len(a.Key) == 0 || len(a.Key) > 256 || a.ActorID < 0 || hash(encoded) != a.ID {
			return taskIdentityAnchor{}, nil, ErrInvalid
		}
	}
	return a, fields, nil
}

// Known task aliases are fenced even when an SQL/map key was renamed. If task
// identity fields themselves cannot be proved, all new claims remain blocked
// until a verified backup can reconcile that record.
func QuarantineWorkPolicy(records map[string]QuarantinedRecord) (map[string]bool, error) {
	blocked := map[string]bool{}
	for _, record := range records {
		if err := ValidateQuarantine(record); err != nil {
			return nil, err
		}
		if record.Repair != nil || !record.Original.Type.IsTask() {
			continue
		}
		anchor, _, err := taskAnchor(record.Original)
		if err != nil {
			return nil, ErrQuarantined
		}
		blocked[anchor.ID] = true
		if digest.MatchString(record.Original.Key) {
			blocked[record.Original.Key] = true
		}
	}
	return blocked, nil
}

// Incomplete event receipts can initiate enqueue work. Fence their known event
// keys and storage aliases; an unprovable event identity fences that entire
// class. Completed/cancelled confirmations do not initiate delivery work.
func CheckQuarantineFanout(records map[string]QuarantinedRecord, key string) error {
	for _, q := range records {
		if err := ValidateQuarantine(q); err != nil {
			return err
		}
		if q.Repair != nil || q.Original.Type.IsTask() {
			continue
		}
		fields, err := readJSONObject(q.Original.Data)
		if err == nil && q.Original.Type == RecordFanoutHistory {
			var raw json.RawMessage
			var found bool
			raw, found, err = rawField(fields, "receipt")
			if !found {
				err = ErrInvalid
			} else if err == nil {
				fields, err = readJSONObject(raw)
			}
		}
		if err != nil {
			return ErrQuarantined
		}
		event, err := readField[Event](fields, "event", true)
		if err != nil || ValidateFanout(FanoutReceipt{Event: event}) != nil || event.Key == key || q.Original.Key == key {
			return ErrQuarantined
		}
	}
	return nil
}

// Duplicate logical IDs and reissue commands are diagnostic facts too. Mark
// every implicated record so selecting either side requires an explicit audit.
func DiagnoseRecordSet(records []StoredRecord) []RecordDiagnostic {
	result := make([]RecordDiagnostic, len(records))
	identities := map[string][]int{}
	for i, record := range records {
		result[i] = DiagnoseStoredRecord(record)
		value, err := decodeRecord(record.Type, record.Data)
		if err != nil {
			continue
		}
		prefix := "fanout:"
		if record.Type.IsTask() {
			prefix = "task:"
		}
		key := prefix + value.key()
		identities[key] = append(identities[key], i)
		if review := value.review(); review != nil {
			key = "reissue:" + review.RequestID
			identities[key] = append(identities[key], i)
		}
	}
	for _, indexes := range identities {
		if len(indexes) > 1 {
			for _, i := range indexes {
				result[i].Fault = "duplicate_identity"
			}
		}
	}
	return result
}

func sortRecords(records []StoredRecord) {
	slices.SortFunc(records, func(a, b StoredRecord) int {
		if order := strings.Compare(string(a.Type), string(b.Type)); order != 0 {
			return order
		}
		return strings.Compare(a.Key, b.Key)
	})
}
