package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestImageReusePreservesEveryVariant(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, alpha := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/alpha=%t", disk, alpha), func(t *testing.T) {
				root := ""
				if disk {
					root = t.TempDir()
				}
				m, err := NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
				raw := imageFixture(t, alpha)
				source, format, err := image.Decode(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				source = NormalizeDecodedColors(source, format)
				item, err := m.Upload(t.Context(), bytes.NewReader(raw), UploadInput{Filename: "reuse.png"}, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				focal := ImageInput{100, 0}
				item, err = m.ProcessImage(t.Context(), item.ID, item.Version, focal)
				if err != nil {
					t.Fatal(err)
				}
				if len(item.Objects()) != 13 {
					t.Fatal("lost variant objects")
				}
				if disk {
					m, err = NewManager(root)
					if err != nil {
						t.Fatal(err)
					}
				}
				keys := map[string]bool{}
				for _, spec := range imageSizes {
					resized := referenceResize(source, spec.width, spec.height, spec.crop, focal)
					for _, encoding := range []string{"native", "webp"} {
						want, mime, err := encodeImage(resized, encoding)
						if err != nil {
							t.Fatal(err)
						}
						object, reader, err := m.OpenVariant(item.ID, spec.name, encoding, false)
						if err != nil {
							t.Fatal(err)
						}
						got, err := io.ReadAll(reader)
						reader.Close()
						if err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(got, want) || object.MIMEType != mime {
							t.Fatalf("variant changed: %s/%s", spec.name, encoding)
						}
						if keys[object.ObjectKey] {
							t.Fatal("variants shared object namespace")
						}
						keys[object.ObjectKey] = true
					}
				}
			})
		}
	}
}

func TestImageGeometryReuseKey(t *testing.T) {
	seen := map[imageGeometry]bool{}
	for _, spec := range imageSizes {
		geometry := imageResizeGeometry(image.Rect(0, 0, 2560, 1600), spec.width, spec.height, spec.crop, ImageInput{50, 50})
		if seen[geometry] {
			t.Fatal("no-reuse benchmark contains equivalent transforms")
		}
		seen[geometry] = true
	}
	b := image.Rect(-7, 5, 1273, 725)
	medium := imageResizeGeometry(b, 1280, 1280, false, ImageInput{50, 50})
	large := imageResizeGeometry(b, 1920, 1920, false, ImageInput{50, 50})
	if medium != large {
		t.Fatal("expected equivalent non-upscaled transforms")
	}
	left := imageResizeGeometry(b, 320, 320, true, ImageInput{0, 50})
	right := imageResizeGeometry(b, 320, 320, true, ImageInput{100, 50})
	if left == right || left.size != right.size {
		t.Fatal("crop position missing from key")
	}
}

func TestCopyImageStageFailures(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stage, err := m.stageUpload(t.Context(), bytes.NewReader([]byte("encoded bytes")), maxDerivativeBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.discard()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = m.copyImageStage(ctx, stage); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if err = os.WriteFile(stage.path, []byte("changed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.copyImageStage(t.Context(), stage); !errors.Is(err, ErrObjectCorrupt) {
		t.Fatalf("corruption: %v", err)
	}
	if err = os.Remove(stage.path); err != nil {
		t.Fatal(err)
	}
	if _, err = m.copyImageStage(t.Context(), stage); err == nil {
		t.Fatal("missing stage accepted")
	}
	entries, err := os.ReadDir(filepath.Join(m.root, "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("leaked stages: %v %v", entries, err)
	}
}

func BenchmarkImageReuseSizes(b *testing.B) {
	for _, size := range []image.Point{{320, 180}, {2560, 1440}} {
		b.Run(fmt.Sprintf("%dx%d", size.X, size.Y), func(b *testing.B) {
			img := image.NewNRGBA(image.Rectangle{Max: size})
			for y := 0; y < size.Y; y++ {
				for x := 0; x < size.X; x++ {
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
		})
	}
}
