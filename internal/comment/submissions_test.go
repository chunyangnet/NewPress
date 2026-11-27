package comment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

func submissionFixture(key string) Submission {
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	return Submission{Key: digest(key), Fingerprint: digest("body-" + key), Input: CreateInput{PostID: 7, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "重试只收到一条", Status: StatusApproved}}
}

func TestCommentSubmissionAtomicConcurrentAndRestart(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "comments.json")
			}
			store, err := NewFileStore(path, false)
			if err != nil {
				t.Fatal(err)
			}
			request := submissionFixture("concurrent")
			start := make(chan struct{})
			results := make(chan SubmissionResult, 24)
			var workers sync.WaitGroup
			for range 24 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					result, err := store.CreateOnce(t.Context(), request)
					if err != nil {
						t.Error(err)
						return
					}
					results <- result
				}()
			}
			close(start)
			workers.Wait()
			close(results)
			first, id := 0, int64(0)
			for result := range results {
				if !result.Replayed {
					first++
				}
				if id != 0 && id != result.Receipt.CommentID {
					t.Fatal("multiple comments")
				}
				id = result.Receipt.CommentID
			}
			if first != 1 || id <= 0 || len(store.items) != 1 || len(store.submissions) != 1 {
				t.Fatal(first, id, len(store.items), len(store.submissions))
			}
			events := len(store.activities)
			changed := request
			changed.Fingerprint = strings.Repeat("f", 64)
			if _, err := store.CreateOnce(t.Context(), changed); !errors.Is(err, idempotency.ErrConflict) {
				t.Fatal(err)
			}
			if _, err := store.FindSubmission(t.Context(), changed.Key, changed.Fingerprint); !errors.Is(err, idempotency.ErrConflict) {
				t.Fatal(err)
			}
			if _, err := store.ChangeStatus(t.Context(), id, Mutation{ExpectedVersion: 1}, StatusTrash); err != nil {
				t.Fatal(err)
			}
			if err := store.Delete(t.Context(), id, Mutation{ExpectedVersion: 2}); err != nil {
				t.Fatal(err)
			}
			if disk {
				store, err = NewFileStore(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Replay does not revalidate a deleted parent or changed write policy.
			request.Input = CreateInput{}
			result, err := store.CreateOnce(t.Context(), request)
			if err != nil || !result.Replayed || result.Receipt.CommentID != id || len(store.items) != 0 {
				t.Fatal(result, err, store.items)
			}
			if events == 0 || len(store.activities) != events {
				t.Fatal("replays or deletion duplicated delivery events", events, len(store.activities))
			}
			if item, err := store.Create(t.Context(), submissionFixture("next").Input); err != nil || item.ID <= id {
				t.Fatal(item, err)
			}
		})
	}
}

func TestCommentSubmissionFileFailureIsAtomic(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewFileStore("", false)
	store.path = filepath.Join(blocked, "comments.json")
	request := submissionFixture("rollback")
	if _, err := store.CreateOnce(t.Context(), request); err == nil {
		t.Fatal("expected write failure")
	}
	if len(store.items) != 0 || len(store.submissions) != 0 || store.nextID != 1 || len(store.activities) != 0 {
		t.Fatal("partial commit")
	}
	store.path = filepath.Join(dir, "comments.json")
	result, err := store.CreateOnce(t.Context(), request)
	if err != nil || result.Replayed || result.Receipt.CommentID != 1 {
		t.Fatal(result, err)
	}
	loaded, err := NewFileStore(store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := loaded.FindSubmission(t.Context(), request.Key, request.Fingerprint); err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
}

func TestCommentSubmissionCapacityKeepsOldReceipts(t *testing.T) {
	store, _ := NewFileStore("", false)
	request := submissionFixture("existing")
	first, err := store.CreateOnce(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxSubmissionReceipts; i++ {
		key := fmt.Sprintf("%064x", i)
		store.submissions[key] = SubmissionReceipt{Key: key, Fingerprint: key, CommentID: int64(i + 1), PostID: 7, CreatedAt: time.Now()}
	}
	if _, err := store.CreateOnce(t.Context(), submissionFixture("new")); !errors.Is(err, idempotency.ErrCapacity) {
		t.Fatal(err)
	}
	if replay, err := store.CreateOnce(t.Context(), request); err != nil || !replay.Replayed || replay.Receipt != first.Receipt {
		t.Fatal(replay, err)
	}
	if len(store.items) != 1 {
		t.Fatal("capacity wrote a comment")
	}
}

func TestCommentSubmissionValidationAndCancellation(t *testing.T) {
	store, _ := NewFileStore("", false)
	request := submissionFixture("invalid")
	bad := request
	bad.Key = "bad"
	if _, err := store.CreateOnce(t.Context(), bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	bad = request
	bad.Input.ParentID = 99
	if _, err := store.CreateOnce(t.Context(), bad); !errors.Is(err, ErrInvalidParent) {
		t.Fatal(err)
	}
	bad = request
	bad.Input.ContentSource = ""
	if _, err := store.CreateOnce(t.Context(), bad); FieldErrors(err)["content_source"] == "" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.CreateOnce(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.FindSubmission(ctx, request.Key, request.Fingerprint); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(store.items) != 0 || len(store.submissions) != 0 {
		t.Fatal("invalid create persisted")
	}
}

func TestCommentSubmissionDocumentValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateOnce(t.Context(), submissionFixture("stored")); err != nil {
		t.Fatal(err)
	}
	original, err := loadFileDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*fileDocument){
		"legacy-with-receipt": func(d *fileDocument) { d.Schema = 2 },
		"duplicate":           func(d *fileDocument) { d.Submissions = append(d.Submissions, d.Submissions[0]) },
		"invalid-digest":      func(d *fileDocument) { d.Submissions[0].Key = "invalid" },
		"wrong-post":          func(d *fileDocument) { d.Submissions[0].PostID++ },
		"future-comment":      func(d *fileDocument) { d.Submissions[0].CommentID = d.NextID },
		"wrong-time":          func(d *fileDocument) { d.Submissions[0].CreatedAt = d.UpdatedAt.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			doc := original
			doc.Submissions = append([]SubmissionReceipt(nil), original.Submissions...)
			mutate(&doc)
			payload, _ := json.Marshal(doc)
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path, false); err == nil {
				t.Fatal("corrupt receipt accepted")
			}
		})
	}
	for _, schema := range []int{1, 2} {
		doc := original
		doc.Schema = schema
		doc.Submissions = nil
		payload, _ := json.Marshal(doc)
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path, false); err == nil {
			t.Fatal("retired schema accepted", schema)
		}
	}
}
