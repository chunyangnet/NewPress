package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/content"
)

func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_posts`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	var userID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO np_users(username,email,password_hash,display_name,status)
		VALUES ('newpress','newpress@example.invalid','!disabled!','NewPress Team','system')
		ON CONFLICT (username) DO UPDATE SET display_name=EXCLUDED.display_name
		RETURNING id`).Scan(&userID)
	if err != nil {
		return fmt.Errorf("seed user: %w", err)
	}

	seedTx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin demo seed: %w", err)
	}
	defer seedTx.Rollback(ctx)
	for _, post := range content.NewMemoryRepository(true).PostsForSeed() {
		var postID int64
		err := seedTx.QueryRow(ctx, `
			INSERT INTO np_posts(author_id,type,status,slug,title,excerpt,content_source,content_html,cover_url,comment_status,metadata,published_at,created_at,updated_at,version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (type,slug) DO NOTHING
			RETURNING id`,
			userID, post.Type, post.Status, post.Slug, post.Title, post.Excerpt,
			strings.TrimSpace(post.ContentSource), post.ContentHTML, post.CoverURL, post.CommentStatus,
			map[string]any{"category": post.Category, "author_name": post.AuthorName}, post.PublishedAt,
			post.CreatedAt, post.UpdatedAt, post.Version,
		).Scan(&postID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = seedTx.QueryRow(ctx, `SELECT id FROM np_posts WHERE type=$1 AND slug=$2`, post.Type, post.Slug).Scan(&postID)
		}
		if err != nil {
			return fmt.Errorf("seed post %s: %w", post.Slug, err)
		}
		termInput := content.PostInput{Type: post.Type, Category: post.Category}
		for _, term := range post.Tags {
			termInput.TagNames = append(termInput.TagNames, term.Name)
		}
		categories, tags, err := resolvePostTerms(ctx, seedTx, termInput)
		if err != nil {
			return fmt.Errorf("resolve seed terms for %s: %w", post.Slug, err)
		}
		if err := replacePostTerms(ctx, seedTx, postID, categories, tags); err != nil {
			return fmt.Errorf("assign seed terms for %s: %w", post.Slug, err)
		}
	}
	if err := seedTx.Commit(ctx); err != nil {
		return fmt.Errorf("commit demo seed: %w", err)
	}
	repository := NewPostRepository(pool)
	items, err := repository.ListAdmin(ctx, content.AdminQuery{Limit: 100})
	if err != nil {
		return fmt.Errorf("list seeded posts for revisions: %w", err)
	}
	for _, post := range items.Items {
		snapshot, err := json.Marshal(post)
		if err != nil {
			return fmt.Errorf("encode seeded revision: %w", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO np_post_revisions(post_id,post_version,snapshot,actor,created_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (post_id,post_version) DO NOTHING`,
			post.ID, post.Version, snapshot, post.AuthorName, post.UpdatedAt); err != nil {
			return fmt.Errorf("record seeded revision: %w", err)
		}
	}
	return nil
}

var _ content.Repository = (*PostRepository)(nil)
