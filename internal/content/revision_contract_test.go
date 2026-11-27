package content

import (
	"reflect"
	"testing"
)

func TestRestoreRevisionRequiresTypedCategorySnapshot(t *testing.T) {
	repository := NewMemoryRepository(false)
	post, err := repository.Create(t.Context(), PostInput{Title: "完整修订", Slug: "revision-contract", Category: "保留分类"})
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatal(revisions, err)
	}
	stored := repository.revisions[post.ID]
	stored[0].Snapshot.Categories = nil
	repository.revisions[post.ID] = stored
	terms, err := repository.ListTerms(t.Context(), TermQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.RestoreRevision(t.Context(), post.ID, revisions[0].ID, Mutation{ExpectedVersion: post.Version}); FieldErrors(err)["category_ids"] == "" {
		t.Fatal("incomplete category snapshot accepted", err)
	}
	after, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(post, after) {
		t.Fatal("rejected restore changed content", after, err)
	}
	afterTerms, err := repository.ListTerms(t.Context(), TermQuery{Limit: 100})
	if err != nil || !reflect.DeepEqual(terms, afterTerms) {
		t.Fatal("rejected restore changed taxonomy", afterTerms, err)
	}
}
