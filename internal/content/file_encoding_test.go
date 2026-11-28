package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func assertContentJSONMatches(t *testing.T, document contentDocument) {
	t.Helper()
	want, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	var got bytes.Buffer
	size, err := writeContentJSON(&got, document, len(want))
	if err != nil || size != len(want) || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("streamed JSON differs: size=%d want=%d error=%v\ngot=%s\nwant=%s", size, len(want), err, got.Bytes(), want)
	}
	got.Reset()
	if _, err := writeContentJSON(&got, document, len(want)-1); !errors.Is(err, errContentFileCapacity) || got.Len() >= len(want) {
		t.Fatal("byte limit did not include the trailing newline", got.Len(), err)
	}
}

func TestContentJSONAllFieldsAndEmptyValues(t *testing.T) {
	// Populate every top-level field independently of the streaming field list;
	// adding a document field without serializing it must fail this comparison.
	for _, shape := range []string{"nil", "empty", "populated"} {
		t.Run(shape, func(t *testing.T) {
			var document contentDocument
			value := reflect.ValueOf(&document).Elem()
			if shape != "nil" {
				for i := range value.NumField() {
					field := value.Field(i)
					switch field.Kind() {
					case reflect.Int, reflect.Int64:
						field.SetInt(int64(i + 1))
					case reflect.String:
						field.SetString("中文 <>&\u2028\u2029 \"\\")
					case reflect.Slice:
						length := 0
						if shape == "populated" {
							length = 1
						}
						field.Set(reflect.MakeSlice(field.Type(), length, length))
					default:
						t.Fatalf("add a fixture for contentDocument.%s", value.Type().Field(i).Name)
					}
				}
			}
			assertContentJSONMatches(t, document)
		})
	}
}

func TestContentJSONRecordsMatchStandardEncoding(t *testing.T) {
	random := rand.New(rand.NewSource(20261128))
	texts := []string{"", "plain", "中文 😀", "<>&", "\u2028\u2029", "\x00\t\n\"\\", "bad-\xff"}
	for trial := range 200 {
		document := contentDocument{Schema: 1 + trial%3, Kind: "newpress.content", NextID: int64(trial + 1)}
		for i := range random.Intn(12) {
			text := texts[random.Intn(len(texts))]
			document.Posts = append(document.Posts, Post{ID: int64(i), Title: text, ContentSource: text, ContentHTML: text,
				Meta: map[string]any{"a": json.Number("9007199254740993"), "z": map[string]any{text: []any{nil, false, text, i}}}})
		}
		if trial%2 == 0 {
			document.Revisions, document.Autosaves = []Revision{}, []Autosave{}
		}
		assertContentJSONMatches(t, document)
	}
}

type failingContentWriter struct{ failure error }

func (w failingContentWriter) Write(p []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	return len(p) - 1, nil
}

func TestContentJSONErrorsAndShortWrites(t *testing.T) {
	failure := errors.New("injected write failure")
	for _, cause := range []error{failure, nil} {
		want := cause
		if want == nil {
			want = io.ErrShortWrite
		}
		if _, err := writeContentJSON(failingContentWriter{cause}, contentDocument{}, maxContentFileBytes); !errors.Is(err, want) {
			t.Fatal("writer error lost", err)
		}
	}
	for _, invalid := range []any{math.NaN(), make(chan int), json.Number("invalid")} {
		document := contentDocument{Posts: []Post{{Meta: map[string]any{"invalid": invalid}}}}
		if _, err := writeContentJSON(io.Discard, document, maxContentFileBytes); err == nil {
			t.Fatal("invalid metadata encoded", invalid)
		}
	}
}

func TestContentFileStreamBytesDigestAndReopen(t *testing.T) {
	state := contentFileEncodingFixture(t, 32, 1024)
	state.revisions[1] = []Revision{{ID: 2, PostID: 1, Version: 1, Snapshot: clonePost(state.posts[0]), Actor: "fixture", CreatedAt: state.posts[0].UpdatedAt}}
	state.nextRevisionID = 3
	want, err := json.Marshal(contentDocumentForEncoding(state))
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join(t.TempDir(), "content.json")
	digest, err := writeContentState(t.Context(), path, state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) || digest != sha256.Sum256(got) {
		t.Fatal("persisted bytes or digest differ", err)
	}
	reopened, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Ready(t.Context()); err != nil {
		t.Fatal("reopened digest rejected", err)
	}
	page, err := reopened.SearchPublished(t.Context(), PublishedQuery{Search: "needle"})
	if err != nil || page.Total != 32 {
		t.Fatal("streamed content did not reopen", page.Total, err)
	}
}

func TestContentFileStreamFailuresKeepCommittedState(t *testing.T) {
	for _, failure := range []string{"encode", "write", "cancel", "replace"} {
		t.Run(failure, func(t *testing.T) {
			r, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), true)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.path)
			if err != nil {
				t.Fatal(err)
			}
			state, digest := r.state, r.digest
			if failure == "encode" || failure == "replace" {
				if failure == "replace" {
					if err := os.Mkdir(r.path+".bak", 0700); err != nil {
						t.Fatal(err)
					}
				}
				_, err = fileContentChange(t.Context(), r, func(candidate *MemoryRepository) (struct{}, error) {
					candidate.posts[0].Title = "changed candidate"
					if failure == "encode" {
						candidate.posts[0].Meta = map[string]any{"bad": math.NaN()}
					}
					return struct{}{}, nil
				})
			} else {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				err = replaceContentFile(ctx, r.path, func(w io.Writer) error {
					if _, err := io.WriteString(w, "partial candidate"); err != nil {
						return err
					}
					if failure == "cancel" {
						cancel()
						return nil
					}
					return fmt.Errorf("injected write failure")
				})
			}
			if err == nil {
				t.Fatal("injected failure accepted")
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			after, readErr := os.ReadFile(r.path)
			if readErr != nil || !bytes.Equal(before, after) || r.state != state || r.digest != digest || r.searchState.Load() != state {
				t.Fatal("failed stream changed committed state", readErr)
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(r.path), ".newpress-content-*.tmp"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("temporary file leaked", leftovers, err)
			}
		})
	}
}

func BenchmarkContentFileStreamEncode(b *testing.B) {
	for _, fixture := range []struct{ count, bytes int }{{1000, 1024}, {10000, 1024}, {1000, 16384}} {
		b.Run(fmt.Sprintf("%d-%d", fixture.count, fixture.bytes), func(b *testing.B) {
			state := contentFileEncodingFixture(b, fixture.count, fixture.bytes)
			payload, err := encodeContentDocument(state)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "content.json")
			if err := writeContentFile(path, payload); err != nil {
				b.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := encodeContentState(io.Discard, file.state); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
