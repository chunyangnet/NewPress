package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestSnippetReceiptBudgetMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_RECEIPT_BUDGET_LOAD") != "1" {
		t.Skip("opt-in incremental receipt budget measurements")
	}
	for _, shape := range []string{"bytes", "count"} {
		t.Run(shape, func(t *testing.T) {
			for _, kind := range []string{"memory", "file"} {
				t.Run(kind, func(t *testing.T) { measureSnippetReceiptBudget(t, shape, kind) })
			}
		})
	}
}

func measureSnippetReceiptBudget(t *testing.T, shape, kind string) {
	state, updates := snippetReceiptBudgetFixture(t, shape, 7)
	repository, file, current := snippetBudgetStore(t, kind, state)
	retained := current()
	if file == nil {
		retained = cloneContentState(retained)
	}
	oldBytes := snippetReceiptCloneBytes(t, retained)
	operations := []string{"update", "replay"}
	if file != nil {
		operations = append(operations, "open")
	}
	for _, operation := range operations {
		position := 0
		var opened *FileRepository
		run := func() {
			if operation == "open" {
				var err error
				opened, err = NewFileRepository(file.path, false)
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			u := updates[6]
			if operation == "update" {
				u = updates[position]
				position++
			}
			result, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
			if err != nil || result.Replayed != (operation == "replay") {
				t.Fatal("unexpected update/replay result", result, err)
			}
			result.Receipt.Items[0].Version = 999
		}
		runtime.GC()
		runtime.GC()
		warmupStarted := time.Now()
		run()
		warmupMS := float64(time.Since(warmupStarted).Nanoseconds()) / 1e6
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
		total := snippetReceiptBudgetStandardSize(t, current())
		if shape == "bytes" && total != MaxSnippetReceiptBytes || shape == "count" && len(current().snippetReceipts) != MaxSnippetReceipts {
			t.Fatal("measured new updates did not reach capacity", total, len(current().snippetReceipts))
		}
		if file != nil {
			if operation != "open" {
				var err error
				opened, err = NewFileRepository(file.path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(opened.state.posts, current().posts) || !reflect.DeepEqual(opened.state.revisions, current().revisions) ||
				!reflect.DeepEqual(opened.state.snippetReceipts, current().snippetReceipts) {
				t.Fatal("measured state changed on reopen")
			}
		}
		report, err := json.Marshal(map[string]any{"shape": shape, "backend": kind, "operation": operation, "milliseconds": milliseconds,
			"warmup_ms": warmupMS, "operations": len(milliseconds), "allocated_bytes": after.TotalAlloc - before.TotalAlloc,
			"heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc, "heap_after_gc": first.HeapAlloc,
			"heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC,
			"receipt_bytes": total, "receipts": len(current().snippetReceipts)})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("SNIPPET_RECEIPT_BUDGET " + string(report))
		runtime.KeepAlive(opened)
	}
	if _, err := repository.ApplySnippetUpdate(t.Context(), updates[7], snippetManager()); !errors.Is(err, ErrSnippetCapacity) {
		t.Fatal("next request exceeded measured capacity", err)
	}
	for _, u := range updates[:7] {
		result, err := repository.ApplySnippetUpdate(t.Context(), u, snippetManager())
		if err != nil || !result.Replayed || result.Receipt.Items[0].Version != 2 {
			t.Fatal("stored receipt changed or could not replay at capacity", result, err)
		}
	}
	if !bytes.Equal(oldBytes, snippetReceiptCloneBytes(t, retained)) {
		t.Fatal("later writes changed retained state")
	}
	runtime.KeepAlive(retained)
	runtime.KeepAlive(repository)
}
