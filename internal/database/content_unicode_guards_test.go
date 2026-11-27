package database

import (
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgresContentEncodingRejectedBeforeStorage(t *testing.T) {
	mutation := content.Mutation{ExpectedVersion: 1, Actor: "private-\xff"}
	input := content.PostInput{Title: "有效内容"}
	for _, operation := range []string{"update", "status", "restore", "bulk", "autosave", "update transaction", "status transaction", "restore transaction"} {
		t.Run(operation, func(t *testing.T) {
			// An invalid request must return before accessing the absent pool or
			// transaction. This tests the real repository guard without a server.
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("invalid encoding reached storage: %v", recovered)
				}
			}()
			r := &PostRepository{}
			var err error
			field := "actor"
			switch operation {
			case "update":
				_, err = r.Update(t.Context(), 1, mutation, input)
			case "status":
				_, err = r.ChangeStatus(t.Context(), 1, mutation, content.StatusPublish)
			case "restore":
				_, err = r.RestoreRevision(t.Context(), 1, 1, mutation)
			case "bulk":
				_, err = r.BulkChangeStatus(t.Context(), content.BulkStatusChange{Actor: mutation.Actor, Status: content.StatusPublish, Items: []content.BulkMutation{{ID: 1, ExpectedVersion: 1}}})
			case "autosave":
				field = "snapshot"
				_, err = r.SaveAutosave(t.Context(), content.Autosave{PostID: 1, SourceVersion: 1, OwnerKey: "user:1", Snapshot: content.Post{ID: 1, Version: 1, Title: "private-\xff"}, ExpiresAt: time.Now().Add(time.Hour)})
			case "update transaction":
				_, err = updatePostInTransaction(t.Context(), nil, 1, mutation, input, time.Now())
			case "status transaction":
				_, err = changePostStatusInTransaction(t.Context(), nil, 1, mutation, content.StatusPublish, time.Now())
			case "restore transaction":
				_, err = restorePostRevisionInTransaction(t.Context(), nil, 1, 1, mutation, time.Now())
			}
			if content.FieldErrors(err)[field] == "" {
				t.Errorf("missing %s validation error: %v", field, err)
			}
		})
	}
}
