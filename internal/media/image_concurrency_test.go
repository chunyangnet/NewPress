package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestImageProcessingSharesInputBudgets(t *testing.T) {
	for _, resource := range []string{"pixels", "bytes"} {
		t.Run(resource, func(t *testing.T) {
			m, err := NewManager("")
			if err != nil {
				t.Fatal(err)
			}
			item, err := m.Upload(t.Context(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "test.png"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			other := item
			other.ID++
			if resource == "pixels" {
				other.Width, other.Height = 4000, 4000
			} else {
				other.SizeBytes = MaxProcessBytes
			}
			if !m.processing.tryAcquire(other) {
				t.Fatal("could not reserve the input budget")
			}
			_, err = m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50})
			m.processing.release(other)
			if !errors.Is(err, ErrImageBusy) {
				t.Fatalf("aggregate %s budget was not enforced: %v", resource, err)
			}
			if _, err := m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50}); err != nil {
				t.Fatal("input budget did not recover after release", err)
			}
		})
	}
}

func TestImageProcessingRejectsDuplicateWork(t *testing.T) {
	m, err := NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	item, err := m.Upload(t.Context(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "test.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !m.processing.tryAcquire(item) {
		t.Fatal("could not reserve the first worker")
	}
	_, err = m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50})
	m.processing.release(item)
	if !errors.Is(err, ErrImageBusy) {
		t.Fatal("duplicate work was accepted despite an available worker", err)
	}
}

func TestImageProcessingFailureReleasesCapacity(t *testing.T) {
	m, err := NewManagerWithOptions("", ManagerOptions{ImageConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	item, err := m.Upload(t.Context(), bytes.NewReader(imageFixture(t, false)), UploadInput{Filename: "test.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(m.memoryObjects[item.ObjectKey])
	m.memoryObjects[item.ObjectKey][len(original)-1] ^= 1
	if _, err := m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50}); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatal("corrupt source was not rejected", err)
	}
	m.memoryObjects[item.ObjectKey] = original
	if _, err := m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50}); err != nil {
		t.Fatal("processing failure leaked a worker or input budget", err)
	}
}

func TestImageProcessingConcurrentCommitsPreserveObjects(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			root := ""
			if disk {
				root = t.TempDir()
			}
			m, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			var originals [2]Item
			for i := range originals {
				originals[i], err = m.Upload(t.Context(), bytes.NewReader(imageFixture(t, i == 0)), UploadInput{Filename: fmt.Sprintf("image-%d.png", i)}, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			var workers sync.WaitGroup
			var results [2]Item
			var failures [2]error
			for i, item := range originals {
				workers.Go(func() {
					<-start
					results[i], failures[i] = m.ProcessImage(t.Context(), item.ID, item.Version, ImageInput{50, 50})
				})
			}
			close(start)
			workers.Wait()
			for _, err := range failures {
				if err != nil {
					t.Fatal("independent image failed", err)
				}
			}
			if disk {
				staging, err := os.ReadDir(filepath.Join(root, "tmp"))
				if err != nil || len(staging) != 0 {
					t.Fatal("concurrent processing left temporary files", staging, err)
				}
				m, err = NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
			}
			for i, item := range results {
				loaded, err := m.Get(item.ID)
				if err != nil || loaded != item || item.Version != originals[i].Version+1 {
					t.Fatal("concurrent commit lost metadata or version", err)
				}
				for _, object := range item.Objects() {
					_, reader, err := m.OpenStorageObject(item.ID, object.ObjectKey)
					if err != nil {
						t.Fatal("another image commit deleted an indexed object", err)
					}
					data, err := io.ReadAll(reader)
					reader.Close()
					checksum := sha256.Sum256(data)
					if err != nil || hex.EncodeToString(checksum[:]) != object.ChecksumSHA256 {
						t.Fatal("concurrent processing corrupted an object", err)
					}
				}
			}
		})
	}
}

// BenchmarkProcessImage includes decode, normalization, six resizes, twelve
// encodes, checksums, staging and commit. It uses memory storage to isolate CPU
// work; filesystem durability and production request latency are separate.
func BenchmarkProcessImage(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 1280, 720))
	for y := range 720 {
		for x := range 1280 {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x * y / 255), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatal(err)
	}
	m, err := NewManager("")
	if err != nil {
		b.Fatal(err)
	}
	item, err := m.Upload(b.Context(), bytes.NewReader(encoded.Bytes()), UploadInput{Filename: "benchmark.jpg"}, 1<<20)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		item, err = m.ProcessImage(b.Context(), item.ID, item.Version, ImageInput{50, 50})
		if err != nil {
			b.Fatal(err)
		}
	}
}
