package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"newpress.local/newpress/internal/content"
)

const publicSearchJoin = ` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id `
const publicSearchFilters = ` WHERE (p.type=$1 OR ($2::boolean AND p.type='flash'))
 AND (NOT $3::boolean OR p.metadata->>'flash_important'='true')
 AND p.status IN ('publish','future') AND p.published_at IS NOT NULL AND p.published_at <= now()
 AND ($4='' OR EXISTS (SELECT 1 FROM np_post_terms pt JOIN np_terms t ON t.id=pt.term_id
     JOIN np_taxonomies tax ON tax.id=t.taxonomy_id WHERE pt.post_id=p.id AND tax.slug='category'
     AND (t.slug=$5 OR t.public_search_name=np_public_search_normalize_v1($4))))
 AND ($6::bigint=0 OR (p.author_id=$6 AND u.status<>'system'))
 AND ($7='' OR ((p.author_id IS NULL OR u.status='system') AND COALESCE(NULLIF(p.metadata->>'author_name',''),'NewPress')=$7))
 AND ($8::bigint[] IS NULL OR p.id=ANY($8::bigint[]))
 AND ($9::bigint[] IS NULL OR (p.author_id=ANY($9::bigint[]) AND u.status<>'system'))`

// Each field group contributes once, matching content.PublicSearchScore.
// strpos treats %, _ and backslashes literally.
const publicSearchRank = ` CROSS JOIN LATERAL (SELECT
   (CASE WHEN strpos(p.public_search_title,$10)>0 THEN 64 ELSE 0 END)
 + (CASE WHEN strpos(p.public_search_excerpt,$10)>0 THEN 16 ELSE 0 END)
 + (CASE WHEN EXISTS (SELECT 1 FROM np_post_terms pt JOIN np_terms t ON t.id=pt.term_id
       JOIN np_taxonomies tax ON tax.id=t.taxonomy_id WHERE pt.post_id=p.id AND tax.slug IN ('category','post_tag')
       AND strpos(t.public_search_name,$10)>0) THEN 8 ELSE 0 END)
 + (CASE WHEN strpos(COALESCE(CASE WHEN u.status<>'system' THEN u.public_search_name END,p.public_search_author),$10)>0 THEN 4 ELSE 0 END)
 + (CASE WHEN strpos(p.public_search_body,$10)>0 THEN 1 ELSE 0 END) AS score) search `

func (r *PostRepository) SearchPublished(ctx context.Context, query content.PublishedQuery) (content.PublishedPage, error) {
	query, err := content.NormalizePublishedQuery(query)
	if err != nil {
		return content.PublishedPage{}, err
	}
	for attempt := 0; ; attempt++ {
		page, err := r.searchPublishedSnapshot(ctx, query)
		var pgErr *pgconn.PgError
		// Concurrent first readers may build the same old missing projection.
		// Only derived data is written; retry with a fresh consistent snapshot.
		if attempt >= 2 || !errors.As(err, &pgErr) || pgErr.Code != "40001" || ctx.Err() != nil {
			return page, err
		}
	}
}

func (r *PostRepository) searchPublishedSnapshot(ctx context.Context, query content.PublishedQuery) (content.PublishedPage, error) {
	mode := pgx.ReadOnly
	if query.Search != "" {
		mode = pgx.ReadWrite
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: mode})
	if err != nil {
		return content.PublishedPage{}, fmt.Errorf("begin public content search: %w", err)
	}
	defer tx.Rollback(ctx)
	args := []any{query.Type, query.IncludeFlash, query.Important, query.Category, content.NormalizeTermSlug(query.Category), query.AuthorID, query.LegacyAuthor, query.PostIDs, query.AuthorIDs}
	from := publicSearchJoin + publicSearchFilters
	if query.Search != "" {
		if err := preparePublicSearchBodies(ctx, tx, from, args); err != nil {
			return content.PublishedPage{}, err
		}
		args = append(args, content.NormalizePublicSearchText(query.Search))
		from = publicSearchJoin + publicSearchRank + publicSearchFilters + " AND search.score>0"
	}
	var total int
	if err := tx.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		return content.PublishedPage{}, fmt.Errorf("count public content search: %w", err)
	}
	order := map[string]string{"published": "p.published_at DESC,p.id DESC", "updated": "p.updated_at DESC,p.id DESC", "oldest": "p.published_at ASC,p.id ASC", "title": `p.public_search_title COLLATE "C" ASC,p.id DESC`, "relevance": "search.score DESC,p.published_at DESC,p.id DESC"}[query.Sort]
	if query.Sort == "published" && !query.IgnorePinning {
		order = pinnedPostOrder + order
	}
	if query.Sort == "selection" {
		order = "array_position($8::bigint[],p.id) ASC"
	}
	limit := fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, query.Limit, query.Offset)
	rows, err := tx.Query(ctx, "SELECT "+postColumns+from+" ORDER BY "+order+limit, args...)
	if err != nil {
		return content.PublishedPage{}, fmt.Errorf("search public content: %w", err)
	}
	items := make([]content.Post, 0, min(query.Limit, total))
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			rows.Close()
			return content.PublishedPage{}, err
		}
		items = append(items, post)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return content.PublishedPage{}, err
	}
	if err := hydratePostTerms(ctx, tx, items); err != nil {
		return content.PublishedPage{}, err
	}
	return content.PublishedPage{Items: items, Total: total}, tx.Commit(ctx)
}
