package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestBackupRestoresSnippetReferencesAndReceipts(t *testing.T) {
	f := fixtureSite(t, false)
	source := filepath.Join(f.cfg.DataDir, "content.json")
	r, err := content.NewFileRepository(source, false)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:backup", Actor: "backup", Read: true, Manage: true}
	s, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "history", ContentSource: "<p>first</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "reference", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(t.Context(), s.ID, content.Mutation{ExpectedVersion: 1}, content.SnippetInput{Title: "history", ContentSource: "<p>second</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("e", 64)}
	plan, err := r.PreviewSnippetUpdate(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(t.Context(), u, a); err != nil {
		t.Fatal(err)
	}
	if _, err = r.WithdrawSnippet(t.Context(), s.ID, content.Mutation{ExpectedVersion: s.Version}, a); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "snippets.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-snippets"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	path := filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json")
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("content changed during backup restoration", err)
	}
	r, err = content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.ApplySnippetUpdate(t.Context(), u, a)
	if err != nil || !replay.Replayed || replay.Receipt.Items[0].Version != p.Version+1 {
		t.Fatal(replay, err)
	}
	resolved, err := r.ResolveSnippets(t.Context(), []contenthtml.SnippetKey{{ID: s.ID, Version: 1}, {ID: s.ID, Version: 2}})
	if err != nil || len(resolved) != 0 {
		t.Fatal("withdrawn reference revived", resolved, err)
	}
	item, err := r.GetSnippet(t.Context(), s.ID, a)
	if err != nil || len(item.Revisions) != 2 {
		t.Fatal(item, err)
	}
}
