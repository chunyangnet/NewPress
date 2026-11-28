package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadVerifiesRetainedOriginalBeforeReuse(t *testing.T) {
	for _, state := range []string{"valid", "same-size-corrupt", "wrong-size", "index-failure"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			manager, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			payload := testPNG(t, 4, 3)
			input := UploadInput{Filename: "original.png"}
			original, err := manager.Upload(t.Context(), bytes.NewReader(payload), input, 4096)
			if err != nil {
				t.Fatal(err)
			}
			// An interrupted index commit or an older restored index can leave
			// an unindexed content-addressed object behind.
			if err := os.Remove(manager.indexPath); err != nil {
				t.Fatal(err)
			}
			manager, err = NewManager(root)
			if err != nil || manager.Snapshot().Count != 0 {
				t.Fatalf("prepare unindexed object: %v", err)
			}
			target := filepath.Join(root, filepath.FromSlash(original.ObjectKey))
			retained := bytes.Clone(payload)
			switch state {
			case "same-size-corrupt":
				retained[len(retained)-1] ^= 0xFF
			case "wrong-size":
				retained = retained[:len(retained)-1]
			case "index-failure":
				manager.indexPath = t.TempDir()
			}
			if err := os.WriteFile(target, retained, 0600); err != nil {
				t.Fatal(err)
			}
			before := manager.Snapshot()
			item, err := manager.Upload(t.Context(), bytes.NewReader(payload), input, 4096)
			if state == "valid" {
				if err != nil || item.ChecksumSHA256 != original.ChecksumSHA256 || manager.Snapshot().Count != 1 {
					t.Fatalf("valid object was not reused: %+v %v", item, err)
				}
				_, reader, err := manager.Open(item.ID)
				if err != nil {
					t.Fatal(err)
				}
				actual, readErr := io.ReadAll(reader)
				reader.Close()
				if readErr != nil || !bytes.Equal(actual, payload) {
					t.Fatal("reuse returned different bytes")
				}
			} else {
				if state == "index-failure" {
					if err == nil {
						t.Error("index failure was ignored")
					}
				} else if !errors.Is(err, ErrObjectCorrupt) {
					t.Errorf("damaged retained object was reused: err=%v count=%d", err, manager.Snapshot().Count)
				}
				if manager.Snapshot().Revision != before.Revision || manager.Snapshot().Count != 0 {
					t.Error("failed reuse changed media metadata")
				}
				actual, readErr := os.ReadFile(target)
				if readErr != nil || !bytes.Equal(actual, retained) {
					t.Fatal("failed upload removed or overwrote a retained object")
				}
			}
			staging, err := os.ReadDir(filepath.Join(root, "tmp"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("staging files leaked: %v %v", staging, err)
			}
		})
	}
}

func TestDuplicateUploadRejectsUnavailableOriginal(t *testing.T) {
	for _, storage := range []string{"memory", "file"} {
		for _, state := range []string{"valid", "missing", "same-size-corrupt"} {
			t.Run(storage+"/"+state, func(t *testing.T) {
				root := ""
				if storage == "file" {
					root = t.TempDir()
				}
				manager, err := NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
				payload := testPNG(t, 4, 3)
				input := UploadInput{Filename: "duplicate.png"}
				item, err := manager.Upload(t.Context(), bytes.NewReader(payload), input, 4096)
				if err != nil {
					t.Fatal(err)
				}
				before := manager.Snapshot()
				corrupt := bytes.Clone(payload)
				corrupt[len(corrupt)-1] ^= 0xFF
				if storage == "file" {
					path := filepath.Join(root, filepath.FromSlash(item.ObjectKey))
					if state == "missing" {
						err = os.Remove(path)
					}
					if state == "same-size-corrupt" {
						err = os.WriteFile(path, corrupt, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if state == "missing" {
						delete(manager.memoryObjects, item.ObjectKey)
					}
					if state == "same-size-corrupt" {
						manager.memoryObjects[item.ObjectKey] = corrupt
					}
				}
				duplicate, err := manager.Upload(t.Context(), bytes.NewReader(payload), input, 4096)
				want := ErrDuplicate
				if state == "missing" {
					want = ErrObjectMissing
				}
				if state == "same-size-corrupt" {
					want = ErrObjectCorrupt
				}
				if !errors.Is(err, want) || state == "valid" && duplicate.ID != item.ID {
					t.Errorf("duplicate result=%d error=%v want=%v", duplicate.ID, err, want)
				}
				if manager.Snapshot().Revision != before.Revision {
					t.Fatal("duplicate verification changed the library")
				}
			})
		}
	}
}

func TestVerifyRetainedObjectHonorsFinalReadCancellation(t *testing.T) {
	payload := []byte("retained object fixture")
	digest := sha256.Sum256(payload)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &finalUploadReader{data: payload, done: cancel}
	err := verifyObjectContent(ctx, reader, Item{SizeBytes: int64(len(payload)), ChecksumSHA256: hex.EncodeToString(digest[:])})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled integrity check returned %v", err)
	}
}
