package mediausage

import (
	"errors"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestSnippetHistoricalMediaProtectedAndRedacted(t *testing.T) {
	s, item := newUsageTestService(t, content.NewMemoryRepository(false))
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	input := content.SnippetInput{Title: "私密片段标题", ContentSource: `<a href="` + item.PublicURL + `">附件</a>`, Visibility: content.SnippetPrivate}
	snippet, err := s.Content().SaveSnippet(t.Context(), 0, content.Mutation{}, input, a)
	if err != nil {
		t.Fatal(err)
	}
	input.ContentSource = "<p>no attachment</p>"
	snippet, err = s.Content().SaveSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, input, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Content().WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, a); err != nil {
		t.Fatal(err)
	}
	hidden, err := s.Report(t.Context(), item, Access{ContentRead: true}, 0)
	if err != nil || hidden.Hidden != 1 || len(hidden.Items) != 0 {
		t.Fatal(hidden, err)
	}
	visible, err := s.Report(t.Context(), item, Access{SnippetsManage: true}, 0)
	if err != nil || len(visible.Items) != 1 || visible.Items[0].Source != content.MediaSourceSnippet || visible.Items[0].Version != 1 {
		t.Fatal(visible, err)
	}
	if err = s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal("retained revision lost protection", err)
	}
}

func TestSnippetCannotReuseTrashedMedia(t *testing.T) {
	s, item := newUsageTestService(t, content.NewMemoryRepository(false))
	if err := s.Trash(t.Context(), item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:editor", Actor: "editor", Read: true, Manage: true}
	_, err := s.Content().SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "invalid", ContentSource: `<a href="` + item.PublicURL + `">file</a>`, Visibility: content.SnippetPrivate}, a)
	if len(content.FieldErrors(err)) == 0 {
		t.Fatal(err)
	}
}
