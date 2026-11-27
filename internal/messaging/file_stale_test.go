package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRejectsWritesFromStaleSnapshot(t *testing.T) {
	for _, operation := range []string{"send", "replay", "read", "notice-read", "notice-clear"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messages.json")
			writer, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			first := sendFixture(t, writer, 1, 2, 1)
			stale, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			beforeMemory, err := json.Marshal(stale.data)
			if err != nil {
				t.Fatal(err)
			}
			second := sendFixture(t, writer, 2, 1, 2)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "send":
				_, err = stale.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: "new message", Nonce: "new-message-nonce-0003"})
			case "replay":
				_, err = stale.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: first.Content, Nonce: first.Nonce})
			case "read":
				err = stale.Read(t.Context(), 2, 1, first.ID)
			case "notice-read":
				err = stale.UpdateNotices(t.Context(), 2, first.ID, "read_all")
			case "notice-clear":
				err = stale.UpdateNotices(t.Context(), 2, first.ID, "clear")
			}
			if !errors.Is(err, ErrChanged) {
				t.Error("stale writer did not report an external change", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("stale snapshot overwrote newer durable messages", err)
			}
			afterMemory, err := json.Marshal(stale.data)
			if err != nil || !bytes.Equal(beforeMemory, afterMemory) {
				t.Error("rejected write changed stale in-memory state", err)
			}
			reloaded, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := reloaded.Messages(t.Context(), 1, 2, MessageQuery{})
			if err != nil || len(page.Items) != 2 || page.Items[0].ID != second.ID || page.Items[0].Nonce != second.Nonce {
				t.Fatal("newer message was lost", page, err)
			}
			third := sendFixture(t, reloaded, 1, 2, 3)
			if third.ID != second.ID+1 {
				t.Fatal("reopened store did not continue from newer facts", third)
			}
		})
	}
}

func TestFileStoreDetectsCreationAfterEmptyLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	stale, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sendFixture(t, writer, 1, 2, 1)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: "new", Nonce: "stale-empty-nonce-0002"}); !errors.Is(err, ErrChanged) {
		t.Error("empty snapshot did not detect a file created after loading", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || len(stale.data.Messages) != 0 {
		t.Fatal("rejected first write changed durable or in-memory data", err)
	}
}

func TestFileStoreDetectsExternalChangesAndRecoversAfterExactRestore(t *testing.T) {
	for _, change := range []string{"same-size-and-mtime", "corrupt", "deleted"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messages.json")
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			input := Input{SenderID: 1, RecipientID: 2, Content: "before", Nonce: "external-change-nonce-1"}
			first, err := store.Send(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			var changed []byte
			switch change {
			case "same-size-and-mtime":
				changed = bytes.Replace(original, []byte(`"before"`), []byte(`"after!"`), 1)
				if bytes.Equal(changed, original) || len(changed) != len(original) {
					t.Fatal("fixture must change bytes without changing file size")
				}
			case "corrupt":
				changed = []byte("external incomplete write")
			case "deleted":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if change != "deleted" {
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Read(t.Context(), 2, 1, first.ID); !errors.Is(err, ErrChanged) {
				t.Error("external change was not reported", err)
			}
			after, err := os.ReadFile(path)
			if change == "deleted" {
				if !os.IsNotExist(err) {
					t.Error("stale writer recreated a deleted file", err)
				}
			} else if err != nil || !bytes.Equal(after, changed) {
				t.Error("stale writer replaced external bytes", err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.Read(t.Context(), 2, 1, first.ID); err != nil {
				t.Fatal("exact restored snapshot should allow a normal write", err)
			}
			reloaded, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := reloaded.Messages(t.Context(), 1, 2, MessageQuery{})
			if err != nil || len(page.Items) != 1 || page.Items[0].Content != input.Content || page.Items[0].ReadAt == nil {
				t.Fatal("recovery lost message or read receipt", page, err)
			}
		})
	}
}

func TestFileStoreKeepsResolvedPathAfterWorkingDirectoryChange(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	store, err := NewFileStore("messages.json")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := t.TempDir()
	t.Chdir(unrelated)
	sendFixture(t, store, 1, 2, 1)
	if _, err := os.Stat(filepath.Join(unrelated, "messages.json")); !os.IsNotExist(err) {
		t.Fatal("working directory change redirected the message store", err)
	}
	reloaded, err := NewFileStore(filepath.Join(directory, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := reloaded.Messages(t.Context(), 1, 2, MessageQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("message was not persisted in the original store", page, err)
	}
}
