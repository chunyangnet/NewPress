package content

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRedirectContractMemoryAndFile(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			file := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				store, err := NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
				r = store
			}
			ctx := t.Context()
			p, err := r.Create(ctx, PostInput{Title: "Old", Slug: "old", Status: StatusPublish})
			if err != nil {
				t.Fatal(err)
			}
			revisions, _ := r.ListRevisions(ctx, p.ID, 10)
			first := revisions[0]
			manual, err := r.SaveRedirect(ctx, 0, Mutation{}, RedirectInput{Path: "/2020/旧文章/", PostID: p.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.SaveRedirect(ctx, 0, Mutation{}, RedirectInput{Path: PublicPath(p), PostID: p.ID}); !errors.Is(err, ErrRedirectConflict) {
				t.Fatal(err)
			}
			p, err = r.Update(ctx, p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: "Middle", Slug: "middle", Status: StatusPublish})
			if err != nil {
				t.Fatal(err)
			}
			p, err = r.Update(ctx, p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Type: TypePage, Title: "Latest", Slug: "latest", Status: StatusPublish})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/posts/old", "/posts/middle", "/2020/旧文章/"} {
				got, err := r.ResolveRedirect(ctx, path)
				if err != nil || got != "/pages/latest" {
					t.Fatal(path, got, err)
				}
			}
			list, _ := r.ListRedirects(ctx, RedirectQuery{})
			if list.Total != 3 {
				t.Fatal(list)
			}
			p, err = r.ChangeStatus(ctx, p.ID, Mutation{ExpectedVersion: p.Version}, StatusPrivate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ResolveRedirect(ctx, manual.Path); !errors.Is(err, ErrNotFound) {
				t.Fatal("private target exposed", err)
			}
			p, err = r.ChangeStatus(ctx, p.ID, Mutation{ExpectedVersion: p.Version}, StatusPublish)
			if err != nil {
				t.Fatal(err)
			}
			p, err = r.RestoreRevision(ctx, p.ID, first.ID, Mutation{ExpectedVersion: p.Version})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := r.ResolveRedirect(ctx, "/pages/latest"); err != nil || got != "/posts/old" {
				t.Fatal(got, err)
			}
			if _, err = r.ResolveRedirect(ctx, "/posts/old"); !errors.Is(err, ErrNotFound) {
				t.Fatal("canonical path must win", err)
			}
			// A newly created private item owns its URL and must suppress an alias,
			// without revealing either its title or the other rule's target.
			occupier, err := r.Create(ctx, PostInput{Title: "Private owner", Slug: "middle", Status: StatusPrivate})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ResolveRedirect(ctx, "/posts/middle"); !errors.Is(err, ErrNotFound) {
				t.Fatal("private occupied path redirected", err)
			}
			occupier, err = r.ChangeStatus(ctx, occupier.ID, Mutation{ExpectedVersion: occupier.Version}, StatusPublish)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := r.ListRevisions(ctx, occupier.ID, 100)
			_, err = r.Update(ctx, occupier.ID, Mutation{ExpectedVersion: occupier.Version}, PostInput{Title: "Move", Slug: "moved", Status: StatusPublish})
			if !errors.Is(err, ErrRedirectConflict) {
				t.Fatal("conflicting auto rule accepted", err)
			}
			after, _ := r.ListRevisions(ctx, occupier.ID, 100)
			unchanged, _ := r.GetByID(ctx, occupier.ID)
			if !reflect.DeepEqual(before, after) || unchanged.Version != occupier.Version || unchanged.Slug != "middle" {
				t.Fatal("partial update")
			}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := r.SaveRedirect(ctx, manual.ID, Mutation{ExpectedVersion: manual.Version}, RedirectInput{Path: "/legacy-new/", PostID: p.ID})
					results <- err
				}()
			}
			wg.Wait()
			close(results)
			ok, conflict := 0, 0
			for err := range results {
				if err == nil {
					ok++
				} else if errors.Is(err, ErrVersionConflict) {
					conflict++
				} else {
					t.Fatal(err)
				}
			}
			if ok != 1 || conflict != 1 {
				t.Fatal(ok, conflict)
			}
			if kind == "file" {
				store, err := NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
				r = store
			}
			if got, err := r.ResolveRedirect(ctx, "/legacy-new/"); err != nil || got != "/posts/old" {
				t.Fatal("reload", got, err)
			}
			if err = r.DeleteRedirect(ctx, manual.ID, Mutation{ExpectedVersion: 1}); !errors.Is(err, ErrVersionConflict) {
				t.Fatal(err)
			}
			if err = r.DeleteRedirect(ctx, manual.ID, Mutation{ExpectedVersion: 2}); err != nil {
				t.Fatal(err)
			}
			if _, err = r.ResolveRedirect(ctx, "/legacy-new/"); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestRedirectValidationAndBoundedAtomicFailure(t *testing.T) {
	for _, path := range []string{"/", "//elsewhere.test", "https://site.test/path", "/admin/x", "/API/x", "/posts/a/comments", "/posts/a/", "/posts/UPPER", "/a?next=x", "/a#x", "/a%2fb", "/a\\b", "/a/../b", "/a/.b", "/a//b", "/a b", "/a\n", "/robots.txt", "/media/1/a.png", "/series/old/"} {
		if ValidRedirectPath(path) {
			t.Fatalf("accepted %q", path)
		}
	}
	for _, path := range []string{"/posts/a", "/pages/about", "/2020/旧文章/", "/legacy.html"} {
		if !ValidRedirectPath(path) {
			t.Fatalf("rejected %q", path)
		}
	}
	r := NewMemoryRepository(false)
	p, err := r.Create(t.Context(), PostInput{Title: "Cap", Slug: "cap", Status: StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxRedirects {
		r.redirects = append(r.redirects, Redirect{ID: int64(i + 1), RedirectInput: RedirectInput{Path: fmt.Sprintf("/old/%d", i), PostID: p.ID}, Version: 1, CreatedAt: p.CreatedAt, UpdatedAt: p.CreatedAt})
	}
	r.nextRedirectID = MaxRedirects + 1
	if _, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: "After", Slug: "after", Status: StatusPublish}); len(FieldErrors(err)) == 0 {
		t.Fatal(err)
	}
	after, _ := r.GetByID(t.Context(), p.ID)
	if after.Slug != p.Slug || after.Version != p.Version {
		t.Fatal("capacity caused partial content update")
	}
	draft, err := r.Create(t.Context(), PostInput{Title: "Draft", Slug: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Update(t.Context(), draft.ID, Mutation{ExpectedVersion: draft.Version}, PostInput{Title: "Draft", Slug: "draft-new"}); err != nil {
		t.Fatal("draft rename should not capture path", err)
	}
}

func TestRedirectFileFailurePreservesContentAndRules(t *testing.T) {
	file := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(file, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), PostInput{Title: "Before", Slug: "before", Status: StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(file, file+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: "After", Slug: "after", Status: StatusPublish}); err == nil {
		t.Fatal("write should fail")
	}
	after, _ := r.GetByID(t.Context(), p.ID)
	rules, _ := r.ListRedirects(t.Context(), RedirectQuery{})
	if after.Slug != p.Slug || after.Version != p.Version || rules.Total != 0 {
		t.Fatal("failed durable commit became visible")
	}
	os.Remove(file)
	if err = os.WriteFile(file, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileRepository(file, false); err != nil {
		t.Fatal(err)
	}
}
