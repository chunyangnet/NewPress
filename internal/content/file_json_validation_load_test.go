package content

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

func TestContentJSONValidationMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_JSON_VALIDATION_LOAD") != "1" {
		t.Skip("opt-in strict JSON validation and complete file loading measurements")
	}
	for _, shape := range []string{"receipts-bytes-8m", "receipts-count-10000", "history-127-large"} {
		t.Run(shape, func(t *testing.T) { measureContentJSONValidation(t, shape) })
	}
}

func measureContentJSONValidation(t *testing.T, shape string) {
	var file *FileRepository
	if shape == "history-127-large" {
		state := contentFileSnippetFixture(t, 127, MaxSnippetBodyBytes-7, false)
		_, file, _ = snippetBudgetStore(t, "file", state)
	} else {
		budgetShape := "bytes"
		if shape == "receipts-count-10000" {
			budgetShape = "count"
		}
		state, updates := snippetReceiptBudgetFixture(t, budgetShape, 1)
		_, file, _ = snippetBudgetStore(t, "file", state)
		if result, err := file.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err != nil || result.Replayed {
			t.Fatal(result, err)
		}
	}
	payload, err := os.ReadFile(file.path)
	if err != nil {
		t.Fatal(err)
	}
	original := file.state
	for _, operation := range []string{"validate", "decode", "open"} {
		var decoded *MemoryRepository
		run := func() {
			var err error
			switch operation {
			case "validate":
				err = packagefs.ValidateUniqueJSONFor(payload, new(contentDocument))
			case "decode":
				decoded, err = decodeContentDocument(payload)
			case "open":
				var opened *FileRepository
				opened, err = NewFileRepository(file.path, false)
				if err == nil {
					decoded = opened.state
				}
			}
			if err != nil {
				t.Fatal(operation, err)
			}
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
		if decoded != nil && (!reflect.DeepEqual(decoded.posts, original.posts) || !reflect.DeepEqual(decoded.revisions, original.revisions) ||
			!reflect.DeepEqual(decoded.snippets, original.snippets) || !reflect.DeepEqual(decoded.snippetReceipts, original.snippetReceipts)) {
			t.Fatal("complete decoded state differs")
		}
		raw, err := os.ReadFile(file.path)
		if err != nil || !bytes.Equal(raw, payload) {
			t.Fatal("read changed the source file", err)
		}
		report, err := json.Marshal(map[string]any{"shape": shape, "operation": operation, "milliseconds": milliseconds,
			"operations": len(milliseconds), "file_bytes": len(payload), "receipts": len(original.snippetReceipts),
			"allocated_bytes": after.TotalAlloc - before.TotalAlloc, "heap_before": before.HeapAlloc, "heap_after": after.HeapAlloc,
			"heap_after_gc": first.HeapAlloc, "heap_after_second_gc": second.HeapAlloc, "gc_cycles": after.NumGC - before.NumGC})
		if err != nil {
			t.Fatal(err)
		}
		t.Log("CONTENT_JSON_VALIDATION " + string(report))
		runtime.KeepAlive(decoded)
	}
	runtime.KeepAlive(original)
	runtime.KeepAlive(payload)
}

func TestContentJSONValidationProfile(t *testing.T) {
	directory := os.Getenv("NEWPRESS_JSON_VALIDATION_PROFILE")
	if directory == "" {
		t.Skip("opt-in validation CPU and allocation profiles")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	state, updates := snippetReceiptBudgetFixture(t, "bytes", 1)
	_, file, _ := snippetBudgetStore(t, "file", state)
	if _, err := file.ApplySnippetUpdate(t.Context(), updates[0], snippetManager()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(file.path)
	if err != nil {
		t.Fatal(err)
	}
	writeAllocs := func(name string) {
		runtime.GC()
		runtime.GC()
		output, err := os.Create(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.Lookup("allocs").WriteTo(output, 0)
		closeErr := output.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	writeAllocs("before.pprof")
	cpu, err := os.Create(filepath.Join(directory, "cpu.pprof"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if err := packagefs.ValidateUniqueJSONFor(payload, new(contentDocument)); err != nil {
			pprof.StopCPUProfile()
			_ = cpu.Close()
			t.Fatal(err)
		}
	}
	pprof.StopCPUProfile()
	if err := cpu.Close(); err != nil {
		t.Fatal(err)
	}
	writeAllocs("after.pprof")
	runtime.KeepAlive(payload)
	runtime.KeepAlive(file)
}
