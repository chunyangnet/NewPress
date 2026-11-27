package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestPostgreSQLSnippetVersionsAtomicityReplayAndMedia(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private", ContentSource: `<p>secret</p><img src="/media/retained.png">`, Visibility: content.SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	private := contenthtml.SnippetKey{ID: s.ID, Version: 1}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>one</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	first := contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion}
	p, err := r.Create(ctx, content.PostInput{Title: "p", Slug: "snippet-p", ContentSource: "<p>retain</p>\n" + contenthtml.SnippetShell(first), Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	q, err := r.Create(ctx, content.PostInput{Title: "q", Slug: "snippet-q", ContentSource: contenthtml.SnippetShell(first), Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>two</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{private, first, {ID: s.ID, Version: s.CurrentVersion}})
	if err != nil || len(resolved) != 2 || resolved[private] != "" || resolved[first] != "<p>one</p>" {
		t.Fatal(resolved, err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}, {ID: q.ID, Version: q.Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	if _, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_snippet_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d THEN RAISE EXCEPTION 'injected snippet failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER snippet_rollback BEFORE UPDATE ON np_posts FOR EACH ROW EXECUTE FUNCTION fail_snippet_update()`, q.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplySnippetUpdate(ctx, u, a); err == nil {
		t.Fatal("injected failure missing")
	}
	actual, _ := r.GetByID(ctx, p.ID)
	if actual.Version != p.Version || actual.ContentSource != p.ContentSource {
		t.Fatal("partial transaction", actual)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER snippet_rollback ON np_posts; DROP FUNCTION fail_snippet_update()`); err != nil {
		t.Fatal(err)
	}
	q, err = r.ChangeStatus(ctx, q.ID, content.Mutation{ExpectedVersion: q.Version, Actor: "editor"}, content.StatusDraft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplySnippetUpdate(ctx, u, a); !errors.Is(err, content.ErrVersionConflict) {
		t.Fatal("stale selection", err)
	}
	u.Items[1].Version = q.Version
	plan, err = r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	var wg sync.WaitGroup
	results := make(chan content.SnippetResult, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { result, err := r.ApplySnippetUpdate(ctx, u, a); results <- result; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	fresh := 0
	for result := range results {
		if !result.Replayed {
			fresh++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fresh != 1 {
		t.Fatal(fresh)
	}
	actual, err = r.GetByID(ctx, p.ID)
	if err != nil || actual.Version != p.Version+1 || actual.Title != p.Title || actual.Status != p.Status || actual.Excerpt != p.Excerpt || !strings.HasPrefix(actual.ContentSource, "<p>retain</p>\n") {
		t.Fatal(actual, err)
	}
	var body string
	if err = pool.QueryRow(ctx, `SELECT public_search_body FROM np_posts WHERE id=$1`, p.ID).Scan(&body); err != nil || strings.Contains(body, "two") {
		t.Fatal("dynamic body indexed", body, err)
	}
	media := 0
	err = r.WalkMediaSnapshots(ctx, func(v content.MediaSnapshot) error {
		if v.Source == content.MediaSourceSnippet && strings.Contains(v.ContentSource, "retained.png") {
			media++
		}
		return nil
	})
	if err != nil || media != 1 {
		t.Fatal(media, err)
	}
	s, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = r.ResolveSnippets(ctx, []contenthtml.SnippetKey{first, {ID: s.ID, Version: s.CurrentVersion}})
	if err != nil || len(resolved) != 0 {
		t.Fatal(resolved, err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "new release", ContentSource: "<p>new</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = r.ResolveSnippets(ctx, []contenthtml.SnippetKey{first, {ID: s.ID, Version: s.CurrentVersion}})
	if err != nil || len(resolved) != 1 || resolved[first] != "" {
		t.Fatal(resolved, err)
	}
	replay, err := NewPostRepository(pool).ApplySnippetUpdate(ctx, u, a)
	if err != nil || !replay.Replayed || replay.Receipt.Items[0].Version != p.Version+1 {
		t.Fatal(replay, err)
	}
	u.Digest = strings.Repeat("0", 64)
	if _, err = r.ApplySnippetUpdate(ctx, u, a); !errors.Is(err, content.ErrSnippetReplay) {
		t.Fatal(err)
	}
	reader := content.SnippetAccess{Identity: "user:9", UserID: 9, Actor: "reader", Read: true}
	record, err := r.GetSnippet(ctx, s.ID, reader)
	if err != nil || len(record.Revisions) != 1 || record.Revisions[0].Version != s.CurrentVersion {
		t.Fatal(record, err)
	}
}

func TestPostgreSQLSnippetCurrentAccountAndReceiptCapacity(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	role := auth.Role{Slug: "snippet-editor", Name: "Snippet editor", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityContentRead, auth.CapabilityContentWrite}}
	if err := users.EnsureRoles(ctx, []auth.Role{role}); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Snippet test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "test"}, auth.UserInput{Username: "snippet-editor", DisplayName: "editor", RoleSlugs: []string{role.Slug}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{UserID: user.ID, Identity: fmt.Sprintf("user:%d", user.ID), Actor: user.Username, Read: true, Manage: true}
	r := NewPostRepository(pool)
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "one", ContentSource: "<p>one</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "article", AuthorID: user.ID, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "two", ContentSource: "<p>two</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("b", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	capabilities, _ := json.Marshal([]string{auth.CapabilityAdminAccess, auth.CapabilityContentRead})
	if _, err = pool.Exec(ctx, `UPDATE np_roles SET capabilities=$1 WHERE slug=$2`, capabilities, role.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplySnippetUpdate(ctx, u, a); !errors.Is(err, content.ErrSnippetPermission) {
		t.Fatal("stale role", err)
	}
	capabilities, _ = json.Marshal(role.Capabilities)
	if _, err = pool.Exec(ctx, `UPDATE np_roles SET capabilities=$1 WHERE slug=$2`, capabilities, role.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO np_snippet_receipts(identity,request_key,snippet_id,data) SELECT 'fixture',lpad(i::text,64,'0'),$1,convert_to('{}','UTF8') FROM generate_series(1,$2) i`, s.ID, content.MaxSnippetReceipts); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplySnippetUpdate(ctx, u, a); !errors.Is(err, content.ErrSnippetCapacity) {
		t.Fatal("capacity", err)
	}
	current, _ := r.GetByID(ctx, p.ID)
	if current.Version != p.Version {
		t.Fatal("capacity partially wrote")
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal("withdraw despite full receipts", err)
	}
}
