package content

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/idempotency"
)

func submissionDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func TestSubmissionAtomicReplayAndRestart(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var repository Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				var err error
				repository, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			request := Submission{Key: submissionDigest("owner:1/create/one"), Fingerprint: submissionDigest("first"), Action: "create", Input: PostInput{AuthorID: 1, Title: "不重复创建", Slug: "once", ContentSource: "<p>保留正文</p>"}}
			created, err := repository.Submit(t.Context(), request)
			if err != nil || created.Replayed || created.Receipt.Version != 1 {
				t.Fatal(created, err)
			}
			if kind == "file" {
				repository, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := repository.Submit(t.Context(), request)
					if err != nil || !result.Replayed || result.Receipt.PostID != created.Receipt.PostID || result.Receipt.Version != created.Receipt.Version || !result.Receipt.CreatedAt.Equal(created.Receipt.CreatedAt) {
						t.Errorf("replay = %+v, %v", result, err)
					}
				}()
			}
			wg.Wait()
			changed := request
			changed.Fingerprint = submissionDigest("different")
			if _, err := repository.Submit(t.Context(), changed); !errors.Is(err, idempotency.ErrConflict) {
				t.Fatal(err)
			}
			update := Submission{Key: submissionDigest("update-one"), Fingerprint: submissionDigest("edited"), Action: "save", PostID: created.Receipt.PostID, Version: 1, Input: PostInput{AuthorID: 1, Title: "第二版", Slug: "once", ContentSource: "<p>保留正文</p>"}}
			saved, err := repository.Submit(t.Context(), update)
			if err != nil || saved.Receipt.Version != 2 {
				t.Fatal(saved, err)
			}
			if _, err = repository.ChangeStatus(t.Context(), created.Receipt.PostID, Mutation{ExpectedVersion: 2}, StatusPrivate); err != nil {
				t.Fatal(err)
			}
			replayed, err := repository.Submit(t.Context(), update)
			if err != nil || !replayed.Replayed || replayed.Receipt.Version != 2 {
				t.Fatal(replayed, err)
			}
			list, err := repository.ListAdmin(t.Context(), AdminQuery{Limit: 10})
			if err != nil || list.Total != 1 || list.Items[0].Version != 3 {
				t.Fatal(list, err)
			}
			revisions, err := repository.ListRevisions(t.Context(), created.Receipt.PostID, 20)
			if err != nil || len(revisions) != 3 {
				t.Fatal(revisions, err)
			}
		})
	}
}

func TestSubmissionConcurrentFirstCreationAndLifecycle(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var repository Repository = NewMemoryRepository(false)
			if kind == "file" {
				var err error
				repository, err = NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
				if err != nil {
					t.Fatal(err)
				}
			}
			request := Submission{Key: submissionDigest("concurrent"), Fingerprint: submissionDigest("fresh"), Action: "create", Input: PostInput{Title: "并发首次创建"}}
			start := make(chan struct{})
			results := make(chan SubmissionResult, 12)
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					result, err := repository.Submit(t.Context(), request)
					if err != nil {
						t.Error(err)
						return
					}
					results <- result
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			created, postID := 0, int64(0)
			for result := range results {
				if !result.Replayed {
					created++
				}
				postID = result.Receipt.PostID
			}
			list, err := repository.ListAdmin(t.Context(), AdminQuery{Limit: 100})
			if err != nil || created != 1 || list.Total != 1 {
				t.Fatal(created, list, err)
			}
			revisions, _ := repository.ListRevisions(t.Context(), postID, 20)
			for i, action := range []string{"publish", "trash", "restore", "restore_revision"} {
				command := Submission{Key: submissionDigest(action), Fingerprint: submissionDigest(action), Action: action, PostID: postID, Version: int64(i + 1), RevisionID: revisions[0].ID}
				for attempt := range 2 {
					result, err := repository.Submit(t.Context(), command)
					if err != nil || result.Replayed != (attempt == 1) || result.Receipt.Version != int64(i+2) {
						t.Fatal(result, err)
					}
				}
			}
			revisions, err = repository.ListRevisions(t.Context(), postID, 20)
			if err != nil || len(revisions) != 5 {
				t.Fatal(revisions, err)
			}
		})
	}
}

func TestSubmissionFileFailureRollsBackContentAndReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	request := Submission{Key: submissionDigest("disk-failure"), Fingerprint: submissionDigest("disk"), Action: "create", Input: PostInput{Title: "原子创建", TagNames: []string{"不泄漏标签"}}}
	if _, err := repository.Submit(t.Context(), request); err == nil {
		t.Fatal("write succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || len(repository.state.posts) != 0 || len(repository.state.submissions) != 0 || repository.state.nextID != 1 {
		t.Fatal("failed candidate escaped")
	}
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	result, err := repository.Submit(t.Context(), request)
	if err != nil || result.Replayed || result.Receipt.PostID != 1 {
		t.Fatal(result, err)
	}
	// A read-only replay lookup must succeed even while subsequent writes fail.
	if err := os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FindSubmission(t.Context(), request.Key, request.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionReceiptValidation(t *testing.T) {
	valid := SubmissionReceipt{Key: submissionDigest("key"), Fingerprint: submissionDigest("body"), PostID: 1, Version: 1, Action: "create", CreatedAt: time.Now()}
	posts := map[int64]Post{1: {ID: 1, Version: 2}}
	for _, change := range []func(*SubmissionReceipt){
		func(r *SubmissionReceipt) { r.Key = "bad" }, func(r *SubmissionReceipt) { r.Fingerprint = strings.Repeat("z", 64) },
		func(r *SubmissionReceipt) { r.PostID = 2 }, func(r *SubmissionReceipt) { r.Version = 0 },
		func(r *SubmissionReceipt) { r.Version = 3 }, func(r *SubmissionReceipt) { r.Version = 2 },
		func(r *SubmissionReceipt) { r.Action = "unknown" }, func(r *SubmissionReceipt) { r.CreatedAt = time.Time{} },
	} {
		r := valid
		change(&r)
		if err := ValidateSubmissionReceipts([]SubmissionReceipt{r}, posts); err == nil {
			t.Fatal(r)
		}
	}
	if err := ValidateSubmissionReceipts([]SubmissionReceipt{valid, valid}, posts); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := ValidateSubmissionReceipts([]SubmissionReceipt{valid}, posts); err != nil {
		t.Fatal(err)
	}
}

func TestSubmissionFailedWriteLeavesNoReceiptAndCanBeCorrected(t *testing.T) {
	repository := NewMemoryRepository(false)
	request := Submission{Key: submissionDigest("invalid"), Fingerprint: submissionDigest("one"), Action: "create", Input: PostInput{Title: ""}}
	if _, err := repository.Submit(t.Context(), request); err == nil {
		t.Fatal("invalid create succeeded")
	}
	if len(repository.submissions) != 0 || len(repository.posts) != 0 {
		t.Fatal("failed write changed state")
	}
	request.Input.Title = "已修正"
	request.Fingerprint = submissionDigest("two")
	result, err := repository.Submit(t.Context(), request)
	if err != nil || result.Replayed {
		t.Fatal(result, err)
	}
}

func TestSubmissionCreationReceiptsDoNotExpireAndOldUpdatesStayVersionProtected(t *testing.T) {
	repository := NewMemoryRepository(false)
	now := time.Now()
	repository.now = func() time.Time { return now }
	create := Submission{Key: submissionDigest("never-expire"), Fingerprint: submissionDigest("create"), Action: "create", Input: PostInput{Title: "创建记录", Slug: "stable"}}
	result, err := repository.Submit(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	update := Submission{Key: submissionDigest("update-expiry"), Fingerprint: submissionDigest("update"), Action: "trash", PostID: result.Receipt.PostID, Version: 1}
	if _, err := repository.Submit(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	now = now.Add(SubmissionReplayWindow + time.Hour)
	if result, err := repository.Submit(t.Context(), create); err != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	if _, err := repository.Submit(t.Context(), update); !errors.Is(err, ErrVersionConflict) {
		t.Fatal(err)
	}
}
