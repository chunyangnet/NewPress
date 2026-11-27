package content

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func unicodeContentFixture(t *testing.T) ([]byte, int64) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "content.json")
	repository, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repository.Create(t.Context(), PostInput{Title: "unicode-fixture", Slug: "unicode-post", ContentSource: "<p>原文</p>"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return body, post.ID
}

func TestContentRejectsLossyUnicodeWithoutRecovery(t *testing.T) {
	base, _ := unicodeContentFixture(t)
	for _, test := range []struct{ name, title string }{
		{"invalid UTF8", "\"private-\xff\""},
		{"high surrogate", `"private-\ud800"`},
		{"low surrogate", `"private-\udfff"`},
	} {
		for _, rollbackOnly := range []bool{false, true} {
			name := test.name + "/primary with valid rollback"
			if rollbackOnly {
				name = test.name + "/rollback with missing primary"
			}
			t.Run(name, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "content.json")
				body := bytes.ReplaceAll(base, []byte(`"unicode-fixture"`), []byte(test.title))
				if bytes.Equal(body, base) {
					t.Fatal("fixture did not change title")
				}
				files := map[string][]byte{filename: body, filename + ".bak": base}
				if rollbackOnly {
					files = map[string][]byte{filename + ".bak": body}
				}
				for path, data := range files {
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if repository, err := NewFileRepository(filename, true); err == nil || repository != nil {
					t.Errorf("corrupt text loaded or silently recovered: %v", err)
				}
				for path, want := range files {
					if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, want) {
						t.Errorf("failed load changed a source file: %v", err)
					}
				}
				if rollbackOnly {
					if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
						t.Error("invalid rollback published a primary file")
					}
				}
				entries, err := os.ReadDir(filepath.Dir(filename))
				if err != nil || len(entries) != len(files) {
					t.Errorf("failed validation left additional files: %v", err)
				}
			})
		}
	}
}

func TestContentPreservesValidUnicodeAcrossSaveAndRestart(t *testing.T) {
	base, postID := unicodeContentFixture(t)
	for _, test := range []struct{ name, title, want string }{
		{"Chinese and emoji", `"发布\ud83d\ude00"`, "发布😀"},
		{"literal surrogate spelling", `"\\ud800"`, `\ud800`},
		{"intentional replacement character", `"�\ufffd"`, "��"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "content.json")
			body := bytes.ReplaceAll(base, []byte(`"unicode-fixture"`), []byte(test.title))
			if bytes.Equal(body, base) {
				t.Fatal("fixture did not change title")
			}
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			repository, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
				t.Fatalf("read changed the original file: %v", err)
			}
			if _, err := repository.Create(t.Context(), PostInput{Title: "触发保存", Slug: "next-post"}); err != nil {
				t.Fatal(err)
			}
			repository, err = NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			post, err := repository.GetByID(t.Context(), postID)
			if err != nil || post.Title != test.want {
				t.Fatalf("saved title changed: %q, %v", post.Title, err)
			}
			revisions, err := repository.ListRevisions(t.Context(), postID, 10)
			if err != nil || len(revisions) != 1 || revisions[0].Snapshot.Title != test.want {
				t.Fatalf("saved revision changed: %#v, %v", revisions, err)
			}
		})
	}
}
