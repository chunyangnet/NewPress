package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

// Snippet writes lock this before account, snippet and ordered post rows.
// Ordinary post mutations never acquire the snippet lock.
const snippetWriteLock int64 = 1760034190

func decodeSnippet(row scanner) (content.Snippet, error) {
	var id, version int64
	var visibility string
	var raw []byte
	if err := row.Scan(&id, &version, &visibility, &raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return content.Snippet{}, content.ErrNotFound
		}
		return content.Snippet{}, err
	}
	s, err := packagefs.DecodeJSON[content.Snippet](bytes.NewReader(raw), 4096, "snippet")
	if err != nil || s.ID != id || s.Version != version || s.Visibility != visibility || content.ValidateSnippetMetadata(s) != nil {
		return content.Snippet{}, content.ErrSnippetCorrupt
	}
	return s, nil
}

func querySnippet(ctx context.Context, q postQuerier, id int64, lock bool) (content.Snippet, error) {
	if err := snippetMaintenanceGate(ctx, q); err != nil {
		return content.Snippet{}, err
	}
	sql := `SELECT id,version,visibility,data FROM np_snippets WHERE id=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return decodeSnippet(q.QueryRow(ctx, sql, id))
}

func loadSnippetRecord(ctx context.Context, tx pgx.Tx, s content.Snippet) (content.SnippetRecord, error) {
	r := content.SnippetRecord{Snippet: s, Revisions: []content.SnippetRevision{}}
	err := walkSnippetHistory(ctx, tx, s, func(revision content.SnippetRevision) {
		r.Revisions = append(r.Revisions, revision)
	})
	return r, err
}

// Saving needs every historical body checked, but only appends one revision.
// A nil visitor avoids retaining the full history just to validate it.
func walkSnippetHistory(ctx context.Context, tx pgx.Tx, s content.Snippet, visit func(content.SnippetRevision)) error {
	validation, err := content.NewSnippetHistoryValidator(s)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT version,data FROM np_snippet_revisions WHERE snippet_id=$1 ORDER BY version LIMIT $2`, s.ID, content.MaxSnippetRevisions+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var version int64
		var raw []byte
		if err = rows.Scan(&version, &raw); err != nil {
			return err
		}
		size += len(raw)
		if size > content.MaxSnippetStorageBytes {
			return content.ErrSnippetCorrupt
		}
		rev, err := packagefs.DecodeJSON[content.SnippetRevision](bytes.NewReader(raw), 2<<20, "snippet revision")
		if err != nil || rev.Version != version || validation.Add(rev) != nil {
			return content.ErrSnippetCorrupt
		}
		if visit != nil {
			visit(rev)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return validation.Complete()
}

// Persistent account and role locks are held through commit. Bootstrap has no
// persistent user row and is validated by the HTTP authentication manager.
func lockSnippetAccount(ctx context.Context, tx pgx.Tx, a content.SnippetAccess) (content.SnippetAccess, error) {
	if err := content.CheckSnippetAccess(a, true); err != nil {
		return a, err
	}
	if a.UserID == 0 {
		if !strings.HasPrefix(a.Identity, "legacy:") || !a.Manage {
			return a, content.ErrSnippetPermission
		}
		return a, nil
	}
	if a.Identity != fmt.Sprintf("user:%d", a.UserID) {
		return a, content.ErrSnippetPermission
	}
	u, err := scanAuthUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM np_users u WHERE u.id=$1 FOR SHARE OF u`, a.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, content.ErrSnippetPermission
	}
	if err != nil {
		return a, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.slug,r.name,r.description,r.capabilities,r.built_in FROM np_user_roles ur JOIN np_roles r ON r.id=ur.role_id WHERE ur.user_id=$1 ORDER BY r.slug FOR SHARE OF ur,r`, a.UserID)
	if err != nil {
		return a, err
	}
	for rows.Next() {
		role, e := scanAuthRole(rows, false)
		if e != nil {
			rows.Close()
			return a, e
		}
		u.Roles = append(u.Roles, role)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return a, err
	}
	if u.Status != auth.UserStatusActive || !u.Can(auth.CapabilityAdminAccess) {
		return a, content.ErrSnippetPermission
	}
	a.Read = a.Read && u.Can(auth.CapabilityContentRead)
	a.Manage = a.Manage && u.Can(auth.CapabilityContentWrite)
	a.WriteOwn = a.WriteOwn && u.Can(auth.CapabilityContentWriteOwn)
	return a, content.CheckSnippetAccess(a, true)
}

func (r *PostRepository) ListSnippets(ctx context.Context, a content.SnippetAccess, limit, offset int) (content.SnippetList, error) {
	if err := content.CheckSnippetAccess(a, false); err != nil {
		return content.SnippetList{}, err
	}
	if limit <= 0 {
		limit = 24
	}
	limit = min(limit, 100)
	offset = max(0, offset)
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetList{}, err
	}
	defer tx.Rollback(ctx)
	result := content.SnippetList{Items: []content.Snippet{}}
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return result, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_snippets WHERE $1 OR visibility='shared'`, a.Manage).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT id,version,visibility,data FROM np_snippets WHERE $1 OR visibility='shared' ORDER BY id DESC LIMIT $2 OFFSET $3`, a.Manage, limit, offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		s, e := decodeSnippet(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Items = append(result.Items, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (r *PostRepository) GetSnippet(ctx context.Context, id int64, a content.SnippetAccess) (content.SnippetRecord, error) {
	if err := content.CheckSnippetAccess(a, false); err != nil {
		return content.SnippetRecord{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetRecord{}, err
	}
	defer tx.Rollback(ctx)
	s, err := querySnippet(ctx, tx, id, false)
	if err != nil {
		return content.SnippetRecord{}, err
	}
	if !content.SnippetReadable(s, a) {
		return content.SnippetRecord{}, content.ErrNotFound
	}
	record, err := loadSnippetRecord(ctx, tx, s)
	if err != nil {
		return content.SnippetRecord{}, err
	}
	return content.SnippetRecordForAccess(record, a), tx.Commit(ctx)
}

func (r *PostRepository) SaveSnippet(ctx context.Context, id int64, m content.Mutation, input content.SnippetInput, a content.SnippetAccess) (content.Snippet, error) {
	if content.CheckSnippetAccess(a, true) != nil || !a.Manage {
		return content.Snippet{}, content.ErrSnippetPermission
	}
	if _, _, err := content.NormalizeSnippetInput(input); err != nil {
		return content.Snippet{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Snippet{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
		return content.Snippet{}, err
	}
	if err = snippetMaintenanceGate(ctx, tx); err != nil {
		return content.Snippet{}, err
	}
	a, err = lockSnippetAccount(ctx, tx, a)
	if err != nil {
		return content.Snippet{}, err
	}
	if !a.Manage {
		return content.Snippet{}, content.ErrSnippetPermission
	}
	var current content.SnippetRecord
	creating := id == 0
	if !creating {
		s, e := querySnippet(ctx, tx, id, true)
		if e != nil {
			return content.Snippet{}, e
		}
		if err = walkSnippetHistory(ctx, tx, s, nil); err != nil {
			return content.Snippet{}, err
		}
		current.Snippet = s
	}
	if creating {
		if m.ExpectedVersion != 0 {
			return content.Snippet{}, content.ErrVersionConflict
		}
		if err = tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('np_snippets','id'))`).Scan(&id); err != nil {
			return content.Snippet{}, err
		}
	}
	next, err := content.NextSnippetRecord(current, id, m, input, a, time.Now().UTC())
	if err != nil {
		return content.Snippet{}, err
	}
	// Existing history bytes are unchanged. Charge exactly the new encoded
	// revision, plus the existing fixed metadata reservation when creating.
	revision, err := json.Marshal(next.Revisions[0])
	if err != nil {
		return content.Snippet{}, err
	}
	addedBytes := len(revision)
	if creating {
		addedBytes += 4096
	}
	var count, revCount, total int64
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_snippets),(SELECT count(*) FROM np_snippet_revisions),(SELECT count(*)*4096 FROM np_snippets)+(SELECT coalesce(sum(octet_length(data)),0) FROM np_snippet_revisions)`).Scan(&count, &revCount, &total); err != nil {
		return content.Snippet{}, err
	}
	if creating && count >= content.MaxSnippets || revCount >= content.MaxSnippetRevisions || total+int64(addedBytes) > content.MaxSnippetStorageBytes {
		return content.Snippet{}, content.ErrSnippetCapacity
	}
	meta, _ := json.Marshal(next.Snippet)
	if creating {
		_, err = tx.Exec(ctx, `INSERT INTO np_snippets(id,version,visibility,data) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4)`, id, next.Version, next.Visibility, meta)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_snippets SET version=$2,visibility=$3,data=$4 WHERE id=$1`, id, next.Version, next.Visibility, meta)
	}
	if err != nil {
		return content.Snippet{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_snippet_revisions(snippet_id,version,data) VALUES($1,$2,$3)`, id, next.CurrentVersion, revision); err != nil {
		return content.Snippet{}, err
	}
	return next.Snippet, tx.Commit(ctx)
}

func (r *PostRepository) WithdrawSnippet(ctx context.Context, id int64, m content.Mutation, a content.SnippetAccess) (content.Snippet, error) {
	if content.CheckSnippetAccess(a, true) != nil || !a.Manage {
		return content.Snippet{}, content.ErrSnippetPermission
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Snippet{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, snippetWriteLock); err != nil {
		return content.Snippet{}, err
	}
	a, err = lockSnippetAccount(ctx, tx, a)
	if err != nil {
		return content.Snippet{}, err
	}
	s, err := querySnippet(ctx, tx, id, true)
	if err != nil {
		return content.Snippet{}, err
	}
	next, err := content.WithdrawSnippetRecord(content.SnippetRecord{Snippet: s}, m, a, time.Now().UTC())
	if err != nil {
		return content.Snippet{}, err
	}
	meta, _ := json.Marshal(next.Snippet)
	if _, err = tx.Exec(ctx, `UPDATE np_snippets SET version=$2,visibility=$3,data=$4 WHERE id=$1`, id, next.Version, next.Visibility, meta); err != nil {
		return content.Snippet{}, err
	}
	return next.Snippet, tx.Commit(ctx)
}

func (r *PostRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	if len(keys) > content.MaxSnippetResolve {
		return nil, content.ErrSnippetCapacity
	}
	result := map[contenthtml.SnippetKey]string{}
	if len(keys) == 0 {
		return result, nil
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	bodies, err := loadSharedSnippetBodies(ctx, tx, keys)
	if err != nil {
		return nil, err
	}
	for key, body := range bodies {
		result[key] = body.Revision.ContentHTML
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
