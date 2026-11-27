package database

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
)

const redirectColumns = `id,path,post_id,version,automatic,created_at,updated_at`

func redirectOccupiedKey(path string) (string, string) {
	if strings.HasPrefix(path, "/flash/") && strings.HasSuffix(path, "/") {
		return content.TypeFlash, strings.TrimSuffix(strings.TrimPrefix(path, "/flash/"), "/")
	}
	for _, pair := range [][2]string{{"/posts/", content.TypePost}, {"/pages/", content.TypePage}} {
		if strings.HasPrefix(path, pair[0]) {
			return pair[1], strings.TrimPrefix(path, pair[0])
		}
	}
	return "", ""
}

func scanRedirect(row interface{ Scan(...any) error }) (content.Redirect, error) {
	var item content.Redirect
	err := row.Scan(&item.ID, &item.Path, &item.PostID, &item.Version, &item.Automatic, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNotFound
	}
	return item, err
}

func (r *PostRepository) ListRedirects(ctx context.Context, query content.RedirectQuery) (content.RedirectList, error) {
	q, err := content.NormalizeRedirectQuery(query)
	if err != nil {
		return content.RedirectList{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.RedirectList{}, err
	}
	defer tx.Rollback(ctx)
	result := content.RedirectList{Items: []content.Redirect{}}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_content_redirects WHERE $1='' OR strpos(lower(path),lower($1))>0`, q.Search).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT `+redirectColumns+` FROM np_content_redirects WHERE $1='' OR strpos(lower(path),lower($1))>0 ORDER BY id DESC LIMIT $2 OFFSET $3`, q.Search, q.Limit, q.Offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		item, e := scanRedirect(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (r *PostRepository) GetRedirect(ctx context.Context, id int64) (content.Redirect, error) {
	return scanRedirect(r.pool.QueryRow(ctx, `SELECT `+redirectColumns+` FROM np_content_redirects WHERE id=$1`, id))
}

// Content rows precede the registry advisory lock everywhere. Neither registry
// deletion nor automatic capture locks another content row.
func lockRedirects(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034105)`)
	return err
}

func redirectCapacity(ctx context.Context, tx pgx.Tx) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_content_redirects`).Scan(&count); err != nil {
		return err
	}
	if count >= content.MaxRedirects {
		return content.RedirectLimitError()
	}
	return nil
}

func (r *PostRepository) SaveRedirect(ctx context.Context, id int64, m content.Mutation, input content.RedirectInput) (content.Redirect, error) {
	input, fields := content.NormalizeRedirectInput(input)
	if len(fields) > 0 {
		return content.Redirect{}, &content.ValidationError{Fields: fields}
	}
	if id < 0 {
		return content.Redirect{}, content.ErrNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Redirect{}, err
	}
	defer tx.Rollback(ctx)
	var target int64
	err = tx.QueryRow(ctx, `SELECT id FROM np_posts WHERE id=$1 FOR SHARE`, input.PostID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Redirect{}, &content.ValidationError{Fields: map[string]string{"post_id": "目标内容不存在"}}
	}
	if err != nil {
		return content.Redirect{}, err
	}
	if err = lockRedirects(ctx, tx); err != nil {
		return content.Redirect{}, err
	}
	var current content.Redirect
	if id > 0 {
		current, err = scanRedirect(tx.QueryRow(ctx, `SELECT `+redirectColumns+` FROM np_content_redirects WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return current, err
		}
	}
	if current.Version != m.ExpectedVersion || current.Version >= math.MaxInt64-1 {
		return current, content.ErrVersionConflict
	}
	var occupied bool
	occupiedType, occupiedSlug := redirectOccupiedKey(input.Path)
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_posts WHERE type=$3 AND slug=$4) OR EXISTS(SELECT 1 FROM np_content_redirects WHERE path=$1 AND id<>$2)`, input.Path, id, occupiedType, occupiedSlug).Scan(&occupied)
	if err != nil {
		return current, err
	}
	if occupied {
		return current, content.ErrRedirectConflict
	}
	if id > 0 && input == current.RedirectInput {
		return current, tx.Commit(ctx)
	}
	now := time.Now().UTC()
	if id == 0 {
		if err = redirectCapacity(ctx, tx); err != nil {
			return current, err
		}
		current, err = scanRedirect(tx.QueryRow(ctx, `INSERT INTO np_content_redirects(path,post_id,created_at,updated_at) VALUES($1,$2,$3,$3) RETURNING `+redirectColumns, input.Path, input.PostID, now))
	} else {
		current, err = scanRedirect(tx.QueryRow(ctx, `UPDATE np_content_redirects SET path=$2,post_id=$3,version=version+1,automatic=false,updated_at=$4 WHERE id=$1 RETURNING `+redirectColumns, id, input.Path, input.PostID, now))
	}
	if err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}

func (r *PostRepository) DeleteRedirect(ctx context.Context, id int64, m content.Mutation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockRedirects(ctx, tx); err != nil {
		return err
	}
	current, err := scanRedirect(tx.QueryRow(ctx, `SELECT `+redirectColumns+` FROM np_content_redirects WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if current.Version != m.ExpectedVersion {
		return content.ErrVersionConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM np_content_redirects WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostRepository) ResolveRedirect(ctx context.Context, path string) (string, error) {
	if !content.ValidRedirectPath(path) {
		return "", content.ErrNotFound
	}
	var target string
	occupiedType, occupiedSlug := redirectOccupiedKey(path)
	err := r.pool.QueryRow(ctx, `SELECT CASE WHEN p.type='flash' THEN '/flash/'||p.slug||'/' WHEN p.type='page' THEN '/pages/'||p.slug ELSE '/posts/'||p.slug END
	FROM np_content_redirects r JOIN np_posts p ON p.id=r.post_id WHERE r.path=$1 AND p.status IN ('publish','future') AND p.published_at<=now()
	AND NOT EXISTS(SELECT 1 FROM np_posts occupied WHERE occupied.type=$2 AND occupied.slug=$3)`, path, occupiedType, occupiedSlug).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNotFound
	}
	return target, err
}

func preserveOldContentPath(ctx context.Context, tx pgx.Tx, current content.Post, newType, newSlug string, now time.Time) error {
	if !content.PublicAt(current, now) || current.Type == newType && current.Slug == newSlug {
		return nil
	}
	if err := lockRedirects(ctx, tx); err != nil {
		return err
	}
	path := content.PublicPath(current)
	var target int64
	err := tx.QueryRow(ctx, `SELECT post_id FROM np_content_redirects WHERE path=$1`, path).Scan(&target)
	if err == nil {
		if target != current.ID {
			return content.ErrRedirectConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err = redirectCapacity(ctx, tx); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_content_redirects(path,post_id,automatic,created_at,updated_at) VALUES($1,$2,true,$3,$3)`, path, current.ID, now)
	return err
}
