package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrashLifecyclePreservesIdentityAndFilters(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			root := ""
			if disk {
				root = t.TempDir()
			}
			m, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			payload := testPNG(t, 3, 2)
			original, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "retained.png", Title: "Original", AltText: "Alt"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			document, err := m.Upload(t.Context(), strings.NewReader("%PDF-1.7\n%%EOF\n"), UploadInput{Filename: "guide.pdf"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Delete(original.ID, original.Version); !errors.Is(err, ErrNotTrashed) {
				t.Fatal(err)
			}
			trashed, err := m.Trash(original.ID, original.Version)
			if err != nil || trashed.Version != 2 || trashed.Status() != StatusTrash || trashed.TrashedAt.Before(original.CreatedAt) {
				t.Fatalf("trash = %+v %v", trashed, err)
			}
			if _, err := m.Trash(original.ID, original.Version); !errors.Is(err, ErrVersionConflict) {
				t.Fatal(err)
			}
			if _, err := m.Trash(trashed.ID, trashed.Version); !errors.Is(err, ErrTrashed) {
				t.Fatal(err)
			}
			if _, _, err := m.Open(original.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			_, stream, err := m.OpenForManagement(original.ID)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := io.ReadAll(stream)
			_ = stream.Close()
			if err != nil || !bytes.Equal(retained, payload) {
				t.Fatal("retained bytes differ", err)
			}
			duplicate, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "again.png", Title: "Do not overwrite"}, 4096)
			if !errors.Is(err, ErrDuplicateTrash) || duplicate != trashed {
				t.Fatalf("trash duplicate = %+v %v", duplicate, err)
			}
			active, err := m.List(Query{})
			if err != nil || active.Total != 1 || active.Items[0].ID != document.ID || active.TotalBytes != document.SizeBytes || active.KindCounts[KindImage] != 0 || active.StatusCounts[StatusTrash] != 1 {
				t.Fatalf("active = %+v %v", active, err)
			}
			trash, err := m.List(Query{Status: StatusTrash, Kind: KindImage, Search: "Original"})
			if err != nil || trash.Total != 1 || trash.Items[0] != trashed || trash.TotalBytes != original.SizeBytes {
				t.Fatalf("trash list = %+v %v", trash, err)
			}
			all, err := m.List(Query{Status: StatusAll, Limit: 1, Offset: 1})
			if err != nil || all.Total != 2 || len(all.Items) != 1 {
				t.Fatalf("all = %+v %v", all, err)
			}
			if _, err := m.List(Query{Status: "deleted"}); !errors.Is(err, ErrInvalidInput) {
				t.Fatal(err)
			}
			if disk {
				m, err = NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
			}
			if snapshot := m.Snapshot(); snapshot.Count != 2 || snapshot.ActiveCount != 1 || snapshot.TrashCount != 1 || snapshot.TotalBytes != original.SizeBytes+document.SizeBytes {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if _, err := m.Restore(t.Context(), original.ID, original.Version); !errors.Is(err, ErrVersionConflict) {
				t.Fatal(err)
			}
			restored, err := m.Restore(t.Context(), trashed.ID, trashed.Version)
			if err != nil || restored.Status() != StatusActive || restored.Version != 3 || restored.PublicURL != original.PublicURL || restored.ObjectKey != original.ObjectKey || restored.ChecksumSHA256 != original.ChecksumSHA256 || restored.Title != original.Title || restored.AltText != original.AltText || restored.CreatedAt != original.CreatedAt {
				t.Fatalf("restored = %+v %v", restored, err)
			}
			if _, err := m.Restore(t.Context(), restored.ID, restored.Version); !errors.Is(err, ErrNotTrashed) {
				t.Fatal(err)
			}
			_, stream, err = m.Open(restored.ID)
			if err != nil {
				t.Fatal(err)
			}
			_ = stream.Close()
			trashed, err = m.Trash(restored.ID, restored.Version)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Delete(trashed.ID, trashed.Version); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Get(original.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			next, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "new.png"}, 4096)
			if err != nil || next.ID <= document.ID {
				t.Fatalf("purge did not release checksum or preserve sequence: %+v %v", next, err)
			}
			if disk {
				if _, err := NewManager(root); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTrashTransitionsRollBackOnIndexFailure(t *testing.T) {
	for _, action := range []string{"trash", "restore", "delete"} {
		t.Run(action, func(t *testing.T) {
			m, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			payload := testPNG(t, 2, 2)
			item, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "rollback.png"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if action != "trash" {
				item, err = m.Trash(item.ID, item.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := m.Snapshot()
			indexPath := m.indexPath
			m.indexPath = t.TempDir()
			switch action {
			case "trash":
				_, err = m.Trash(item.ID, item.Version)
			case "restore":
				_, err = m.Restore(t.Context(), item.ID, item.Version)
			case "delete":
				err = m.Delete(item.ID, item.Version)
			}
			if err == nil {
				t.Fatal("index write unexpectedly succeeded")
			}
			m.indexPath = indexPath
			current, err := m.Get(item.ID)
			if err != nil || current != item || m.Snapshot().Revision != before.Revision || m.Snapshot().UpdatedAt != before.UpdatedAt {
				t.Fatalf("in-memory rollback failed: %+v %v", current, err)
			}
			reloaded, err := NewManager(m.root)
			if err != nil {
				t.Fatal(err)
			}
			current, stream, err := reloaded.OpenForManagement(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(stream)
			_ = stream.Close()
			if err != nil || current != item || !bytes.Equal(actual, payload) {
				t.Fatal("disk rollback failed", err)
			}
			duplicate, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "duplicate.png"}, 4096)
			if duplicate.ID != item.ID || (!errors.Is(err, ErrDuplicate) && !errors.Is(err, ErrDuplicateTrash)) {
				t.Fatalf("checksum index lost: %+v %v", duplicate, err)
			}
		})
	}
}

func TestRestoreRejectsCanceledMissingAndCorruptObjects(t *testing.T) {
	for _, failure := range []string{"canceled", "missing", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			m, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			payload := testPNG(t, 2, 2)
			item, err := m.Upload(t.Context(), bytes.NewReader(payload), UploadInput{Filename: "check.png"}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			item, err = m.Trash(item.ID, item.Version)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			want := ErrObjectCorrupt
			path := filepath.Join(m.root, filepath.FromSlash(item.ObjectKey))
			switch failure {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				want = ErrObjectMissing
			case "corrupt":
				payload[len(payload)-1] ^= 0xff
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Restore(ctx, item.ID, item.Version); !errors.Is(err, want) {
				t.Fatalf("restore = %v, want %v", err, want)
			}
			if current, err := m.Get(item.ID); err != nil || current != item {
				t.Fatalf("failed restore mutated state: %+v %v", current, err)
			}
			if _, _, err := m.Open(item.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaIndexDigestAndTrashReload(t *testing.T) {
	root := t.TempDir()
	payload := []byte("%PDF-1.7\n%%EOF\n")
	hash := sha256.Sum256(payload)
	checksum := hex.EncodeToString(hash[:])
	objectKey := "objects/" + checksum[:2] + "/" + checksum + "-media-library.pdf"
	path := filepath.Join(root, filepath.FromSlash(objectKey))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	// The current wire layout is independent of the Item encoder.
	storedItem := fmt.Sprintf(`{"id":1,"version":1,"storage":"local","object_key":%q,"public_url":"/media/1/document.pdf","filename":"document.pdf","title":"Document","mime_type":"application/pdf","kind":"document","size_bytes":%d,"checksum_sha256":%q,"upload_source":"media-library","created_at":"2026-09-07T00:00:00Z","updated_at":"2026-09-07T00:00:00Z"}`, objectKey, len(payload), checksum)
	digest := sha256.Sum256([]byte(`{"next_id":2,"items":[` + storedItem + `]}`))
	revision := hex.EncodeToString(digest[:8])
	stored := []byte(fmt.Sprintf(`{"schema":3,"kind":"newpress/media-library","revision":%q,"next_id":2,"updated_at":"2026-09-07T00:00:00Z","items":[%s]}`, revision, storedItem))
	indexPath := filepath.Join(root, "library.json")
	if err := os.WriteFile(indexPath, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Revision != revision {
		t.Fatal("stored digest changed")
	}
	if after, err := os.ReadFile(indexPath); err != nil || !bytes.Equal(after, stored) {
		t.Fatal("reading rewrote stored data", err)
	}
	item, err := m.Trash(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := loadDocument(indexPath)
	if err != nil || doc.Schema != documentSchema {
		t.Fatalf("persisted document = %+v %v", doc, err)
	}
	reloaded, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := reloaded.Get(1); err != nil || current != item {
		t.Fatalf("trash restart = %+v %v", current, err)
	}
	doc.Schema = 1
	if err := writeDocument(indexPath, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root); err == nil {
		t.Fatal("schema 1 accepted trash timestamp")
	}
}
