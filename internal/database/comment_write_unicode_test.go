package database

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/comment"
)

func TestCommentMutationTextRejectedBeforeDatabase(t *testing.T) {
	for _, operation := range []string{"edit", "restore", "reply", "status", "bulk", "delete"} {
		for name, actor := range map[string]string{"bad_byte": "editor\xff", "NUL": "editor\x00", "too_long": strings.Repeat("字", 161)} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Errorf("invalid actor reached database access: %v", recovered)
					}
				}()
				repository := NewCommentRepository(&pgxpool.Pool{})
				mutation := comment.Mutation{ExpectedVersion: 1, Actor: actor}
				var err error
				switch operation {
				case "edit":
					_, err = repository.Edit(t.Context(), 1, mutation, comment.EditInput{AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "正文"})
				case "restore":
					_, err = repository.RestoreRevision(t.Context(), 1, 1, mutation)
				case "reply":
					_, err = repository.Reply(t.Context(), comment.ReplyInput{ParentID: 1, ExpectedParentVersion: 1, AuthorName: "编辑", AuthorEmail: "editor@example.test", ContentSource: "回复", Actor: actor})
				case "status":
					_, err = repository.ChangeStatus(t.Context(), 1, mutation, comment.StatusSpam)
				case "bulk":
					_, err = repository.BulkChangeStatus(t.Context(), comment.BulkStatusChange{Status: comment.StatusSpam, Actor: actor, Items: []comment.BulkMutation{{ID: 1, ExpectedVersion: 1}}})
				case "delete":
					err = repository.Delete(t.Context(), 1, mutation)
				}
				if comment.FieldErrors(err)["actor"] == "" {
					t.Errorf("missing actor validation: %v", err)
				}
			})
		}
	}
}

func TestCommentInputEncodingRejectedBeforeDatabase(t *testing.T) {
	for _, operation := range []string{"create email", "edit email", "reply email", "create user agent"} {
		t.Run(operation, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("invalid encoding reached database access: %v", recovered)
				}
			}()
			repository := NewCommentRepository(&pgxpool.Pool{})
			field := "author_email"
			var err error
			switch operation {
			case "create email":
				_, err = repository.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "bad\xff@example.test", ContentSource: "正文"})
			case "edit email":
				_, err = repository.Edit(t.Context(), 1, comment.Mutation{ExpectedVersion: 1, Actor: "editor"}, comment.EditInput{AuthorName: "读者", AuthorEmail: "bad\xff@example.test", ContentSource: "正文"})
			case "reply email":
				_, err = repository.Reply(t.Context(), comment.ReplyInput{ParentID: 1, ExpectedParentVersion: 1, AuthorName: "编辑", AuthorEmail: "bad\xff@example.test", ContentSource: "回复", Actor: "editor"})
			case "create user agent":
				field = "user_agent"
				_, err = repository.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "正文", UserAgent: "browser\xff"})
			}
			if comment.FieldErrors(err)[field] == "" {
				t.Errorf("missing %s validation: %v", field, err)
			}
		})
	}
}
