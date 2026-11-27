package media

import (
	"errors"
	"strings"
	"testing"
)

func TestUploadSourceIsolationAndRestart(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	const payload = "%PDF-1.7\nsource isolation\n%%EOF"
	upload := func(source string) (Item, error) {
		return m.Upload(t.Context(), strings.NewReader(payload), UploadInput{Filename: "same.pdf", UploadSource: source}, 4096)
	}
	a, err := upload("avatar")
	if err != nil {
		t.Fatal(err)
	}
	b, err := upload("editor-body")
	if err != nil || a.ID == b.ID || a.ObjectKey == b.ObjectKey {
		t.Fatal(a, b, err)
	}
	if duplicate, err := upload("avatar"); !errors.Is(err, ErrDuplicate) || duplicate.ID != a.ID {
		t.Fatal(duplicate, err)
	}
	m, err = NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := upload("editor-body"); !errors.Is(err, ErrDuplicate) || duplicate.ID != b.ID {
		t.Fatal(duplicate, err)
	}
	trashed, err := m.Trash(a.ID, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(a.ID, trashed.Version); err != nil {
		t.Fatal(err)
	}
	_, reader, err := m.Open(b.ID)
	if err != nil {
		t.Fatal("deleting another entry removed this object", err)
	}
	reader.Close()
}
