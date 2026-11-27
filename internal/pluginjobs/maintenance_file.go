package pluginjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

var ErrMaintenanceEnvelope = errors.New("invalid plugin maintenance envelope; restore a verified backup")

// FileMaintenance deliberately does not open FileBackend: runtime validation
// rejects damaged records before a normal backend can be created. Callers must
// hold the site's directory lock and quiesce every worker before any mutation.
type FileMaintenance struct {
	mu   sync.Mutex
	path string
}

var _ MaintenanceStore = (*FileMaintenance)(nil)

func NewFileMaintenance(path string) (*FileMaintenance, error) {
	if path == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &FileMaintenance{path: absolute}, nil
}

type maintenanceEnvelope struct {
	schema      int
	fields      map[string]json.RawMessage
	maps        map[RecordType]map[string]json.RawMessage
	quarantines map[string]QuarantinedRecord
}

var maintenanceMapNames = map[RecordType]string{
	RecordTask: "jobs", RecordTaskHistory: "archived_jobs",
	RecordFanout: "fanouts", RecordFanoutHistory: "archived_fanouts",
}

func decodeMaintenanceEnvelope(body []byte) (*maintenanceEnvelope, error) {
	fields, err := readJSONObject(body)
	if err != nil {
		return nil, ErrMaintenanceEnvelope
	}
	allowed := map[string]bool{"schema": true, "kind": true, "jobs": true, "fanouts": true, "archived_jobs": true, "archived_fanouts": true, "quarantines": true}
	for key := range fields {
		if !allowed[key] {
			return nil, ErrMaintenanceEnvelope
		}
	}
	schema, err := readField[int](fields, "schema", true)
	kind, e := readField[string](fields, "kind", true)
	if err != nil || e != nil || schema < 1 || schema > CurrentSchema || kind != "newpress.pluginjobs" {
		return nil, ErrMaintenanceEnvelope
	}
	envelope := &maintenanceEnvelope{schema: schema, fields: fields, maps: map[RecordType]map[string]json.RawMessage{}, quarantines: map[string]QuarantinedRecord{}}
	for recordType, name := range maintenanceMapNames {
		raw, found := fields[name]
		values := map[string]json.RawMessage{}
		if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if recordType == RecordTask {
				return nil, ErrMaintenanceEnvelope
			}
		} else if values, err = readJSONObject(raw); err != nil {
			return nil, ErrMaintenanceEnvelope
		}
		limit := MaxFanouts
		if recordType == RecordTaskHistory || recordType == RecordFanoutHistory {
			limit = MaxArchivedJobs
			if schema == 1 && len(values) > 0 {
				return nil, ErrMaintenanceEnvelope
			}
		}
		if len(values) > limit {
			return nil, ErrCapacity
		}
		for key := range values {
			if !utf8.ValidString(key) || len(key) > 4096 {
				return nil, ErrMaintenanceEnvelope
			}
		}
		envelope.maps[recordType] = values
	}
	if raw, found := fields["quarantines"]; found && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		values, err := readJSONObject(raw)
		if err != nil || len(values) > MaxQuarantineRecords || schema < 4 && len(values) > 0 {
			return nil, ErrMaintenanceEnvelope
		}
		for id, raw := range values {
			q, err := packagefs.DecodeJSON[QuarantinedRecord](bytes.NewReader(raw), MaxQuarantineBytes, "quarantined plugin record")
			if err != nil || id != q.ID || ValidateQuarantine(q) != nil {
				return nil, ErrMaintenanceEnvelope
			}
			envelope.quarantines[id] = q
		}
	}
	return envelope, nil
}

// encoding/json compacts/escapes RawMessage values when marshaling maps. Write
// the already parsed values directly so untouched records retain their exact
// bytes, including damaged duplicate fields and insignificant whitespace.
func writeRawObject(fields map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		raw, _ := json.Marshal(key)
		out.Write(raw)
		out.WriteByte(':')
		out.Write(fields[key])
	}
	out.WriteByte('}')
	return out.Bytes()
}

func (e *maintenanceEnvelope) records() []StoredRecord {
	items := []StoredRecord{}
	for kind, values := range e.maps {
		for key, raw := range values {
			items = append(items, StoredRecord{Type: kind, Key: key, Data: raw})
		}
	}
	sortRecords(items)
	return items
}

func (e *maintenanceEnvelope) diagnostics(records []StoredRecord) []RecordDiagnostic {
	items := DiagnoseRecordSet(records)
	if e.schema < ReviewSchema {
		for i, record := range records {
			if value, err := decodeRecord(record.Type, record.Data); err == nil && value.review() != nil && items[i].Fault == "" {
				items[i].Fault = "invalid_data"
			}
		}
	}
	return items
}

func canonicalRecord(value decodedRecord) ([]byte, error) {
	switch {
	case value.task != nil:
		return json.Marshal(value.task)
	case value.taskHistory != nil:
		return json.Marshal(value.taskHistory)
	case value.fanout != nil:
		return json.Marshal(value.fanout)
	default:
		return json.Marshal(value.fanoutHistory)
	}
}

func (e *maintenanceEnvelope) encode() ([]byte, error) {
	fields := map[string]json.RawMessage{}
	for key, raw := range e.fields {
		fields[key] = raw
	}
	fields["schema"] = json.RawMessage("4")
	budget := map[string]json.RawMessage{"schema": fields["schema"], "kind": fields["kind"]}
	for kind, values := range e.maps {
		name := maintenanceMapNames[kind]
		if len(values) > 0 || kind == RecordTask {
			fields[name] = writeRawObject(values)
		} else {
			delete(fields, name)
		}
		encoded := map[string]json.RawMessage{}
		for key, raw := range values {
			if value, err := decodeRecord(kind, raw); err == nil {
				encoded[key], err = canonicalRecord(value)
				if err != nil {
					return nil, err
				}
			} else {
				var compact bytes.Buffer
				if err := json.Compact(&compact, raw); err != nil {
					return nil, ErrMaintenanceEnvelope
				}
				encoded[key] = compact.Bytes()
			}
		}
		budget[name] = writeRawObject(encoded)
	}
	active := map[string]json.RawMessage{"schema": budget["schema"], "kind": budget["kind"], "jobs": budget["jobs"], "fanouts": budget["fanouts"]}
	history := map[string]json.RawMessage{"archived_jobs": budget["archived_jobs"], "archived_fanouts": budget["archived_fanouts"]}
	activeSize, historySize := int64(len(writeRawObject(active))), int64(len(writeRawObject(history)))
	for _, r := range e.records() {
		value, err := decodeRecord(r.Type, r.Data)
		if err != nil {
			continue
		}
		if value.task != nil && value.task.Status != "succeeded" && value.task.Status != "cancelled" {
			activeSize += TaskTransitionReserveBytes
		}
		if value.fanout != nil {
			if !value.fanout.Delivered && !value.fanout.Cancelled {
				activeSize += int64(len(`,"delivered":true`))
			}
			if !value.fanout.Cancelled {
				activeSize += int64(fanoutCancellationReserveBytes)
			}
		}
		if value.fanoutHistory != nil && !value.fanoutHistory.Receipt.Cancelled {
			historySize += int64(fanoutCancellationReserveBytes)
		}
	}
	quarantineSize, err := QuarantineRequiredBytes(e.quarantines)
	if err != nil {
		return nil, err
	}
	if len(e.quarantines) > MaxQuarantineRecords || activeSize > MaxStateBytes || historySize > MaxArchiveBytes || quarantineSize > MaxQuarantineBytes {
		return nil, ErrCapacity
	}
	if len(e.quarantines) > 0 {
		fields["quarantines"], err = json.Marshal(e.quarantines)
		if err != nil {
			return nil, err
		}
	} else {
		delete(fields, "quarantines")
	}
	body := writeRawObject(fields)
	if len(body) > MaxSnapshotBytes {
		return nil, ErrCapacity
	}
	if _, err := decodeMaintenanceEnvelope(body); err != nil {
		return nil, err
	}
	return body, nil
}

func (m *FileMaintenance) read(ctx context.Context) (*maintenanceEnvelope, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	body, exists, err := readStateFile(m.path)
	if err != nil {
		return nil, nil, exists, err
	}
	if !exists {
		body, _ = json.Marshal(InitialState())
	}
	envelope, err := decodeMaintenanceEnvelope(body)
	return envelope, body, exists, err
}

func (m *FileMaintenance) InspectRecords(ctx context.Context, query DiagnosticQuery) (DiagnosticPage, error) {
	query, err := NormalizeDiagnosticQuery(query)
	if err != nil {
		return DiagnosticPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, body, _, err := m.read(ctx)
	if errors.Is(err, ErrMaintenanceEnvelope) || errors.Is(err, ErrCapacity) {
		code := "invalid_envelope"
		if errors.Is(err, ErrCapacity) {
			code = "storage_capacity"
		}
		digest := ""
		if len(body) > 0 {
			digest = hash(body)
		}
		return DiagnosticPage{Items: []RecordDiagnostic{}, NextOffset: -1, Fatal: code, SnapshotDigest: digest}, nil
	}
	if err != nil {
		return DiagnosticPage{}, err
	}
	page := paginateRecordDiagnostics(e.diagnostics(e.records()), query)
	page.SnapshotDigest = hash(body)
	return page, nil
}

func (m *FileMaintenance) ListQuarantines(ctx context.Context, query DiagnosticQuery) (QuarantinePage, error) {
	query, err := NormalizeDiagnosticQuery(query)
	if err != nil {
		return QuarantinePage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, _, _, err := m.read(ctx)
	if err != nil {
		return QuarantinePage{}, err
	}
	return PaginateQuarantines(e.quarantines, query), nil
}

func (m *FileMaintenance) commit(ctx context.Context, before, after []byte, existed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if _, err := packagefs.LstatNoSymlink(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pluginjobs-maintenance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(after)
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	current, exists, err := readStateFile(m.path)
	if err != nil {
		return err
	}
	if exists != existed || existed && !bytes.Equal(current, before) {
		return ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return packagefs.ReplaceRegularFileNoSymlink(f.Name(), m.path)
}

func (m *FileMaintenance) QuarantineRecord(ctx context.Context, request QuarantineRequest, now time.Time) (QuarantineSummary, error) {
	request, err := PrepareQuarantineRequest(request)
	if err != nil || now.IsZero() {
		return QuarantineSummary{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, body, exists, err := m.read(ctx)
	if err != nil {
		return QuarantineSummary{}, err
	}
	if old, found := e.quarantines[QuarantineRequestID(request)]; found {
		if err = MatchQuarantineRequest(old, request); err != nil {
			return QuarantineSummary{}, err
		}
		return SummarizeQuarantine(old), nil
	}
	if request.SnapshotDigest != "" && request.SnapshotDigest != hash(body) {
		return QuarantineSummary{}, ErrConflict
	}
	records := e.records()
	diagnostics := e.diagnostics(records)
	for i, diagnostic := range diagnostics {
		if diagnostic.Reference != request.Reference || diagnostic.Type != request.Type {
			continue
		}
		if diagnostic.Fault == "" {
			return QuarantineSummary{}, ErrInvalid
		}
		captured, err := CaptureQuarantine(records[i], diagnostic.Fault, request, now)
		if err != nil {
			return QuarantineSummary{}, err
		}
		e.quarantines[captured.ID] = captured
		delete(e.maps[records[i].Type], records[i].Key)
		after, err := e.encode()
		if err != nil {
			return QuarantineSummary{}, err
		}
		if err = m.commit(ctx, body, after, exists); err != nil {
			return QuarantineSummary{}, err
		}
		return SummarizeQuarantine(captured), nil
	}
	return QuarantineSummary{}, ErrConflict
}

func (m *FileMaintenance) RepairRecord(ctx context.Context, request RepairRequest, now time.Time) (QuarantineSummary, error) {
	request, err := PrepareRepairRequest(request)
	if err != nil {
		return QuarantineSummary{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, body, exists, err := m.read(ctx)
	if err != nil {
		return QuarantineSummary{}, err
	}
	q, found := e.quarantines[request.ID]
	if !found {
		return QuarantineSummary{}, ErrConflict
	}
	if q.Repair != nil {
		if err = MatchRepairRequest(q, request); err != nil {
			return QuarantineSummary{}, err
		}
		return SummarizeQuarantine(q), nil
	}
	if request.SnapshotDigest != "" && request.SnapshotDigest != hash(body) {
		return QuarantineSummary{}, ErrConflict
	}
	var audit RepairAudit
	records := e.records()
	if request.KeepReference != "" {
		var kept *StoredRecord
		others := []StoredRecord{}
		for i, record := range records {
			if RecordReference(record) == request.KeepReference {
				kept = &records[i]
			} else {
				others = append(others, record)
			}
		}
		if kept == nil {
			return QuarantineSummary{}, ErrConflict
		}
		if err = ValidateRepairTarget(*kept, others); err != nil {
			return QuarantineSummary{}, err
		}
		audit, err = ReconcileQuarantinedRecord(q, request, *kept, now)
	} else {
		var out StoredRecord
		out, audit, err = BuildRepairedRecord(q, request, now)
		if err == nil {
			err = ValidateRepairTarget(out, records)
		}
		if err == nil {
			e.maps[out.Type][out.Key] = out.Data
		}
	}
	if err != nil {
		return QuarantineSummary{}, err
	}
	q.Repair = &audit
	if err = ValidateQuarantine(q); err != nil {
		return QuarantineSummary{}, err
	}
	e.quarantines[q.ID] = q
	after, err := e.encode()
	if err != nil {
		return QuarantineSummary{}, err
	}
	if err = m.commit(ctx, body, after, exists); err != nil {
		return QuarantineSummary{}, err
	}
	return SummarizeQuarantine(q), nil
}
