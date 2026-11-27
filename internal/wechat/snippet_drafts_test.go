package wechat

import (
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetDraftUsesOrdinaryWeChatPreparation(t *testing.T) {
	r := content.NewMemoryRepository(false)
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	s, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>copied text</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "source", ContentSource: "<p>ordinary</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("a", 64)
	request, _ := content.NewSnippetDraftSubmission(u, a)
	created, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := r.GetByID(t.Context(), created.Receipt.PostID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := buildSyncPlan(Config{}, draft, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range prepared.issues {
		if strings.Contains(issue.Message, "共享片段") {
			t.Fatal("ordinary draft blocked", issue)
		}
	}
	if !strings.Contains(draft.ContentHTML, "copied text") {
		t.Fatal("missing copied body")
	}
}
