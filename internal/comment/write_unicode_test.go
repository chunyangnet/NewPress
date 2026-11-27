package comment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unicodeCommentFixture(t *testing.T, mode string) (*FileStore, string, Comment) {
	t.Helper()
	filename := ""
	if mode == "file" {
		filename = filepath.Join(t.TempDir(), "comments.json")
	}
	store, err := NewFileStore(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(t.Context(), CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "原文", Status: StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Create(t.Context(), CreateInput{PostID: 1, ParentID: root.ID, AuthorName: "回复者", AuthorEmail: "reply@example.test", ContentSource: "保留的回复", Status: StatusApproved}); err != nil {
		t.Fatal(err)
	}
	root, err = store.Edit(t.Context(), root.ID, Mutation{ExpectedVersion: root.Version, Actor: "editor"}, EditInput{AuthorName: root.AuthorName, AuthorEmail: root.AuthorEmail, ContentSource: "第二版"})
	if err != nil {
		t.Fatal(err)
	}
	return store, filename, root
}

func commentWriteState(t *testing.T, store *FileStore) []byte {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	payload, err := json.Marshal(map[string]any{
		"items": store.items, "revisions": store.revisions, "next_id": store.nextID,
		"updated_at": store.updatedAt, "activities": store.activities, "submissions": store.submissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertCommentWriteRejected(t *testing.T, mode, field string, attempt func(*FileStore, Comment) error) {
	t.Helper()
	store, filename, root := unicodeCommentFixture(t, mode)
	before := commentWriteState(t, store)
	var disk []byte
	var err error
	if filename != "" {
		disk, err = os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := attempt(store, root); FieldErrors(err)[field] == "" {
		t.Errorf("invalid input lacks %s validation: %v", field, err)
	}
	if !bytes.Equal(before, commentWriteState(t, store)) {
		t.Error("invalid write changed comments, revisions, counters, activities or receipts")
	}
	if filename != "" {
		after, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(disk, after) {
			t.Errorf("invalid write changed the durable document: %v", err)
		}
		reloaded, err := NewFileStore(filename, false)
		if err != nil {
			t.Fatalf("invalid write blocked reopening: %v", err)
		}
		got, err := reloaded.Get(t.Context(), root.ID)
		if err != nil || got != root {
			t.Errorf("reopen changed the original comment: %#v, %v", got, err)
		}
	}
}

func TestCommentMutationsRejectInvalidActorWithoutChanges(t *testing.T) {
	for _, mode := range []string{"volatile", "file"} {
		for name, actor := range map[string]string{
			"bad_byte": "editor\xff", "truncated": "editor\xe4\xb8", "NUL": "editor\x00", "DEL": "editor\x7f", "too_long": strings.Repeat("字", 161),
		} {
			for _, operation := range []string{"edit", "restore", "reply", "status", "bulk", "delete"} {
				t.Run(mode+"/"+operation+"/"+name, func(t *testing.T) {
					assertCommentWriteRejected(t, mode, "actor", func(store *FileStore, root Comment) error {
						mutation := Mutation{ExpectedVersion: root.Version, Actor: actor}
						var err error
						switch operation {
						case "edit":
							_, err = store.Edit(t.Context(), root.ID, mutation, EditInput{AuthorName: root.AuthorName, AuthorEmail: root.AuthorEmail, ContentSource: "不应保存"})
						case "restore":
							_, err = store.RestoreRevision(t.Context(), root.ID, 1, mutation)
						case "reply":
							_, err = store.Reply(t.Context(), ReplyInput{ParentID: root.ID, ExpectedParentVersion: root.Version, AuthorName: "编辑", AuthorEmail: "editor@example.test", ContentSource: "不应保存", Actor: actor})
						case "status":
							_, err = store.ChangeStatus(t.Context(), root.ID, mutation, StatusSpam)
						case "bulk":
							_, err = store.BulkChangeStatus(t.Context(), BulkStatusChange{Status: StatusSpam, Actor: actor, Items: []BulkMutation{{ID: root.ID, ExpectedVersion: root.Version}, {ID: root.ID + 1, ExpectedVersion: 1}}})
						case "delete":
							err = store.Delete(t.Context(), root.ID, mutation)
						}
						return err
					})
				})
			}
		}
	}
}

func TestCommentWritesRejectMalformedEmailAndUserAgent(t *testing.T) {
	for _, mode := range []string{"volatile", "file"} {
		for _, operation := range []string{"create email", "edit email", "reply email", "create user agent"} {
			for name, malformed := range map[string]string{"bad_byte": "\xff", "truncated": "\xe4\xb8", "surrogate": "\xed\xa0\x80"} {
				t.Run(mode+"/"+operation+"/"+name, func(t *testing.T) {
					field := "author_email"
					if operation == "create user agent" {
						field = "user_agent"
					}
					assertCommentWriteRejected(t, mode, field, func(store *FileStore, root Comment) error {
						email := "bad" + malformed + "@example.test"
						var err error
						switch operation {
						case "create email":
							_, err = store.Create(t.Context(), CreateInput{PostID: root.PostID, AuthorName: "读者", AuthorEmail: email, ContentSource: "不应保存"})
						case "edit email":
							_, err = store.Edit(t.Context(), root.ID, Mutation{ExpectedVersion: root.Version, Actor: "editor"}, EditInput{AuthorName: root.AuthorName, AuthorEmail: email, ContentSource: "不应保存"})
						case "reply email":
							_, err = store.Reply(t.Context(), ReplyInput{ParentID: root.ID, ExpectedParentVersion: root.Version, AuthorName: "编辑", AuthorEmail: email, ContentSource: "不应保存", Actor: "editor"})
						case "create user agent":
							_, err = store.Create(t.Context(), CreateInput{PostID: root.PostID, AuthorName: "读者", AuthorEmail: root.AuthorEmail, ContentSource: "不应保存", UserAgent: "browser" + malformed})
						}
						return err
					})
				})
			}
		}
	}
}

func TestCommentActorCompatibilityAndReopen(t *testing.T) {
	for _, mode := range []string{"volatile", "file"} {
		for name, actor := range map[string]string{"unicode": "编辑 中文 🌐 �", "maximum": strings.Repeat("字", 160), "optional empty": ""} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				store, filename, root := unicodeCommentFixture(t, mode)
				inputActor := " \u3000" + actor + "\t"
				item, err := store.ChangeStatus(t.Context(), root.ID, Mutation{ExpectedVersion: root.Version, Actor: inputActor}, StatusPending)
				if err != nil || item.ModeratedBy != actor {
					t.Fatalf("valid moderation actor: %#v, %v", item, err)
				}
				updated, err := store.BulkChangeStatus(t.Context(), BulkStatusChange{Status: StatusApproved, Actor: inputActor, Items: []BulkMutation{{ID: item.ID, ExpectedVersion: item.Version}}})
				if err != nil || len(updated) != 1 || updated[0].ModeratedBy != actor {
					t.Fatalf("valid bulk actor: %#v, %v", updated, err)
				}
				item = updated[0]
				if actor != "" {
					item, err = store.Edit(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version, Actor: inputActor}, EditInput{AuthorName: item.AuthorName, AuthorEmail: "读者�@example.test", ContentSource: "编辑后的 中文 🌐 �"})
					if err != nil || item.EditedBy != actor {
						t.Fatalf("valid edit actor: %#v, %v", item, err)
					}
					item, err = store.RestoreRevision(t.Context(), item.ID, 1, Mutation{ExpectedVersion: item.Version, Actor: inputActor})
					if err != nil || item.EditedBy != actor {
						t.Fatalf("valid restore actor: %#v, %v", item, err)
					}
					reply, err := store.Reply(t.Context(), ReplyInput{ParentID: item.ID, ExpectedParentVersion: item.Version, AuthorName: "编辑", AuthorEmail: "编辑�@example.test", ContentSource: "回复 中文 🌐 �", Actor: inputActor})
					if err != nil || reply.CreatedBy != actor {
						t.Fatalf("valid reply actor: %#v, %v", reply, err)
					}
					if _, err := store.Create(t.Context(), CreateInput{PostID: root.PostID, AuthorName: "读者", AuthorEmail: "读者�@example.test", ContentSource: "正文", UserAgent: "浏览器 🌐 �"}); err != nil {
						t.Fatalf("valid Unicode metadata: %v", err)
					}
				}
				if filename != "" {
					store, err = NewFileStore(filename, false)
					if err != nil {
						t.Fatal(err)
					}
					loaded, err := store.Get(t.Context(), item.ID)
					if err != nil || loaded != item {
						t.Errorf("valid metadata changed after reopen: %v", err)
					}
				}
				if err = store.Delete(t.Context(), item.ID, Mutation{ExpectedVersion: item.Version, Actor: inputActor}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
