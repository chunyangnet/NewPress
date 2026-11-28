package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetReceiptCloneFixture(t *testing.T) (*FileRepository, SnippetUpdate, []SnippetUpdate) {
	t.Helper()
	r, old := snippetConfirmationFixture(t)
	if result, err := r.ApplySnippetUpdate(t.Context(), old, snippetManager()); err != nil || result.Replayed {
		t.Fatal(result, err)
	}
	var updates []SnippetUpdate
	for i := range 3 {
		p, err := r.Create(t.Context(), PostInput{Title: fmt.Sprintf("next %d", i), Slug: fmt.Sprintf("next-%d", i), Status: StatusPublish,
			AuthorID: 7, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1}), Actor: "editor"})
		if err != nil {
			t.Fatal(err)
		}
		u := SnippetUpdate{SnippetID: 1, SnippetVersion: 2, Items: []SnippetSelection{{ID: p.ID, Version: p.Version}},
			RequestKey: strings.Repeat(fmt.Sprint(i+1), 64)}
		plan, err := r.PreviewSnippetUpdate(t.Context(), u, snippetManager())
		if err != nil {
			t.Fatal(err)
		}
		u.Digest = plan.Digest
		updates = append(updates, u)
	}
	return r, old, updates
}

func snippetReceiptCloneBytes(t *testing.T, state *MemoryRepository) []byte {
	t.Helper()
	raw, err := encodeContentDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSnippetReceiptCloneCandidateIsolation(t *testing.T) {
	r, old, updates := snippetReceiptCloneFixture(t)
	before := snippetReceiptCloneBytes(t, r.state)
	left, right := cloneContentState(r.state), cloneContentState(r.state)
	for i, candidate := range []*MemoryRepository{left, right} {
		result, err := candidate.ApplySnippetUpdate(t.Context(), updates[i], snippetManager())
		if err != nil || result.Replayed {
			t.Fatal(result, err)
		}
		result.Receipt.Items[0].Version = 999
		result.Receipt.Items = append(result.Receipt.Items, SnippetSelection{ID: 999, Version: 999})
		replay, err := candidate.ApplySnippetUpdate(t.Context(), updates[i], snippetManager())
		if err != nil || !replay.Replayed || len(replay.Receipt.Items) != 1 || replay.Receipt.Items[0].Version != 2 {
			t.Fatal("new result retained a mutable alias", replay, err)
		}
		oldReplay, err := candidate.ApplySnippetUpdate(t.Context(), old, snippetManager())
		if err != nil || !oldReplay.Replayed {
			t.Fatal(oldReplay, err)
		}
		oldReplay.Receipt.Items[0].ID = 999
		if len(candidate.snippetReceipts) != 2 {
			t.Fatal("candidate receipt append was lost")
		}
	}
	// Candidate-local metadata changes must not leak through the outer list.
	left.snippetReceipts[0].Identity = "candidate-only"
	if right.snippetReceipts[0].Identity != snippetManager().Identity || !bytes.Equal(before, snippetReceiptCloneBytes(t, r.state)) {
		t.Fatal("candidate or returned receipt changed the source/sibling snapshot")
	}
	if left.posts[1].Version != 2 || left.posts[2].Version != 1 || right.posts[1].Version != 1 || right.posts[2].Version != 2 {
		t.Fatal("candidate article changes leaked between clones")
	}
}

func TestSnippetReceiptCloneCommitBoundary(t *testing.T) {
	for _, outcome := range []string{"commit", "cancel", "replace", "callback-error"} {
		t.Run(outcome, func(t *testing.T) {
			r, old, updates := snippetReceiptCloneFixture(t)
			original, digest := r.state, r.digest
			before := snippetReceiptCloneBytes(t, original)
			if outcome == "replace" {
				if err := os.Mkdir(r.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prepared, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			done := make(chan error, 1)
			go func() {
				_, err := fileContentChange(ctx, r, func(candidate *MemoryRepository) (SnippetResult, error) {
					result, err := candidate.ApplySnippetUpdate(ctx, updates[0], snippetManager())
					if err != nil {
						close(prepared)
						return result, err
					}
					result.Receipt.Items[0].Version = 999
					close(prepared)
					<-release
					if outcome == "callback-error" {
						return SnippetResult{}, errors.New("injected candidate rejection")
					}
					return result, nil
				})
				done <- err
			}()
			select {
			case <-prepared:
			case <-time.After(5 * time.Second):
				finish()
				<-done
				t.Fatal("candidate did not reach commit boundary")
			}
			// Read and mutate detached replays from the retained old snapshot
			// while the file writer holds a prepared candidate.
			for range 8 {
				replayed, err := original.ApplySnippetUpdate(t.Context(), old, snippetManager())
				if err != nil || !replayed.Replayed {
					finish()
					<-done
					t.Fatal(replayed, err)
				}
				replayed.Receipt.Items[0].Version++
			}
			if !bytes.Equal(before, snippetReceiptCloneBytes(t, original)) || r.searchState.Load() != original {
				finish()
				<-done
				t.Fatal("prepared candidate changed old published state")
			}
			if outcome == "cancel" {
				cancel()
			}
			finish()
			err := <-done
			if (err == nil) != (outcome == "commit") || outcome == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("unexpected commit result", err)
			}
			if !bytes.Equal(before, snippetReceiptCloneBytes(t, original)) {
				t.Fatal("commit or failure changed retained snapshot")
			}
			if outcome == "commit" {
				if r.state == original || len(r.state.snippetReceipts) != 2 || r.state.snippetReceipts[1].Items[0].Version != 2 || r.searchState.Load() != r.state {
					t.Fatal("new receipt was not isolated and published")
				}
			} else if r.state != original || r.digest != digest || r.searchState.Load() != original {
				t.Fatal("failed candidate published")
			}
			reopened, err := NewFileRepository(r.path, false)
			if err != nil || !bytes.Equal(snippetReceiptCloneBytes(t, r.state), snippetReceiptCloneBytes(t, reopened.state)) {
				t.Fatal("committed state differs after reopen", err)
			}
		})
	}
}

func TestSnippetReceiptCloneConcurrentSnapshots(t *testing.T) {
	r, old, updates := snippetReceiptCloneFixture(t)
	original := r.state
	before := snippetReceiptCloneBytes(t, original)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 25 {
				result, err := original.ApplySnippetUpdate(t.Context(), old, snippetManager())
				if err != nil || !result.Replayed {
					t.Error(result, err)
					return
				}
				result.Receipt.Items[0].ID = 999
			}
		})
	}
	for _, u := range updates {
		group.Go(func() {
			result, err := r.ApplySnippetUpdate(t.Context(), u, snippetManager())
			if err != nil || result.Replayed {
				t.Error(result, err)
				return
			}
			result.Receipt.Items[0].Version = 999
		})
	}
	group.Wait()
	if !bytes.Equal(before, snippetReceiptCloneBytes(t, original)) || len(r.state.snippetReceipts) != 4 {
		t.Fatal("concurrent writes changed old history or lost receipts")
	}
	for _, u := range updates {
		result, err := r.ApplySnippetUpdate(t.Context(), u, snippetManager())
		if err != nil || !result.Replayed || result.Receipt.Items[0].Version != 2 {
			t.Fatal("concurrent result changed stored receipt", result, err)
		}
	}
	reopened, err := NewFileRepository(r.path, false)
	if err != nil || !reflect.DeepEqual(reopened.state.snippetReceipts, r.state.snippetReceipts) {
		t.Fatal("concurrent receipts did not survive reopen", err)
	}
}

func TestSnippetReceiptCloneEncodingPreserved(t *testing.T) {
	r, _, _ := snippetReceiptCloneFixture(t)
	before, err := json.Marshal(r.state.snippetReceipts)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cloneContentState(r.state)
	after, err := json.Marshal(candidate.snippetReceipts)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cloned receipt encoding changed", err)
	}
}
