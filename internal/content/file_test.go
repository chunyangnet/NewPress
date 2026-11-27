package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileRepositoryEditorialLifecycleSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	category, err := repository.CreateTerm(ctx, TermInput{Taxonomy: TaxonomyCategory, Name: "项目进展", Slug: "progress"})
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{AuthorID: 17, Title: "持续创作", Slug: "persistent-post", ContentSource: "<p>第一版</p><script>alert(1)</script>", CategoryIDs: []int64{category.ID}, TagNames: []string{"发布"}}
	post, err := repository.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.ListRevisions(ctx, post.ID, 20)
	if err != nil || len(first) != 1 {
		t.Fatalf("revisions = %#v, %v", first, err)
	}
	input.Title, input.ContentSource, input.Status = "已发布文章", "<p>第二版</p>", StatusPublish
	post, err = repository.Update(ctx, post.ID, Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := clonePost(post)
	snapshot.Title = "尚未正式保存的标题"
	if _, err := repository.SaveAutosave(ctx, Autosave{PostID: post.ID, OwnerKey: "user:17", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	page, err := repository.Create(ctx, PostInput{Type: TypePage, Status: StatusPublish, Title: "关于本站", Slug: "about", ContentSource: "<p>持久页面</p>"})
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileRepository(path, true)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.ListAdmin(ctx, AdminQuery{Limit: 100})
	if err != nil || list.Total != 2 {
		t.Fatalf("seed was reapplied after restart: %#v, %v", list, err)
	}
	loaded, err := reopened.GetPublishedBySlug(ctx, post.Slug)
	if err != nil || loaded.ID != post.ID || loaded.Version != 2 || loaded.Title != input.Title || loaded.AuthorID != 17 || len(loaded.Tags) != 1 || loaded.Categories[0].ID != category.ID {
		t.Fatalf("restored post = %#v, %v", loaded, err)
	}
	if loadedPage, err := reopened.GetPublishedPageBySlug(ctx, "about"); err != nil || loadedPage.ID != page.ID {
		t.Fatalf("restored page = %#v, %v", loadedPage, err)
	}
	autosave, err := reopened.GetAutosave(ctx, post.ID, "user:17")
	if err != nil || autosave.Snapshot.Title != snapshot.Title {
		t.Fatalf("restored autosave = %#v, %v", autosave, err)
	}
	if _, err := reopened.GetAutosave(ctx, post.ID, "user:18"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatalf("cross-user autosave = %v", err)
	}
	if err := reopened.DeleteTerm(ctx, category.ID, TermMutation{ExpectedVersion: category.Version}); err != nil {
		t.Fatal(err)
	}
	uncategorized, _ := reopened.GetByID(ctx, post.ID)
	if uncategorized.Categories[0].ID != 1 {
		t.Fatal("deleted category did not fall back")
	}
	restored, err := reopened.RestoreRevision(ctx, post.ID, first[0].ID, Mutation{ExpectedVersion: post.Version})
	if err != nil || restored.Status != StatusPublish || restored.Title != "持续创作" || restored.Categories[0].Name != category.Name || restored.Categories[0].ID == category.ID || strings.Contains(restored.ContentHTML, "<script") {
		t.Fatalf("restored revision = %#v, %v", restored, err)
	}
	if _, err := reopened.GetAutosave(ctx, post.ID, "user:17"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatalf("stale autosave survived official write: %v", err)
	}
	reopened, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	final, _ := reopened.GetByID(ctx, post.ID)
	if final.Version != 3 || final.Categories[0].ID != restored.Categories[0].ID {
		t.Fatalf("final content = %#v", final)
	}
	next, err := reopened.Create(ctx, PostInput{Title: "后续内容", Slug: "next-post"})
	if err != nil || next.ID <= page.ID {
		t.Fatalf("next identity = %#v, %v", next, err)
	}
}

func TestFileRepositoryRollbackAndConflictDoNotPublishCandidate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repository.Create(ctx, PostInput{Title: "保留原稿", Slug: "original"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	// A non-regular rollback target fails on every platform, including Windows.
	if err := os.Mkdir(path+".bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Update(ctx, post.ID, Mutation{ExpectedVersion: 1}, PostInput{Title: "不能提交", Slug: "changed", TagNames: []string{"不能泄漏的标签"}}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	current, _ := repository.GetByID(ctx, post.ID)
	terms, _ := repository.ListTerms(ctx, TermQuery{Taxonomy: TaxonomyTag})
	after, _ := os.ReadFile(path)
	if current.Version != 1 || current.Title != post.Title || terms.Total != 0 || string(before) != string(after) {
		t.Fatal("failed write changed canonical content, terms or disk")
	}
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BulkChangeStatus(ctx, BulkStatusChange{Items: []BulkMutation{{ID: post.ID, ExpectedVersion: 1}, {ID: 999, ExpectedVersion: 1}}, Status: StatusPublish}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bulk failure = %v", err)
	}
	current, _ = repository.GetByID(ctx, post.ID)
	if current.Status != StatusDraft || current.Version != 1 {
		t.Fatal("partial bulk mutation")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.Create(canceled, PostInput{Title: "已取消"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation = %v", err)
	}
	other, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ChangeStatus(ctx, post.ID, Mutation{ExpectedVersion: 1}, StatusPublish); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ChangeStatus(ctx, post.ID, Mutation{ExpectedVersion: 1}, StatusTrash); err == nil {
		t.Fatal("stale store overwrote a newer disk snapshot")
	}
	if err := other.Ready(ctx); err == nil {
		t.Fatal("stale store reported ready")
	}
}

func TestFileRepositoryConcurrentWritersPreserveIdentityAndVersions(t *testing.T) {
	repository, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for i := 0; i < 12; i++ {
		writers.Go(func() {
			if _, err := repository.Create(context.Background(), PostInput{Title: fmt.Sprintf("文章 %d", i), Slug: fmt.Sprintf("post-%d", i)}); err != nil {
				t.Error(err)
			}
		})
	}
	writers.Wait()
	reopened, err := NewFileRepository(repository.path, false)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := reopened.ListAdmin(context.Background(), AdminQuery{Limit: 100})
	if list.Total != 12 {
		t.Fatalf("persisted count = %d", list.Total)
	}
	seen := make(map[int64]bool)
	for _, post := range list.Items {
		if seen[post.ID] {
			t.Fatal("duplicate identity")
		}
		seen[post.ID] = true
	}
}

func TestFileRepositoryStrictLoadingAndInterruptedReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(path, true)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := os.ReadFile(path)
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatalf("recover missing primary: %v", err)
	}
	if err := reopened.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repository.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, corrupt := range map[string][]byte{
		"empty": {}, "duplicate": []byte(`{"schema":1,"schema":1}`),
		"unknown":         []byte(strings.Replace(string(payload), `"schema":1`, `"schema":1,"unexpected":true`, 1)),
		"trailing":        append(append([]byte{}, payload...), []byte(`{}`)...),
		"invalid counter": []byte(strings.Replace(string(payload), `"next_id":10`, `"next_id":1`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(candidate, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(candidate+".bak", payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileRepository(candidate, true); err == nil {
				t.Fatal("invalid primary accepted or silently replaced")
			}
		})
	}
	var document contentDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	document.Posts[0].ContentHTML = `<script>window.injected=true</script>`
	document.Revisions[0].Snapshot.ContentHTML = document.Posts[0].ContentHTML
	modified, _ := json.Marshal(document)
	if err := os.WriteFile(path, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := reopened.GetByID(context.Background(), document.Posts[0].ID)
	revision, _ := reopened.GetRevision(context.Background(), document.Revisions[0].PostID, document.Revisions[0].ID)
	if strings.Contains(post.ContentHTML, "<script") || strings.Contains(revision.Snapshot.ContentHTML, "<script") {
		t.Fatal("stored HTML was trusted")
	}
}
