package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type finalUploadReader struct {
	data []byte
	done func()
}

func (r *finalUploadReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		r.done()
		return n, io.EOF
	}
	return n, nil
}

func TestUploadCancelledBeforeCommit(t *testing.T) {
	for _, storage := range []string{"memory", "file"} {
		for _, stage := range []string{"last-read", "waiting-for-lock", "duplicate"} {
			t.Run(storage+"/"+stage, func(t *testing.T) {
				root := ""
				if storage == "file" {
					root = t.TempDir()
				}
				manager, err := NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
				payload := testPNG(t, 3, 2)
				input := UploadInput{Filename: "cancelled.png"}
				if stage == "duplicate" {
					if _, err := manager.Upload(t.Context(), bytes.NewReader(payload), input, 1024); err != nil {
						t.Fatal(err)
					}
				}
				before, nextID := manager.Snapshot(), manager.nextID
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				reader := &finalUploadReader{data: payload, done: cancel}
				if stage == "waiting-for-lock" {
					consumed := make(chan struct{})
					reader.done = func() { close(consumed) }
					manager.mu.Lock()
					result := make(chan error, 1)
					go func() {
						_, uploadErr := manager.Upload(ctx, reader, input, 1024)
						result <- uploadErr
					}()
					<-consumed
					cancel()
					manager.mu.Unlock()
					err = <-result
				} else {
					_, err = manager.Upload(ctx, reader, input, 1024)
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancelled upload returned %v; media delta=%d", err, manager.Snapshot().Count-before.Count)
				}
				if manager.Snapshot().Count != before.Count || manager.nextID != nextID {
					t.Error("cancelled upload changed media metadata")
				}
				if storage == "memory" {
					if len(manager.memoryObjects) != before.Count {
						t.Error("cancelled upload left a memory object")
					}
					return
				}
				entries, err := os.ReadDir(filepath.Join(root, "tmp"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("staging files leaked: %v, %v", entries, err)
				}
				objects := 0
				if err := filepath.WalkDir(filepath.Join(root, "objects"), func(_ string, entry fs.DirEntry, err error) error {
					if err == nil && !entry.IsDir() {
						objects++
					}
					return err
				}); err != nil || objects != before.Count {
					t.Errorf("cancelled upload left objects: count=%d err=%v", objects, err)
				}
				reloaded, err := NewManager(root)
				if err != nil || reloaded.Snapshot().Count != before.Count {
					t.Errorf("cancelled upload changed persistent metadata: %v", err)
				}
			})
		}
	}
}
