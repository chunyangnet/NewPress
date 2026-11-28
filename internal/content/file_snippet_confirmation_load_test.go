package content

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetFileConfirmationMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_CONFIRMATION_LOAD") != "1" {
		t.Skip("opt-in repeated confirmation and file capacity measurements")
	}
	for _, shape := range []struct {
		name        string
		count, body int
		near        bool
	}{{"many-small", 1, 128, false}, {"history-10000", 10000, 1450, false},
		{"large-revisions", 127, MaxSnippetBodyBytes - 7, false}, {"file-limit", 127, MaxSnippetBodyBytes - 7, true},
		{"receipts-8m", 2, 128, false}} {
		t.Run(shape.name, func(t *testing.T) {
			state := contentFileSnippetFixture(t, shape.count, shape.body, shape.near)
			if shape.name == "many-small" {
				first := state.snippets[0]
				state.snippets = make([]SnippetRecord, MaxSnippets)
				for i := range state.snippets {
					state.snippets[i] = first
					state.snippets[i].ID = int64(i + 1)
				}
				state.nextSnippetID = MaxSnippets + 1
			}
			var u SnippetUpdate
			if shape.name == "receipts-8m" {
				state.posts[0].ContentSource = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1})
				state.posts[0].ContentHTML = contenthtml.Sanitize(state.posts[0].ContentSource)
				state.indexSnippetReferencesLocked(state.posts[0])
				u = SnippetUpdate{SnippetID: 1, SnippetVersion: 2, Items: []SnippetSelection{{ID: 1, Version: state.posts[0].Version}}, RequestKey: strings.Repeat("f", 64)}
				plan, err := state.PreviewSnippetUpdate(t.Context(), u, snippetManager())
				if err != nil {
					t.Fatal(err)
				}
				u.Digest = plan.Digest
				stamp := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
				receipt := NewSnippetReceipt(u, plan, snippetManager(), stamp)
				raw, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				state.snippetReceipts = snippetBudgetReceiptFixture(t, MaxSnippetReceiptBytes-len(raw), stamp)
			}
			var file *FileRepository
			var err error
			if shape.name == "receipts-8m" {
				_, file, _ = snippetBudgetStore(t, "file", state)
				// The measured receipt is genuinely committed after all retained
				// receipts, so confirmation must find it at the end of the scan.
				result, err := file.ApplySnippetUpdate(t.Context(), u, snippetManager())
				if err != nil || result.Replayed {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(t.TempDir(), "content.json")
				if _, err := writeContentState(t.Context(), path, state); err != nil {
					t.Fatal(err)
				}
				file, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			state = nil
			operations := []string{"withdraw"}
			if shape.name == "receipts-8m" {
				operations = []string{"receipt"}
			}
			if shape.name == "many-small" || shape.name == "large-revisions" {
				operations = append(operations, "save-control")
			}
			for _, operation := range operations {
				run := func() error {
					if operation == "receipt" {
						result, err := file.ApplySnippetUpdate(t.Context(), u, snippetManager())
						if err == nil && !result.Replayed {
							t.Fatal("measured request was not a confirmation")
						}
						return err
					}
					if operation == "save-control" {
						_, err := file.SaveSnippet(t.Context(), 1, Mutation{ExpectedVersion: file.state.snippets[0].Version},
							SnippetInput{Title: "new version", ContentSource: "<p>body</p>", Visibility: SnippetShared}, snippetManager())
						return err
					}
					_, err := file.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: file.state.snippets[0].Version}, snippetManager())
					return err
				}
				runtime.GC()
				runtime.GC()
				if err := run(); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(file.path)
				if err != nil {
					t.Fatal(err)
				}
				digest := file.digest
				var before, after, first, second runtime.MemStats
				runtime.ReadMemStats(&before)
				var milliseconds [6]float64
				for i := range milliseconds {
					started := time.Now()
					if err := run(); err != nil {
						t.Fatal(err)
					}
					milliseconds[i] = float64(time.Since(started).Nanoseconds()) / 1e6
				}
				runtime.ReadMemStats(&after)
				runtime.GC()
				runtime.ReadMemStats(&first)
				runtime.GC()
				runtime.ReadMemStats(&second)
				end, err := os.Stat(file.path)
				if err != nil {
					t.Fatal(err)
				}
				if operation != "save-control" && digest != file.digest {
					t.Fatal("confirmation changed persisted bytes")
				}
				report, err := json.Marshal(map[string]any{"shape": shape.name, "operation": operation, "milliseconds": milliseconds,
					"operations": len(milliseconds), "allocated_bytes": after.TotalAlloc - before.TotalAlloc,
					"heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc, "heap_after_gc": first.HeapAlloc,
					"heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC,
					"file_replaced": !os.SameFile(info, end), "file_bytes": end.Size(), "receipts": len(file.state.snippetReceipts)})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("SNIPPET_CONFIRMATION_LOAD " + string(report))
			}
			reopened, err := NewFileRepository(file.path, false)
			if err != nil || !reflect.DeepEqual(reopened.state.snippets, file.state.snippets) || !reflect.DeepEqual(reopened.state.snippetReceipts, file.state.snippetReceipts) ||
				!reflect.DeepEqual(reopened.state.posts, file.state.posts) || !reflect.DeepEqual(reopened.state.revisions, file.state.revisions) {
				t.Fatal("confirmation state did not survive full reopen", err)
			}
			runtime.KeepAlive(file)
		})
	}
}
