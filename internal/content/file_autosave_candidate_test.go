package content

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func autosaveCandidateFile(t *testing.T) *FileRepository {
	t.Helper()
	state := contentHistoryLoadingFixture(t, 3, 7, 3)
	path := filepath.Join(t.TempDir(), "content.json")
	if _, err := writeContentState(t.Context(), path, state); err != nil {
		t.Fatal(err)
	}
	file, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	file.state.now = state.now
	return file
}

func TestAutosaveCandidateRetainedStates(t *testing.T) {
	file := autosaveCandidateFile(t)
	var retained []*MemoryRepository
	var retainedBytes [][]byte
	remember := func() {
		retained = append(retained, file.state)
		retainedBytes = append(retainedBytes, contentCandidateBytes(t, file.state))
	}
	check := func() {
		t.Helper()
		for i, state := range retained {
			if !bytes.Equal(retainedBytes[i], contentCandidateBytes(t, state)) {
				t.Fatalf("mutation changed retained state %d", i)
			}
		}
		loaded, err := NewFileRepository(file.path, false)
		if err != nil {
			t.Fatal(err)
		}
		loaded.state.now = file.state.now
		if file.searchState.Load() != file.state || !bytes.Equal(contentCandidateBytes(t, file.state), contentCandidateBytes(t, loaded.state)) {
			t.Fatal("published state differs from durable content")
		}
	}
	for i := range 5 {
		remember()
		post, err := file.GetByID(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		post.Meta["nested"].([]any)[0] = "saved draft"
		post.Title = ""
		saved, err := file.SaveAutosave(t.Context(), Autosave{PostID: 1, OwnerKey: "user:1", SourceVersion: post.Version,
			Snapshot: post, ExpiresAt: file.state.now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		post.Meta["nested"].([]any)[0] = "caller input"
		saved.Snapshot.Meta["nested"].([]any)[0] = "caller output"
		got, err := file.GetAutosave(t.Context(), 1, "user:1")
		if err != nil || got.Snapshot.Meta["nested"].([]any)[0] != "saved draft" {
			t.Fatal("autosave input or output escaped ownership", err)
		}
		got.Snapshot.Categories[0].Name = "caller category"
		got.Snapshot.Meta["nested"].([]any)[0] = "caller read"
		if i%2 == 0 {
			remember()
			if err := file.DeleteAutosave(t.Context(), 2, "user:2"); err != nil {
				t.Fatal(err)
			}
		}
		check()
	}
	remember()
	if _, err := file.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: 7, Actor: "editor"}, StatusPrivate); err != nil {
		t.Fatal(err)
	}
	check()
	remember()
	if _, err := file.RestoreRevision(t.Context(), 1, retained[0].revisions[1][0].ID, Mutation{ExpectedVersion: 8, Actor: "editor"}); err != nil {
		t.Fatal(err)
	}
	check()
	remember()
	term, err := file.GetTermByID(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.UpdateTerm(t.Context(), 1, TermMutation{ExpectedVersion: term.Version}, TermInput{Taxonomy: term.Taxonomy, Slug: term.Slug, Name: "更新分类"}); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestAutosaveCandidateExpiryMapsAreIndependent(t *testing.T) {
	file := autosaveCandidateFile(t)
	before := file.state
	input := before.autosaves[1]["user:1"]
	if _, err := file.SaveAutosave(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	after := file.state
	before.now = func() time.Time { return input.ExpiresAt }
	for _, id := range []int64{1, 2, 3} {
		if _, err := before.GetAutosave(t.Context(), id, "user:1"); !errors.Is(err, ErrAutosaveNotFound) {
			t.Fatal("expired retained autosave survived", err)
		}
		if len(after.autosaves[id]) != 3 {
			t.Fatal("old getter removed a current owner", id)
		}
	}
	after.now = before.now
	for _, id := range []int64{1, 2, 3} {
		if _, err := after.GetAutosave(t.Context(), id, "user:2"); !errors.Is(err, ErrAutosaveNotFound) {
			t.Fatal(err)
		}
		if _, ok := before.autosaves[id]["user:2"]; !ok {
			t.Fatal("current getter removed a retained owner", id)
		}
	}
}

func TestAutosaveCandidateKeepsSnippetReceipts(t *testing.T) {
	file, request, updates := snippetReceiptCloneFixture(t)
	retained := file.state
	original := contentCandidateBytes(t, retained)
	post, err := file.GetByID(t.Context(), updates[0].Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	input := Autosave{PostID: post.ID, OwnerKey: "user:7", SourceVersion: post.Version, Snapshot: post, ExpiresAt: file.state.now().Add(time.Hour)}
	if _, err := file.SaveAutosave(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if err := file.DeleteAutosave(t.Context(), post.ID, input.OwnerKey); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, contentCandidateBytes(t, file.state)) || !bytes.Equal(original, contentCandidateBytes(t, retained)) {
		t.Fatal("autosave changed snippets, receipts or unrelated state")
	}
	result, err := file.ApplySnippetUpdate(t.Context(), request, snippetManager())
	if err != nil || !result.Replayed {
		t.Fatal("old receipt stopped replaying", err)
	}
	result.Receipt.Items[0].Version++
	if _, err := file.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, contentCandidateBytes(t, retained)) {
		t.Fatal("subsequent snippet update changed retained data")
	}
}

type autosaveCandidateCancelEncoding struct{ cancel context.CancelFunc }

func (value autosaveCandidateCancelEncoding) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`{"value":9007199254740993}`), nil
}

func TestAutosaveCandidateFailuresKeepState(t *testing.T) {
	for _, failure := range []string{"cancelled", "encoding-cancels", "metadata", "version", "author", "missing", "expiry", "replace", "external-change"} {
		t.Run(failure, func(t *testing.T) {
			file := autosaveCandidateFile(t)
			before, digest := file.state, file.digest
			original := contentCandidateBytes(t, before)
			diskBefore := bytes.Clone(original)
			input := cloneAutosave(before.autosaves[1]["user:1"])
			input.Snapshot.Meta["nested"].([]any)[0] = "rejected draft"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "cancelled":
				cancel()
			case "encoding-cancels":
				input.Snapshot.Meta["cancel"] = autosaveCandidateCancelEncoding{cancel}
			case "metadata":
				input.Snapshot.Meta["invalid"] = make(chan int)
			case "version":
				input.SourceVersion--
			case "author":
				input.Snapshot.AuthorID++
			case "missing":
				input.PostID, input.Snapshot.ID = 99, 99
			case "expiry":
				input.ExpiresAt = before.now()
			case "replace":
				if err := os.Mkdir(file.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			case "external-change":
				diskBefore = append(diskBefore, ' ')
				if err := os.WriteFile(file.path, diskBefore, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := file.SaveAutosave(ctx, input); err == nil {
				t.Fatal("failed save was accepted")
			}
			if file.state != before || file.searchState.Load() != before || file.digest != digest || !bytes.Equal(original, contentCandidateBytes(t, before)) {
				t.Fatal("failed save mutated or published candidate")
			}
			disk, err := os.ReadFile(file.path)
			if err != nil || !bytes.Equal(disk, diskBefore) {
				t.Fatal("failed save changed file", err)
			}
		})
	}
}

func TestAutosaveCandidateConcurrentReads(t *testing.T) {
	file := autosaveCandidateFile(t)
	retained := file.state
	original := contentCandidateBytes(t, retained)
	var group sync.WaitGroup
	for worker := range 4 {
		group.Go(func() {
			for range 12 {
				if worker == 0 {
					input := cloneAutosave(retained.autosaves[1]["user:1"])
					if _, err := file.SaveAutosave(t.Context(), input); err != nil {
						t.Error(err)
					}
				} else {
					post, err := file.GetByID(t.Context(), 1)
					if err != nil || post.Version != 7 {
						t.Error("canonical post changed", err)
					}
					post.Meta["nested"].([]any)[0] = "caller"
					got, err := file.GetAutosave(t.Context(), 2, "user:2")
					if err != nil || !reflect.DeepEqual(got.Snapshot.Meta, retained.autosaves[2]["user:2"].Snapshot.Meta) {
						t.Error("unrelated recovery changed", err)
					}
					got.Snapshot.Meta["nested"].([]any)[0] = "caller"
					if _, err := file.SearchPublished(t.Context(), PublishedQuery{Limit: 2}); err != nil {
						t.Error(err)
					}
				}
			}
		})
	}
	group.Wait()
	if !bytes.Equal(original, contentCandidateBytes(t, retained)) {
		t.Fatal("concurrent saves or reads changed retained content")
	}
}
