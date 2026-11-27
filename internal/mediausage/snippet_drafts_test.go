package mediausage

import (
	"errors"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetDraftMediaGuardAndReplay(t *testing.T) {
	raw := content.NewMemoryRepository(false)
	s, item := newUsageTestService(t, raw)
	r := s.Content()
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	snippet, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "media", ContentSource: `<img src="` + item.PublicURL + `">`, Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "article", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: 1}
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("a", 64)
	request, _ := content.NewSnippetDraftSubmission(u, a)
	v, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal("copy media not protected", err)
	}
	if _, err = s.library.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	// Replay is safe after media disappearance, but a new copy is rejected.
	if replay, err := r.Submit(t.Context(), request); err != nil || !replay.Replayed || replay.Receipt.PostID != v.Receipt.PostID {
		t.Fatal(replay, err)
	}
	u.RequestKey = strings.Repeat("b", 64)
	fresh, _ := content.NewSnippetDraftSubmission(u, a)
	if _, err = r.Submit(t.Context(), fresh); len(content.FieldErrors(err)) == 0 {
		t.Fatal("trashed media accepted", err)
	}
	request.SnippetDraft.Access.Read = false
	if _, err = r.Submit(t.Context(), request); !errors.Is(err, content.ErrSnippetPermission) {
		t.Fatal("wrapper bypassed replay permissions", err)
	}
}
