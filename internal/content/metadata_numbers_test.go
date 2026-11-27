package content

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func metadataNumberCases() map[string]any {
	return map[string]any{
		"large integer":  int64(9007199254740993),
		"max integer":    int64(math.MaxInt64),
		"max unsigned":   uint64(math.MaxUint64),
		"decimal":        json.Number("1234567890.1234567890123456789"),
		"large exponent": json.Number("1e400"),
		"tiny exponent":  json.Number("1e-400"),
		"raw nested":     json.RawMessage(`{"numbers":[9007199254740993,0.1234567890123456789]}`),
	}
}

func TestAutosaveMetadataNumbersSurviveReloadAndUnrelatedWrites(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for name, value := range metadataNumberCases() {
			t.Run(mode+"/"+name, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: "原稿"})
				if err != nil {
					t.Fatal(err)
				}
				post.Meta = map[string]any{"number": value}
				want := metadataJSON(t, post.Meta)
				saved, err := r.SaveAutosave(t.Context(), Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil || !bytes.Equal(want, metadataJSON(t, saved.Snapshot.Meta)) {
					t.Fatalf("save changed a number: %v", err)
				}
				loaded := reopen()
				got, err := loaded.GetAutosave(t.Context(), post.ID, "user:1")
				if err != nil || !bytes.Equal(want, metadataJSON(t, got.Snapshot.Meta)) {
					t.Errorf("reload lost metadata precision: got=%s, want=%s, err=%v", metadataJSON(t, got.Snapshot.Meta), want, err)
				}
				if _, err := loaded.Create(t.Context(), PostInput{Title: "触发另一次持久化", Slug: "other-post"}); err != nil {
					t.Fatal(err)
				}
				got, err = reopen().GetAutosave(t.Context(), post.ID, "user:1")
				if err != nil || !bytes.Equal(want, metadataJSON(t, got.Snapshot.Meta)) {
					t.Error("unrelated write persisted a rounded metadata number")
				}
			})
		}
	}
}

func TestFileMetadataNumbersSurviveCanonicalAndRevisionLifecycle(t *testing.T) {
	for name, value := range metadataNumberCases() {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "content.json")
			r, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			post, err := r.Create(t.Context(), PostInput{Title: "第一版", Slug: "precision"})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var document contentDocument
			if err := json.Unmarshal(payload, &document); err != nil {
				t.Fatal(err)
			}
			document.Posts[0].Meta["number"] = value
			document.Revisions[0].Snapshot.Meta["number"] = value
			want := metadataJSON(t, document.Posts[0].Meta)
			if err := os.WriteFile(filename, autosaveJSON(t, document), 0600); err != nil {
				t.Fatal(err)
			}
			r, err = NewFileRepository(filename, false)
			if err != nil {
				t.Fatalf("valid metadata number made file unreadable: %v", err)
			}
			check := func(label string, p Post) {
				t.Helper()
				if got := metadataJSON(t, p.Meta); !bytes.Equal(got, want) {
					t.Errorf("%s changed metadata: got=%s want=%s", label, got, want)
				}
			}
			current, err := r.GetByID(t.Context(), post.ID)
			if err != nil {
				t.Fatal(err)
			}
			check("canonical reload", current)
			first, err := r.GetRevision(t.Context(), post.ID, document.Revisions[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			check("revision reload", first.Snapshot)
			updated, err := r.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, PostInput{Title: "第二版", Slug: post.Slug})
			if err != nil {
				t.Fatal(err)
			}
			check("canonical update", updated)
			if _, err := r.RestoreRevision(t.Context(), post.ID, first.ID, Mutation{ExpectedVersion: updated.Version}); err != nil {
				t.Fatal(err)
			}
			r, err = NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			current, err = r.GetByID(t.Context(), post.ID)
			if err != nil {
				t.Fatal(err)
			}
			check("restored canonical", current)
			revisions, err := r.ListRevisions(t.Context(), post.ID, 10)
			if err != nil || len(revisions) != 3 {
				t.Fatalf("revision lifecycle: %d, %v", len(revisions), err)
			}
			for _, revision := range revisions {
				check("retained revision", revision.Snapshot)
			}
		})
	}
}
