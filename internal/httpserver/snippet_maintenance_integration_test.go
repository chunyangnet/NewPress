package httpserver

import (
	"context"
	"crypto/sha256"
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

func TestPostgreSQLSnippetMaintenanceHTTPGateAndRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("np_snippet_maintenance_http_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(clean, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	r := database.NewPostRepository(pool)
	s.repository = r
	a := snippetAccess(principal)
	snippet, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>older retained history</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	snippet, err = r.SaveSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>quarantine-visible-body</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "source", Status: content.StatusPublish, ContentSource: "<p>ordinary-static</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicIncludeBody = true
	if _, fields, e := s.siteSettings.Save(snapshot.Revision, settings); e != nil || len(fields) > 0 {
		t.Fatal(e, fields)
	}
	checkPublic := func(visible bool) {
		t.Helper()
		response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/posts?limit=1", nil, nil)
		body := response.Body.String()
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "quarantine-visible-body") != visible || !strings.Contains(body, "ordinary-static") {
			t.Fatal("public maintenance response", response.Code, body)
		}
	}
	checkPublic(true)
	draft := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(ctx, draft, a)
	if err != nil {
		t.Fatal(err)
	}
	draft.Digest, draft.RequestKey = plan.Digest, strings.Repeat("a", 64)
	draftBody, _ := json.Marshal(draft)
	target := database.SnippetMaintenanceTarget{SnippetID: snippet.ID}
	replacement, err := database.ExportSnippetRepair(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, snippet.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	// A broken unselected history row does not make the fast pinned read a full audit.
	checkPublic(true)
	review, err := database.ReviewSnippetMaintenance(ctx, pool, target)
	if err != nil || review.Healthy {
		t.Fatal(review, err)
	}
	q, err := database.QuarantineSnippetRecords(ctx, pool, database.SnippetCaptureRequest{Target: target, Reference: review.Reference, RequestKey: strings.Repeat("b", 64), Operator: "operator", Reason: "retain invalid old history"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	checkPublic(false)
	response := serveContentAPI(handler, cookie, http.MethodGet, fmt.Sprintf("https://example.test/api/v1/admin/snippets/%d?view=summary", snippet.ID), "", nil)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "snippets.maintenance") {
		t.Fatal(response.Code, response.Body.String())
	}
	draftURL := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d/snippet-draft", p.ID)
	response = serveContentAPI(handler, cookie, http.MethodPost, draftURL, string(draftBody), authenticatedAPIHeaders(principal))
	if response.Code != 503 || !strings.Contains(response.Body.String(), "snippet_draft.maintenance") {
		t.Fatal(response.Code, response.Body.String())
	}
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "blocked-editorial-maintenance"
	response = serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"ordinary draft","content_source":"<p>ordinary</p>"}`, headers)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "content.maintenance") {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err = database.RepairSnippetRecords(ctx, pool, database.SnippetRepairRequest{ID: q.ID, Operator: "operator", Reason: "verified backup", Replacement: replacement, ReplacementSHA256: fmt.Sprintf("%x", sha256.Sum256(replacement))}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	checkPublic(false)
	response = serveContentAPI(handler, cookie, http.MethodPost, draftURL, string(draftBody), authenticatedAPIHeaders(principal))
	if response.Code != 404 && response.Code != 412 {
		t.Fatal("old uncommitted confirmation copied repaired history", response.Code, response.Body.String())
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_posts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("blocked requests created content", count, err)
	}
	response = serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"ordinary draft","content_source":"<p>ordinary</p>"}`, headers)
	if response.Code != 201 {
		t.Fatal("ordinary submission did not recover", response.Code, response.Body.String())
	}
}
