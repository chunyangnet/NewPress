package media

import (
	"image"
	"image/color"
)

// NormalizeDecodedColors gives x/image/webp's lossy VP8 planes their display
// colors. The decoder returns video-range YCbCr, while Go's image.YCbCr uses
// full-range JPEG conversion. Only trust the format returned by image.Decode;
// JPEG and lossless WebP must not receive this correction. ICC is not handled.
//
// A lazy RGBA64Image lets draw/Scale convert without a full-size RGB copy or
// per-pixel color-interface allocations. The decoded planes remain immutable.
func NormalizeDecodedColors(source image.Image, format string) image.Image {
	if format != "webp" {
		return source
	}
	switch src := source.(type) {
	case *image.YCbCr:
		return &webpColorImage{yuv: src, rect: src.Bounds(), orientation: 1}
	case *image.NYCbCrA:
		return &webpColorImage{yuv: &src.YCbCr, alpha: src.A, alphaStride: src.AStride, rect: src.Bounds(), orientation: 1}
	default:
		return source
	}
}

type webpColorImage struct {
	yuv         *image.YCbCr
	alpha       []byte
	alphaStride int
	rect        image.Rectangle
	orientation int
}

var _ image.RGBA64Image = (*webpColorImage)(nil)

func (p *webpColorImage) Bounds() image.Rectangle { return p.rect }
func (p *webpColorImage) ColorModel() color.Model { return color.RGBA64Model }
func (p *webpColorImage) Opaque() bool            { return p.alpha == nil }
func (p *webpColorImage) At(x, y int) color.Color { return p.RGBA64At(x, y) }

func (p *webpColorImage) RGBA64At(x, y int) color.RGBA64 {
	if !image.Pt(x, y).In(p.rect) {
		return color.RGBA64{}
	}
	if p.orientation != 1 {
		w, h := p.yuv.Rect.Dx(), p.yuv.Rect.Dy()
		switch p.orientation {
		case 2:
			x = w - 1 - x
		case 3:
			x, y = w-1-x, h-1-y
		case 4:
			y = h - 1 - y
		case 5:
			x, y = y, x
		case 6:
			x, y = y, h-1-x
		case 7:
			x, y = w-1-y, h-1-x
		case 8:
			x, y = w-1-y, x
		}
		x, y = x+p.yuv.Rect.Min.X, y+p.yuv.Rect.Min.Y
	}
	a := uint32(255)
	if p.alpha != nil {
		a = uint32(p.alpha[(y-p.yuv.Rect.Min.Y)*p.alphaStride+x-p.yuv.Rect.Min.X])
		if a == 0 {
			return color.RGBA64{}
		}
	}
	yi, ci := p.yuv.YOffset(x, y), p.yuv.COffset(x, y)
	yy := 19077 * (int(p.yuv.Y[yi]) - 16)
	u, v := int(p.yuv.Cb[ci])-128, int(p.yuv.Cr[ci])-128
	// BT.601 limited-range coefficients at 14-bit precision, as documented by
	// libwebp src/dsp/yuv.h. Clip RGB after conversion, not the input planes:
	// compression may produce values outside nominal Y/U/V ranges.
	r := clipWebPChannel((yy + 26149*v + 8192) >> 14)
	g := clipWebPChannel((yy - 6419*u - 13320*v + 8192) >> 14)
	b := clipWebPChannel((yy + 33050*u + 8192) >> 14)
	r, g, b = r*257, g*257, b*257
	if a != 255 {
		r, g, b = r*a/255, g*a/255, b*a/255
	}
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a * 257)}
}

func clipWebPChannel(value int) uint32 {
	return uint32(min(255, max(0, value)))
}
