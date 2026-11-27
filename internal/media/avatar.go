package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"sync"
)

const MaxAvatarBytes int64 = 2 << 20

// A small compressed PNG can decode to 32 MiB at the accepted dimensions
// (2048 squared, 16-bit RGBA). Bound that work across accounts and managers,
// independently of the per-account HTTP rate limit, without retaining a queue.
var avatarProcessing sync.Mutex

// PrepareAvatar bounds decoded memory, crops to the centre square, downsamples
// with a box filter, and re-encodes without source metadata or animation.
func PrepareAvatar(source io.ReadSeeker) (*bytes.Reader, error) {
	if !avatarProcessing.TryLock() {
		return nil, ErrImageBusy
	}
	defer avatarProcessing.Unlock()
	length, err := source.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if length > MaxAvatarBytes {
		return nil, ErrTooLarge
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(io.LimitReader(source, MaxAvatarBytes))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return nil, ErrUnsupportedType
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(io.LimitReader(source, MaxAvatarBytes))
	if err != nil {
		return nil, ErrUnsupportedType
	}
	short := min(config.Width, config.Height)
	side := min(256, short)
	result := image.NewRGBA(image.Rect(0, 0, side, side))
	x0, y0 := img.Bounds().Min.X+(config.Width-short)/2, img.Bounds().Min.Y+(config.Height-short)/2
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			var red, green, blue, n uint64
			for sy := y * short / side; sy < (y+1)*short/side; sy++ {
				for sx := x * short / side; sx < (x+1)*short/side; sx++ {
					r, g, b, a := img.At(x0+sx, y0+sy).RGBA()
					red += uint64(r + 65535 - a)
					green += uint64(g + 65535 - a)
					blue += uint64(b + 65535 - a)
					n++
				}
			}
			result.SetRGBA(x, y, color.RGBA{uint8(red / n >> 8), uint8(green / n >> 8), uint8(blue / n >> 8), 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, result, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return bytes.NewReader(output.Bytes()), nil
}
