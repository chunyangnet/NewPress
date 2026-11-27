package backup

import (
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestBackupRestoresSnippetDraftAndCreationReceipt(t *testing.T) {
	f := fixtureSite(t, false)
	r, err := content.NewFileRepository(filepath.Join(f.cfg.DataDir, "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:backup", Actor: "backup", Read: true, Manage: true}
	s, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>retained copy</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "source", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("e", 64)
	request, _ := content.NewSnippetDraftSubmission(u, a)
	created, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(t.Context(), s.ID, content.Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "snippet-draft.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-snippet-draft"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	r, err = content.NewFileRepository(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Submit(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Receipt.PostID != created.Receipt.PostID {
		t.Fatal(replay, err)
	}
	draft, err := r.GetByID(t.Context(), created.Receipt.PostID)
	if err != nil || !strings.Contains(draft.ContentHTML, "retained copy") || len(contenthtml.SnippetReferences(draft.ContentSource)) != 0 {
		t.Fatal(draft, err)
	}
}
