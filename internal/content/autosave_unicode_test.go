package content

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type snapshotTextAlias string

func TestAutosaveRejectsLossyTextWithoutReplacingRecovery(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, existing := range []bool{false, true} {
			for _, field := range []string{
				"title", "excerpt", "source", "html", "cover", "author", "category",
				"category name", "category description", "tag name", "meta key", "meta value",
				"nested meta", "typed strings", "typed map key", "typed map value", "named string",
				"raw UTF8", "raw surrogate", "raw duplicate", "split fields",
			} {
				name := mode + "/create/" + field
				if existing {
					name = mode + "/replace/" + field
				}
				t.Run(name, func(t *testing.T) {
					r, state, reopen := unicodeWriteRepository(t, mode)
					post, err := r.Create(t.Context(), PostInput{Title: "正式原文", Slug: "snapshot", ContentSource: "<p>正式正文</p>", Category: "原分类", TagNames: []string{"原标签"}})
					if err != nil {
						t.Fatal(err)
					}
					input := Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: clonePost(post), ExpiresAt: time.Now().Add(time.Hour)}
					var original Autosave
					if existing {
						input.Snapshot.Title = "已有恢复点"
						original, err = r.SaveAutosave(t.Context(), input)
						if err != nil {
							t.Fatal(err)
						}
					}
					before := state()
					snapshot := &input.Snapshot
					const bad = "private-\xff"
					switch field {
					case "title":
						snapshot.Title = bad
					case "excerpt":
						snapshot.Excerpt = bad
					case "source":
						snapshot.ContentSource = bad
					case "html":
						snapshot.ContentHTML = "<p>" + bad + "</p>"
					case "cover":
						snapshot.CoverURL = "/media/" + bad
					case "author":
						snapshot.AuthorName = bad
					case "category":
						snapshot.Category = bad
					case "category name":
						snapshot.Categories[0].Name = bad
					case "category description":
						snapshot.Categories[0].Description = bad
					case "tag name":
						snapshot.Tags[0].Name = bad
					case "meta key":
						snapshot.Meta[bad] = "value"
					case "meta value":
						snapshot.Meta["plugin"] = bad
					case "nested meta":
						snapshot.Meta["plugin"] = []any{map[string]any{"text": bad}}
					case "typed strings":
						snapshot.Meta["plugin"] = []string{bad}
					case "typed map key":
						snapshot.Meta["plugin"] = map[string]string{bad: "value"}
					case "typed map value":
						snapshot.Meta["plugin"] = map[string]string{"text": bad}
					case "named string":
						snapshot.Meta["plugin"] = snapshotTextAlias(bad)
					case "raw UTF8":
						snapshot.Meta["plugin"] = json.RawMessage("{\"text\":\"" + bad + "\"}")
					case "raw surrogate":
						snapshot.Meta["plugin"] = json.RawMessage(`{"text":"\ud800"}`)
					case "raw duplicate":
						snapshot.Meta["plugin"] = json.RawMessage(`{"text":1,"text":2}`)
					case "split fields":
						snapshot.Title, snapshot.Excerpt = "\xc2", "\xa9"
					}
					_, err = r.SaveAutosave(t.Context(), input)
					if len(FieldErrors(err)) == 0 {
						t.Errorf("lossy recovery input accepted without validation error: %v", err)
					}
					if err != nil && strings.Contains(err.Error(), "private-") {
						t.Error("validation error exposed snapshot contents")
					}
					if !bytes.Equal(before, state()) {
						t.Error("rejected recovery changed persisted state")
					}
					if err == nil && mode == "file" {
						// A round trip can fail entirely for a duplicate raw object;
						// read the file through the same checked loader to expose it.
						loaded := reopen()
						got, readErr := loaded.GetAutosave(t.Context(), post.ID, input.OwnerKey)
						if readErr != nil || got.Snapshot.Title != snapshot.Title || got.Snapshot.Excerpt != snapshot.Excerpt {
							t.Errorf("accepted recovery changed after reopening: %v", readErr)
						}
					}
					if existing {
						got, loadErr := r.GetAutosave(t.Context(), post.ID, input.OwnerKey)
						if loadErr != nil || !reflect.DeepEqual(got, original) {
							t.Errorf("failed replacement lost original recovery: %v", loadErr)
						}
					}
				})
			}
		}
	}
}

func TestAutosaveUnicodeKeepsDraftAndMetadataCompatibility(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, text := range []string{"中文😀", "原文�", `字面\ud800`} {
			t.Run(mode+"/"+text, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: "正式标题", Slug: "draft", ContentSource: "正文"})
				if err != nil {
					t.Fatal(err)
				}
				snapshot := clonePost(post)
				snapshot.Title = text
				snapshot.ContentSource, snapshot.ContentHTML = "", ""
				snapshot.Excerpt = ""
				snapshot.Tags = []Term{{Name: text}}
				snapshot.Meta = map[string]any{"text": text, "nested": []any{map[string]any{"key": text}}, "strings": []string{text}, "map": map[string]string{"label": text}, "bytes": []byte{0xff, 0x80}, "raw": json.RawMessage(`{"emoji":"\ud83d\ude00","original":"�","literal":"\\ud800"}`)}
				saved, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				loaded := reopen()
				got, err := loaded.GetAutosave(t.Context(), post.ID, "user:1")
				if err != nil || got.Snapshot.Title != text || got.Snapshot.ContentSource != "" || got.Snapshot.Excerpt != "" || got.Snapshot.Tags[0].Name != text || !got.UpdatedAt.Equal(saved.UpdatedAt) {
					t.Fatalf("valid draft changed: %#v %v", got, err)
				}
				beforeMeta, err := json.Marshal(snapshot.Meta)
				if err != nil {
					t.Fatal(err)
				}
				afterMeta, err := json.Marshal(got.Snapshot.Meta)
				var expected, actual any
				if err != nil || json.Unmarshal(beforeMeta, &expected) != nil || json.Unmarshal(afterMeta, &actual) != nil || !reflect.DeepEqual(expected, actual) {
					t.Error("valid metadata or binary payload changed")
				}
				canonical, err := loaded.GetByID(t.Context(), post.ID)
				revisions, revErr := loaded.ListRevisions(t.Context(), post.ID, 10)
				if err != nil || revErr != nil || canonical.Title != post.Title || canonical.Version != post.Version || len(revisions) != 1 {
					t.Error("autosave changed canonical content or revisions")
				}
			})
		}
	}
}
