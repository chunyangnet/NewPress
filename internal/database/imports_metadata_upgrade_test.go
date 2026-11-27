package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/wechat"
)

func TestPostgreSQLImportMetadataRequiredAndPersistent(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := pgx.Identifier{fmt.Sprintf("import_metadata_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewPostRepository(pool)
	a, err := wechat.ParseArticle("https://mp.weixin.qq.com/s/current", `<meta property="og:title" content="导入核对"><meta name="description" content="来源摘要"><meta property="og:image" content="https://mmbiz.qpic.cn/cover"><div id="js_content"><p>正文</p></div>`)
	if err != nil {
		t.Fatal(err)
	}
	batch := wechat.Batch([]wechat.Collected{a}, nil, 0, "NewPress", "test")
	constraintInput := batch.Items[0].Input
	constraintInput.Slug = "metadata-constraint-fixture"
	post, err := repo.Create(ctx, constraintInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO np_content_imports(source,format,source_key,digest,post_id,created_at) VALUES($1,$2,$3,$4,$5,now())`, batch.Source, batch.Format, a.Key, batch.Items[0].Digest, post.ID)
	var constraintError *pgconn.PgError
	if !errors.As(err, &constraintError) || constraintError.Code != "23514" {
		t.Fatal("missing metadata bypassed database constraint", err)
	}
	plan, err := repo.PreviewImport(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.ApplyImport(ctx, batch, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	postID := created.Items[0].PostID
	for _, value := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("x", 64)} {
		_, err = pool.Exec(ctx, `UPDATE np_content_imports SET metadata_digest=$1 WHERE post_id=$2`, value, postID)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
			t.Fatal("invalid metadata bypassed database constraint", value, err)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE np_content_imports SET format='markdown',metadata_digest=$1 WHERE post_id=$2`, strings.Repeat("a", 64), postID)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("metadata allowed for unrelated format", err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repo = NewPostRepository(otherPool)
	var digest string
	// New mappings keep a known baseline across changed/unchanged replays.
	a.URL = "https://mp.weixin.qq.com/s/modern"
	a.Key = fmt.Sprintf("%x", sha256.Sum256([]byte(a.URL)))
	modern := wechat.Batch([]wechat.Collected{a}, nil, 0, "NewPress", "test")
	plan, err = repo.PreviewImport(ctx, modern)
	if err != nil {
		t.Fatal(err)
	}
	created, err = repo.ApplyImport(ctx, modern, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	modernID := created.Items[0].PostID
	for _, field := range []string{"excerpt", "cover", "unchanged"} {
		changed := a
		want := "changed"
		switch field {
		case "excerpt":
			changed.Excerpt = "新摘要"
		case "cover":
			changed.Cover = "https://mmbiz.qpic.cn/new-cover"
		default:
			want = "skip"
		}
		batch := wechat.Batch([]wechat.Collected{changed}, nil, 0, "NewPress", "test")
		plan, err = repo.PreviewImport(ctx, batch)
		if err != nil || plan.Items[0].Action != want {
			t.Fatal(field, plan, err)
		}
		if _, err = repo.ApplyImport(ctx, batch, plan.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT metadata_digest FROM np_content_imports WHERE post_id=$1`, modernID).Scan(&digest); err != nil || digest != modern.Items[0].MetadataDigest {
		t.Fatal("known baseline changed", digest, err)
	}
}
