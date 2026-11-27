package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/audit"
)

// AuditReader projects the PostgreSQL audit table through the same bounded,
// metadata-free contract used by the local JSONL ledger.
type AuditReader struct {
	pool *pgxpool.Pool
}

func NewAuditReader(pool *pgxpool.Pool) *AuditReader {
	return &AuditReader{pool: pool}
}

func (reader *AuditReader) List(ctx context.Context, query audit.Query) (audit.Page, error) {
	if reader == nil || reader.pool == nil {
		return audit.Page{}, errors.New("audit database reader is unavailable")
	}
	normalized, err := audit.NormalizeQuery(query)
	if err != nil {
		return audit.Page{}, err
	}
	filterWhere := []string{"1=1"}
	filterArgs := make([]any, 0, 2)
	filterArg := 1
	if normalized.Action != "" {
		filterWhere = append(filterWhere, fmt.Sprintf("action=$%d", filterArg))
		filterArgs = append(filterArgs, normalized.Action)
		filterArg++
	}
	if normalized.ObjectType != "" {
		filterWhere = append(filterWhere, fmt.Sprintf("object_type=$%d", filterArg))
		filterArgs = append(filterArgs, normalized.ObjectType)
		filterArg++
	}
	filterSQL := strings.Join(filterWhere, " AND ")
	var total int
	if err := reader.pool.QueryRow(ctx, `SELECT count(*) FROM np_audit_events WHERE `+filterSQL, filterArgs...).Scan(&total); err != nil {
		return audit.Page{}, fmt.Errorf("count audit events: %w", err)
	}

	where := append([]string(nil), filterWhere...)
	args := append([]any(nil), filterArgs...)
	arg := filterArg
	var cursorAt time.Time
	var cursorID uint64
	if audit.HasCursor(normalized) {
		at, id, err := audit.DecodeCursor(normalized.Cursor)
		if err != nil {
			return audit.Page{}, err
		}
		if id > uint64(1<<63-1) {
			return audit.Page{}, audit.ErrInvalidCursor
		}
		cursorAt = at
		cursorID = id
	}
	if !cursorAt.IsZero() {
		where = append(where, fmt.Sprintf("(created_at < $%d OR (created_at = $%d AND id < $%d))", arg, arg, arg+1))
		args = append(args, cursorAt, int64(cursorID))
		arg += 2
	}
	whereSQL := strings.Join(where, " AND ")
	args = append(args, normalized.Limit+1)
	limitArg := arg
	rows, err := reader.pool.Query(ctx, `SELECT id, actor_label, action, object_type, object_id, created_at
		FROM np_audit_events WHERE `+whereSQL+` ORDER BY created_at DESC, id DESC LIMIT $`+strconv.Itoa(limitArg), args...)
	if err != nil {
		return audit.Page{}, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]audit.Entry, 0, normalized.Limit+1)
	for rows.Next() {
		var id int64
		var actor, action, objectType, objectID string
		var createdAt time.Time
		if err := rows.Scan(&id, &actor, &action, &objectType, &objectID, &createdAt); err != nil {
			return audit.Page{}, fmt.Errorf("scan audit event: %w", err)
		}
		if id <= 0 || createdAt.IsZero() {
			return audit.Page{}, errors.New("audit event has invalid timestamp or id")
		}
		events = append(events, audit.SafeEntry(uint64(id), createdAt, action, objectType, objectID, actor))
	}
	if err := rows.Err(); err != nil {
		return audit.Page{}, fmt.Errorf("iterate audit events: %w", err)
	}
	return finishDatabasePage(events, total, normalized.Limit), nil
}

func finishDatabasePage(events []audit.Entry, total, limit int) audit.Page {
	page := audit.Page{Events: events, Total: total}
	if len(events) > limit {
		page.Events = events[:limit]
		last := page.Events[len(page.Events)-1]
		page.HasMore = true
		page.NextCursor = audit.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page
}

var _ audit.Reader = (*AuditReader)(nil)
