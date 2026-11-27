package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagerUploadListOpenUpdateDeleteAndPersistence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNG(t, 3, 2)
	item, err := manager.Upload(context.Background(), bytes.NewReader(payload), UploadInput{
		Filename: "设计稿.final.HTML", AltText: "后台卡片预览", Caption: "第一版素材", UploadedBy: "editor",
	}, int64(len(payload)+32))
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != 1 || item.Version != 1 || item.Filename != "设计稿-final.png" || item.Title != "设计稿-final" {
		t.Fatalf("unexpected uploaded item: %#v", item)
	}
	if item.MIMEType != "image/png" || item.Kind != KindImage || item.Width != 3 || item.Height != 2 || item.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected media metadata: %#v", item)
	}
	if item.PublicURL != "/media/1/%E8%AE%BE%E8%AE%A1%E7%A8%BF-final.png" || len(item.ChecksumSHA256) != 64 {
		t.Fatalf("unexpected public identity: %#v", item)
	}

	listed, err := manager.List(Query{Kind: KindImage, Search: "卡片", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 || listed.KindCounts[KindImage] != 1 || listed.TotalBytes != int64(len(payload)) {
		t.Fatalf("unexpected media list: %#v", listed)
	}

	opened, reader, err := manager.Open(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	readPayload, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || opened.ID != item.ID || !bytes.Equal(readPayload, payload) {
		t.Fatalf("opened media mismatch: item=%#v err=%v bytes=%d", opened, err, len(readPayload))
	}

	updated, err := manager.Update(item.ID, item.Version, UpdateInput{Title: "组件封面", AltText: "新的替代文本", Caption: "更新后的说明"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Title != "组件封面" || updated.Filename != item.Filename {
		t.Fatalf("unexpected updated item: %#v", updated)
	}
	if current, err := manager.Update(item.ID, item.Version, UpdateInput{Title: "过期写入"}); !errors.Is(err, ErrVersionConflict) || current.Version != updated.Version {
		t.Fatalf("stale update = %#v, %v", current, err)
	}

	reloaded, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reloaded.Get(item.ID)
	if err != nil || persisted.Title != updated.Title || persisted.Version != updated.Version {
		t.Fatalf("persisted item = %#v, %v", persisted, err)
	}
	duplicate, err := reloaded.Upload(context.Background(), bytes.NewReader(payload), UploadInput{Filename: "duplicate.png"}, int64(len(payload)+1))
	if !errors.Is(err, ErrDuplicate) || duplicate.ID != item.ID || duplicate.Title != updated.Title {
		t.Fatalf("duplicate upload = %#v, %v", duplicate, err)
	}

	if err := reloaded.Delete(item.ID, item.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	trashed, err := reloaded.Trash(item.ID, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Delete(item.ID, trashed.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Get(item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted item error = %v", err)
	}
	if snapshot := reloaded.Snapshot(); snapshot.Count != 0 || snapshot.TotalBytes != 0 || snapshot.Revision == "" {
		t.Fatalf("snapshot after delete = %#v", snapshot)
	}
	if _, err := NewManager(root); err != nil {
		t.Fatalf("reload empty persisted library: %v", err)
	}
}

func TestManagerMemoryModeAndDocumentUpload(t *testing.T) {
	t.Parallel()
	manager, err := NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")
	item, err := manager.Upload(context.Background(), bytes.NewReader(pdf), UploadInput{Filename: "specification.exe", Title: "接口规范"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if item.Storage != "memory" || item.Kind != KindDocument || item.MIMEType != "application/pdf" || item.Filename != "specification.pdf" {
		t.Fatalf("document item = %#v", item)
	}
	_, reader, err := manager.Open(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if payload, err := io.ReadAll(reader); err != nil || !bytes.Equal(payload, pdf) {
		t.Fatalf("memory object = %q, %v", payload, err)
	}
}

func TestManagerRejectsUnsupportedOversizedCancelledAndUnsafeImages(t *testing.T) {
	t.Parallel()
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"active.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"page.html":  []byte("<!doctype html><script>alert(1)</script>"),
	} {
		if _, err := manager.Upload(context.Background(), bytes.NewReader(payload), UploadInput{Filename: name}, 1024); !errors.Is(err, ErrUnsupportedType) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	if _, err := manager.Upload(context.Background(), strings.NewReader("%PDF-1.7\n0123456789"), UploadInput{Filename: "large.pdf"}, 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Upload(cancelled, bytes.NewReader(testPNG(t, 1, 1)), UploadInput{Filename: "cancelled.png"}, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}

	// GIF logical screen width/height are little endian at bytes 6-9. This is a
	// valid header with dimensions that exceed the decompression safety budget.
	unsafeGIF := []byte{'G', 'I', 'F', '8', '9', 'a', 0xff, 0x7f, 0xff, 0x7f, 0x00, 0x00, 0x00, ';'}
	if _, err := manager.Upload(context.Background(), bytes.NewReader(unsafeGIF), UploadInput{Filename: "bomb.gif"}, 1024); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsafe GIF error = %v", err)
	}
}

func TestManagerRejectsCorruptIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNG(t, 2, 2)
	if _, err := manager.Upload(context.Background(), bytes.NewReader(payload), UploadInput{Filename: "cover.png"}, 4096); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "library.json")
	encoded, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(encoded, []byte(`"schema": 3`), []byte(`"schema": 3, "schema": 3`), 1)
	if err := os.WriteFile(indexPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
		t.Fatalf("corrupt index error = %v", err)
	}
}

func TestManagerRejectsLinkedIndexAndObjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "library.json")
	if err := os.Symlink(outside, indexPath); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root); err == nil {
		t.Fatal("accepted linked media index")
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	item, err := manager.Upload(context.Background(), bytes.NewReader(testPNG(t, 2, 2)), UploadInput{Filename: "linked.png"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, filepath.FromSlash(item.ObjectKey))
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, objectPath); err != nil {
		t.Skipf("creating object symlink is unavailable: %v", err)
	}
	if _, _, err := manager.Open(item.ID); err == nil {
		t.Fatal("opened linked media object")
	}
	if _, err := NewManager(root); err == nil {
		t.Fatal("accepted linked media object during reload")
	}
}

func TestManagerDeleteWaitsForTransientWindowsReadLock(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows sharing semantics only")
	}
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNG(t, 2, 2)
	item, err := manager.Upload(context.Background(), bytes.NewReader(payload), UploadInput{Filename: "locked.png"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	item, err = manager.Trash(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := manager.OpenForManagement(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		time.Sleep(35 * time.Millisecond)
		closed <- reader.Close()
	}()
	if err := manager.Delete(item.ID, item.Version); err != nil {
		t.Fatalf("delete after transient read lock: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close locked reader: %v", err)
	}
	if _, err := manager.Get(item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted item error = %v", err)
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(20 + x), G: uint8(130 + y), B: 190, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
