package media

import (
	"bytes"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodedWebPColorView(t *testing.T) {
	for _, name := range []string{"palette", "alpha"} {
		t.Run(name, func(t *testing.T) {
			read := func(ext string) image.Image {
				data, err := os.ReadFile(filepath.Join("testdata", "webp-color", name+ext))
				if err != nil {
					t.Fatal(err)
				}
				src, _, err := image.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				return src
			}
			source, reference := read(".webp"), read(".png")
			var yuv *image.YCbCr
			var alpha []byte
			switch src := source.(type) {
			case *image.YCbCr:
				yuv = src
			case *image.NYCbCrA:
				yuv, alpha = &src.YCbCr, src.A
			default:
				t.Fatalf("fixture lost its lossy color planes: %T", src)
			}
			planes := [][]byte{yuv.Y, yuv.Cb, yuv.Cr, alpha}
			copies := make([][]byte, len(planes))
			for i, plane := range planes {
				copies[i] = bytes.Clone(plane)
			}
			// A cropped decoder view keeps padded strides and starts at an odd,
			// nonzero coordinate. Color and alpha offsets must still agree.
			crop := image.Rect(3, 5, 298, 196)
			source = source.(interface {
				SubImage(image.Rectangle) image.Image
			}).SubImage(crop)
			reference = reference.(interface {
				SubImage(image.Rectangle) image.Image
			}).SubImage(crop)
			view := NormalizeDecodedColors(source, "webp")
			if NormalizeDecodedColors(view, "webp") != view || NormalizeDecodedColors(source, "jpeg") != source {
				t.Fatal("applied color conversion twice or to a JPEG")
			}
			for orientation := 1; orientation <= 8; orientation++ {
				got := orientImage(view, orientation)
				want := orientImage(reference, orientation)
				assertWebPReference(t, got, want, 2.5)
				rgba, ok := got.(image.RGBA64Image)
				if !ok {
					t.Fatal("scaling lost its allocation-free pixel interface")
				}
				bounds := got.Bounds()
				for _, point := range []image.Point{bounds.Min.Sub(image.Pt(1, 0)), bounds.Min.Sub(image.Pt(0, 1)), bounds.Max} {
					if rgba.RGBA64At(point.X, point.Y) != (color.RGBA64{}) {
						t.Fatal("out-of-bounds pixels must be transparent", point)
					}
				}
			}
			for i, plane := range planes {
				if !bytes.Equal(plane, copies[i]) {
					t.Fatal("changed a decoded source plane", i)
				}
			}
		})
	}
	for _, filename := range []string{"palette.png", "lossless.webp"} {
		data, err := os.ReadFile(filepath.Join("testdata", "webp-color", filename))
		if err != nil {
			t.Fatal(err)
		}
		source, format, err := image.Decode(bytes.NewReader(data))
		if err != nil || NormalizeDecodedColors(source, format) != source {
			t.Fatal("RGB image was corrected again", filename, err)
		}
	}
}

func TestImageProcessingWebPColors(t *testing.T) {
	for _, name := range []string{"palette", "alpha", "alpha-rotated", "gradient", "lossless", "narrow"} {
		t.Run(name, func(t *testing.T) {
			original, err := os.ReadFile(filepath.Join("testdata", "webp-color", name+".webp"))
			if err != nil {
				t.Fatal(err)
			}
			png, err := os.ReadFile(filepath.Join("testdata", "webp-color", name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			reference, _, err := image.Decode(bytes.NewReader(png))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			library, err := NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			item, err := library.Upload(t.Context(), bytes.NewReader(original), UploadInput{Filename: name + ".webp"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			focal := ImageInput{FocalX: 70, FocalY: 30}
			processed, err := library.ProcessImage(t.Context(), item.ID, item.Version, focal)
			if err != nil {
				t.Fatal(err)
			}
			if processed.Image.Width != reference.Bounds().Dx() || processed.Image.Height != reference.Bounds().Dy() {
				t.Fatal("display dimensions differ", processed.Image, reference.Bounds())
			}
			library, err = NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			_, stream, err := library.Open(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || !bytes.Equal(stored, original) {
				t.Fatal("color correction altered the original", err)
			}
			for _, spec := range imageSizes {
				want := resizeImage(reference, spec.width, spec.height, spec.crop, focal)
				for _, format := range []string{"native", "webp"} {
					t.Run(spec.name+"/"+format, func(t *testing.T) {
						_, stream, err := library.OpenVariant(item.ID, spec.name, format, false)
						if err != nil {
							t.Fatal(err)
						}
						got, _, err := image.Decode(stream)
						stream.Close()
						if err != nil {
							t.Fatal(err)
						}
						assertWebPReference(t, got, want, 2.5)
					})
				}
			}
		})
	}
}

// Compare against libwebp's checked-in RGBA pixels, not a duplicate of the
// conversion formula. Premultiplied RGB represents visible color under alpha.
func assertWebPReference(t testing.TB, got, want image.Image, tolerance float64) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("dimensions: got %v, want %v", got.Bounds(), want.Bounds())
	}
	var sums [3]uint64
	for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
		for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			if absColorDifference(ga, wa) > 257 {
				t.Fatalf("alpha changed at (%d,%d): got %d, want %d", x, y, ga, wa)
			}
			for i, pair := range [][2]uint32{{gr, wr}, {gg, wg}, {gb, wb}} {
				sums[i] += uint64(absColorDifference(pair[0], pair[1]))
			}
		}
	}
	for channel, sum := range sums {
		mean := float64(sum) / float64(want.Bounds().Dx()*want.Bounds().Dy()) / 257
		if mean > tolerance {
			t.Errorf("channel %d mean visible error %.3f exceeds %.1f", channel, mean, tolerance)
		}
	}
}

func absColorDifference(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
