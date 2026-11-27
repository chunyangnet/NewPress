package database

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/packagefs"
)

func (r *PostRepository) GetSnippetPage(ctx context.Context, id int64, a content.SnippetAccess, before int64, limit int) (content.SnippetPage, error) {
	if err := content.CheckSnippetAccess(a, false); err != nil {
		return content.SnippetPage{}, err
	}
	before, limit, err := content.NormalizeSnippetPage(before, limit)
	if err != nil {
		return content.SnippetPage{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SnippetPage{}, err
	}
	defer tx.Rollback(ctx)
	s, err := querySnippet(ctx, tx, id, false)
	if err != nil {
		return content.SnippetPage{}, err
	}
	if !content.SnippetReadable(s, a) {
		return content.SnippetPage{}, content.ErrNotFound
	}
	result := content.SnippetPage{Snippet: s, History: []content.SnippetRevisionInfo{}}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM np_snippet_revisions WHERE snippet_id=$1 AND version=$2`, id, s.CurrentVersion).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = content.ErrSnippetCorrupt
		}
		return result, err
	}
	result.Current, err = packagefs.DecodeJSON[content.SnippetRevision](bytes.NewReader(raw), 2<<20, "snippet current revision")
	if err != nil || result.Current.Version != s.CurrentVersion || content.ValidateSnippetRevision(s, result.Current) != nil {
		return result, content.ErrSnippetCorrupt
	}
	if s.Visibility == content.SnippetShared {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_snippet_revisions WHERE snippet_id=$1 AND version=$2)`, id, s.SharedSince).Scan(&exists); err != nil {
			return result, err
		}
		if !exists {
			return result, content.ErrSnippetCorrupt
		}
	}
	// JSON projection only touches the selected page and transfers no old bodies.
	rows, err := tx.Query(ctx, `SELECT version,convert_to((convert_from(data,'UTF8')::jsonb-'content_source'-'content_html')::text,'UTF8')
 FROM np_snippet_revisions WHERE snippet_id=$1 AND ($2::bigint=0 OR version<$2) AND ($3 OR version>=$4)
 ORDER BY version DESC LIMIT $5`, id, before, a.Manage, s.SharedSince, limit+1)
	if err != nil {
		return result, err
	}
	previous := int64(0)
	for rows.Next() {
		var version int64
		if err = rows.Scan(&version, &raw); err != nil {
			rows.Close()
			return result, err
		}
		info, e := packagefs.DecodeJSON[content.SnippetRevisionInfo](bytes.NewReader(raw), 4096, "snippet revision metadata")
		if e != nil || info.Version != version || content.ValidateSnippetRevisionInfo(s, info) != nil || previous > 0 && version >= previous {
			rows.Close()
			return result, content.ErrSnippetCorrupt
		}
		previous = version
		if len(result.History) == limit {
			result.NextBefore = result.History[len(result.History)-1].Version
			continue
		}
		result.History = append(result.History, info)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
