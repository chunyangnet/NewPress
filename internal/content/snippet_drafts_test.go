package content

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/idempotency"
)

func draftFixture(t *testing.T, r Repository) (SnippetAccess, Snippet, Post, SnippetDraftRequest) {
	t.Helper()
	a := SnippetAccess{UserID: 7, Identity: "user:7", Actor: "editor", Read: true, Manage: true, WriteOwn: true}
	s, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{Title: "共享说明", ContentSource: "<p>固定第一版</p>", Visibility: SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), PostInput{Title: "原文章", Slug: "draft-source", AuthorID: 7, Status: StatusPublish, ContentSource: "<p>原文章正文</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	return a, s, p, u
}

func draftConfirm(t *testing.T, r Repository, u SnippetDraftRequest, a SnippetAccess) Submission {
	t.Helper()
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("a", 64)
	s, err := NewSnippetDraftSubmission(u, a)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnippetDraftAtomicCopyReplayAndRestart(t *testing.T) {
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
			a, s, p, u := draftFixture(t, r)
			// Still copies the pinned body, not the latest revision.
			s, err := r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: "新说明", ContentSource: "<p>第二版</p>", Visibility: SnippetShared}, a)
			if err != nil {
				t.Fatal(err)
			}
			request := draftConfirm(t, r, u, a)
			var wg sync.WaitGroup
			results := make(chan SubmissionResult, 6)
			errs := make(chan error, 6)
			for range 6 {
				wg.Go(func() { v, e := r.Submit(t.Context(), request); results <- v; errs <- e })
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
				if id != 0 && id != v.Receipt.PostID {
					t.Fatal("multiple drafts")
				}
				id = v.Receipt.PostID
			}
			if fresh != 1 {
				t.Fatal("fresh", fresh)
			}
			draft, err := r.GetByID(t.Context(), id)
			if err != nil || draft.Status != StatusDraft || draft.Version != 1 || draft.AuthorID != a.UserID || draft.CommentStatus != CommentClosed || !draft.PublishedAt.IsZero() || strings.Contains(draft.ContentSource, "data-np-snippet") || !strings.Contains(draft.ContentSource, "固定第一版") || strings.Contains(draft.ContentSource, "第二版") {
				t.Fatal(draft, err)
			}
			original, _ := r.GetByID(t.Context(), p.ID)
			if original.Version != p.Version || original.ContentSource != p.ContentSource || original.Status != p.Status {
				t.Fatal("source changed")
			}
			if _, err = r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			v, err := r.Submit(t.Context(), request)
			if err != nil || !v.Replayed || v.Receipt.PostID != id {
				t.Fatal(v, err)
			}
			draft, _ = r.GetByID(t.Context(), id)
			if !strings.Contains(draft.ContentSource, "固定第一版") {
				t.Fatal("withdrawal altered copy")
			}
			changed := *request.SnippetDraft
			changed.Request.Digest = strings.Repeat("0", 64)
			conflict, _ := NewSnippetDraftSubmission(changed.Request, a)
			if _, err = r.Submit(t.Context(), conflict); !errors.Is(err, idempotency.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestSnippetDraftConflictsPermissionsAndBounds(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, scenario := range []string{"source", "withdraw", "new_revision", "digest", "permission", "cancel", "capacity"} {
			if kind == "file" && scenario == "capacity" {
				continue
			}
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				var r Repository = NewMemoryRepository(false)
				if kind == "file" {
					var err error
					r, err = NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
					if err != nil {
						t.Fatal(err)
					}
				}
				a, s, p, u := draftFixture(t, r)
				request := draftConfirm(t, r, u, a)
				ctx := t.Context()
				want := ErrVersionConflict
				switch scenario {
				case "source":
					_, _ = r.ChangeStatus(ctx, p.ID, Mutation{ExpectedVersion: 1, Actor: a.Actor}, StatusPrivate)
				case "withdraw":
					_, _ = r.WithdrawSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, a)
					want = ErrNotFound
				case "new_revision":
					_, _ = r.SaveSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: "changed", ContentSource: "<p>new</p>", Visibility: SnippetShared}, a)
				case "digest":
					request.SnippetDraft.Request.Digest = strings.Repeat("f", 64)
					request, _ = NewSnippetDraftSubmission(request.SnippetDraft.Request, a)
				case "permission":
					a.Manage = false
					a.UserID = 8
					a.Identity = "user:8"
					request, _ = NewSnippetDraftSubmission(request.SnippetDraft.Request, a)
					want = ErrNotFound
				case "cancel":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					want = context.Canceled
				case "capacity":
					// The memory candidate must not publish a partially created post.
					state := r.(*MemoryRepository)
					state.submissions = make([]SubmissionReceipt, MaxSubmissionReceipts)
					for i := range state.submissions {
						state.submissions[i] = SubmissionReceipt{Action: "create", Key: fmt.Sprintf("%064x", i+1)}
					}
					want = idempotency.ErrCapacity
				}
				if _, err := r.Submit(ctx, request); !errors.Is(err, want) {
					t.Fatal("wanted", want, "got", err)
				}
				all, _ := r.ListAdmin(t.Context(), AdminQuery{Limit: 100})
				if all.Total != 1 {
					t.Fatal("partial draft", all.Total)
				}
			})
		}
	}
	for _, scenario := range []string{"private", "missing", "invalid", "unclosed", "self_closing", "positions", "keys", "bytes", "dynamic", "no_refs"} {
		t.Run(scenario, func(t *testing.T) {
			r := NewMemoryRepository(false)
			a, s, p, u := draftFixture(t, r)
			source := p.ContentSource
			switch scenario {
			case "private":
				_, _ = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: s.Title, ContentSource: "<p>secret</p>", Visibility: SnippetPrivate}, a)
			case "missing":
				source = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 99, Version: 1})
			case "invalid":
				source = `<aside data-np-snippet="bogus">never copy fallback</aside>`
			case "unclosed":
				source = `<aside data-np-snippet="1" data-np-snippet-version="1">unclosed ordinary text`
			case "self_closing":
				source = `<aside data-np-snippet="1" data-np-snippet-version="1"/><p>must not swallow</p>`
			case "positions":
				source = strings.Repeat(contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1}), 201)
			case "keys":
				source = ""
				for i := int64(1); i <= 101; i++ {
					source += contenthtml.SnippetShell(contenthtml.SnippetKey{ID: i, Version: 1})
				}
			case "bytes":
				s, _ = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: s.Title, ContentSource: "<p>" + strings.Repeat("x", 120<<10) + "</p>", Visibility: SnippetShared}, a)
				source = strings.Repeat(contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion}), 20)
			case "dynamic":
				source += `<aside data-np-resource="paid"></aside>`
			case "no_refs":
				source = "<p>ordinary</p>"
			}
			p, err := r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version, Actor: a.Actor}, PostInput{Title: p.Title, AuthorID: 7, ContentSource: source})
			if err != nil {
				t.Fatal(err)
			}
			u.Version = p.Version
			if plan, err := r.PreviewSnippetDraft(t.Context(), u, a); err == nil {
				t.Fatal("bad source accepted", plan)
			}
		})
	}
}

func TestSnippetDraftMaximumReferencesAndSavedReferenceIndex(t *testing.T) {
	r := NewMemoryRepository(false)
	a := SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	source := ""
	for i := range 100 {
		s, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{Title: fmt.Sprint("snippet ", i), ContentSource: "<p>copy</p>", Visibility: SnippetShared}, a)
		if err != nil {
			t.Fatal(err)
		}
		source += strings.Repeat(contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1}), 2)
	}
	created, err := r.Submit(t.Context(), Submission{Key: submissionDigest("source"), Fingerprint: submissionDigest("source-body"), Action: "create", Input: PostInput{Title: "page", Type: TypePage, ContentSource: source}})
	if err != nil {
		t.Fatal(err)
	}
	impacts, err := r.SnippetImpacts(t.Context(), 1, a, 10, 0)
	if err != nil || impacts.Total != 1 {
		t.Fatal("create index", impacts, err)
	}
	plan, err := r.PreviewSnippetDraft(t.Context(), SnippetDraftRequest{PostID: created.Receipt.PostID, Version: 1}, a)
	if err != nil || plan.Occurrences != 200 || len(plan.References) != 100 || strings.Count(plan.ContentHTML, "<p>copy</p>") != 200 || plan.Type != TypePage {
		t.Fatal(plan, err)
	}
	_, err = r.Submit(t.Context(), Submission{Key: submissionDigest("remove"), Fingerprint: submissionDigest("remove-body"), Action: "save", PostID: created.Receipt.PostID, Version: 1, Input: PostInput{Title: "page", Type: TypePage, ContentSource: "<p>no references</p>"}})
	if err != nil {
		t.Fatal(err)
	}
	impacts, err = r.SnippetImpacts(t.Context(), 1, a, 10, 0)
	if err != nil || impacts.Total != 0 {
		t.Fatal("save index", impacts, err)
	}
}

func TestSnippetDraftFileFailureAndReplayPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _, u := draftFixture(t, r)
	request := draftConfirm(t, r, u, a)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A filesystem obstruction prevents replacement; memory and receipt stay absent.
	if err = os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Submit(t.Context(), request); err == nil {
		t.Fatal("storage failure accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed write changed file")
	}
	v, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	owner := a
	owner.Manage = false
	// Simulate administrative ownership transfer in the stored state. Ordinary
	// article Update deliberately preserves authorship.
	_, err = fileContentChange(t.Context(), r, func(state *MemoryRepository) (bool, error) {
		state.posts[state.postIndexLocked(v.Receipt.PostID)].AuthorID = 8
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ = NewSnippetDraftSubmission(request.SnippetDraft.Request, owner)
	if _, err = r.Submit(t.Context(), request); !errors.Is(err, ErrSnippetPermission) {
		t.Fatal("replay ownership", err)
	}
}
