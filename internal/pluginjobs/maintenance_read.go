package pluginjobs

import (
	"context"
	"encoding/json"
)

var _ MaintenanceReader = (*Queue)(nil)

// The application's existing directory lock protects these diagnostic reads.
// File snapshots may be damaged externally after startup; inspect their raw
// on-disk records independently while excluding normal in-process writers.
func (q *Queue) InspectRecords(ctx context.Context, query DiagnosticQuery) (DiagnosticPage, error) {
	query, err := NormalizeDiagnosticQuery(query)
	if err != nil {
		return DiagnosticPage{}, err
	}
	if b, ok := q.Backend.(*FileBackend); ok && b.path != "" {
		b.mu.Lock()
		defer b.mu.Unlock()
		m, err := NewFileMaintenance(b.path)
		if err != nil {
			return DiagnosticPage{}, err
		}
		return m.InspectRecords(ctx, query)
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return DiagnosticPage{}, err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return DiagnosticPage{}, err
	}
	envelope, err := decodeMaintenanceEnvelope(raw)
	if err != nil {
		return DiagnosticPage{}, err
	}
	return paginateRecordDiagnostics(envelope.diagnostics(envelope.records()), query), nil
}

func (q *Queue) ListQuarantines(ctx context.Context, query DiagnosticQuery) (QuarantinePage, error) {
	query, err := NormalizeDiagnosticQuery(query)
	if err != nil {
		return QuarantinePage{}, err
	}
	if b, ok := q.Backend.(*FileBackend); ok && b.path != "" {
		b.mu.Lock()
		defer b.mu.Unlock()
		m, err := NewFileMaintenance(b.path)
		if err != nil {
			return QuarantinePage{}, err
		}
		return m.ListQuarantines(ctx, query)
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return QuarantinePage{}, err
	}
	return PaginateQuarantines(state.Quarantines, query), nil
}
