package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/database"
)

func TestPostgreSQLSnippetPublicResponseAcrossRequests(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("np_snippet_output_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repository, other := database.NewPostRepository(pool), database.NewPostRepository(otherPool)
	access := content.SnippetAccess{Identity: "legacy:test", Actor: "test", Read: true, Manage: true}
	snippet, err := repository.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>pg-visible-body</p>", Visibility: content.SnippetShared}, access)
	if err != nil {
		t.Fatal(err)
	}
	shell := contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion})
	var originals []content.Post
	for i := range 2 {
		post, err := repository.Create(ctx, content.PostInput{Title: fmt.Sprint("post-", i), Slug: fmt.Sprint("post-", i), Status: content.StatusPublish, ContentSource: "<p>ordinary</p>" + strings.Repeat(shell, 120)})
		if err != nil {
			t.Fatal(err)
		}
		originals = append(originals, post)
	}
	s, handler, _, _, _ := newAuthenticatedContentServer(t)
	repo := &snippetOutputRepository{Repository: repository}
	s.repository = repo
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicIncludeBody = true
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	check := func(visible int) {
		t.Helper()
		repo.calls = nil
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/posts?limit=2", nil, nil)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || len(repo.calls) != 1 || len(repo.calls[0]) != 1 {
			t.Fatal("PG response caching/lookup", response.Code, response.Header(), len(repo.calls))
		}
		var page struct {
			Items []publicPostResponse `json:"items"`
		}
		decodeAPIResponse(t, response, &page)
		bodies := ""
		for _, item := range page.Items {
			bodies += item.ContentHTML
		}
		if len(page.Items) != 2 || strings.Count(bodies, "pg-visible-body") != visible || strings.Count(bodies, "ordinary") != 2 {
			t.Fatal("PG response body visibility/budget", len(page.Items), strings.Count(bodies, "pg-visible-body"), strings.Count(bodies, "ordinary"))
		}
	}
	check(200)
	// Bad metadata or selected revision facts must hide the dynamic body on
	// real public HTTP responses, while leaving the article's static text intact.
	var originalMeta, originalRevision []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_snippets WHERE id=$1`, snippet.ID).Scan(&originalMeta); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT data FROM np_snippet_revisions WHERE snippet_id=$1 AND version=1`, snippet.ID).Scan(&originalRevision); err != nil {
		t.Fatal(err)
	}
	badMeta := snippet
	badMeta.Title = ""
	encoded, _ := json.Marshal(badMeta)
	if _, err = pool.Exec(ctx, `UPDATE np_snippets SET data=$2 WHERE id=$1`, snippet.ID, encoded); err != nil {
		t.Fatal(err)
	}
	check(0)
	if _, err = pool.Exec(ctx, `UPDATE np_snippets SET data=$2 WHERE id=$1`, snippet.ID, originalMeta); err != nil {
		t.Fatal(err)
	}
	var badRevision content.SnippetRevision
	if err = json.Unmarshal(originalRevision, &badRevision); err != nil {
		t.Fatal(err)
	}
	badRevision.CreatedAt = snippet.CreatedAt.Add(-time.Hour)
	encoded, _ = json.Marshal(badRevision)
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, snippet.ID, encoded); err != nil {
		t.Fatal(err)
	}
	check(0)
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, snippet.ID, originalRevision); err != nil {
		t.Fatal(err)
	}
	check(200)
	snippet, err = other.WithdrawSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, access)
	if err != nil {
		t.Fatal(err)
	}
	check(0)
	if _, err = other.SaveSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "shared again", ContentSource: "<p>pg-visible-body</p>", Visibility: content.SnippetShared}, access); err != nil {
		t.Fatal(err)
	}
	check(0)
	for _, original := range originals {
		current, err := repository.GetByID(ctx, original.ID)
		if err != nil || current.Version != original.Version || current.ContentSource != original.ContentSource || current.ContentHTML != original.ContentHTML {
			t.Fatal("projection modified persisted article", original.ID, err)
		}
	}
}
