package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func imageFixture(t testing.TB, transparent bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 96, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 96; x++ {
			a := uint8(255)
			if transparent && x < 20 {
				a = 0
			}
			c := color.NRGBA{R: uint8(x * 2), G: uint8(y * 4), B: 90, A: a}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImageProcessingPreservesOriginalAndLifecycle(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "disk"}[disk], func(t *testing.T) {
			root := ""
			if disk {
				root = t.TempDir()
			}
			m, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			original := imageFixture(t, true)
			item, err := m.Upload(context.Background(), bytes.NewReader(original), UploadInput{Filename: "透明.png"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			processed, err := m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{100, 0})
			if err != nil {
				t.Fatal(err)
			}
			if processed.Version != 2 || processed.PublicURL != item.PublicURL || processed.ChecksumSHA256 != item.ChecksumSHA256 || processed.Image.FocalX != 100 || processed.Image.Width != 96 || len(processed.Objects()) != 13 {
				t.Fatalf("processed=%+v", processed)
			}
			if err := validateImageSet(processed); err != nil {
				t.Fatal(err)
			}
			_, reader, err := m.Open(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := io.ReadAll(reader)
			reader.Close()
			if !bytes.Equal(original, actual) {
				t.Fatal("original was changed")
			}
			for _, variant := range processed.Image.Variants {
				for _, format := range []string{"native", "webp", ""} {
					object, stream, err := m.OpenVariant(item.ID, variant.Name, format, false)
					if err != nil {
						t.Fatal(err)
					}
					decoded, _, err := image.Decode(stream)
					stream.Close()
					if err != nil {
						t.Fatalf("%s/%s: %v", variant.Name, format, err)
					}
					if decoded.Bounds().Dx() != variant.Width || decoded.Bounds().Dy() != variant.Height || object.SizeBytes == 0 {
						t.Fatal("variant dimensions mismatch")
					}
				}
			}
			if processed.Image.Variants[3].Native.MIMEType != "image/png" {
				t.Fatal("lost transparency")
			}
			if snapshot := m.Snapshot(); snapshot.Count != 1 || snapshot.TotalBytes <= item.SizeBytes {
				t.Fatal("storage total excludes derivatives")
			}
			if disk {
				m, err = NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
				loaded, _ := m.Get(item.ID)
				if loaded != processed {
					t.Fatal("restart changed image recipe")
				}
			}
			oldKey := processed.Image.Variants[0].WebP.ObjectKey
			if _, err := m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50}); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale: %v", err)
			}
			processed, err = m.ProcessImage(context.Background(), item.ID, processed.Version, ImageInput{0, 50})
			if err != nil {
				t.Fatal(err)
			}
			if oldKey == processed.Image.Variants[0].WebP.ObjectKey {
				t.Fatal("focal point did not change crop")
			}
			if disk {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(oldKey))); !os.IsNotExist(err) {
					t.Fatal("retired crop remains")
				}
			} else if _, ok := m.memoryObjects[oldKey]; ok {
				t.Fatal("retired crop remains")
			}
			trashed, err := m.Trash(item.ID, processed.Version)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.OpenVariant(item.ID, "small", "", false); !errors.Is(err, ErrNotFound) {
				t.Fatalf("public trash variant: %v", err)
			}
			_, preview, err := m.OpenVariant(item.ID, "small", "", true)
			if err != nil {
				t.Fatal(err)
			}
			preview.Close()
			restored, err := m.Restore(context.Background(), item.ID, trashed.Version)
			if err != nil {
				t.Fatal(err)
			}
			trashed, err = m.Trash(item.ID, restored.Version)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Delete(item.ID, trashed.Version); err != nil {
				t.Fatal(err)
			}
			if disk {
				for _, object := range processed.Objects() {
					if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(object.ObjectKey))); !os.IsNotExist(err) {
						t.Fatalf("retained deleted object %s", object.ObjectKey)
					}
				}
			} else if len(m.memoryObjects) != 0 {
				t.Fatal("deleted objects retained")
			}
		})
	}
}

func TestImageProcessingRejectsUnsafeInputsAndRollsBack(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	item, err := m.Upload(context.Background(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "test.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, focal := range []ImageInput{{-1, 50}, {50, 101}} {
		if _, err := m.ProcessImage(context.Background(), item.ID, item.Version, focal); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("bad focal: %v", err)
		}
	}
	m.processing.limit = 1
	if !m.processing.tryAcquire(item) {
		t.Fatal("could not reserve image capacity")
	}
	_, err = m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50})
	m.processing.release(item)
	if !errors.Is(err, ErrImageBusy) {
		t.Fatalf("busy: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ProcessImage(cancelled, item.ID, item.Version, ImageInput{50, 50}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	oversized := item
	oversized.Width = MaxProcessDimension + 1
	if !errors.Is(oversized.CanProcessImage(), ErrImageBudget) {
		t.Fatal("unbounded image accepted")
	}
	gif := item
	gif.MIMEType = "image/gif"
	if !errors.Is(gif.CanProcessImage(), ErrImageUnsupported) {
		t.Fatal("GIF accepted")
	}
	processed, err := m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	index := m.indexPath
	blocker := filepath.Join(m.root, "blocked-parent")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.indexPath = filepath.Join(blocker, "index.json")
	_, err = m.ProcessImage(context.Background(), item.ID, processed.Version, ImageInput{100, 100})
	m.indexPath = index
	if err == nil {
		t.Fatal("index failure accepted")
	}
	current, _ := m.Get(item.ID)
	if current != processed {
		t.Fatal("failed generation changed facts")
	}
	for _, object := range processed.Objects() {
		if _, err := os.Stat(filepath.Join(m.root, filepath.FromSlash(object.ObjectKey))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewManager(m.root); err != nil {
		t.Fatal(err)
	}
	trashed, err := m.Trash(item.ID, processed.Version)
	if err != nil {
		t.Fatal(err)
	}
	variantPath := filepath.Join(m.root, filepath.FromSlash(processed.Image.Variants[0].WebP.ObjectKey))
	data, _ := os.ReadFile(variantPath)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(variantPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Restore(context.Background(), item.ID, trashed.Version); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corrupt derivative restored: %v", err)
	}
}

func TestManagerImageConcurrencyOptionsBoundProcessing(t *testing.T) {
	manager, err := NewManagerWithOptions("", ManagerOptions{ImageConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if manager.processing.limit != 1 {
		t.Fatalf("unexpected image concurrency: %d", manager.processing.limit)
	}
	for _, limit := range []int{-1, 17} {
		if _, err := NewManagerWithOptions("", ManagerOptions{ImageConcurrency: limit}); err == nil {
			t.Fatalf("accepted an unsafe image concurrency limit: %d", limit)
		}
	}
	item, err := manager.Upload(context.Background(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "test.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	// A full gate must fail fast instead of waiting while holding request state.
	reservation := item
	reservation.ID++
	if !manager.processing.tryAcquire(reservation) {
		t.Fatal("could not reserve image capacity")
	}
	if _, err := manager.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50}); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("full image gate returned %v", err)
	}
	manager.processing.release(reservation)
	if _, err := manager.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50}); err != nil {
		t.Fatalf("released image capacity was not reusable: %v", err)
	}
}

func TestImageMetadataOrientationAndAnimation(t *testing.T) {
	// Deliberately asymmetric colors make all eight transforms observable.
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(1 + x + 3*y), A: 255})
		}
	}
	want := [][]uint8{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for orientation := 1; orientation <= 8; orientation++ {
		out := orientImage(img, orientation)
		index := 0
		for y := 0; y < out.Bounds().Dy(); y++ {
			for x := 0; x < out.Bounds().Dx(); x++ {
				c := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA)
				if c.R != want[orientation-1][index] {
					t.Fatalf("orientation %d pixel %d = %d", orientation, index, c.R)
				}
				index++
			}
		}
	}
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	exif := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	metadata := append([]byte("Exif\x00\x00"), exif...)
	metadata = append(metadata, []byte("private-GPS-marker")...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(metadata)+2))
	segment = append(segment, metadata...)
	original := append(append(append([]byte{}, jpegBytes.Bytes()[:2]...), segment...), jpegBytes.Bytes()[2:]...)
	m, _ := NewManager("")
	item, err := m.Upload(context.Background(), bytes.NewReader(original), UploadInput{Filename: "oriented.jpg"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50})
	if err != nil {
		t.Fatal(err)
	}
	if processed.Image.Width != 2 || processed.Image.Height != 3 {
		t.Fatal("EXIF not applied")
	}
	for _, v := range processed.Image.Variants {
		for _, format := range []string{"native", "webp"} {
			_, stream, err := m.OpenVariant(item.ID, v.Name, format, false)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := io.ReadAll(stream)
			stream.Close()
			if bytes.Contains(payload, []byte("private-GPS-marker")) || bytes.Contains(payload, []byte("Exif")) {
				t.Fatal("metadata retained")
			}
		}
	}
	pngData := imageFixture(t, false)
	animated := append([]byte{}, pngData[:33]...)
	animated = append(animated, []byte{0, 0, 0, 0, 'a', 'c', 'T', 'L', 0, 0, 0, 0}...)
	animated = append(animated, pngData[33:]...)
	if _, err := imageOrientation(animated, "image/png"); !errors.Is(err, ErrImageUnsupported) {
		t.Fatalf("APNG: %v", err)
	}
	webp := []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	if _, err := imageOrientation(webp, "image/webp"); !errors.Is(err, ErrImageUnsupported) {
		t.Fatalf("animated WebP: %v", err)
	}
}

func TestImageIndexAndOrphanCleanup(t *testing.T) {
	root := t.TempDir()
	m, _ := NewManager(root)
	item, err := m.Upload(context.Background(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "image.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := loadDocument(m.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	doc.Schema = documentSchema
	if err := writeDocument(m.indexPath, doc); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(item)
	if bytes.Contains(encoded, []byte(`"image":`)) {
		t.Fatal("unprocessed image unexpectedly has variants")
	}
	if _, err := NewManager(root); err != nil {
		t.Fatal(err)
	}
	processed, err := m.ProcessImage(context.Background(), item.ID, item.Version, ImageInput{50, 50})
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, filepath.FromSlash(derivativeKey(999, "small", item.ChecksumSHA256, ".webp")))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("unpublished"), 0o640); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan not removed")
	}
	current, _ := reloaded.Get(item.ID)
	if current != processed {
		t.Fatal("cleanup changed current generation")
	}
	doc, _ = loadDocument(m.indexPath)
	doc.Items[0].Image.Variants[0].WebP.ObjectKey = item.ObjectKey
	doc.Revision = libraryRevision(doc.NextID, doc.Items)
	if err := writeDocument(m.indexPath, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(root); err == nil {
		t.Fatal("unsafe derivative alias accepted")
	}
}
