package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// Keep the pre-optimization recipe as an independent pixel oracle.
func referenceResize(source image.Image, width, height int, crop bool, focal ImageInput) *image.NRGBA {
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if crop {
		cw, ch := w, h
		if w*height > h*width {
			cw = max(1, h*width/height)
		} else {
			ch = max(1, w*height/width)
		}
		x := min(w-cw, max(0, w*focal.FocalX/100-cw/2))
		y := min(h-ch, max(0, h*focal.FocalY/100-ch/2))
		bounds = image.Rect(bounds.Min.X+x, bounds.Min.Y+y, bounds.Min.X+x+cw, bounds.Min.Y+y+ch)
		w, h = cw, ch
	}
	scale := min(1.0, min(float64(width)/float64(w), float64(height)/float64(h)))
	dst := image.NewNRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), source, bounds, draw.Src, nil)
	return dst
}

func TestResizeOptimizationMatchesRecipe(t *testing.T) {
	sources := map[string]image.Image{}
	for _, alpha := range []bool{false, true} {
		img := image.NewNRGBA(image.Rect(-3, 5, 78, 64))
		for y := 5; y < 64; y++ {
			for x := -3; x < 78; x++ {
				a := uint8(255)
				if alpha {
					a = uint8((x+3)*17 + y*11)
				}
				img.SetNRGBA(x, y, color.NRGBA{uint8(x * 31), uint8(y * 17), uint8(x * y), a})
			}
		}
		sources[fmt.Sprintf("nrgba-%t", alpha)] = img.SubImage(image.Rect(1, 7, 72, 60))
		if !alpha {
			rgba := image.NewRGBA(img.Rect)
			draw.Draw(rgba, rgba.Rect, img, img.Rect.Min, draw.Src)
			sources["rgba"] = rgba.SubImage(image.Rect(1, 7, 72, 60))
			var b bytes.Buffer
			if err := jpeg.Encode(&b, img, nil); err != nil {
				t.Fatal(err)
			}
			decoded, err := jpeg.Decode(&b)
			if err != nil {
				t.Fatal(err)
			}
			sources["jpeg"] = decoded
		}
	}
	wide := image.NewNRGBA64(image.Rect(0, 0, 17, 13))
	for y := 0; y < 13; y++ {
		for x := 0; x < 17; x++ {
			wide.SetNRGBA64(x, y, color.NRGBA64{uint16(x*3817 + 29), uint16(y*4901 + 11), uint16(x * y * 297), 65535})
		}
	}
	sources["16-bit"] = wide
	for _, name := range []string{"palette.webp", "alpha.webp", "lossless.webp"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "webp-color", name))
		if err != nil {
			t.Fatal(err)
		}
		decoded, format, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = NormalizeDecodedColors(decoded, format)
	}
	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			for orientation := 1; orientation <= 8; orientation++ {
				src := orientImage(source, orientation)
				for _, spec := range []struct {
					w, h int
					crop bool
				}{{4096, 4096, false}, {40, 30, false}, {320, 180, true}, {30, 20, true}} {
					for _, focal := range []ImageInput{{0, 0}, {50, 50}, {100, 100}} {
						want := referenceResize(src, spec.w, spec.h, spec.crop, focal)
						got := resizeImage(src, spec.w, spec.h, spec.crop, focal)
						if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
							t.Fatalf("pixel mismatch orientation=%d spec=%+v focal=%+v", orientation, spec, focal)
						}
						if len(got.Pix) > 0 {
							got.Pix[0] ^= 255
							again := referenceResize(src, spec.w, spec.h, spec.crop, focal)
							if !bytes.Equal(again.Pix, want.Pix) {
								t.Fatal("resize output aliases its source")
							}
						}
					}
				}
			}
		})
	}
}

func TestEncodeOptimizationPreservesBytesAndSource(t *testing.T) {
	for _, alpha := range []bool{false, true} {
		for _, rect := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(-3, 7, 66, 42)} {
			parent := image.NewNRGBA(rect.Inset(-2))
			for y := parent.Rect.Min.Y; y < parent.Rect.Max.Y; y++ {
				for x := parent.Rect.Min.X; x < parent.Rect.Max.X; x++ {
					a := uint8(255)
					if alpha {
						a = uint8(x*19 + y*31)
					}
					parent.SetNRGBA(x, y, color.NRGBA{uint8(x * 17), uint8(y * 23), uint8(x * y), a})
				}
			}
			src := parent.SubImage(rect).(*image.NRGBA)
			before := bytes.Clone(parent.Pix)
			var want bytes.Buffer
			mime := "image/jpeg"
			var err error
			if src.Opaque() {
				err = jpeg.Encode(&want, src, &jpeg.Options{Quality: 84})
			} else {
				mime = "image/png"
				err = png.Encode(&want, src)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, typ, err := encodeImage(src, "native")
			if err != nil || typ != mime || !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("encoding differs alpha=%t bounds=%v mime=%s err=%v", alpha, rect, typ, err)
			}
			if !bytes.Equal(before, parent.Pix) {
				t.Fatal("encoding modified source")
			}
		}
	}
}

func BenchmarkJPEGPixelPath(b *testing.B) {
	src := image.NewNRGBA(image.Rect(0, 0, 1280, 720))
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x * y / 255), 255})
		}
	}
	for _, optimized := range []bool{false, true} {
		b.Run(fmt.Sprintf("optimized=%t", optimized), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if optimized {
					if _, _, err := encodeImage(src, "native"); err != nil {
						b.Fatal(err)
					}
				} else {
					var out bytes.Buffer
					if !src.Opaque() {
						b.Fatal("expected opaque")
					}
					if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: 84}); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
