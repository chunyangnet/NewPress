package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestContentFileSnippetEncodingFields(t *testing.T) {
	// Fill every field independently of the writer's field list. Future fields
	// in the embedded metadata, record or revision must survive serialization.
	stamp := time.Date(2026, 11, 28, 8, 0, 0, 123, time.FixedZone("test", 8*3600))
	var fill func(reflect.Value)
	fill = func(v reflect.Value) {
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(stamp))
			return
		}
		switch v.Kind() {
		case reflect.Int64:
			v.SetInt(9007199254740993)
		case reflect.String:
			v.SetString("中文 😀 <>& \"\\\n\u2028\u2029\xff")
		case reflect.Struct:
			for i := range v.NumField() {
				fill(v.Field(i))
			}
		case reflect.Slice:
			v.Set(reflect.MakeSlice(v.Type(), 2, 2))
			for i := range v.Len() {
				fill(v.Index(i))
			}
		default:
			t.Fatalf("add a fixture for %s", v.Type())
		}
	}
	var populated SnippetRecord
	fill(reflect.ValueOf(&populated).Elem())
	for _, records := range [][]SnippetRecord{nil, {}, {{}}, {{Revisions: []SnippetRevision{}}}, {populated, {}, populated}} {
		assertContentJSONMatches(t, contentDocument{Schema: 3, Snippets: records, NextSnippetID: 9,
			SnippetReceipts:    []SnippetReceipt{{Identity: "following field"}},
			SnippetQuarantines: []SnippetFileQuarantine{{ID: "last field"}}})
	}
}

func TestContentFileSnippetEncodingRecords(t *testing.T) {
	random := rand.New(rand.NewSource(20261128))
	texts := []string{"", "plain", "中文 😀", "<>&", "\u2028\u2029", "\x00\t\n\"\\", "bad-\xff"}
	for trial := range 100 {
		doc := contentDocument{Schema: 2, NextSnippetID: int64(trial + 1)}
		for i := range 1 + random.Intn(4) {
			r := SnippetRecord{Snippet: Snippet{ID: int64(i + 1), Title: texts[random.Intn(len(texts))]}}
			for j := range random.Intn(12) {
				r.Revisions = append(r.Revisions, SnippetRevision{Version: int64(j + 1), Title: texts[random.Intn(len(texts))],
					ContentSource: texts[random.Intn(len(texts))], ContentHTML: texts[random.Intn(len(texts))]})
			}
			doc.Snippets = append(doc.Snippets, r)
		}
		assertContentJSONMatches(t, doc)
	}
}

func TestContentFileSnippetEncodingFailures(t *testing.T) {
	doc := contentDocument{Schema: 2, Snippets: []SnippetRecord{{Snippet: Snippet{ID: 1, Title: "first"},
		Revisions: []SnippetRevision{{Version: 1, ContentSource: strings.Repeat("<中文>", 200)}, {Version: 2, Title: "last"}}},
		{Snippet: Snippet{ID: 2, Title: "second"}}}, NextSnippetID: 3}
	want, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	failure := errors.New("injected snippet output failure")
	for _, after := range []int{0, bytes.Index(want, []byte(`"snippets"`)), bytes.Index(want, []byte(`"revisions"`)) + 16,
		bytes.Index(want, []byte(`"last"`)) + 2, len(want) - 1} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("after-%d-short-%t", after, short), func(t *testing.T) {
				writer := &interruptedContentValueWriter{remaining: after, failure: failure}
				wantErr := failure
				if short {
					writer.failure, wantErr = nil, io.ErrShortWrite
				}
				size, err := writeContentJSON(struct{ io.Writer }{writer}, doc, len(want))
				if !errors.Is(err, wantErr) || size != after || !bytes.Equal(writer.Bytes(), want[:after]) {
					t.Fatal("partial history output changed error or bytes", size, after, err)
				}
			})
		}
	}
	for _, invalid := range []string{"metadata", "later revision"} {
		t.Run(invalid, func(t *testing.T) {
			bad := SnippetRecord{Snippet: Snippet{ID: 1}, Revisions: []SnippetRevision{{Version: 1}, {Version: 2}}}
			stamp := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			if invalid == "metadata" {
				bad.UpdatedAt = stamp
			} else {
				bad.Revisions[1].CreatedAt = stamp
			}
			_, standardErr := json.Marshal(bad)
			_, err := writeContentJSON(io.Discard, contentDocument{Snippets: []SnippetRecord{bad}}, maxContentFileBytes)
			var marshaler *json.MarshalerError
			if standardErr == nil || !errors.As(err, &marshaler) {
				t.Fatal("invalid time was accepted", err)
			}
		})
	}
}

func TestContentFileSnippetEncodingCommitAndFailure(t *testing.T) {
	file, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	var s Snippet
	for i := range 5 {
		s, err = file.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version},
			SnippetInput{Title: "历史", ContentSource: fmt.Sprintf("<p>版本 %d 中文</p>", i), Visibility: SnippetShared}, snippetManager())
		if err != nil {
			t.Fatal(err)
		}
	}
	committed, digest := file.state, file.digest
	_, err = fileContentChange(t.Context(), file, func(candidate *MemoryRepository) (struct{}, error) {
		candidate.snippets[0].Revisions[4].CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		return struct{}{}, nil
	})
	if err == nil || file.state != committed || file.searchState.Load() != committed || file.digest != digest {
		t.Fatal("encoding failure published candidate", err)
	}
	raw, err := os.ReadFile(file.path)
	if err != nil || sha256.Sum256(raw) != digest {
		t.Fatal("encoding failure changed committed file", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(file.path), ".newpress-content-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("failed history encoding left temporary files", leftovers, err)
	}
	reopened, err := NewFileRepository(file.path, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetSnippet(t.Context(), s.ID, snippetManager())
	if err != nil || !reflect.DeepEqual(got, committed.snippets[0]) {
		t.Fatal("reopened history differs", err)
	}
}
