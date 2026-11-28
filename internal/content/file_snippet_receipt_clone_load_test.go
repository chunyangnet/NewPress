package content

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

// Retain one committed snapshot while distinct new requests fill the actual
// byte/count quota. Compare with identical tests in the unoptimized sources.
func TestSnippetReceiptCloneMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_RECEIPT_CLONE_LOAD") != "1" {
		t.Skip("opt-in full receipt clone and durable update measurements")
	}
	for _, shape := range []string{"items-100-bytes-8m", "items-1-count-10000"} {
		t.Run(shape, func(t *testing.T) { measureSnippetReceiptClones(t, shape) })
	}
}

func measureSnippetReceiptClones(t *testing.T, shape string) {
	state := contentFileSnippetFixture(t, 2, 128, false)
	state.posts[0].ContentSource = contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1})
	state.posts[0].ContentHTML = contenthtml.Sanitize(state.posts[0].ContentSource)
	for i := 1; i < 7; i++ {
		p := clonePost(state.posts[0])
		p.ID, p.Slug = int64(i+1), fmt.Sprintf("article-%d", i+1)
		state.posts = append(state.posts, p)
	}
	state.nextID = 8
	stamp := time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC)
	var updates []SnippetUpdate
	newBytes := 0
	for _, p := range state.posts {
		state.indexSnippetReferencesLocked(p)
		u := SnippetUpdate{SnippetID: 1, SnippetVersion: 2, Items: []SnippetSelection{{ID: p.ID, Version: p.Version}},
			RequestKey: strings.Repeat("f", 63) + fmt.Sprint(p.ID)}
		plan, err := state.PreviewSnippetUpdate(t.Context(), u, snippetManager())
		if err != nil {
			t.Fatal(err)
		}
		u.Digest = plan.Digest
		raw, err := json.Marshal(NewSnippetReceipt(u, plan, snippetManager(), stamp))
		if err != nil {
			t.Fatal(err)
		}
		newBytes += len(raw)
		updates = append(updates, u)
	}
	if shape == "items-100-bytes-8m" {
		state.snippetReceipts = snippetBudgetReceiptFixture(t, MaxSnippetReceiptBytes-newBytes, stamp)
	} else {
		for i := range MaxSnippetReceipts - len(updates) {
			state.snippetReceipts = append(state.snippetReceipts, SnippetReceipt{Identity: "x", RequestKey: fmt.Sprintf("%064x", i+1),
				Fingerprint: strings.Repeat("a", 64), SnippetID: 1, SnippetVersion: 2, TargetVersion: 2, CreatedAt: stamp,
				Items: []SnippetSelection{{ID: 1, Version: 2}}})
		}
	}
	_, file, _ := snippetBudgetStore(t, "file", state)
	state = nil
	retained := file.state
	oldBytes := snippetReceiptCloneBytes(t, retained)
	for _, operation := range []string{"clone", "update"} {
		position := 0
		apply := func(u SnippetUpdate) {
			result, err := file.ApplySnippetUpdate(t.Context(), u, snippetManager())
			if err != nil || result.Replayed {
				t.Fatal("new update was rejected or treated as a confirmation", result, err)
			}
		}
		run := func() {
			if operation == "clone" {
				runtime.KeepAlive(cloneContentState(file.state))
				return
			}
			apply(updates[position])
			position++
		}
		runtime.GC()
		runtime.GC()
		run()
		var before, after, first, second runtime.MemStats
		runtime.ReadMemStats(&before)
		var milliseconds [6]float64
		for i := range milliseconds {
			started := time.Now()
			run()
			milliseconds[i] = float64(time.Since(started).Nanoseconds()) / 1e6
		}
		runtime.ReadMemStats(&after)
		runtime.GC()
		runtime.ReadMemStats(&first)
		runtime.GC()
		runtime.ReadMemStats(&second)
		total := 0
		for _, receipt := range file.state.snippetReceipts {
			raw, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			total += len(raw)
		}
		if operation == "update" && (shape == "items-100-bytes-8m" && total != MaxSnippetReceiptBytes || shape == "items-1-count-10000" && len(file.state.snippetReceipts) != MaxSnippetReceipts) {
			t.Fatal("new requests did not reach the exact receipt budget", total, len(file.state.snippetReceipts))
		}
		reopened, err := NewFileRepository(file.path, false)
		if err != nil || !reflect.DeepEqual(reopened.state.posts, file.state.posts) || !reflect.DeepEqual(reopened.state.revisions, file.state.revisions) ||
			!reflect.DeepEqual(reopened.state.snippetReceipts, file.state.snippetReceipts) {
			t.Fatal("new receipts did not survive reopen", err)
		}
		report, err := json.Marshal(map[string]any{"shape": shape, "operation": operation, "milliseconds": milliseconds, "operations": len(milliseconds),
			"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
			"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC,
			"receipt_bytes": total, "receipts": len(file.state.snippetReceipts)})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("SNIPPET_RECEIPT_CLONE " + string(report))
	}
	if !bytes.Equal(oldBytes, snippetReceiptCloneBytes(t, retained)) {
		t.Fatal("later commits changed the retained snapshot")
	}
	runtime.KeepAlive(retained)
	runtime.KeepAlive(file)
}
