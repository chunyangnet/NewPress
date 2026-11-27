package content

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetManager() SnippetAccess {
	return SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
}
func snippetOwn(id int64) SnippetAccess {
	return SnippetAccess{UserID: id, Identity: fmt.Sprintf("user:%d", id), Actor: "author", Read: true, WriteOwn: true}
}

func TestSnippetStoresVersionPermissionAndAtomicUpdates(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			a := snippetManager()
			ctx := t.Context()
			s, err := r.SaveSnippet(ctx, 0, Mutation{}, SnippetInput{"仅管理者标题", "<p>私密历史</p>", SnippetPrivate}, a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.GetSnippet(ctx, s.ID, snippetOwn(7)); !errors.Is(err, ErrNotFound) {
				t.Fatal("private read", err)
			}
			if _, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{"越权", "bad", SnippetShared}, snippetOwn(7)); !errors.Is(err, ErrSnippetPermission) {
				t.Fatal("author managed", err)
			}
			oldPrivate := contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion}
			s, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{"共享说明", "<p>第一版</p>", SnippetShared}, a)
			if err != nil {
				t.Fatal(err)
			}
			first := contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion}
			resolved, err := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{oldPrivate, first})
			if err != nil || len(resolved) != 1 || resolved[first] != "<p>第一版</p>" {
				t.Fatal(resolved, err)
			}
			visible, err := r.GetSnippet(ctx, s.ID, snippetOwn(7))
			if err != nil || len(visible.Revisions) != 1 {
				t.Fatal(visible, err)
			}
			posts := []Post{}
			for i, author := range []int64{7, 8} {
				p, e := r.Create(ctx, PostInput{Title: fmt.Sprintf("文章%d", i), Slug: fmt.Sprintf("snippet-%d", i), AuthorID: author, Status: StatusPublish, ContentSource: "<p>保留</p>\n" + contenthtml.SnippetShell(first), Actor: "editor"})
				if e != nil {
					t.Fatal(e)
				}
				posts = append(posts, p)
			}
			s, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{"共享说明", "<p>第二版</p>", SnippetShared}, a)
			if err != nil {
				t.Fatal(err)
			}
			impacts, err := r.SnippetImpacts(ctx, s.ID, snippetOwn(7), 24, 0)
			if err != nil || impacts.Total != 1 || impacts.Items[0].ID != posts[0].ID {
				t.Fatal(impacts, err)
			}
			u := SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []SnippetSelection{{posts[0].ID, posts[0].Version}, {posts[1].ID, posts[1].Version}}}
			if _, err = r.PreviewSnippetUpdate(ctx, u, snippetOwn(7)); !errors.Is(err, ErrSnippetPermission) {
				t.Fatal("cross author", err)
			}
			plan, err := r.PreviewSnippetUpdate(ctx, u, a)
			if err != nil {
				t.Fatal(err)
			}
			u.Digest = plan.Digest
			u.RequestKey = strings.Repeat("a", 64)
			p, err := r.ChangeStatus(ctx, posts[1].ID, Mutation{ExpectedVersion: posts[1].Version, Actor: "editor"}, StatusPrivate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ApplySnippetUpdate(ctx, u, a); !errors.Is(err, ErrVersionConflict) {
				t.Fatal("conflict accepted", err)
			}
			unchanged, _ := r.GetByID(ctx, posts[0].ID)
			if unchanged.Version != posts[0].Version || unchanged.ContentSource != posts[0].ContentSource {
				t.Fatal("partial update")
			}
			u.Items[1].Version = p.Version
			plan, err = r.PreviewSnippetUpdate(ctx, u, a)
			if err != nil {
				t.Fatal(err)
			}
			u.Digest = plan.Digest
			result, err := r.ApplySnippetUpdate(ctx, u, a)
			if err != nil || result.Replayed || len(result.Receipt.Items) != 2 {
				t.Fatal(result, err)
			}
			if kind == "file" {
				payload, e := os.ReadFile(path)
				if e != nil || !strings.Contains(string(payload), `"schema":2`) {
					t.Fatal("schema", e)
				}
				r, e = NewFileRepository(path, false)
				if e != nil {
					t.Fatal("reopen", e)
				}
			}
			replay, err := r.ApplySnippetUpdate(ctx, u, a)
			if err != nil || !replay.Replayed || replay.Receipt.Items[0].Version != result.Receipt.Items[0].Version {
				t.Fatal(replay, err)
			}
			changed := u
			changed.Items = append([]SnippetSelection(nil), u.Items...)
			changed.Items[0].Version++
			if _, err = r.ApplySnippetUpdate(ctx, changed, a); !errors.Is(err, ErrSnippetReplay) {
				t.Fatal("key mutation", err)
			}
			for i, original := range posts {
				current, e := r.GetByID(ctx, original.ID)
				if e != nil || current.Title != original.Title || !strings.HasPrefix(current.ContentSource, "<p>保留</p>\n") || strings.Contains(current.ContentSource, "第二版") {
					t.Fatal(current, e)
				}
				want := original.Version + 1
				if i == 1 {
					want++
				}
				if current.Version != want {
					t.Fatal(current.Version, want)
				}
				revs, e := r.ListRevisions(ctx, current.ID, 10)
				if e != nil || len(revs) < 2 {
					t.Fatal(revs, e)
				}
			}
			s, err = r.WithdrawSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, a)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err = r.ResolveSnippets(ctx, []contenthtml.SnippetKey{first, {ID: s.ID, Version: s.CurrentVersion}})
			if err != nil || len(resolved) != 0 {
				t.Fatal("withdrawal", resolved, err)
			}
			s, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{"重新共享", "<p>新共享期</p>", SnippetShared}, a)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err = r.ResolveSnippets(ctx, []contenthtml.SnippetKey{first, {ID: s.ID, Version: s.CurrentVersion}})
			if err != nil || len(resolved) != 1 || resolved[first] != "" {
				t.Fatal("old revived", resolved, err)
			}
			replay, err = r.ApplySnippetUpdate(ctx, u, a)
			if err != nil || !replay.Replayed {
				t.Fatal("replay after withdrawal", replay, err)
			}
			if kind == "file" {
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				record, e := r.GetSnippet(ctx, s.ID, a)
				if e != nil || len(record.Revisions) != 4 {
					t.Fatal(record, e)
				}
			}
		})
	}
}

func TestSnippetConcurrentApplyAndWithdrawalCapacity(t *testing.T) {
	r := NewMemoryRepository(false)
	a := snippetManager()
	ctx := t.Context()
	s, err := r.SaveSnippet(ctx, 0, Mutation{}, SnippetInput{"One", "<p>first</p>", SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, PostInput{Title: "post", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: 1}, SnippetInput{"Two", "<p>second</p>", SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	u := SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []SnippetSelection{{p.ID, p.Version}}, RequestKey: strings.Repeat("b", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	var wg sync.WaitGroup
	results := make(chan SnippetResult, 8)
	errs := make(chan error, 8)
	for range 8 {
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
	current, _ := r.GetByID(ctx, p.ID)
	if current.Version != p.Version+1 {
		t.Fatal(current.Version)
	}
	record := r.snippets[0]
	last := record.Revisions[len(record.Revisions)-1]
	for len(record.Revisions) < MaxSnippetRevisions {
		last.Version++
		record.Revisions = append(record.Revisions, last)
	}
	record.Version = last.Version
	record.CurrentVersion = last.Version
	r.snippets[0] = record
	if _, err = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: record.Version}, SnippetInput{"over", "<p>full</p>", SnippetShared}, a); !errors.Is(err, ErrSnippetCapacity) {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, Mutation{ExpectedVersion: record.Version}, a); err != nil {
		t.Fatal("withdraw at capacity", err)
	}
	encoded, err := encodeContentDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeContentDocument(encoded); err != nil {
		t.Fatal("withdraw state", err)
	}
}

func TestSnippetValidationAndRetainedMedia(t *testing.T) {
	for _, body := range []string{`<aside data-np-snippet="1" data-np-snippet-version="1"></aside>`, `<aside data-np-widget="post"></aside>`, `<aside data-npr-resource="secret"></aside>`, "", strings.Repeat("a", MaxSnippetBodyBytes+1)} {
		if _, _, err := NormalizeSnippetInput(SnippetInput{"bad", body, SnippetShared}); err == nil {
			t.Fatalf("accepted %q", body[:min(len(body), 80)])
		}
	}
	r := NewMemoryRepository(false)
	a := snippetManager()
	s, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{"secret", `<img src="/media/retained.png" alt="image">`, SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	count := 0
	err = r.WalkMediaSnapshots(t.Context(), func(v MediaSnapshot) error {
		if v.Source == MediaSourceSnippet && strings.Contains(v.ContentHTML, "retained.png") {
			count++
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	u := SnippetUpdate{SnippetID: 1, SnippetVersion: 1, Items: []SnippetSelection{{1, 1}, {1, 1}}}
	if _, err = NormalizeSnippetUpdate(u, false); err == nil {
		t.Fatal("duplicate selection")
	}
	record := r.snippets[0]
	record.SharedSince = 1
	if ValidateSnippetRecord(record) == nil {
		t.Fatal("corrupt sharing accepted")
	}
	s, err = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: record.Version}, SnippetInput{"shared again", "<p>new sharing period</p>", SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	record = r.snippets[0]
	if ValidateSnippetRecord(record) != nil {
		t.Fatal("valid re-sharing rejected")
	}
	// Withdrawal increments the management version without creating a body.
	// A stored boundary must refer to a real immutable body revision.
	record.SharedSince = s.CurrentVersion - 1
	if ValidateSnippetRecord(record) == nil {
		t.Fatal("nonexistent sharing boundary accepted")
	}
}
