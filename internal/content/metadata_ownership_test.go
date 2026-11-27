package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type metadataLabels []string
type metadataObject struct{ Labels []string }
type metadataCustom struct{ labels []string }
type metadataText struct{ value []byte }
type metadataKey struct{ name string }
type metadataEmbedded struct{ Labels []string }
type metadataOuter struct{ metadataEmbedded }

func (v *metadataCustom) MarshalJSON() ([]byte, error) { return json.Marshal(v.labels) }
func (v *metadataText) MarshalText() ([]byte, error)   { return v.value, nil }
func (v *metadataKey) MarshalText() ([]byte, error)    { return []byte(v.name), nil }

func metadataJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAutosaveMetadataIsDetachedAtEveryBoundary(t *testing.T) {
	tests := []struct {
		name   string
		value  func() any
		mutate func(any)
	}{
		{"bytes", func() any { return []byte("one") }, func(v any) { copy(v.([]byte), "two") }},
		{"raw JSON", func() any { return json.RawMessage(`{"label":"one"}`) }, func(v any) { copy(v.(json.RawMessage), `{"label":"two"}`) }},
		{"named slice", func() any { return metadataLabels{"one"} }, func(v any) { v.(metadataLabels)[0] = "two" }},
		{"typed map", func() any { return map[string][]string{"labels": {"one"}} }, func(v any) { v.(map[string][]string)["labels"][0] = "two" }},
		{"typed slice", func() any { return []map[string]string{{"label": "one"}} }, func(v any) { v.([]map[string]string)[0]["label"] = "two" }},
		{"pointer object", func() any { return &metadataObject{[]string{"one"}} }, func(v any) { v.(*metadataObject).Labels[0] = "two" }},
		{"custom JSON", func() any { return &metadataCustom{[]string{"one"}} }, func(v any) { v.(*metadataCustom).labels[0] = "two" }},
		{"custom text", func() any { return &metadataText{[]byte("one")} }, func(v any) { copy(v.(*metadataText).value, "two") }},
		{"array with pointers", func() any { return [1]*metadataObject{{[]string{"one"}}} }, func(v any) { v.([1]*metadataObject)[0].Labels[0] = "two" }},
		{"embedded value", func() any { return metadataOuter{metadataEmbedded{[]string{"one"}}} }, func(v any) { v.(metadataOuter).Labels[0] = "two" }},
		{"text map key", func() any { return map[*metadataKey]string{{"one"}: "value"} }, func(v any) {
			for key := range v.(map[*metadataKey]string) {
				key.name = "two"
			}
		}},
		{"ordinary nested control", func() any { return map[string]any{"labels": []any{"one"}} }, func(v any) { v.(map[string]any)["labels"].([]any)[0] = "two" }},
	}
	for _, mode := range []string{"memory", "file"} {
		for _, phase := range []string{"input", "saved result", "read result"} {
			for _, test := range tests {
				t.Run(mode+"/"+phase+"/"+test.name, func(t *testing.T) {
					r, state, reopen := unicodeWriteRepository(t, mode)
					post, err := r.Create(t.Context(), PostInput{Title: "正式原文"})
					if err != nil {
						t.Fatal(err)
					}
					post.Meta = map[string]any{"plugin": test.value()}
					before := metadataJSON(t, post.Meta)
					saved, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, metadataJSON(t, saved.Snapshot.Meta)) {
						t.Fatal("save changed JSON metadata")
					}
					persisted := state()
					target := post.Meta
					if phase == "saved result" {
						target = saved.Snapshot.Meta
					} else if phase == "read result" {
						loaded, err := r.GetAutosave(t.Context(), post.ID, "user:1")
						if err != nil {
							t.Fatal(err)
						}
						target = loaded.Snapshot.Meta
					}
					if phase == "input" {
						test.mutate(target["plugin"])
					} else {
						// Persistent adapters return JSON-shaped values. Exercise
						// their nested buffers, while also exposing aliases in the
						// older implementation's original concrete Go values.
						switch value := target["plugin"].(type) {
						case map[string]any:
							value["changed"] = "two"
						case []any:
							value[0] = "two"
						case string:
							target["plugin"] = "two"
						default:
							test.mutate(value)
						}
					}
					got, err := r.GetAutosave(t.Context(), post.ID, "user:1")
					if err != nil || !bytes.Equal(before, metadataJSON(t, got.Snapshot.Meta)) || !got.UpdatedAt.Equal(saved.UpdatedAt) {
						t.Errorf("caller changed stored recovery without a save: %v", err)
					}
					if !bytes.Equal(persisted, state()) {
						t.Error("caller changed repository state")
					}
					loaded := reopen()
					disk, err := loaded.GetAutosave(t.Context(), post.ID, "user:1")
					if err != nil || !bytes.Equal(before, metadataJSON(t, disk.Snapshot.Meta)) {
						t.Errorf("persisted recovery changed: %v", err)
					}
				})
			}
		}
	}
}

type metadataOneEncoding struct {
	calls int
}

func (v *metadataOneEncoding) MarshalJSON() ([]byte, error) {
	v.calls++
	if v.calls != 1 {
		return nil, errors.New("metadata encoder invoked again")
	}
	return []byte(`{"number":9007199254740993,"labels":["one"]}`), nil
}

func TestAutosaveMetadataEncodingIsFrozenOnce(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			r, _, reopen := unicodeWriteRepository(t, mode)
			post, err := r.Create(t.Context(), PostInput{Title: "原稿"})
			if err != nil {
				t.Fatal(err)
			}
			custom := &metadataOneEncoding{}
			post.Meta = map[string]any{"plugin": custom}
			saved, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatalf("valid first encoding was not frozen: %v", err)
			}
			want := []byte(`{"plugin":{"labels":["one"],"number":9007199254740993}}`)
			if !bytes.Equal(metadataJSON(t, saved.Snapshot.Meta), want) {
				t.Error("save did not preserve encoded representation")
			}
			if _, err := r.Create(t.Context(), PostInput{Title: "另一篇内容", Slug: "other-post"}); err != nil {
				t.Fatalf("later transaction executed the caller's encoder: %v", err)
			}
			got, err := reopen().GetAutosave(t.Context(), post.ID, "user:1")
			if err != nil || !bytes.Equal(metadataJSON(t, got.Snapshot.Meta), want) || custom.calls != 1 {
				t.Errorf("saved metadata was encoded again or lost precision: calls=%d, %v", custom.calls, err)
			}
		})
	}
}

func TestAutosaveMetadataFailurePreservesRecovery(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, kind := range []string{"cycle", "unsupported", "nil and shared"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				r, state, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: "原稿"})
				if err != nil {
					t.Fatal(err)
				}
				input := Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}
				original, err := r.SaveAutosave(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				before := state()
				input.Snapshot.Meta = map[string]any{}
				switch kind {
				case "cycle":
					input.Snapshot.Meta["cycle"] = input.Snapshot.Meta
				case "unsupported":
					input.Snapshot.Meta["function"] = func() {}
				case "nil and shared":
					shared := map[string]any{"name": "one"}
					input.Snapshot.Meta = map[string]any{"left": shared, "right": shared, "pointer": (*metadataCustom)(nil), "bytes": []byte(nil), "slice": []string(nil), "map": map[string]string(nil)}
				}
				saved, err := r.SaveAutosave(t.Context(), input)
				if kind != "nil and shared" {
					if FieldErrors(err)["snapshot"] == "" || !bytes.Equal(before, state()) {
						t.Fatalf("bad metadata replaced recovery: %v", err)
					}
					got, err := reopen().GetAutosave(t.Context(), post.ID, input.OwnerKey)
					if err != nil || !bytes.Equal(autosaveJSON(t, got), autosaveJSON(t, original)) {
						t.Fatal("failed save changed the original recovery")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := metadataJSON(t, input.Snapshot.Meta)
				if !bytes.Equal(want, metadataJSON(t, saved.Snapshot.Meta)) {
					t.Error("typed nil changed its JSON meaning")
				}
				saved.Snapshot.Meta["left"].(map[string]any)["name"] = "two"
				if saved.Snapshot.Meta["right"].(map[string]any)["name"] != "one" {
					t.Error("JSON object copies still share mutable children")
				}
				got, err := reopen().GetAutosave(t.Context(), post.ID, "user:1")
				if err != nil || !bytes.Equal(want, metadataJSON(t, got.Snapshot.Meta)) {
					t.Fatalf("nil/shared metadata changed after reopen: %v", err)
				}
			})
		}
	}
}

func TestFileMetadataCandidateRollbackKeepsPreviousRecovery(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := r.Create(t.Context(), PostInput{Title: "原稿"})
	if err != nil {
		t.Fatal(err)
	}
	original := &metadataCustom{[]string{"one"}}
	post.Meta = map[string]any{"plugin": original}
	input := Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}
	saved, err := r.SaveAutosave(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := metadataJSON(t, saved.Snapshot.Meta)
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	// The original caller changes its encoder after a successful save. A later
	// transaction must still work from the independently frozen recovery.
	original.labels[0] = "changed after save"
	replacement := &metadataCustom{[]string{"replacement"}}
	input.Snapshot.Meta = map[string]any{"plugin": replacement}
	if err := os.Mkdir(filename+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveAutosave(t.Context(), input); err == nil {
		t.Fatal("replacement succeeded despite blocked rollback target")
	}
	replacement.labels[0] = "changed after failed save"
	got, err := r.GetAutosave(t.Context(), post.ID, input.OwnerKey)
	if err != nil || !got.UpdatedAt.Equal(saved.UpdatedAt) || !bytes.Equal(want, metadataJSON(t, got.Snapshot.Meta)) {
		t.Errorf("failed transaction changed previous recovery: %v", err)
	}
	after, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("failed transaction changed durable bytes")
	}
	loaded, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err = loaded.GetAutosave(t.Context(), post.ID, input.OwnerKey)
	if err != nil || !bytes.Equal(want, metadataJSON(t, got.Snapshot.Meta)) {
		t.Error("reopened state does not match the retained recovery")
	}
}
