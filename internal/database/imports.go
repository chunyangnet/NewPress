package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
)

func loadImportState(ctx context.Context, tx pgx.Tx, batch content.ImportBatch) (content.ImportState, error) {
	state := content.ImportState{}
	keys, paths, slugs := []string{}, []string{}, []string{}
	for _, entry := range batch.Items {
		keys = append(keys, entry.Key)
		slugs = append(slugs, entry.Input.Slug)
		paths = append(paths, content.PublicPath(content.Post{Type: entry.Input.Type, Slug: entry.Input.Slug}))
		if entry.OldPath != "" {
			paths = append(paths, entry.OldPath)
			_, slug := redirectOccupiedKey(entry.OldPath)
			if slug != "" {
				slugs = append(slugs, slug)
			}
		}
	}
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_content_imports),(SELECT count(*) FROM np_content_redirects)`).Scan(&state.MappingCount, &state.RedirectCount); err != nil {
		return state, err
	}
	rows, err := tx.Query(ctx, `SELECT source,format,source_key,digest,metadata_digest,post_id,created_at FROM np_content_imports WHERE source=$1 AND format=$2 AND source_key=ANY($3::text[]) ORDER BY source_key`, batch.Source, batch.Format, keys)
	if err != nil {
		return state, err
	}
	ids := []int64{}
	for rows.Next() {
		var m content.ImportMapping
		if err = rows.Scan(&m.Source, &m.Format, &m.Key, &m.Digest, &m.MetadataDigest, &m.PostID, &m.CreatedAt); err != nil {
			rows.Close()
			return state, err
		}
		state.Mappings = append(state.Mappings, m)
		ids = append(ids, m.PostID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	rows, err = tx.Query(ctx, `SELECT id,type,slug,title,status,version FROM np_posts WHERE id=ANY($1::bigint[]) OR slug=ANY($2::text[]) ORDER BY id`, ids, slugs)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var p content.Post
		if err = rows.Scan(&p.ID, &p.Type, &p.Slug, &p.Title, &p.Status, &p.Version); err != nil {
			rows.Close()
			return state, err
		}
		state.Posts = append(state.Posts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	rows, err = tx.Query(ctx, `SELECT `+redirectColumns+` FROM np_content_redirects WHERE path=ANY($1::text[]) ORDER BY id`, paths)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		item, e := scanRedirect(rows)
		if e != nil {
			rows.Close()
			return state, e
		}
		state.Redirects = append(state.Redirects, item)
	}
	err = rows.Err()
	rows.Close()
	return state, err
}

func (r *PostRepository) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	batch, err := content.NormalizeImportBatch(batch, time.Now().UTC())
	if err != nil {
		return content.ImportPlan{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return content.ImportPlan{}, err
	}
	defer tx.Rollback(ctx)
	state, err := loadImportState(ctx, tx, batch)
	if err != nil {
		return content.ImportPlan{}, err
	}
	plan := content.BuildImportPlan(batch, state)
	return plan, tx.Commit(ctx)
}

func (r *PostRepository) ApplyImport(ctx context.Context, batch content.ImportBatch, revision string) (content.ImportPlan, error) {
	now := time.Now().UTC()
	batch, err := content.NormalizeImportBatch(batch, now)
	if err != nil {
		return content.ImportPlan{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.ImportPlan{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize source mappings and the global capacity limit across instances.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1760034106)`); err != nil {
		return content.ImportPlan{}, err
	}
	state, err := loadImportState(ctx, tx, batch)
	if err != nil {
		return content.ImportPlan{}, err
	}
	plan := content.BuildImportPlan(batch, state)
	if err = content.CheckImportPlan(plan, revision); err != nil {
		return plan, err
	}
	if plan.New == 0 {
		return plan, tx.Commit(ctx)
	}
	created := map[int]content.Post{}
	for i, entry := range batch.Items {
		if plan.Items[i].Action != "create" {
			continue
		}
		for _, name := range entry.Categories {
			term, e := ensureTermByName(ctx, tx, content.TaxonomyCategory, name)
			if e != nil {
				return content.ImportPlan{}, e
			}
			entry.Input.CategoryIDs = append(entry.Input.CategoryIDs, term.ID)
		}
		post, e := createImportedOrRegularPost(ctx, tx, entry.Input, now)
		if e != nil {
			return content.ImportPlan{}, e
		}
		created[i] = post
		if _, err = tx.Exec(ctx, `DELETE FROM np_outbox WHERE topic='notification.post' AND (payload->>'post_id')::bigint=$1`, post.ID); err != nil {
			return content.ImportPlan{}, err
		}
	}
	if err = lockRedirects(ctx, tx); err != nil {
		return content.ImportPlan{}, err
	}
	for i, entry := range batch.Items {
		post, ok := created[i]
		if !ok {
			continue
		}
		var canonicalOccupied bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_content_redirects WHERE path=$1)`, content.PublicPath(post)).Scan(&canonicalOccupied); err != nil {
			return content.ImportPlan{}, err
		}
		if canonicalOccupied {
			return content.ImportPlan{}, content.ErrImportConflict
		}
		if entry.OldPath != "" && entry.OldPath != content.PublicPath(post) {
			typ, slug := redirectOccupiedKey(entry.OldPath)
			var occupied bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_posts WHERE type=$1 AND slug=$2) OR EXISTS(SELECT 1 FROM np_content_redirects WHERE path=$3)`, typ, slug, entry.OldPath).Scan(&occupied); err != nil {
				return content.ImportPlan{}, err
			}
			if occupied {
				return content.ImportPlan{}, content.ErrImportConflict
			}
			if err = redirectCapacity(ctx, tx); err != nil {
				return content.ImportPlan{}, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO np_content_redirects(path,post_id,created_at,updated_at) VALUES($1,$2,$3,$3)`, entry.OldPath, post.ID, now); err != nil {
				return content.ImportPlan{}, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO np_content_imports(source,format,source_key,digest,metadata_digest,post_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, batch.Source, batch.Format, entry.Key, entry.Digest, entry.MetadataDigest, post.ID, now); err != nil {
			return content.ImportPlan{}, fmt.Errorf("save content import identity: %w", err)
		}
		plan.Items[i].Action = "created"
		plan.Items[i].PostID = post.ID
		plan.Items[i].Version = post.Version
	}
	return plan, tx.Commit(ctx)
}
