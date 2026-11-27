package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.MaintenanceStore = (*PluginJobRepository)(nil)

var errPluginQuarantineRow = errors.New("inconsistent stored plugin quarantine")

type pgPluginRecordTable struct {
	kind                pluginjobs.RecordType
	table, key, columns string
	limit               int
	bytes               int64
}

var pgPluginRecordTables = []pgPluginRecordTable{
	{pluginjobs.RecordTask, "np_plugin_jobs", "id", pluginTaskColumns, pluginjobs.MaxJobs, pluginjobs.MaxStateBytes},
	{pluginjobs.RecordTaskHistory, "np_plugin_job_archives", "id", archivedTaskColumns, pluginjobs.MaxArchivedJobs, pluginjobs.MaxArchiveBytes},
	{pluginjobs.RecordFanout, "np_plugin_fanouts", "event_key", "event_key,data", pluginjobs.MaxFanouts, pluginjobs.MaxStateBytes},
	{pluginjobs.RecordFanoutHistory, "np_plugin_fanout_archives", "event_key", "event_key,archived_at,cancelled,data", pluginjobs.MaxArchivedFanouts, pluginjobs.MaxArchiveBytes},
}

func scanPGPluginRecord(kind pluginjobs.RecordType, row interface{ Scan(...any) error }) (pluginjobs.StoredRecord, error) {
	record := pluginjobs.StoredRecord{Type: kind}
	index := pluginjobs.RecordIndex{}
	var err error
	switch kind {
	case pluginjobs.RecordTask:
		var at time.Time
		err = row.Scan(&record.Key, &index.Plugin, &index.Status, &at, &index.UpdatedAt, &index.LeaseUntil, &record.Data, &index.ReissueID)
		index.AvailableAt = &at
	case pluginjobs.RecordTaskHistory:
		var at time.Time
		err = row.Scan(&record.Key, &index.Plugin, &index.Status, &index.UpdatedAt, &at, &record.Data, &index.ReissueID)
		index.ArchivedAt = &at
	case pluginjobs.RecordFanout:
		err = row.Scan(&record.Key, &record.Data)
	case pluginjobs.RecordFanoutHistory:
		var at time.Time
		var cancelled bool
		err = row.Scan(&record.Key, &at, &cancelled, &record.Data)
		index.ArchivedAt, index.Cancelled = &at, &cancelled
	default:
		return pluginjobs.StoredRecord{}, pluginjobs.ErrInvalid
	}
	if err != nil {
		return pluginjobs.StoredRecord{}, err
	}
	record.Metadata, err = json.Marshal(index)
	return record, err
}

// Preflight before materializing bytea: damaged SQL need not respect the normal
// application's budgets. Every query has a hard count and byte boundary.
func loadPGPluginRecords(ctx context.Context, tx pgx.Tx) ([]pluginjobs.StoredRecord, error) {
	result := []pluginjobs.StoredRecord{}
	var historySize int64
	for _, table := range pgPluginRecordTables {
		var count int
		var size int64
		if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM `+table.table).Scan(&count, &size); err != nil {
			return nil, err
		}
		if count > table.limit || size > table.bytes {
			return nil, pluginjobs.ErrCapacity
		}
		if table.kind == pluginjobs.RecordTaskHistory || table.kind == pluginjobs.RecordFanoutHistory {
			historySize += size
			if historySize > pluginjobs.MaxArchiveBytes {
				return nil, pluginjobs.ErrCapacity
			}
		}
		rows, err := tx.Query(ctx, `SELECT `+table.columns+` FROM `+table.table+` ORDER BY `+table.key+` LIMIT $1`, table.limit+1)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			record, err := scanPGPluginRecord(table.kind, rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Never filter by SQL repaired/type before verification: a damaged index could
// otherwise conceal unresolved evidence and re-enable new admission or claims.
func loadPGPluginQuarantines(ctx context.Context, tx pgx.Tx) (map[string]pluginjobs.QuarantinedRecord, error) {
	var count int
	var size int64
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_plugin_record_quarantines`).Scan(&count, &size); err != nil {
		return nil, err
	}
	if count > pluginjobs.MaxQuarantineRecords || size > pluginjobs.MaxQuarantineBytes {
		return nil, pluginjobs.ErrCapacity
	}
	result := map[string]pluginjobs.QuarantinedRecord{}
	if count == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,record_type,reference,captured_at,repaired,data FROM np_plugin_record_quarantines ORDER BY id LIMIT $1`, pluginjobs.MaxQuarantineRecords+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, kind, reference string
		var captured time.Time
		var repaired bool
		var raw []byte
		if err = rows.Scan(&id, &kind, &reference, &captured, &repaired, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		q, err := packagefs.DecodeJSON[pluginjobs.QuarantinedRecord](bytes.NewReader(raw), pluginjobs.MaxQuarantineBytes, "quarantined plugin record")
		if err != nil || pluginjobs.ValidateQuarantine(q) != nil || id != q.ID || kind != string(q.Original.Type) || reference != q.Reference ||
			!captured.Equal(q.CapturedAt.Truncate(time.Microsecond)) || repaired != (q.Repair != nil) {
			rows.Close()
			return nil, errPluginQuarantineRow
		}
		result[id] = q
		if q.Repair == nil {
			size += pluginjobs.QuarantineRepairReserveBytes
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if size > pluginjobs.MaxQuarantineBytes || len(result) > pluginjobs.MaxQuarantineRecords {
		return nil, pluginjobs.ErrCapacity
	}
	return result, nil
}

func pluginQuarantineAdmission(ctx context.Context, tx pgx.Tx, task bool) error {
	if err := sharedQuarantineGate(ctx, tx); err != nil {
		return err
	}
	records, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	return pluginjobs.QuarantineAdmission(records, task)
}

// Same lock order as restore and archive; the table locks also serialize claims
// and completions. Workers executing guest code must still be stopped by the
// offline operator; a SQL transaction cannot recall an external side effect.
func lockPGPluginMaintenance(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-pluginjobs-admission',0))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-fanout-admission',0))`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `LOCK TABLE np_plugin_fanout_archives, np_plugin_fanouts, np_plugin_job_archives, np_plugin_jobs, np_plugin_record_quarantines IN EXCLUSIVE MODE`)
	return err
}

func (r *PluginJobRepository) InspectRecords(ctx context.Context, query pluginjobs.DiagnosticQuery) (pluginjobs.DiagnosticPage, error) {
	query, err := pluginjobs.NormalizeDiagnosticQuery(query)
	if err != nil {
		return pluginjobs.DiagnosticPage{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return pluginjobs.DiagnosticPage{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = loadPGPluginQuarantines(ctx, tx); err != nil {
		if errors.Is(err, errPluginQuarantineRow) || errors.Is(err, pluginjobs.ErrCapacity) {
			return pluginjobs.DiagnosticPage{Items: []pluginjobs.RecordDiagnostic{}, NextOffset: -1, Fatal: "invalid_quarantine"}, nil
		}
		return pluginjobs.DiagnosticPage{}, err
	}
	records, err := loadPGPluginRecords(ctx, tx)
	if errors.Is(err, pluginjobs.ErrCapacity) {
		return pluginjobs.DiagnosticPage{Items: []pluginjobs.RecordDiagnostic{}, NextOffset: -1, Fatal: "storage_capacity"}, nil
	}
	if err != nil {
		return pluginjobs.DiagnosticPage{}, err
	}
	page := pluginjobs.PaginateDiagnostics(records, query)
	return page, tx.Commit(ctx)
}

func (r *PluginJobRepository) ListQuarantines(ctx context.Context, query pluginjobs.DiagnosticQuery) (pluginjobs.QuarantinePage, error) {
	query, err := pluginjobs.NormalizeDiagnosticQuery(query)
	if err != nil {
		return pluginjobs.QuarantinePage{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return pluginjobs.QuarantinePage{}, err
	}
	defer tx.Rollback(context.Background())
	records, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.QuarantinePage{}, err
	}
	return pluginjobs.PaginateQuarantines(records, query), tx.Commit(ctx)
}

func savePGPluginQuarantine(ctx context.Context, tx pgx.Tx, q pluginjobs.QuarantinedRecord, insert bool) error {
	if err := pluginjobs.ValidateQuarantine(q); err != nil {
		return err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if insert {
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_record_quarantines(id,record_type,reference,captured_at,repaired,data) VALUES($1,$2,$3,$4,$5,$6)`,
			q.ID, string(q.Original.Type), q.Reference, q.CapturedAt, q.Repair != nil, raw)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_plugin_record_quarantines SET repaired=$2,data=$3 WHERE id=$1`, q.ID, q.Repair != nil, raw)
	}
	return err
}

func (r *PluginJobRepository) QuarantineRecord(ctx context.Context, request pluginjobs.QuarantineRequest, now time.Time) (pluginjobs.QuarantineSummary, error) {
	request, err := pluginjobs.PrepareQuarantineRequest(request)
	if err != nil || now.IsZero() || request.SnapshotDigest != "" {
		return pluginjobs.QuarantineSummary{}, pluginjobs.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockPGPluginMaintenance(ctx, tx); err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	if old, found := quarantines[pluginjobs.QuarantineRequestID(request)]; found {
		if err = pluginjobs.MatchQuarantineRequest(old, request); err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		return pluginjobs.SummarizeQuarantine(old), tx.Commit(ctx)
	}
	records, err := loadPGPluginRecords(ctx, tx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	for i, diagnostic := range pluginjobs.DiagnoseRecordSet(records) {
		if diagnostic.Reference != request.Reference || diagnostic.Type != request.Type {
			continue
		}
		if diagnostic.Fault == "" {
			return pluginjobs.QuarantineSummary{}, pluginjobs.ErrInvalid
		}
		q, err := pluginjobs.CaptureQuarantine(records[i], diagnostic.Fault, request, now)
		if err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		if err = savePGPluginQuarantine(ctx, tx, q, true); err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		for _, table := range pgPluginRecordTables {
			if table.kind == q.Original.Type {
				if _, err = tx.Exec(ctx, `DELETE FROM `+table.table+` WHERE `+table.key+`=$1`, q.Original.Key); err != nil {
					return pluginjobs.QuarantineSummary{}, err
				}
			}
		}
		if _, err = loadPGPluginQuarantines(ctx, tx); err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		return pluginjobs.SummarizeQuarantine(q), tx.Commit(ctx)
	}
	return pluginjobs.QuarantineSummary{}, pluginjobs.ErrConflict
}

func insertPGRepairedRecord(ctx context.Context, tx pgx.Tx, record pluginjobs.StoredRecord) error {
	if err := pluginjobs.ValidateStoredRecord(record); err != nil || len(record.Data) > pluginjobs.RecordByteLimit(record.Type) {
		return pluginjobs.ErrInvalid
	}
	var err error
	switch record.Type {
	case pluginjobs.RecordTask:
		j, e := decodeTask(record.Data)
		if e != nil {
			return e
		}
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_jobs(id,plugin,status,available_at,updated_at,lease_until,data,reissue_id) VALUES($1,$2,$3,$4,$5,NULL,$6,$7)`,
			j.ID, j.Plugin, j.Status, j.AvailableAt, j.UpdatedAt, record.Data, storedReissueID(j.Review))
	case pluginjobs.RecordTaskHistory:
		j, e := decodeArchivedTask(record.Data)
		if e != nil {
			return e
		}
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_job_archives(id,plugin,status,updated_at,archived_at,data,reissue_id) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			j.ID, j.Plugin, j.Status, j.UpdatedAt, j.ArchivedAt, record.Data, storedReissueID(j.Review))
	case pluginjobs.RecordFanout:
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_fanouts(event_key,data) VALUES($1,$2)`, record.Key, record.Data)
	case pluginjobs.RecordFanoutHistory:
		f, e := packagefs.DecodeJSON[pluginjobs.ArchivedFanout](bytes.NewReader(record.Data), 524288, "archived plugin fanout")
		if e != nil {
			return e
		}
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_fanout_archives(event_key,archived_at,cancelled,data) VALUES($1,$2,$3,$4)`,
			record.Key, f.ArchivedAt, f.Receipt.Cancelled, record.Data)
	}
	return err
}

func checkPGMaintenanceBudgets(ctx context.Context, tx pgx.Tx) error {
	records, err := loadPGPluginRecords(ctx, tx)
	if err != nil {
		return err
	}
	var tasksSize, fanoutsSize, historySize int64
	plugins := map[string]int{}
	for _, record := range records {
		switch record.Type {
		case pluginjobs.RecordTask:
			tasksSize += int64(len(record.Data))
			j, err := decodeTask(record.Data)
			if err != nil || j.Status != "succeeded" && j.Status != "cancelled" {
				tasksSize += pluginjobs.TaskTransitionReserveBytes
			}
			if err == nil {
				plugins[j.Plugin]++
				if plugins[j.Plugin] > pluginjobs.MaxPluginJobs {
					return pluginjobs.ErrCapacity
				}
			}
		case pluginjobs.RecordFanout:
			fanoutsSize += int64(len(record.Data))
			f, err := decodeFanout(record.Key, record.Data)
			if err != nil || !f.Cancelled {
				fanoutsSize += int64(len(`,"cancelled":true`))
			}
			if err != nil || !f.Cancelled && !f.Delivered {
				fanoutsSize += int64(len(`,"delivered":true`))
			}
		case pluginjobs.RecordTaskHistory:
			historySize += int64(len(record.Data))
		case pluginjobs.RecordFanoutHistory:
			historySize += int64(len(record.Data))
			f, err := packagefs.DecodeJSON[pluginjobs.ArchivedFanout](bytes.NewReader(record.Data), 524288, "archived plugin fanout")
			if err != nil || !f.Receipt.Cancelled {
				historySize += int64(len(`,"cancelled":true`))
			}
		}
	}
	if tasksSize > pluginjobs.MaxStateBytes || fanoutsSize > pluginjobs.MaxStateBytes || historySize > pluginjobs.MaxArchiveBytes {
		return pluginjobs.ErrCapacity
	}
	_, err = loadPGPluginQuarantines(ctx, tx)
	return err
}

func (r *PluginJobRepository) RepairRecord(ctx context.Context, request pluginjobs.RepairRequest, now time.Time) (pluginjobs.QuarantineSummary, error) {
	request, err := pluginjobs.PrepareRepairRequest(request)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	if request.SnapshotDigest != "" {
		return pluginjobs.QuarantineSummary{}, pluginjobs.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockPGPluginMaintenance(ctx, tx); err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	q, found := quarantines[request.ID]
	if !found {
		return pluginjobs.QuarantineSummary{}, pluginjobs.ErrConflict
	}
	if q.Repair != nil {
		if err = pluginjobs.MatchRepairRequest(q, request); err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		return pluginjobs.SummarizeQuarantine(q), tx.Commit(ctx)
	}
	records, err := loadPGPluginRecords(ctx, tx)
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	var audit pluginjobs.RepairAudit
	if request.KeepReference != "" {
		var kept *pluginjobs.StoredRecord
		others := []pluginjobs.StoredRecord{}
		for i, record := range records {
			if pluginjobs.RecordReference(record) == request.KeepReference {
				kept = &records[i]
			} else {
				others = append(others, record)
			}
		}
		if kept == nil {
			return pluginjobs.QuarantineSummary{}, pluginjobs.ErrConflict
		}
		if err = pluginjobs.ValidateRepairTarget(*kept, others); err != nil {
			return pluginjobs.QuarantineSummary{}, err
		}
		audit, err = pluginjobs.ReconcileQuarantinedRecord(q, request, *kept, now)
	} else {
		var out pluginjobs.StoredRecord
		out, audit, err = pluginjobs.BuildRepairedRecord(q, request, now)
		if err == nil {
			err = pluginjobs.ValidateRepairTarget(out, records)
		}
		if err == nil {
			err = insertPGRepairedRecord(ctx, tx, out)
		}
	}
	if err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	q.Repair = &audit
	if err = savePGPluginQuarantine(ctx, tx, q, false); err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	if err = checkPGMaintenanceBudgets(ctx, tx); err != nil {
		return pluginjobs.QuarantineSummary{}, err
	}
	return pluginjobs.SummarizeQuarantine(q), tx.Commit(ctx)
}
