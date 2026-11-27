package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"newpress.local/newpress/internal/content"
)

const seriesColumns = `s.id,s.kind,s.status,s.title,s.slug,s.description,s.cover_url,s.version,s.created_at,s.updated_at`
const seriesIDs = `ARRAY(SELECT sp.post_id FROM np_series_posts sp JOIN np_posts p ON p.id=sp.post_id WHERE sp.series_id=s.id AND (NOT $1 OR (p.type='post' AND p.status IN ('publish','future') AND p.published_at<=now())) ORDER BY sp.position)`
const seriesWhere = `WHERE (NOT $1 OR s.status='publish') AND ($2='' OR s.kind=$2)
 AND ($3='' OR strpos(lower(s.title || E'\n' || s.description),lower($3))>0)
 AND ($4::bigint=0 OR EXISTS(SELECT 1 FROM np_series_posts sp JOIN np_posts p ON p.id=sp.post_id WHERE sp.series_id=s.id AND sp.post_id=$4 AND (NOT $1 OR (p.type='post' AND p.status IN ('publish','future') AND p.published_at<=now()))))`

func scanSeries(row interface{ Scan(...any) error }) (content.Series, error) {
	var s content.Series
	err := row.Scan(&s.ID, &s.Kind, &s.Status, &s.Title, &s.Slug, &s.Description, &s.CoverURL, &s.Version, &s.CreatedAt, &s.UpdatedAt, &s.PostIDs)
	return s, err
}

func (r *PostRepository) ListSeries(ctx context.Context, query content.SeriesQuery) (content.SeriesList, error) {
	q, err := content.NormalizeSeriesQuery(query)
	if err != nil {
		return content.SeriesList{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.SeriesList{}, err
	}
	defer tx.Rollback(ctx)
	result := content.SeriesList{Items: []content.Series{}}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_series s `+seriesWhere, q.PublicOnly, q.Kind, q.Search, q.PostID).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT `+seriesColumns+`,`+seriesIDs+` FROM np_series s `+seriesWhere+` ORDER BY s.id DESC LIMIT $5 OFFSET $6`, q.PublicOnly, q.Kind, q.Search, q.PostID, q.Limit, q.Offset)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		s, scanErr := scanSeries(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
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

func querySeries(ctx context.Context, q rowQuerier, id int64, lock bool) (content.Series, error) {
	sql := `SELECT ` + seriesColumns + `,` + seriesIDs + ` FROM np_series s WHERE s.id=$2`
	if lock {
		sql += ` FOR UPDATE OF s`
	}
	s, err := scanSeries(q.QueryRow(ctx, sql, false, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNotFound
	}
	return s, err
}
func (r *PostRepository) GetSeries(ctx context.Context, id int64) (content.Series, error) {
	return querySeries(ctx, r.pool, id, false)
}

func (r *PostRepository) SaveSeries(ctx context.Context, id int64, m content.Mutation, input content.SeriesInput) (content.Series, error) {
	input, fields := content.NormalizeSeriesInput(input)
	if len(fields) > 0 {
		return content.Series{}, &content.ValidationError{Fields: fields}
	}
	if id < 0 {
		return content.Series{}, content.ErrNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Series{}, err
	}
	defer tx.Rollback(ctx)
	var current content.Series
	if id == 0 {
		if m.ExpectedVersion != 0 {
			return current, content.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034104)`); err != nil {
			return current, err
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM np_series`).Scan(&count); err != nil {
			return current, err
		}
		if count >= content.MaxSeries {
			return current, &content.ValidationError{Fields: map[string]string{"title": "专题与系列已达到数量上限"}}
		}
	} else {
		current, err = querySeries(ctx, tx, id, true)
		if err != nil {
			return current, err
		}
		if current.Version != m.ExpectedVersion {
			return content.Series{}, content.ErrVersionConflict
		}
	}
	// Lock in ID order so concurrent chapter edits cannot form lock cycles.
	rows, err := tx.Query(ctx, `SELECT id,type FROM np_posts WHERE id=ANY($1::bigint[]) ORDER BY id FOR SHARE`, input.PostIDs)
	if err != nil {
		return current, err
	}
	valid := 0
	for rows.Next() {
		var postID int64
		var kind string
		if err = rows.Scan(&postID, &kind); err != nil {
			rows.Close()
			return current, err
		}
		if kind == content.TypePost {
			valid++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return current, err
	}
	if valid != len(input.PostIDs) {
		return content.Series{}, &content.ValidationError{Fields: map[string]string{"post_ids": "收录的文章已不存在或不再是文章类型，请重新选择"}}
	}
	if id > 0 && content.EqualSeriesInput(current.SeriesInput, input) {
		return current, tx.Commit(ctx)
	}
	now := time.Now().UTC()
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO np_series(kind,status,title,slug,description,cover_url,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7) RETURNING id`, input.Kind, input.Status, input.Title, input.Slug, input.Description, input.CoverURL, now).Scan(&id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_series SET kind=$2,status=$3,title=$4,slug=$5,description=$6,cover_url=$7,version=version+1,updated_at=$8 WHERE id=$1`, id, input.Kind, input.Status, input.Title, input.Slug, input.Description, input.CoverURL, now)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			err = content.ErrSlugConflict
		}
		return content.Series{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM np_series_posts WHERE series_id=$1`, id); err != nil {
		return content.Series{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_series_posts(series_id,post_id,position) SELECT $1,post_id,ordinality-1 FROM unnest($2::bigint[]) WITH ORDINALITY AS chapters(post_id,ordinality)`, id, input.PostIDs); err != nil {
		return content.Series{}, err
	}
	item, err := querySeries(ctx, tx, id, false)
	if err != nil {
		return item, err
	}
	return item, tx.Commit(ctx)
}

func (r *PostRepository) DeleteSeries(ctx context.Context, id int64, m content.Mutation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	item, err := querySeries(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if item.Version != m.ExpectedVersion {
		return content.ErrVersionConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM np_series WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PostRepository) PublishedSeries(ctx context.Context, slug string) (content.Series, []content.Post, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.Series{}, nil, err
	}
	defer tx.Rollback(ctx)
	item, err := scanSeries(tx.QueryRow(ctx, `SELECT `+seriesColumns+`,`+seriesIDs+` FROM np_series s WHERE slug=$2 AND s.status='publish'`, true, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		err = content.ErrNotFound
	}
	if err != nil {
		return item, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+postColumns+` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id WHERE p.id=ANY($1::bigint[]) ORDER BY array_position($1::bigint[],p.id)`, item.PostIDs)
	if err != nil {
		return item, nil, err
	}
	posts := []content.Post{}
	for rows.Next() {
		p, scanErr := scanPost(rows)
		if scanErr != nil {
			rows.Close()
			return item, nil, scanErr
		}
		posts = append(posts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return item, nil, err
	}
	if err = hydratePostTerms(ctx, tx, posts); err != nil {
		return item, nil, err
	}
	return item, posts, tx.Commit(ctx)
}
