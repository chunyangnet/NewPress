package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"testing"
)

func TestSharedOrientationPreservesAlphaAndSourceBounds(t *testing.T) {
	// Non-zero bounds exercise RGBA pixel copies, including premultiplied alpha.
	// They must preserve the same visible colors as the NRGBA path.
	bounds := image.Rect(5, 7, 8, 9)
	for _, alpha := range []uint8{0, 128, 255} {
		t.Run(fmt.Sprint(alpha), func(t *testing.T) {
			rgba := image.NewRGBA(bounds)
			nrgba := image.NewNRGBA(bounds)
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					value := color.RGBA{R: uint8(x + y), G: uint8(x * 3), B: uint8(y * 4), A: alpha}
					if alpha == 0 {
						value = color.RGBA{}
					}
					rgba.SetRGBA(x, y, value)
					nrgba.Set(x, y, value)
				}
			}
			original := append([]byte{}, rgba.Pix...)
			for orientation := 1; orientation <= 8; orientation++ {
				actual := ApplyImageOrientation(rgba, orientation)
				want := ApplyImageOrientation(nrgba, orientation)
				if actual.Bounds() != want.Bounds() {
					t.Fatal("orientation bounds differ", orientation, actual.Bounds(), want.Bounds())
				}
				for y := actual.Bounds().Min.Y; y < actual.Bounds().Max.Y; y++ {
					for x := actual.Bounds().Min.X; x < actual.Bounds().Max.X; x++ {
						a := color.NRGBAModel.Convert(actual.At(x, y))
						b := color.NRGBAModel.Convert(want.At(x, y))
						if a != b {
							t.Fatal("orientation changed color or alpha", orientation, x, y, a, b)
						}
					}
				}
				if !bytes.Equal(original, rgba.Pix) {
					t.Fatal("orientation modified its source", orientation)
				}
			}
		})
	}
}
