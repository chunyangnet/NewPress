package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"newpress.local/newpress/internal/content"
)

type rowsQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *PostRepository) ListPublishedByTerm(ctx context.Context, taxonomy, slug string, limit, offset int) (content.Term, []content.Post, int, error) {
	taxonomy = strings.ToLower(strings.TrimSpace(taxonomy))
	slug = content.NormalizeTermSlug(slug)
	if !content.ValidTaxonomy(taxonomy) || !content.ValidTermSlug(slug) {
		return content.Term{}, nil, 0, content.ErrTermNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	term, err := queryTermBySlug(ctx, r.pool, taxonomy, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Term{}, nil, 0, content.ErrTermNotFound
	}
	if err != nil {
		return content.Term{}, nil, 0, fmt.Errorf("load term archive: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM np_post_terms relation
		JOIN np_posts post ON post.id=relation.post_id
		WHERE relation.term_id=$1 AND post.type='post' AND post.status IN ('publish','future')
		  AND post.published_at IS NOT NULL AND post.published_at <= now()`, term.ID).Scan(&term.Count); err != nil {
		return content.Term{}, nil, 0, fmt.Errorf("count term archive posts: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT `+postColumns+`
		FROM np_post_terms relation
		JOIN np_posts p ON p.id=relation.post_id
		LEFT JOIN np_users u ON u.id=p.author_id
		WHERE relation.term_id=$1 AND p.type='post' AND p.status IN ('publish','future')
		  AND p.published_at IS NOT NULL AND p.published_at <= now()
		ORDER BY `+pinnedPostOrder+`p.published_at DESC, p.id DESC
		LIMIT $2 OFFSET $3`, term.ID, limit, offset)
	if err != nil {
		return content.Term{}, nil, 0, fmt.Errorf("list term archive posts: %w", err)
	}
	defer rows.Close()
	posts := make([]content.Post, 0, min(limit, term.Count))
	for rows.Next() {
		post, scanErr := scanPost(rows)
		if scanErr != nil {
			return content.Term{}, nil, 0, fmt.Errorf("scan term archive post: %w", scanErr)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return content.Term{}, nil, 0, fmt.Errorf("iterate term archive posts: %w", err)
	}
	if err := hydratePostTerms(ctx, r.pool, posts); err != nil {
		return content.Term{}, nil, 0, fmt.Errorf("load term archive post terms: %w", err)
	}
	return term, posts, term.Count, nil
}

func (r *PostRepository) ListTerms(ctx context.Context, query content.TermQuery) (content.TermList, error) {
	query, err := content.NormalizeTermQuery(query)
	if err != nil {
		return content.TermList{}, err
	}
	var total int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM np_terms term
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		WHERE ($1='' OR taxonomy.slug=$1)
		  AND ($2='' OR term.name ILIKE '%' || $2 || '%' OR term.slug ILIKE '%' || $2 || '%' OR term.description ILIKE '%' || $2 || '%')`,
		query.Taxonomy, query.Search).Scan(&total); err != nil {
		return content.TermList{}, fmt.Errorf("count terms: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE tree(id, depth, sort_path, visited) AS (
			SELECT term.id, 0, ARRAY[lower(term.name) || ':' || lpad(term.id::text, 20, '0')], ARRAY[term.id]
			FROM np_terms term
			WHERE term.parent_id IS NULL
			UNION ALL
			SELECT child.id, tree.depth + 1,
			       tree.sort_path || (lower(child.name) || ':' || lpad(child.id::text, 20, '0')),
			       tree.visited || child.id
			FROM np_terms child
			JOIN tree ON tree.id=child.parent_id
			WHERE tree.depth < 63 AND NOT child.id=ANY(tree.visited)
		)
		SELECT term.id, taxonomy.slug, COALESCE(term.parent_id,0), COALESCE(parent.name,''),
		       term.slug, term.name, term.description,
		       (SELECT count(*) FROM np_post_terms relation JOIN np_posts post ON post.id=relation.post_id
		        WHERE relation.term_id=term.id AND post.type='post' AND post.status IN ('publish','future')
		          AND post.published_at IS NOT NULL AND post.published_at <= now()),
		       COALESCE(tree.depth,0), term.is_system, term.version, term.created_at, term.updated_at
		FROM np_terms term
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		LEFT JOIN np_terms parent ON parent.id=term.parent_id
		LEFT JOIN tree ON tree.id=term.id
		WHERE ($1='' OR taxonomy.slug=$1)
		  AND ($2='' OR term.name ILIKE '%' || $2 || '%' OR term.slug ILIKE '%' || $2 || '%' OR term.description ILIKE '%' || $2 || '%')
		ORDER BY CASE taxonomy.slug WHEN 'category' THEN 0 ELSE 1 END,
		         COALESCE(tree.sort_path, ARRAY[lower(term.name) || ':' || lpad(term.id::text, 20, '0')]), term.id
		LIMIT $3 OFFSET $4`, query.Taxonomy, query.Search, query.Limit, query.Offset)
	if err != nil {
		return content.TermList{}, fmt.Errorf("list terms: %w", err)
	}
	defer rows.Close()
	items := make([]content.Term, 0, min(query.Limit, total))
	for rows.Next() {
		term, scanErr := scanTerm(rows)
		if scanErr != nil {
			return content.TermList{}, fmt.Errorf("scan term: %w", scanErr)
		}
		items = append(items, term)
	}
	if err := rows.Err(); err != nil {
		return content.TermList{}, fmt.Errorf("iterate terms: %w", err)
	}
	counts := map[string]int{content.TaxonomyCategory: 0, content.TaxonomyTag: 0}
	countRows, err := r.pool.Query(ctx, `SELECT taxonomy.slug,count(*) FROM np_terms term JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id GROUP BY taxonomy.slug`)
	if err != nil {
		return content.TermList{}, fmt.Errorf("count taxonomy terms: %w", err)
	}
	for countRows.Next() {
		var taxonomy string
		var count int
		if err := countRows.Scan(&taxonomy, &count); err != nil {
			countRows.Close()
			return content.TermList{}, fmt.Errorf("scan taxonomy term count: %w", err)
		}
		counts[taxonomy] = count
	}
	if err := countRows.Err(); err != nil {
		countRows.Close()
		return content.TermList{}, fmt.Errorf("iterate taxonomy term counts: %w", err)
	}
	countRows.Close()
	return content.TermList{Items: items, Total: total, Counts: counts}, nil
}

func (r *PostRepository) GetTermByID(ctx context.Context, id int64) (content.Term, error) {
	term, err := queryTermByID(ctx, r.pool, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Term{}, content.ErrTermNotFound
	}
	if err != nil {
		return content.Term{}, fmt.Errorf("load term: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM np_post_terms relation JOIN np_posts post ON post.id=relation.post_id WHERE relation.term_id=$1 AND post.type='post' AND post.status IN ('publish','future') AND post.published_at IS NOT NULL AND post.published_at <= now()`, id).Scan(&term.Count); err != nil {
		return content.Term{}, fmt.Errorf("count term posts: %w", err)
	}
	return term, nil
}

func (r *PostRepository) CreateTerm(ctx context.Context, input content.TermInput) (content.Term, error) {
	normalized, fields := content.NormalizeTermInput(input)
	if len(fields) > 0 {
		return content.Term{}, &content.ValidationError{Fields: fields}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Term{}, fmt.Errorf("begin create term: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateTermParent(ctx, tx, normalized.Taxonomy, normalized.ParentID, 0); err != nil {
		return content.Term{}, err
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO np_terms(taxonomy_id,parent_id,slug,name,description,version,created_at,updated_at)
		SELECT taxonomy.id,$2,$3,$4,$5,1,$6,$6 FROM np_taxonomies taxonomy WHERE taxonomy.slug=$1
		RETURNING id`, normalized.Taxonomy, nullableID(normalized.ParentID), normalized.Slug, normalized.Name, normalized.Description, time.Now().UTC()).Scan(&id)
	if err != nil {
		return content.Term{}, mapTermWriteError("create term", err)
	}
	term, err := queryTermByID(ctx, tx, id, false)
	if err != nil {
		return content.Term{}, fmt.Errorf("load created term: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Term{}, fmt.Errorf("commit create term: %w", err)
	}
	return term, nil
}

func (r *PostRepository) UpdateTerm(ctx context.Context, id int64, mutation content.TermMutation, input content.TermInput) (content.Term, error) {
	normalized, fields := content.NormalizeTermInput(input)
	if len(fields) > 0 {
		return content.Term{}, &content.ValidationError{Fields: fields}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return content.Term{}, fmt.Errorf("begin update term: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := queryTermByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.Term{}, content.ErrTermNotFound
	}
	if err != nil {
		return content.Term{}, fmt.Errorf("lock term: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return content.Term{}, content.ErrTermVersionConflict
	}
	if current.Taxonomy != normalized.Taxonomy {
		return content.Term{}, &content.ValidationError{Fields: map[string]string{"taxonomy": "已创建的术语不能切换分类法"}}
	}
	if err := validateTermParent(ctx, tx, normalized.Taxonomy, normalized.ParentID, id); err != nil {
		return content.Term{}, err
	}
	now := time.Now().UTC()
	command, err := tx.Exec(ctx, `
		UPDATE np_terms SET parent_id=$2,slug=$3,name=$4,description=$5,updated_at=$6,version=version+1
		WHERE id=$1 AND version=$7`, id, nullableID(normalized.ParentID), normalized.Slug, normalized.Name, normalized.Description, now, mutation.ExpectedVersion)
	if err != nil {
		return content.Term{}, mapTermWriteError("update term", err)
	}
	if command.RowsAffected() != 1 {
		return content.Term{}, content.ErrTermVersionConflict
	}
	if normalized.Taxonomy == content.TaxonomyCategory {
		if _, err := tx.Exec(ctx, `
			UPDATE np_posts post
			SET metadata=jsonb_set(post.metadata,'{category}',to_jsonb($2::text),true)
			WHERE EXISTS (
				SELECT 1 FROM np_post_terms relation
				WHERE relation.post_id=post.id AND relation.term_id=$1 AND relation.position=0
			)`, id, normalized.Name); err != nil {
			return content.Term{}, fmt.Errorf("refresh primary category metadata: %w", err)
		}
	}
	term, err := queryTermByID(ctx, tx, id, false)
	if err != nil {
		return content.Term{}, fmt.Errorf("load updated term: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return content.Term{}, fmt.Errorf("commit update term: %w", err)
	}
	return term, nil
}

func (r *PostRepository) DeleteTerm(ctx context.Context, id int64, mutation content.TermMutation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete term: %w", err)
	}
	defer tx.Rollback(ctx)
	term, err := queryTermByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return content.ErrTermNotFound
	}
	if err != nil {
		return fmt.Errorf("lock term for deletion: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || term.Version != mutation.ExpectedVersion {
		return content.ErrTermVersionConflict
	}
	if term.System {
		return content.ErrTermProtected
	}
	affected := make([]int64, 0)
	rows, err := tx.Query(ctx, `SELECT post_id FROM np_post_terms WHERE term_id=$1`, id)
	if err != nil {
		return fmt.Errorf("list term relations: %w", err)
	}
	for rows.Next() {
		var postID int64
		if err := rows.Scan(&postID); err != nil {
			rows.Close()
			return fmt.Errorf("scan term relation: %w", err)
		}
		affected = append(affected, postID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate term relations: %w", err)
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `UPDATE np_terms SET parent_id=$2,updated_at=now(),version=version+1 WHERE parent_id=$1`, id, nullableID(term.ParentID)); err != nil {
		return fmt.Errorf("reparent child terms: %w", err)
	}
	command, err := tx.Exec(ctx, `DELETE FROM np_terms WHERE id=$1 AND version=$2`, id, mutation.ExpectedVersion)
	if err != nil {
		return fmt.Errorf("delete term: %w", err)
	}
	if command.RowsAffected() != 1 {
		return content.ErrTermVersionConflict
	}
	if term.Taxonomy == content.TaxonomyCategory {
		if _, err := tx.Exec(ctx, `
			WITH default_category AS (
				SELECT term.id FROM np_terms term
				JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
				WHERE taxonomy.slug='category' AND term.is_system
				ORDER BY term.id LIMIT 1
			)
			INSERT INTO np_post_terms(post_id,term_id,position)
			SELECT post.id,default_category.id,0 FROM np_posts post CROSS JOIN default_category
			WHERE post.type='post' AND NOT EXISTS (
				SELECT 1 FROM np_post_terms relation
				JOIN np_terms linked ON linked.id=relation.term_id
				JOIN np_taxonomies taxonomy ON taxonomy.id=linked.taxonomy_id
				WHERE relation.post_id=post.id AND taxonomy.slug='category'
			)
			ON CONFLICT (post_id,term_id) DO NOTHING`); err != nil {
			return fmt.Errorf("assign default category after deletion: %w", err)
		}
		if len(affected) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE np_posts post
				SET metadata=jsonb_set(post.metadata,'{category}',to_jsonb(COALESCE((
					SELECT linked.name FROM np_post_terms relation
					JOIN np_terms linked ON linked.id=relation.term_id
					JOIN np_taxonomies taxonomy ON taxonomy.id=linked.taxonomy_id
					WHERE relation.post_id=post.id AND taxonomy.slug='category'
					ORDER BY relation.position,linked.id LIMIT 1
				),'未分类')::text),true)
				WHERE post.id=ANY($1::bigint[])`, affected); err != nil {
				return fmt.Errorf("refresh category metadata after deletion: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete term: %w", err)
	}
	return nil
}

func hydratePostTerms(ctx context.Context, querier rowsQuerier, posts []content.Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(posts))
	indexes := make(map[int64]int, len(posts))
	for index := range posts {
		ids = append(ids, posts[index].ID)
		indexes[posts[index].ID] = index
		posts[index].Categories = nil
		posts[index].Tags = nil
	}
	rows, err := querier.Query(ctx, `
		SELECT relation.post_id,term.id,taxonomy.slug,COALESCE(term.parent_id,0),term.slug,term.name,
		       term.description,term.is_system,term.version,term.created_at,term.updated_at
		FROM np_post_terms relation
		JOIN np_terms term ON term.id=relation.term_id
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		WHERE relation.post_id=ANY($1::bigint[])
		ORDER BY relation.post_id,CASE taxonomy.slug WHEN 'category' THEN 0 ELSE 1 END,relation.position,term.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var postID int64
		var term content.Term
		if err := rows.Scan(&postID, &term.ID, &term.Taxonomy, &term.ParentID, &term.Slug, &term.Name,
			&term.Description, &term.System, &term.Version, &term.CreatedAt, &term.UpdatedAt); err != nil {
			return err
		}
		index, ok := indexes[postID]
		if !ok {
			continue
		}
		if term.Taxonomy == content.TaxonomyCategory {
			posts[index].Categories = append(posts[index].Categories, term)
		} else if term.Taxonomy == content.TaxonomyTag {
			posts[index].Tags = append(posts[index].Tags, term)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range posts {
		if len(posts[index].Categories) > 0 {
			posts[index].Category = posts[index].Categories[0].Name
		}
	}
	return nil
}

func resolvePostTerms(ctx context.Context, tx pgx.Tx, input content.PostInput) ([]content.Term, []content.Term, error) {
	if input.Type != content.TypePost {
		return nil, nil, nil
	}
	categories := make([]content.Term, 0, max(1, len(input.CategoryIDs)))
	for _, id := range input.CategoryIDs {
		term, err := queryTermByID(ctx, tx, id, false)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && term.Taxonomy != content.TaxonomyCategory) {
			return nil, nil, &content.ValidationError{Fields: map[string]string{"category_ids": "所选分类不存在或已被删除"}}
		}
		if err != nil {
			return nil, nil, fmt.Errorf("load selected category: %w", err)
		}
		categories = append(categories, term)
	}
	if len(categories) == 0 {
		name := strings.TrimSpace(input.Category)
		if name == "" {
			name = "未分类"
		}
		term, err := ensureTermByName(ctx, tx, content.TaxonomyCategory, name)
		if err != nil {
			return nil, nil, err
		}
		categories = append(categories, term)
	}
	tags := make([]content.Term, 0, len(input.TagNames))
	for _, name := range input.TagNames {
		term, err := ensureTermByName(ctx, tx, content.TaxonomyTag, name)
		if err != nil {
			return nil, nil, err
		}
		tags = append(tags, term)
	}
	return categories, tags, nil
}

func replacePostTerms(ctx context.Context, tx pgx.Tx, postID int64, categories, tags []content.Term) error {
	if _, err := tx.Exec(ctx, `DELETE FROM np_post_terms WHERE post_id=$1`, postID); err != nil {
		return fmt.Errorf("clear post terms: %w", err)
	}
	position := 0
	for _, terms := range [][]content.Term{categories, tags} {
		for _, term := range terms {
			if _, err := tx.Exec(ctx, `INSERT INTO np_post_terms(post_id,term_id,position) VALUES ($1,$2,$3)`, postID, term.ID, position); err != nil {
				return fmt.Errorf("assign post term: %w", err)
			}
			position++
		}
	}
	return nil
}

func ensureTermByName(ctx context.Context, tx pgx.Tx, taxonomy, name string) (content.Term, error) {
	name = strings.TrimSpace(name)
	term, err := queryTermByName(ctx, tx, taxonomy, name)
	if err == nil {
		return term, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return content.Term{}, fmt.Errorf("find term by name: %w", err)
	}
	slug := content.NormalizeTermSlug(name)
	if slug == "" {
		slug = "term"
	}
	base := slug
	for suffix := 1; suffix <= 1000; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = content.TermSlugWithSuffix(base, suffix)
		}
		now := time.Now().UTC()
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO np_terms(taxonomy_id,slug,name,description,version,created_at,updated_at)
			SELECT taxonomy.id,$2,$3,'',1,$4,$4 FROM np_taxonomies taxonomy WHERE taxonomy.slug=$1
			ON CONFLICT (taxonomy_id,slug) DO NOTHING
			RETURNING id`, taxonomy, candidate, name, now).Scan(&id)
		if err == nil {
			return queryTermByID(ctx, tx, id, false)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return content.Term{}, mapTermWriteError("create implicit term", err)
		}
		if existing, queryErr := queryTermByName(ctx, tx, taxonomy, name); queryErr == nil {
			return existing, nil
		}
	}
	return content.Term{}, fmt.Errorf("allocate unique term slug")
}

func validateTermParent(ctx context.Context, tx pgx.Tx, taxonomy string, parentID, termID int64) error {
	if parentID == 0 {
		return nil
	}
	if taxonomy != content.TaxonomyCategory || parentID == termID {
		return content.ErrTermHierarchyConflict
	}
	parent, err := queryTermByID(ctx, tx, parentID, false)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && parent.Taxonomy != taxonomy) {
		return &content.ValidationError{Fields: map[string]string{"parent_id": "父级分类不存在"}}
	}
	if err != nil {
		return fmt.Errorf("load parent term: %w", err)
	}
	if termID == 0 {
		return nil
	}
	var cycle bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE ancestors(id,parent_id,visited,depth) AS (
			SELECT id,parent_id,ARRAY[id],0 FROM np_terms WHERE id=$1
			UNION ALL
			SELECT parent.id,parent.parent_id,child.visited || parent.id,child.depth + 1
			FROM np_terms parent
			JOIN ancestors child ON parent.id=child.parent_id
			WHERE child.depth < 63 AND NOT parent.id=ANY(child.visited)
		)
		SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=$2)
		    OR EXISTS(SELECT 1 FROM ancestors WHERE parent_id=ANY(visited) OR (depth >= 63 AND parent_id IS NOT NULL))`, parentID, termID).Scan(&cycle); err != nil {
		return fmt.Errorf("validate term hierarchy: %w", err)
	}
	if cycle {
		return content.ErrTermHierarchyConflict
	}
	return nil
}

func queryTermByID(ctx context.Context, querier rowQuerier, id int64, forUpdate bool) (content.Term, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE OF term"
	}
	return scanTerm(querier.QueryRow(ctx, `
		SELECT term.id,taxonomy.slug,COALESCE(term.parent_id,0),COALESCE(parent.name,''),
		       term.slug,term.name,term.description,0,0,term.is_system,term.version,term.created_at,term.updated_at
		FROM np_terms term
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		LEFT JOIN np_terms parent ON parent.id=term.parent_id
		WHERE term.id=$1`+suffix, id))
}

func queryTermBySlug(ctx context.Context, querier rowQuerier, taxonomy, slug string) (content.Term, error) {
	return scanTerm(querier.QueryRow(ctx, `
		SELECT term.id,taxonomy.slug,COALESCE(term.parent_id,0),COALESCE(parent.name,''),
		       term.slug,term.name,term.description,0,0,term.is_system,term.version,term.created_at,term.updated_at
		FROM np_terms term
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		LEFT JOIN np_terms parent ON parent.id=term.parent_id
		WHERE taxonomy.slug=$1 AND term.slug=$2`, taxonomy, slug))
}

func queryTermByName(ctx context.Context, querier rowQuerier, taxonomy, name string) (content.Term, error) {
	return scanTerm(querier.QueryRow(ctx, `
		SELECT term.id,taxonomy.slug,COALESCE(term.parent_id,0),COALESCE(parent.name,''),
		       term.slug,term.name,term.description,0,0,term.is_system,term.version,term.created_at,term.updated_at
		FROM np_terms term
		JOIN np_taxonomies taxonomy ON taxonomy.id=term.taxonomy_id
		LEFT JOIN np_terms parent ON parent.id=term.parent_id
		WHERE taxonomy.slug=$1 AND lower(term.name)=lower($2)
		ORDER BY term.is_system DESC,term.id LIMIT 1`, taxonomy, name))
}

func scanTerm(row scanner) (content.Term, error) {
	var term content.Term
	if err := row.Scan(
		&term.ID, &term.Taxonomy, &term.ParentID, &term.ParentName,
		&term.Slug, &term.Name, &term.Description, &term.Count, &term.Depth,
		&term.System, &term.Version, &term.CreatedAt, &term.UpdatedAt,
	); err != nil {
		return content.Term{}, err
	}
	return term, nil
}

func postInputFromSnapshot(post content.Post) content.PostInput {
	input := content.PostInput{Type: post.Type}
	for _, term := range post.Categories {
		input.CategoryIDs = append(input.CategoryIDs, term.ID)
	}
	for _, term := range post.Tags {
		input.TagNames = append(input.TagNames, term.Name)
	}
	return input
}

func primaryCategoryName(categories []content.Term) string {
	if len(categories) == 0 {
		return ""
	}
	return categories[0].Name
}

func nullableID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func mapTermWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" && strings.Contains(postgresError.ConstraintName, "terms_taxonomy_id_slug") {
		return content.ErrTermSlugConflict
	}
	return fmt.Errorf("%s: %w", operation, err)
}
