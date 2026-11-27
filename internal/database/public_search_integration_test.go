package database

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLPublicSearchUnicodeMatchesGo(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for c := rune(1); c <= unicode.MaxRune; c++ {
		if unicode.ToLower(c) != c || unicode.IsSpace(c) {
			source.WriteRune(c)
			source.WriteByte(' ')
		}
	}
	for _, input := range []string{source.String(), " İ  Σ\u3000K\u00a0É ẞ ß 中文 😀 ", "100%_literal\\path", "A\tB\nC\vD\fE\rF", "\u001cspace"} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT np_public_search_normalize_v1($1)`, input).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := content.NormalizePublicSearchText(input); got != want {
			t.Fatalf("SQL/Go normalization mismatch: %q != %q", got, want)
		}
	}
}

func TestPostgreSQLPublicSearchUpgradeAndResumablePreparation(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := NewPostRepository(pool)
	for i := range 65 {
		if _, err := store.Create(ctx, content.PostInput{Title: fmt.Sprintf("upgrade %d", i), Slug: fmt.Sprintf("upgrade-%d", i), Status: content.StatusPublish, Excerpt: "summary", ContentSource: "<p>upgradebody</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER np_posts_invalidate_public_search_body ON np_posts; DROP FUNCTION np_invalidate_public_search_body(); ALTER TABLE np_posts DROP COLUMN public_search_title, DROP COLUMN public_search_excerpt, DROP COLUMN public_search_author, DROP COLUMN public_search_body; ALTER TABLE np_users DROP COLUMN public_search_name; ALTER TABLE np_terms DROP COLUMN public_search_name; DROP FUNCTION np_public_search_normalize_v1(text); DELETE FROM newpress_schema_migrations WHERE version='0065_public_search_body.sql'`); err != nil {
		t.Fatal(err)
	}
	canonical := func() string {
		t.Helper()
		var raw string
		if err := pool.QueryRow(ctx, `SELECT json_agg(row_to_json(p) ORDER BY id)::text FROM np_posts p`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := canonical()
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT json_agg(to_jsonb(p)-ARRAY['public_search_body','public_search_title','public_search_excerpt','public_search_author'] ORDER BY id)::text FROM np_posts p`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	// JSON object ordering differs for json/jsonb; compare canonical row values.
	var equal bool
	if err := pool.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, before, after).Scan(&equal); err != nil || !equal {
		t.Fatal("migration changed canonical data", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_second_search_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id>32 THEN RAISE EXCEPTION 'injected second batch failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_second_search_batch BEFORE UPDATE OF public_search_body ON np_posts FOR EACH ROW EXECUTE FUNCTION fail_second_search_batch()`); err != nil {
		t.Fatal(err)
	}
	if err := PreparePublicSearch(ctx, pool); err == nil {
		t.Fatal("second batch failure ignored")
	}
	var ready int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_posts WHERE public_search_body IS NOT NULL`).Scan(&ready); err != nil || ready != 32 {
		t.Fatal("preparation is not resumable by committed batch", ready, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_second_search_batch ON np_posts`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PreparePublicSearch(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_posts WHERE public_search_body='upgradebody'`).Scan(&ready); err != nil || ready != 65 {
		t.Fatal(ready, err)
	}
	if err := pool.QueryRow(ctx, `SELECT json_agg(to_jsonb(p)-ARRAY['public_search_body','public_search_title','public_search_excerpt','public_search_author'] ORDER BY id)::text FROM np_posts p`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, before, after).Scan(&equal); err != nil || !equal {
		t.Fatal("preparation changed editorial data", err)
	}
}

func TestPostgreSQLPublicSearchMatchesMemoryAndFile(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "content.json")
	file, err := content.NewFileRepository(filePath, false)
	if err != nil {
		t.Fatal(err)
	}
	stores := []content.Repository{content.NewMemoryRepository(false), file, NewPostRepository(pool)}
	base := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	for i, input := range []content.PostInput{
		{Title: "ordinary", ContentSource: `<p>needle 深<em>度</em>正文 100%_literal\path</p>`, Pinned: new(true)},
		{Title: "needle title", ContentSource: `<p>other</p>`},
		{Title: "excerpt", Excerpt: "needle"},
		{Title: "term", TagNames: []string{"needle"}},
		{Title: "byline", AuthorName: "needle"},
		{Title: "body tie", ContentSource: `<p>needle</p>`},
		{Title: "unicode", ContentSource: `<p>İ Σ K É ß</p>`},
		{Title: "attributes", ContentSource: `<p><a href="https://attribute.example/secretattribute">visible</a></p><!--secretcomment--><script>secretscript</script>`},
		{Title: "private needle", Status: content.StatusPrivate, ContentSource: `<p>privatebody</p>`},
		{Title: "future needle", Status: content.StatusFuture, PublishedAt: time.Now().Add(time.Hour), ContentSource: `<p>futurebody</p>`},
	} {
		input.Slug = fmt.Sprintf("search-match-%d", i)
		if input.Status == "" {
			input.Status = content.StatusPublish
		}
		if input.PublishedAt.IsZero() {
			input.PublishedAt = base
		}
		if input.Excerpt == "" {
			input.Excerpt = "unrelated explicit summary"
		}
		for _, store := range stores {
			if _, err := store.Create(ctx, input); err != nil {
				t.Fatal(err)
			}
		}
	}
	stores[1], err = content.NewFileRepository(filePath, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"needle", "深度正文", "100%_literal\\path", "i σ k é ß", "secretattribute", "secretcomment", "secretscript", "privatebody", "futurebody"} {
		for _, sort := range []string{"", "relevance", "published", "updated", "oldest", "title"} {
			var expected []string
			expectedTotal := -1
			for _, store := range stores {
				page, err := store.SearchPublished(ctx, content.PublishedQuery{Search: needle, Sort: sort, Limit: 2, Offset: 1})
				if err != nil {
					t.Fatal(needle, sort, err)
				}
				var slugs []string
				for _, post := range page.Items {
					slugs = append(slugs, post.Slug)
				}
				if expectedTotal < 0 {
					expected, expectedTotal = slugs, page.Total
				} else if page.Total != expectedTotal || !slices.Equal(expected, slugs) {
					t.Fatal("storage order mismatch", needle, sort, expected, slugs, expectedTotal, page.Total)
				}
			}
		}
	}
	page, err := stores[2].SearchPublished(ctx, content.PublishedQuery{Search: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, post := range page.Items {
		slugs = append(slugs, post.Slug)
	}
	want := []string{"search-match-1", "search-match-2", "search-match-3", "search-match-4", "search-match-5", "search-match-0"}
	if !slices.Equal(slugs, want) {
		t.Fatal("field weights, tie order or pin priority", slugs)
	}
}

func TestPostgreSQLPublicSearchProjectionLifecycleAndConcurrentRepair(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := NewPostRepository(pool)
	post, err := store.Create(ctx, content.PostInput{Title: "projection", Slug: "projection", Status: content.StatusPublish, Excerpt: "summary", ContentSource: "<p>originalbody</p>"})
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err = pool.QueryRow(ctx, `SELECT public_search_body FROM np_posts WHERE id=$1`, post.ID).Scan(&original); err != nil || original != "originalbody" {
		t.Fatal(original, err)
	}
	revisions, err := store.ListRevisions(ctx, post.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "editor"}, content.PostInput{Title: post.Title, Slug: post.Slug, Status: post.Status, Excerpt: post.Excerpt, Category: post.Category, ContentSource: "<p>updatedbody</p>"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(term string, want int) {
		t.Helper()
		page, e := store.SearchPublished(ctx, content.PublishedQuery{Search: term})
		if e != nil || page.Total != want {
			t.Fatal(term, page, e)
		}
	}
	check("originalbody", 0)
	check("updatedbody", 1)
	post, err = store.RestoreRevision(ctx, post.ID, revisions[0].ID, content.Mutation{ExpectedVersion: updated.Version, Actor: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	check("originalbody", 1)
	check("updatedbody", 0)
	var revisionCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_post_revisions WHERE post_id=$1`, post.ID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	// An older writer only changes canonical HTML. The DB trigger invalidates the projection.
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET content_html='<p>legacybody</p>' WHERE id=$1`, post.ID); err != nil {
		t.Fatal(err)
	}
	var missing bool
	if err = pool.QueryRow(ctx, `SELECT public_search_body IS NULL FROM np_posts WHERE id=$1`, post.ID).Scan(&missing); err != nil || !missing {
		t.Fatal("stale projection survived", missing, err)
	}
	start := make(chan struct{})
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			<-start
			page, e := NewPostRepository(pool).SearchPublished(ctx, content.PublishedQuery{Search: "legacybody"})
			if e == nil && page.Total != 1 {
				e = fmt.Errorf("unexpected total %d", page.Total)
			}
			failures <- e
		})
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var version int64
	if err = pool.QueryRow(ctx, `SELECT version,(SELECT count(*) FROM np_post_revisions WHERE post_id=p.id) FROM np_posts p WHERE id=$1`, post.ID).Scan(&version, &count); err != nil || version != post.Version || count != revisionCount {
		t.Fatal("repair changed editorial state", version, count, err)
	}
	check("originalbody", 0)
	// A failure writing any projection aborts the repair transaction.
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET public_search_body=NULL WHERE id=$1`, post.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_search_repair() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected projection failure'; END $$; CREATE TRIGGER fail_search_repair BEFORE UPDATE OF public_search_body ON np_posts FOR EACH ROW EXECUTE FUNCTION fail_search_repair()`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SearchPublished(ctx, content.PublishedQuery{Search: "legacybody"}); err == nil {
		t.Fatal("repair failure ignored")
	}
	if err = pool.QueryRow(ctx, `SELECT public_search_body IS NULL FROM np_posts WHERE id=$1`, post.ID).Scan(&missing); err != nil || !missing {
		t.Fatal("failed repair committed", missing, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_search_repair ON np_posts`); err != nil {
		t.Fatal(err)
	}
	check("legacybody", 1)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.SearchPublished(cancelled, content.PublishedQuery{Search: "legacybody"}); err == nil {
		t.Fatal("cancelled search accepted")
	}
}

func TestPostgreSQLPublicSearchImportPluginWriteAndMetadataChanges(t *testing.T) {
	f := newContentEffectFixture(t, false)
	store := NewPostRepository(f.pool)
	f.exec(t, `UPDATE np_posts SET status='publish',published_at=now()-interval '1 hour' WHERE id=$1`, f.post.ID)
	body := "<p>插件正<em>文</em>标记</p>"
	f.request.Update.Patch = content.TextPatch{ContentSource: &body}
	if _, err := f.effects.WriteContent(f.ctx, f.request); err != nil {
		t.Fatal(err)
	}
	check := func(query string, want int) {
		t.Helper()
		page, err := store.SearchPublished(f.ctx, content.PublishedQuery{Search: query})
		if err != nil || page.Total != want {
			t.Fatal(query, page, err)
		}
	}
	// Inspect before searching so lazy repair cannot hide a missing write path.
	var projection string
	if err := f.pool.QueryRow(f.ctx, `SELECT public_search_body FROM np_posts WHERE id=$1`, f.post.ID).Scan(&projection); err != nil || projection != "插件正文标记" {
		t.Fatal("plugin transaction omitted search projection", projection, err)
	}
	check("插件正文标记", 1)
	check("Original body", 0)
	f.exec(t, `UPDATE np_users SET display_name='重命名作者' WHERE id=$1`, f.user.ID)
	f.exec(t, `UPDATE np_terms SET name='重命名标签' WHERE slug='keep-tag'`)
	check("重命名作者", 1)
	check("Effect editor", 0)
	check("重命名标签", 1)
	check("keep-tag", 0)
	f.exec(t, `UPDATE np_posts SET title='新标题标记',excerpt='新摘要标记' WHERE id=$1`, f.post.ID)
	check("新标题标记", 1)
	check("新摘要标记", 1)
	batch := content.ImportBatch{Source: "public-search", Format: "markdown", Items: []content.ImportEntry{{Key: "body.md", Digest: strings.Repeat("a", 64), Input: content.PostInput{Title: "迁入内容", Slug: "search-import", Excerpt: "固定摘要", ContentSource: "<p>迁入正文标记</p>", Status: content.StatusPublish, AuthorID: f.user.ID, PublishedAt: time.Now().Add(-time.Hour)}}}}
	plan, err := store.PreviewImport(f.ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyImport(f.ctx, batch, plan.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT public_search_body FROM np_posts WHERE slug='search-import'`).Scan(&projection); err != nil || projection != "迁入正文标记" {
		t.Fatal("import omitted search projection", projection, err)
	}
	check("迁入正文标记", 1)
}
