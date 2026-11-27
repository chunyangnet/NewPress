package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// This non-16:9 source produces six distinct transform geometries.
func BenchmarkImageNoReuse(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 2560, 1600))
	for y := 0; y < 1600; y++ {
		for x := 0; x < 2560; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x * y / 255), 255})
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatal(err)
	}
	m, err := NewManager("")
	if err != nil {
		b.Fatal(err)
	}
	item, err := m.Upload(b.Context(), bytes.NewReader(raw.Bytes()), UploadInput{Filename: "bench.jpg"}, 32<<20)
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
