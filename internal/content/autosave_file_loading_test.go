package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"testing"

	"newpress.local/newpress/internal/packagefs"
)

// Assemble raw values without repairing duplicate keys, malformed Unicode or
// excessive nesting before the real file decoder gets to inspect them.
func autosaveFileLoadingObject(t *testing.T, fields map[string]json.RawMessage) []byte {
	t.Helper()
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result bytes.Buffer
	result.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			result.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		result.Write(name)
		result.WriteByte(':')
		result.Write(fields[key])
	}
	result.WriteByte('}')
	return result.Bytes()
}

func autosaveFileLoadingPayload(t *testing.T, fields map[string]string) []byte {
	t.Helper()
	state := contentHistoryLoadingFixture(t, 1, 0, 1)
	var document, autosave, snapshot map[string]json.RawMessage
	var autosaves []json.RawMessage
	if err := json.Unmarshal(contentCandidateBytes(t, state), &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(document["autosaves"], &autosaves); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(autosaves[0], &autosave); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(autosave["snapshot"], &snapshot); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		snapshot[key] = json.RawMessage(value)
	}
	autosave["snapshot"] = autosaveFileLoadingObject(t, snapshot)
	document["autosaves"] = append(append([]byte{'['}, autosaveFileLoadingObject(t, autosave)...), ']')
	return append(autosaveFileLoadingObject(t, document), '\n')
}

func TestAutosaveFileLoadingMatchesNormalization(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		valid  bool
	}{
		{"incomplete", map[string]string{"title": `""`, "slug": `""`, "status": `""`, "type": `""`, "content_source": `"  <p>待恢复</p><script>bad()</script>  "`, "content_html": `"<script>stale()</script>"`}, true},
		{"empty-collections", map[string]string{"categories": `[]`, "tags": `[]`, "meta": `{}`}, true},
		{"null-collections", map[string]string{"categories": `null`, "tags": `null`, "meta": `null`}, true},
		{"empty-terms", map[string]string{"categories": `[{},null]`, "tags": `[{}]`}, true},
		{"exact-metadata", map[string]string{"meta": `{"integer":9007199254740993,"negative":-0,"fraction":0.1234567890123456789,"huge":1e400,"nested":[{},[],null,"中文😀"],"x":1,"X":2}`}, true},
		{"deep-metadata", map[string]string{"meta": `{"nested":` + strings.Repeat("[", 9990) + "0" + strings.Repeat("]", 9990) + `}`}, true},
		{"too-deep", map[string]string{"meta": `{"nested":` + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + `}`}, false},
		{"duplicate-metadata", map[string]string{"meta": `{"x":1,"x":2}`}, false},
		{"escaped-duplicate", map[string]string{"meta": `{"x":1,"\u0078":2}`}, false},
		{"field-alias", map[string]string{"ID": `1`}, false},
		{"unknown-field", map[string]string{"unknown": `1`}, false},
		{"wrong-metadata-type", map[string]string{"meta": `[]`}, false},
		{"wrong-id-type", map[string]string{"id": `1.0`}, false},
		{"large-id", map[string]string{"id": `9223372036854775808`}, false},
		{"invalid-utf8", map[string]string{"title": "\"\xff\""}, false},
		{"unpaired-surrogate", map[string]string{"meta": `{"text":"\ud800"}`}, false},
		{"unicode-escape", map[string]string{"meta": `{"text":"\u4e2d\ud83d\ude00"}`}, true},
	}
	for _, field := range []string{"published_at", "created_at", "updated_at", "categories/created_at", "categories/updated_at", "tags/created_at", "tags/updated_at"} {
		for _, stamp := range []struct {
			name, value string
			valid       bool
		}{
			{"null", `null`, true},
			{"zero", `"0001-01-01T00:00:00Z"`, true},
			{"minimum-year", `"0000-01-01T00:00:00Z"`, true},
			{"maximum-year", `"9999-12-31T23:59:59.999999999Z"`, true},
			{"utc-offset", `"2026-11-28T00:00:00+00:00"`, true},
			{"negative-zero", `"2026-11-28T00:00:00-00:00"`, true},
			{"fraction", `"2026-11-28T01:00:00,123456789+08:00"`, true},
			{"one-digit-hour", `"2026-11-28T1:00:00+08:00"`, true},
			{"minute-overflow", `"2026-11-28T00:00:00+00:60"`, true},
			{"positive-offset-overflow", `"2026-11-28T00:00:00+24:00"`, false},
			{"negative-offset-overflow", `"2026-11-28T00:00:00-24:00"`, false},
			{"combined-offset-overflow", `"2026-11-28T00:00:00+23:60"`, false},
			{"leap-second", `"2026-11-28T00:00:60Z"`, false},
		} {
			fields := map[string]string{field: stamp.value}
			if group, key, found := strings.Cut(field, "/"); found {
				fields = map[string]string{group: fmt.Sprintf(`[{%q:%s}]`, key, stamp.value)}
			}
			tests = append(tests, struct {
				name   string
				fields map[string]string
				valid  bool
			}{field + "/" + stamp.name, fields, stamp.valid})
		}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := autosaveFileLoadingPayload(t, test.fields)
			original := bytes.Clone(payload)
			decoded, referenceErr := packagefs.DecodeJSONBytesWithNumbers[contentDocument](payload, maxContentFileBytes, "reference")
			var want Autosave
			if referenceErr == nil {
				want, referenceErr = NormalizeAutosave(decoded.Autosaves[0])
			}
			if (referenceErr == nil) != test.valid {
				t.Fatalf("reference acceptance differs from the fixture contract: %v", referenceErr)
			}
			state, err := decodeContentDocument(payload)
			if (err == nil) != test.valid {
				t.Fatalf("file acceptance differs from public normalization: %v / %v", err, referenceErr)
			}
			if test.valid && !reflect.DeepEqual(state.autosaves[1]["user:1"], want) {
				t.Fatal("decoded recovery differs from full normalization")
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if (err == nil) != test.valid {
				t.Fatal("reopening changed acceptance", err)
			}
			if test.valid && !reflect.DeepEqual(file.state.autosaves[1]["user:1"], want) {
				t.Fatal("reopened recovery differs from full normalization")
			}
			disk, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(disk, original) || !bytes.Equal(payload, original) {
				t.Fatal("loading changed input bytes or file", err)
			}
			clear(payload)
			if test.valid && !reflect.DeepEqual(state.autosaves[1]["user:1"], want) {
				t.Fatal("recovery borrowed the input buffer")
			}
		})
	}
}

func TestAutosaveFileLoadingProfile(t *testing.T) {
	directory := os.Getenv("NEWPRESS_AUTOSAVE_FILE_PROFILE")
	if directory == "" {
		t.Skip("opt-in complete autosave file decoding CPU and allocation profile")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	source := contentHistoryLoadingFixture(t, 1000, 0, 4)
	payload := contentCandidateBytes(t, source)
	writeAllocs := func(name string) {
		runtime.GC()
		runtime.GC()
		file, err := os.Create(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.Lookup("allocs").WriteTo(file, 0)
		closeErr := file.Close()
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
		_ = cpu.Close()
		t.Fatal(err)
	}
	var loaded *MemoryRepository
	for range 12 {
		loaded, err = decodeContentDocument(payload)
		if err != nil {
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
	loaded.now = source.now
	if !bytes.Equal(payload, contentCandidateBytes(t, loaded)) {
		t.Fatal("profiled loading changed recovery data")
	}
	t.Logf("AUTOSAVE_FILE_PROFILE bytes=%d sha256=%x iterations=12", len(payload), sha256.Sum256(payload))
	runtime.KeepAlive(payload)
	runtime.KeepAlive(source)
	runtime.KeepAlive(loaded)
}
