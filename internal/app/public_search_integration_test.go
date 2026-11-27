package app

import (
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
)

func TestPostgreSQLAppPreparesSearchBeforeServing(t *testing.T) {
	dsn, _, _ := isolatedAppDatabase(t)
	pool, err := database.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	post, err := database.NewPostRepository(pool).Create(t.Context(), content.PostInput{Title: "startup", Slug: "search-startup", Status: content.StatusPublish, ContentSource: "<p>startupbody</p>"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE np_posts SET public_search_body=NULL,snippet_refs_ready=false WHERE id=$1`, post.ID); err != nil {
		t.Fatal(err)
	}
	application, err := New(t.Context(), postgresAppConfig(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	var body string
	var version int64
	var referencesReady bool
	if err = pool.QueryRow(t.Context(), `SELECT snippet_refs_ready FROM np_posts WHERE id=$1`, post.ID).Scan(&referencesReady); err != nil || !referencesReady {
		t.Fatal("snippet startup preparation", referencesReady, err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT public_search_body,version FROM np_posts WHERE id=$1`, post.ID).Scan(&body, &version); err != nil || body != "startupbody" || version != post.Version {
		t.Fatal("startup preparation", body, version, err)
	}
}
