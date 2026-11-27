package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestPostgreSQLSnippetDraftAtomicReplayAndWithdrawal(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	other, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	r, r2 := NewPostRepository(pool), NewPostRepository(other)
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>pinned body</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "source", Slug: "snippet-draft-source", Status: content.StatusPublish, ContentSource: "<p>source</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: 1}
	plan, err := r.PreviewSnippetDraft(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("a", 64)
	request, err := content.NewSnippetDraftSubmission(u, a)
	if err != nil {
		t.Fatal(err)
	}
	// A failure after the post INSERT must roll back post, revision and receipt.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_snippet_draft() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.slug LIKE 'snippet-draft-%' AND NEW.slug <> 'snippet-draft-source' THEN RAISE EXCEPTION 'injected draft failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_snippet_draft AFTER INSERT ON np_posts FOR EACH ROW EXECUTE FUNCTION fail_snippet_draft()`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, request); err == nil {
		t.Fatal("injected failure missing")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_posts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("partial post", count, err)
	}
	if _, err = r.FindSubmission(ctx, request.Key, request.Fingerprint); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("partial receipt", err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_snippet_draft ON np_posts; DROP FUNCTION fail_snippet_draft()`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan content.SubmissionResult, 8)
	errs := make(chan error, 8)
	for i := range 8 {
		target := r
		if i%2 == 1 {
			target = r2
		}
		wg.Go(func() { v, e := target.Submit(ctx, request); results <- v; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	fresh, id := 0, int64(0)
	for v := range results {
		if !v.Replayed {
			fresh++
		}
		if id > 0 && id != v.Receipt.PostID {
			t.Fatal("duplicate draft")
		}
		id = v.Receipt.PostID
	}
	if fresh != 1 {
		t.Fatal(fresh)
	}
	draft, err := r.GetByID(ctx, id)
	if err != nil || draft.Status != content.StatusDraft || draft.Version != 1 || draft.CommentStatus != content.CommentClosed || !draft.PublishedAt.IsZero() || strings.Contains(draft.ContentSource, "data-np-snippet") || !strings.Contains(draft.ContentHTML, "pinned body") {
		t.Fatal(draft, err)
	}
	s, err = r2.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewPostRepository(other).Submit(ctx, request)
	if err != nil || !v.Replayed || v.Receipt.PostID != id {
		t.Fatal(v, err)
	}
	u.RequestKey = strings.Repeat("b", 64)
	again, _ := content.NewSnippetDraftSubmission(u, a)
	if _, err = r.Submit(ctx, again); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("withdrawal did not prevent new draft", err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "reshared", ContentSource: "<p>new period</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, again); !errors.Is(err, content.ErrNotFound) {
		t.Fatal("old period revived", err)
	}
	original, _ := r.GetByID(ctx, p.ID)
	if original.ContentSource != p.ContentSource || original.Version != p.Version {
		t.Fatal("source changed")
	}
	// Permanent receipt remains after deletion and cannot recreate the resource.
	if _, err = pool.Exec(ctx, `DELETE FROM np_posts WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, request); !errors.Is(err, content.ErrSubmissionResultUnavailable) {
		t.Fatal("deleted replay", err)
	}
}

func TestPostgreSQLSnippetDraftCurrentPermissionsAndVersion(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	role := auth.Role{Slug: "draft-author", Name: "Draft author", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityContentRead, auth.CapabilityContentWriteOwn}}
	if err := users.EnsureRoles(ctx, []auth.Role{role}); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Draft test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "test"}, auth.UserInput{Username: "draft-author", DisplayName: "Author", RoleSlugs: []string{role.Slug}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: fmt.Sprintf("user:%d", user.ID), UserID: user.ID, Actor: user.Username, Read: true, WriteOwn: true}
	manager := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	r := NewPostRepository(pool)
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>body</p>", Visibility: content.SnippetShared}, manager)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "own", AuthorID: a.UserID, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("c", 64)
	request, _ := content.NewSnippetDraftSubmission(u, a)
	if _, err = pool.Exec(ctx, `UPDATE np_roles SET capabilities='["admin:access","content:read"]'::jsonb WHERE slug=$1`, role.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, request); !errors.Is(err, content.ErrSnippetPermission) {
		t.Fatal("stale role", err)
	}
	capabilities, _ := json.Marshal(role.Capabilities)
	if _, err = pool.Exec(ctx, `UPDATE np_roles SET capabilities=$1 WHERE slug=$2`, capabilities, role.Slug); err != nil {
		t.Fatal(err)
	}
	p, err = r.ChangeStatus(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version, Actor: a.Actor}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, request); !errors.Is(err, content.ErrVersionConflict) {
		t.Fatal("stale source", err)
	}
	u = content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err = r.PreviewSnippetDraft(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("d", 64)
	request, _ = content.NewSnippetDraftSubmission(u, a)
	v, err := r.Submit(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_users SET status=$2 WHERE id=$1`, user.ID, auth.UserStatusSuspended); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(ctx, request); !errors.Is(err, content.ErrSnippetPermission) {
		t.Fatal("disabled replay", err)
	}
	post, _ := r.GetByID(ctx, v.Receipt.PostID)
	if post.AuthorID != a.UserID {
		t.Fatal("wrong owner")
	}
}
