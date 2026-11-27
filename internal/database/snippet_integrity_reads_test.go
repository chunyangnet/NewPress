package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestPostgreSQLSnippetPinnedReadIntegrity(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:integrity", Actor: "integrity", Read: true, Manage: true}
	for _, fault := range []string{"metadata title", "metadata chronology", "body chronology", "body title", "body whitespace", "missing sharing boundary", "missing current body", "invalid old unrelated body"} {
		t.Run(fault, func(t *testing.T) {
			s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "first", ContentSource: "<p>first</p>", Visibility: content.SnippetShared}, a)
			if err != nil {
				t.Fatal(err)
			}
			for i := 2; i <= 3; i++ {
				s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: fmt.Sprint("body ", i), ContentSource: fmt.Sprintf("<p>pinned body %d</p>", i), Visibility: content.SnippetShared}, a)
				if err != nil {
					t.Fatal(err)
				}
			}
			key := contenthtml.SnippetKey{ID: s.ID, Version: 3}
			if fault == "missing current body" {
				key.Version = 2
			}
			p, err := r.Create(ctx, content.PostInput{Title: "source", Slug: fmt.Sprint("integrity-source-", s.ID), ContentSource: contenthtml.SnippetShell(key)})
			if err != nil {
				t.Fatal(err)
			}
			u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
			plan, err := r.PreviewSnippetDraft(ctx, u, a)
			if err != nil {
				t.Fatal(err)
			}
			u.Digest, u.RequestKey = plan.Digest, fmt.Sprintf("%064x", s.ID)
			request, err := content.NewSnippetDraftSubmission(u, a)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if fault == "metadata title" || fault == "metadata chronology" {
				if fault == "metadata title" {
					s.Title = ""
				} else {
					s.UpdatedAt = s.CreatedAt.Add(-time.Hour)
				}
				raw, _ = json.Marshal(s)
				_, err = pool.Exec(ctx, `UPDATE np_snippets SET data=$2 WHERE id=$1`, s.ID, raw)
			} else if fault == "missing sharing boundary" || fault == "missing current body" {
				version := int64(1)
				if fault == "missing current body" {
					version = 3
				}
				_, err = pool.Exec(ctx, `DELETE FROM np_snippet_revisions WHERE snippet_id=$1 AND version=$2`, s.ID, version)
			} else if fault == "invalid old unrelated body" {
				_, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=2`, s.ID, []byte(`{`))
			} else {
				if err = pool.QueryRow(ctx, `SELECT data FROM np_snippet_revisions WHERE snippet_id=$1 AND version=3`, s.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rev content.SnippetRevision
				if err = json.Unmarshal(raw, &rev); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "body chronology":
					rev.CreatedAt = s.CreatedAt.Add(-time.Hour)
				case "body title":
					rev.Title = "wrong current title"
				case "body whitespace":
					rev.ContentSource = " " + rev.ContentSource
				}
				raw, _ = json.Marshal(rev)
				_, err = pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=3`, s.ID, raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := snippetMaintenanceFacts(t, ctx, pool)
			resolved, resolveErr := r.ResolveSnippets(ctx, []contenthtml.SnippetKey{key})
			_, previewErr := r.PreviewSnippetDraft(ctx, content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}, a)
			if fault == "invalid old unrelated body" {
				if resolveErr != nil || previewErr != nil || resolved[key] == "" {
					t.Fatal("fast reads unexpectedly scan unrelated old bodies", resolveErr, previewErr)
				}
				if _, err = r.GetSnippet(ctx, s.ID, a); !errors.Is(err, content.ErrSnippetCorrupt) {
					t.Fatal("full history ignored damage", err)
				}
				return
			}
			if !errors.Is(resolveErr, content.ErrSnippetCorrupt) || len(resolved) != 0 {
				t.Error("public resolver accepted corrupt stored facts", resolveErr)
			}
			if !errors.Is(previewErr, content.ErrSnippetCorrupt) {
				t.Error("draft preview accepted corrupt stored facts", previewErr)
			}
			if _, err = r.Submit(ctx, request); !errors.Is(err, content.ErrSnippetCorrupt) {
				t.Error("confirmed draft accepted corrupt stored facts", err)
			}
			after := snippetMaintenanceFacts(t, ctx, pool)
			for table, value := range before {
				if after[table] != value {
					t.Error("rejected operation changed stored facts", table)
				}
			}
		})
	}
}
