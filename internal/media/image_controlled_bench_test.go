package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Environment-selected cases allow each process's peak memory to be measured
// independently. This benchmark is intentionally opt-in.
func BenchmarkImageControlled(b *testing.B) {
	scenario := os.Getenv("MEDIA_BENCH_CASE")
	if scenario == "" {
		b.Skip("set MEDIA_BENCH_CASE")
	}
	var raw []byte
	if scenario == "photo" {
		var err error
		raw, err = os.ReadFile(filepath.Join(runtime.GOROOT(), "src", "image", "testdata", "video-001.jpeg"))
		if err != nil {
			b.Fatal(err)
		}
	} else {
		w, h := 2560, 1600
		if scenario == "portrait" {
			w, h = 1600, 2560
		}
		if scenario == "alpha" {
			w, h = 960, 640
		}
		if scenario == "reuse" {
			w, h = 1280, 720
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				a := uint8(255)
				if scenario == "alpha" {
					a = uint8(64 + (x+y)%192)
				}
				img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x * y / 255), a})
			}
		}
		var buf bytes.Buffer
		var err error
		if scenario == "alpha" {
			err = png.Encode(&buf, img)
		} else {
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
		}
		if err != nil {
			b.Fatal(err)
		}
		raw = buf.Bytes()
	}
	root := ""
	if os.Getenv("MEDIA_BENCH_DISK") == "1" {
		root = b.TempDir()
	}
	m, err := NewManager(root)
	if err != nil {
		b.Fatal(err)
	}
	item, err := m.Upload(b.Context(), bytes.NewReader(raw), UploadInput{Filename: "controlled"}, 32<<20)
	if err != nil {
		b.Fatal(err)
	}
	// Warm codec paths, storage and the same item's regeneration before timing.
	item, err = m.ProcessImage(b.Context(), item.ID, item.Version, ImageInput{50, 50})
	if err != nil {
		b.Fatal(err)
	}
	runtime.GC()
	b.ReportAllocs()
	for b.Loop() {
		item, err = m.ProcessImage(b.Context(), item.ID, item.Version, ImageInput{50, 50})
		if err != nil {
			b.Fatal(err)
		}
	}
}
