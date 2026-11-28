package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetReceiptBudgetFixture(t *testing.T, shape string, permitted int) (*MemoryRepository, []SnippetUpdate) {
	t.Helper()
	state := contentFileSnippetFixture(t, 2, 128, false)
	state.posts[0].ContentSource = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1})
	state.posts[0].ContentHTML = contenthtml.Sanitize(state.posts[0].ContentSource)
	for i := 1; i <= permitted; i++ {
		p := clonePost(state.posts[0])
		p.ID, p.Slug = int64(i+1), fmt.Sprintf("budget-article-%d", i+1)
		state.posts = append(state.posts, p)
	}
	state.nextID = int64(permitted + 2)
	stamp := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	var updates []SnippetUpdate
	reserved := 0
	for i, p := range state.posts {
		state.indexSnippetReferencesLocked(p)
		u := SnippetUpdate{SnippetID: 1, SnippetVersion: 2, Items: []SnippetSelection{{ID: p.ID, Version: p.Version}},
			RequestKey: strings.Repeat("f", 62) + fmt.Sprintf("%02x", p.ID)}
		plan, err := state.PreviewSnippetUpdate(t.Context(), u, snippetManager())
		if err != nil {
			t.Fatal(err)
		}
		u.Digest = plan.Digest
		raw, err := json.Marshal(NewSnippetReceipt(u, plan, snippetManager(), stamp))
		if err != nil {
			t.Fatal(err)
		}
		if i < permitted {
			reserved += len(raw)
		}
		updates = append(updates, u)
	}
	switch shape {
	case "bytes":
		state.snippetReceipts = snippetBudgetReceiptFixture(t, MaxSnippetReceiptBytes-reserved, stamp)
	case "count":
		for i := range MaxSnippetReceipts - permitted {
			state.snippetReceipts = append(state.snippetReceipts, SnippetReceipt{Identity: "x", RequestKey: fmt.Sprintf("%064x", i+1),
				Fingerprint: strings.Repeat("a", 64), SnippetID: 1, SnippetVersion: 2, TargetVersion: 2, CreatedAt: stamp,
				Items: []SnippetSelection{{ID: 1, Version: 2}}})
		}
	case "empty":
	default:
		t.Fatal("unknown receipt shape", shape)
	}
	return state, updates
}

func snippetReceiptBudgetStandardSize(t *testing.T, state *MemoryRepository) int {
	t.Helper()
	total := 0
	for _, r := range state.snippetReceipts {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		total += len(raw)
	}
	return total
}

func TestSnippetReceiptBudgetBoundaries(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, shape := range []string{"bytes", "count"} {
			t.Run(kind+"/"+shape, func(t *testing.T) {
				state, updates := snippetReceiptBudgetFixture(t, shape, 2)
				repository, file, current := snippetBudgetStore(t, kind, state)
				for _, u := range updates[:2] {
					result, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
					if err != nil || result.Replayed {
						t.Fatal("allowed receipt rejected", result, err)
					}
					result.Receipt.Items[0].Version = 999
				}
				if shape == "bytes" && snippetReceiptBudgetStandardSize(t, current()) != MaxSnippetReceiptBytes ||
					shape == "count" && len(current().snippetReceipts) != MaxSnippetReceipts {
					t.Fatal("last permitted update did not fill quota")
				}
				before := snippetReceiptCloneBytes(t, current())
				if _, err := repository.ApplySnippetUpdate(t.Context(), updates[2], snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
					t.Fatal("next receipt exceeded quota", err)
				}
				for _, u := range updates[:2] {
					result, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
					if err != nil || !result.Replayed || result.Receipt.Items[0].Version != 2 {
						t.Fatal("full quota prevented stable replay", result, err)
					}
					result.Receipt.Items[0].ID = 999
				}
				if !bytes.Equal(before, snippetReceiptCloneBytes(t, current())) {
					t.Fatal("rejection or replay changed state")
				}
				if file != nil {
					reopened, err := NewFileRepository(file.path, false)
					if err != nil || !bytes.Equal(before, snippetReceiptCloneBytes(t, reopened.state)) {
						t.Fatal("full quota changed on reopen", err)
					}
					if _, err := reopened.ApplySnippetUpdate(t.Context(), updates[2], snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
						t.Fatal("reopened quota admitted another receipt", err)
					}
					if result, err := reopened.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err != nil || !result.Replayed {
						t.Fatal("reopened full quota rejected replay", result, err)
					}
				}
			})
		}
	}
}

func TestSnippetReceiptBudgetFailedCommit(t *testing.T) {
	for _, outcome := range []string{"cancel", "replace", "callback"} {
		t.Run(outcome, func(t *testing.T) {
			state, updates := snippetReceiptBudgetFixture(t, "bytes", 1)
			_, file, _ := snippetBudgetStore(t, "file", state)
			original, digest := file.state, file.digest
			before := snippetReceiptCloneBytes(t, original)
			if outcome == "replace" {
				if err := os.Mkdir(file.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, err := fileContentChange(ctx, file, func(candidate *MemoryRepository) (SnippetResult, error) {
				result, err := candidate.ApplySnippetUpdate(ctx, updates[0], snippetManager())
				if err != nil {
					return result, err
				}
				if outcome == "cancel" {
					cancel()
				}
				if outcome == "callback" {
					return result, errors.New("injected rejection after mutation")
				}
				return result, nil
			})
			if err == nil || outcome == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("prepared candidate unexpectedly committed", err)
			}
			if file.state != original || file.digest != digest || file.searchState.Load() != original || !bytes.Equal(before, snippetReceiptCloneBytes(t, original)) {
				t.Fatal("failed candidate changed committed state")
			}
			reopened, err := NewFileRepository(file.path, false)
			if err != nil || !bytes.Equal(before, snippetReceiptCloneBytes(t, reopened.state)) {
				t.Fatal("failure changed persisted state", err)
			}
			if outcome == "replace" {
				if err := os.Remove(file.path + ".bak"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := file.ApplySnippetUpdate(t.Context(), updates[0], snippetManager())
			if err != nil || result.Replayed || snippetReceiptBudgetStandardSize(t, file.state) != MaxSnippetReceiptBytes {
				t.Fatal("failed attempt consumed quota", result, err)
			}
			if _, err := file.ApplySnippetUpdate(t.Context(), updates[1], snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
				t.Fatal("retry did not account for new receipt", err)
			}
			if !bytes.Equal(before, snippetReceiptCloneBytes(t, original)) {
				t.Fatal("successful retry changed old snapshot")
			}
		})
	}
}

func TestSnippetReceiptBudgetReload(t *testing.T) {
	state, updates := snippetReceiptBudgetFixture(t, "bytes", 1)
	_, file, _ := snippetBudgetStore(t, "file", state)
	before := snippetReceiptCloneBytes(t, file.state)
	var document contentDocument
	if err := json.Unmarshal(before, &document); err != nil {
		t.Fatal(err)
	}
	// A same-count, valid offline edit changes the actual encoded budget.
	// Reopening must derive it from all records, never a persisted cached sum.
	document.SnippetReceipts[0].Identity += "xx"
	raw, err := json.Marshal(document)
	if err != nil || os.WriteFile(file.path, raw, 0600) != nil {
		t.Fatal("cannot write changed receipt file", err)
	}
	if _, err := file.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err == nil {
		t.Fatal("live repository ignored external file change")
	}
	reopened, err := NewFileRepository(file.path, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened.state.now = state.now
	if _, err := reopened.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
		t.Fatal("reopened total ignored changed stored bytes", err)
	}
	for i := range 3 {
		document.SnippetReceipts[i].Identity = strings.Repeat("z", 160)
	}
	raw, err = json.Marshal(document)
	if err != nil || os.WriteFile(file.path, raw, 0600) != nil {
		t.Fatal("cannot write excessive receipt file", err)
	}
	if _, err := NewFileRepository(file.path, false); !errors.Is(err, ErrSnippetCorrupt) {
		t.Fatal("over-budget file passed complete validation", err)
	}
	if err := os.WriteFile(file.path, before, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err = NewFileRepository(file.path, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened.state.now = state.now
	if result, err := reopened.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err != nil || result.Replayed {
		t.Fatal("restored original budget rejected allowed receipt", result, err)
	}
}

func TestSnippetReceiptBudgetInvalidClock(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			state, updates := snippetReceiptBudgetFixture(t, "empty", 2)
			repository, _, current := snippetBudgetStore(t, kind, state)
			access := snippetManager()
			access.Identity = "author<>&中文"
			for i := range updates {
				plan, err := repository.PreviewSnippetUpdate(t.Context(), updates[i], access)
				if err != nil {
					t.Fatal(err)
				}
				updates[i].Digest = plan.Digest
			}
			if _, err := repository.ApplySnippetUpdate(t.Context(), updates[0], access); err != nil {
				t.Fatal(err)
			}
			before := snippetReceiptCloneBytes(t, current())
			now := current().now
			current().now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			if _, err := repository.ApplySnippetUpdate(t.Context(), updates[1], access); !errors.Is(err, ErrSnippetCapacity) {
				t.Fatal("unencodable new receipt consumed quota", err)
			}
			if result, err := repository.ApplySnippetUpdate(t.Context(), updates[0], access); err != nil || !result.Replayed {
				t.Fatal("clock failure blocked old committed receipt", result, err)
			}
			current().now = now
			if !bytes.Equal(before, snippetReceiptCloneBytes(t, current())) {
				t.Fatal("invalid clock or replay mutated content")
			}
			if result, err := repository.ApplySnippetUpdate(t.Context(), updates[1], access); err != nil || result.Replayed {
				t.Fatal("invalid clock consumed request identity", result, err)
			}
		})
	}
}

func TestSnippetReceiptBudgetConcurrentLastSlot(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			state, updates := snippetReceiptBudgetFixture(t, "bytes", 1)
			repository, _, current := snippetBudgetStore(t, kind, state)
			errorsByRequest := make([]error, 2)
			var group sync.WaitGroup
			for i := range updates {
				group.Go(func() {
					_, errorsByRequest[i] = repository.ApplySnippetUpdate(t.Context(), updates[i], snippetManager())
				})
			}
			group.Wait()
			accepted, rejected := 0, 0
			for i, err := range errorsByRequest {
				if err == nil {
					accepted++
					if result, err := repository.ApplySnippetUpdate(t.Context(), updates[i], snippetManager()); err != nil || !result.Replayed {
						t.Fatal("winning request lost its receipt", result, err)
					}
				} else if errors.Is(err, ErrSnippetCapacity) {
					rejected++
				} else {
					t.Fatal(err)
				}
			}
			if accepted != 1 || rejected != 1 || snippetReceiptBudgetStandardSize(t, current()) != MaxSnippetReceiptBytes {
				t.Fatal("concurrent requests exceeded or lost last capacity", accepted, rejected)
			}
		})
	}
}
