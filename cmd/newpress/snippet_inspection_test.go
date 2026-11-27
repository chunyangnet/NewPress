package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/dirlock"
)

func TestSnippetInspectionCommandValidationAndLocalLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uncreated")
	t.Setenv("NEWPRESS_DATA_DIR", root)
	t.Setenv("NEWPRESS_DATABASE_URL", "")
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", "")
	for _, args := range [][]string{nil, {"repair"}, {"inspect", "--limit", "0"}, {"inspect", "--limit", "101"}, {"inspect", "--offset", "-1"}, {"inspect", "--offset", "1000001"}, {"inspect", "--file", " "}, {"inspect", "extra"}, {"inspect", "--replacement", "x"}} {
		if err := run(append([]string{"snippet", "records"}, args...)); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatal(args, err)
		}
	}
	if err := run([]string{"snippet", "records", "inspect"}); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("inspection created missing data directory")
	}
	path := filepath.Join(root, "content.json")
	r, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "private", ContentSource: "<p>private</p>", Visibility: content.SnippetPrivate}, content.SnippetAccess{Identity: "legacy:cli", Actor: "cli", Read: true, Manage: true}); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	lock, err := dirlock.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = run([]string{"snippet", "records", "inspect"}); err == nil || !strings.Contains(err.Error(), "lock") {
		lock.Close()
		t.Fatal("file inspection bypassed running-site lock", err)
	}
	lock.Close()
	if err = run([]string{"snippet", "records", "inspect"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("inspection changed file")
	}
	if err = os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	if err = run([]string{"snippet", "records", "inspect"}); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection restored rollback file")
	}
}
