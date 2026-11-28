package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetBudgetStandardSize(t testing.TB, record SnippetRecord) int {
	t.Helper()
	size := 4096
	for _, revision := range record.Revisions {
		raw, err := json.Marshal(revision)
		if err != nil {
			t.Fatal(err)
		}
		size += len(raw)
	}
	return size
}

func TestSnippetBudgetCountMatchesStandard(t *testing.T) {
	random := rand.New(rand.NewSource(20261128))
	texts := []string{"", "plain", "中文 😀", "<>&", "\u2028\u2029", "\x00\t\n\"\\", "bad-\xff"}
	stamp := time.Date(2026, 11, 28, 8, 0, 0, 123, time.FixedZone("test", 8*3600))
	for trial := range 200 {
		var record SnippetRecord
		for i := range random.Intn(16) {
			record.Revisions = append(record.Revisions, SnippetRevision{Version: int64(i + 1), Title: texts[random.Intn(len(texts))],
				ContentSource: texts[random.Intn(len(texts))], ContentHTML: texts[random.Intn(len(texts))],
				Actor: texts[random.Intn(len(texts))], CreatedAt: stamp.Add(time.Duration(i) * time.Second)})
		}
		if want, got := snippetBudgetStandardSize(t, record), SnippetStorageSize(record); got != want {
			t.Fatal("history byte accounting changed", trial, got, want)
		}
		receipt := SnippetReceipt{Identity: texts[random.Intn(len(texts))], RequestKey: texts[random.Intn(len(texts))],
			Fingerprint: texts[random.Intn(len(texts))], SnippetID: 9007199254740993, SnippetVersion: 12, TargetVersion: 9, CreatedAt: stamp}
		if trial%2 == 0 {
			receipt.Items = []SnippetSelection{}
		}
		for i := range random.Intn(12) {
			receipt.Items = append(receipt.Items, SnippetSelection{ID: int64(i + 1), Version: 9007199254740993})
		}
		raw, err := json.Marshal(receipt)
		if err != nil || SnippetReceiptSize(receipt) != len(raw) {
			t.Fatal("receipt byte accounting changed", trial, SnippetReceiptSize(receipt), len(raw), err)
		}
	}
	// Reusing the same slice cannot conceal edits; sizes are recalculated.
	r := SnippetRecord{Revisions: []SnippetRevision{{ContentSource: "x"}}}
	small := SnippetStorageSize(r)
	r.Revisions[0].ContentSource = strings.Repeat("<中文>", 30000)
	if size := SnippetStorageSize(r); size <= small || size != snippetBudgetStandardSize(t, r) {
		t.Fatal("changed revision retained stale quota", size, small)
	}
}

func TestSnippetBudgetCountUnencodable(t *testing.T) {
	bad := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("history", func(t *testing.T) {
		r := SnippetRecord{Revisions: []SnippetRevision{{ContentSource: "valid"}, {CreatedAt: bad}, {ContentSource: "later"}}}
		if SnippetStorageSize(r) <= MaxSnippetStorageBytes {
			t.Fatal("unencodable history consumed less than the quota")
		}
	})
	t.Run("receipt", func(t *testing.T) {
		if SnippetReceiptSize(SnippetReceipt{CreatedAt: bad}) <= MaxSnippetReceiptBytes {
			t.Fatal("unencodable receipt consumed less than the quota")
		}
	})
	t.Run("memory-save", func(t *testing.T) {
		r := NewMemoryRepository(false)
		r.now = func() time.Time { return bad }
		_, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{Title: "invalid timestamp", ContentSource: "<p>body</p>", Visibility: SnippetShared}, snippetManager())
		if !errors.Is(err, ErrSnippetCapacity) || len(r.snippets) != 0 || r.nextSnippetID != 1 {
			t.Fatal("unencodable history was accepted", err)
		}
	})
}

func snippetBudgetStore(t *testing.T, kind string, state *MemoryRepository) (Repository, *FileRepository, func() *MemoryRepository) {
	t.Helper()
	stamp := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return stamp }
	for i := range state.terms {
		state.terms[i].CreatedAt, state.terms[i].UpdatedAt = stamp.Add(-time.Hour), stamp.Add(-time.Hour)
	}
	for i := range state.posts {
		state.posts[i].CreatedAt, state.posts[i].UpdatedAt = stamp.Add(-time.Hour), stamp.Add(-time.Hour)
		for j := range state.posts[i].Categories {
			state.posts[i].Categories[j] = state.terms[0]
		}
	}
	if kind == "memory" {
		return state, nil, func() *MemoryRepository { return state }
	}
	path := filepath.Join(t.TempDir(), "content.json")
	if _, err := writeContentState(t.Context(), path, state); err != nil {
		t.Fatal(err)
	}
	file, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	file.state.now = state.now
	return file, file, func() *MemoryRepository { return file.state }
}

func TestSnippetBudgetCountHistoryBoundary(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			state := contentFileSnippetFixture(t, 127, MaxSnippetBodyBytes-7, false)
			repository, file, current := snippetBudgetStore(t, kind, state)
			old := current().snippets[0]
			input := SnippetInput{Title: "last", ContentSource: "<p></p>", Visibility: SnippetShared}
			candidate, err := NextSnippetRecord(old, old.ID, Mutation{ExpectedVersion: old.Version}, input, snippetManager(), current().now())
			if err != nil {
				t.Fatal(err)
			}
			remaining := MaxSnippetStorageBytes - snippetBudgetStandardSize(t, candidate)
			input.ContentSource = "<p>" + strings.Repeat("x", remaining/2) + "</p>"
			if remaining%2 != 0 {
				input.Title += "x"
			}
			saved, err := repository.SaveSnippet(t.Context(), old.ID, Mutation{ExpectedVersion: old.Version}, input, snippetManager())
			if err != nil || snippetBudgetStandardSize(t, current().snippets[0]) != MaxSnippetStorageBytes {
				t.Fatal("last permitted revision rejected or miscounted", err)
			}
			if !reflect.DeepEqual(current().snippets[0].Revisions[:len(old.Revisions)], old.Revisions) {
				t.Fatal("saving changed old history")
			}
			before, err := encodeContentDocument(current())
			if err != nil {
				t.Fatal(err)
			}
			_, err = repository.SaveSnippet(t.Context(), saved.ID, Mutation{ExpectedVersion: saved.Version}, SnippetInput{Title: "over", ContentSource: "<p>x</p>", Visibility: SnippetShared}, snippetManager())
			if !errors.Is(err, ErrSnippetCapacity) {
				t.Fatal("over-budget history accepted", err)
			}
			after, err := encodeContentDocument(current())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected history changed state", err)
			}
			if file != nil {
				if err := file.Ready(t.Context()); err != nil {
					t.Fatal("rejected history changed disk", err)
				}
			}
			withdrawn, err := repository.WithdrawSnippet(t.Context(), saved.ID, Mutation{ExpectedVersion: saved.Version}, snippetManager())
			if err != nil || withdrawn.Visibility != SnippetWithdrawn || SnippetStorageSize(current().snippets[0]) != MaxSnippetStorageBytes {
				t.Fatal("withdrawal at capacity failed", err)
			}
			if file != nil {
				reopened, err := NewFileRepository(file.path, false)
				if err != nil || !reflect.DeepEqual(reopened.state.snippets, file.state.snippets) {
					t.Fatal("history boundary did not survive reopen", err)
				}
			}
		})
	}
}

func snippetBudgetReceiptFixture(t *testing.T, targetBytes int, stamp time.Time) []SnippetReceipt {
	t.Helper()
	var receipts []SnippetReceipt
	remaining := targetBytes
	for remaining != 0 {
		r := SnippetReceipt{Identity: "x", RequestKey: fmt.Sprintf("%064x", len(receipts)+1), Fingerprint: strings.Repeat("a", 64),
			SnippetID: 1, SnippetVersion: 2, TargetVersion: 2, CreatedAt: stamp}
		var size int
		for count := 1; count <= MaxBulkMutations; count++ {
			r.Items = append(r.Items, SnippetSelection{ID: int64(count), Version: 2})
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			size = len(raw)
			if remaining >= size && remaining-size <= 159 {
				r.Identity = strings.Repeat("x", remaining-size+1)
				size = remaining
				break
			}
		}
		// Leave enough for a final minimal receipt if the remaining tail cannot
		// be represented by an identity extension and the current item count.
		for remaining > size && remaining-size < 400 && len(r.Items) > 1 {
			r.Items = r.Items[:len(r.Items)-1]
			raw, _ := json.Marshal(r)
			size = len(raw)
		}
		if size <= 0 || size > remaining || len(receipts) >= MaxSnippetReceipts {
			t.Fatal("receipt fixture cannot fill budget", remaining, size, len(receipts))
		}
		receipts = append(receipts, r)
		remaining -= size
	}
	return receipts
}

func TestSnippetBudgetCountReceiptBoundary(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			state := contentFileSnippetFixture(t, 2, 32, false)
			state.posts[0].ContentSource = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1})
			state.posts[0].ContentHTML = contenthtml.Sanitize(state.posts[0].ContentSource)
			state.indexSnippetReferencesLocked(state.posts[0])
			stamp := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
			u := SnippetUpdate{SnippetID: 1, SnippetVersion: 2, Items: []SnippetSelection{{ID: 1, Version: state.posts[0].Version}}, RequestKey: strings.Repeat("f", 64)}
			plan, err := state.PreviewSnippetUpdate(t.Context(), u, snippetManager())
			if err != nil {
				t.Fatal(err)
			}
			u.Digest = plan.Digest
			receipt := NewSnippetReceipt(u, plan, snippetManager(), stamp)
			raw, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			state.snippetReceipts = snippetBudgetReceiptFixture(t, MaxSnippetReceiptBytes-len(raw), stamp)
			if err := ValidateSnippetState(state.snippets, state.snippetReceipts, state.nextSnippetID); err != nil {
				t.Fatal(err)
			}
			repository, file, current := snippetBudgetStore(t, kind, state)
			result, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
			if err != nil || result.Replayed || !reflect.DeepEqual(result.Receipt, receipt) {
				t.Fatal("last permitted receipt rejected or changed", err)
			}
			total := 0
			for _, r := range current().snippetReceipts {
				raw, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				total += len(raw)
			}
			if total != MaxSnippetReceiptBytes {
				t.Fatal("receipt fixture did not fill the quota", total)
			}
			saved, err := repository.SaveSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, SnippetInput{Title: "third", ContentSource: "<p>next</p>", Visibility: SnippetShared}, snippetManager())
			if err != nil {
				t.Fatal(err)
			}
			next := SnippetUpdate{SnippetID: 1, SnippetVersion: saved.Version, Items: []SnippetSelection{{ID: 1, Version: current().posts[0].Version}}, RequestKey: strings.Repeat("e", 64)}
			plan, err = repository.PreviewSnippetUpdate(t.Context(), next, snippetManager())
			if err != nil {
				t.Fatal(err)
			}
			next.Digest = plan.Digest
			before, err := encodeContentDocument(current())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.ApplySnippetUpdate(t.Context(), next, snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
				t.Fatal("full receipt quota accepted new work", err)
			}
			after, err := encodeContentDocument(current())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("receipt refusal changed article or receipts", err)
			}
			replay, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
			if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Receipt, receipt) {
				t.Fatal("full quota prevented old receipt confirmation", err)
			}
			if file != nil {
				reopened, err := NewFileRepository(file.path, false)
				if err != nil || !reflect.DeepEqual(reopened.state.snippetReceipts, file.state.snippetReceipts) || !reflect.DeepEqual(reopened.state.posts, file.state.posts) {
					t.Fatal("receipt boundary did not survive reopen", err)
				}
			}
		})
	}
}

func BenchmarkSnippetBudgetCount(b *testing.B) {
	for _, size := range []struct{ count, body int }{{1, 128}, {10000, 1450}, {127, MaxSnippetBodyBytes - 7}} {
		b.Run(fmt.Sprintf("%d-%d", size.count, size.body), func(b *testing.B) {
			r := SnippetRecord{Revisions: make([]SnippetRevision, size.count)}
			body := "<p>" + strings.Repeat("x", size.body) + "</p>"
			for i := range r.Revisions {
				r.Revisions[i] = SnippetRevision{Version: int64(i + 1), Title: "history", ContentSource: body, ContentHTML: body, Actor: "editor",
					CreatedAt: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
			}
			want := snippetBudgetStandardSize(b, r)
			b.ReportAllocs()
			for b.Loop() {
				if got := SnippetStorageSize(r); got != want {
					b.Fatal(got, want)
				}
			}
		})
	}
}
