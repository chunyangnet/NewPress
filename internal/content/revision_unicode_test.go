package content

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRevisionActorsRejectMalformedUTF8BeforeAnyMutation(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, actor := range []string{"private-\xff", "private-\xe4\xb8", "private-\xed\xa0\x80"} {
			for _, operation := range []string{"update", "status", "restore", "bulk", "submit save", "submit publish", "submit restore"} {
				t.Run(mode+"/"+operation+"/"+actor, func(t *testing.T) {
					r, state, reopen := unicodeWriteRepository(t, mode)
					input := PostInput{Title: "原文", Slug: "original", AuthorName: "原作者"}
					post, err := r.Create(t.Context(), input)
					if err != nil {
						t.Fatal(err)
					}
					revisions, _ := r.ListRevisions(t.Context(), post.ID, 10)
					recovery, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					before := state()
					mutation := Mutation{ExpectedVersion: post.Version, Actor: actor}
					request := Submission{Key: submissionDigest(operation), Fingerprint: submissionDigest("original-request"), PostID: post.ID, Version: post.Version, RevisionID: revisions[0].ID, Actor: actor, Input: input}
					switch operation {
					case "update":
						input.Slug, input.TagNames = "new-location", []string{"新标签"}
						_, err = r.Update(t.Context(), post.ID, mutation, input)
					case "status":
						_, err = r.ChangeStatus(t.Context(), post.ID, mutation, StatusPublish)
					case "restore":
						_, err = r.RestoreRevision(t.Context(), post.ID, revisions[0].ID, mutation)
					case "bulk":
						_, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Items: []BulkMutation{{ID: post.ID, ExpectedVersion: post.Version}}, Status: StatusPublish, Actor: actor})
					case "submit save":
						request.Action = "save"
						_, err = r.Submit(t.Context(), request)
					case "submit publish":
						request.Action = "publish"
						_, err = r.Submit(t.Context(), request)
					case "submit restore":
						request.Action = "restore_revision"
						_, err = r.Submit(t.Context(), request)
					}
					if FieldErrors(err)["actor"] == "" {
						t.Errorf("malformed revision actor accepted: %v", err)
					}
					if !bytes.Equal(before, state()) {
						t.Error("invalid actor changed content, revision, redirects, terms or receipt")
					}
					got, getErr := r.GetAutosave(t.Context(), post.ID, "user:1")
					if getErr != nil || !reflect.DeepEqual(got, recovery) {
						t.Error("invalid actor removed recovery")
					}
					loaded := reopen()
					gotPost, getErr := loaded.GetByID(t.Context(), post.ID)
					gotRevisions, revErr := loaded.ListRevisions(t.Context(), post.ID, 10)
					if getErr != nil || revErr != nil || gotPost.Version != post.Version || len(gotRevisions) != 1 {
						t.Error("invalid actor persisted a mutation")
					}
					if request.Action != "" {
						if _, findErr := loaded.FindSubmission(t.Context(), request.Key, request.Fingerprint); !errors.Is(findErr, ErrNotFound) {
							t.Errorf("invalid actor consumed submission key: %v", findErr)
						}
					}
				})
			}
		}
	}
}

func TestRevisionActorUnicodeAndFallbackRemainCompatible(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, actor := range []string{" 编辑者😀� ", ""} {
			t.Run(mode+"/"+actor, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				input := PostInput{Title: "原文", AuthorName: "作者😀", Actor: "输入操作者"}
				post, err := r.Create(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				post, err = r.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version, Actor: actor}, input)
				if err != nil {
					t.Fatal(err)
				}
				post, err = r.ChangeStatus(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version, Actor: actor}, StatusPublish)
				if err != nil {
					t.Fatal(err)
				}
				revisions, err := reopen().ListRevisions(t.Context(), post.ID, 10)
				if err != nil || len(revisions) != 3 {
					t.Fatal(revisions, err)
				}
				want := map[int64]string{1: "输入操作者", 2: "编辑者😀�", 3: "编辑者😀�"}
				if actor == "" {
					want[2], want[3] = "输入操作者", "作者😀"
				}
				for _, rev := range revisions {
					if rev.Actor != want[rev.Version] {
						t.Errorf("actor changed for version %d: %q", rev.Version, rev.Actor)
					}
				}
			})
		}
	}
}
