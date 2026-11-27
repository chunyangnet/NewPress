package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
)

func imageOrientation(payload []byte, mime string) (int, error) {
	return readImageOrientation(payload, mime, true)
}

// ReadImageOrientation reads the display orientation without deciding whether
// animation is acceptable. A caller rendering a single frame may use it, while
// media derivatives continue to reject animation through imageOrientation.
// Container lengths are checked before slicing, including unused metadata.
func ReadImageOrientation(payload []byte, mime string) (int, error) {
	return readImageOrientation(payload, mime, false)
}

func readImageOrientation(payload []byte, mime string, rejectAnimation bool) (int, error) {
	invalid := func() (int, error) { return 0, fmt.Errorf("%w: malformed image metadata", ErrInvalidInput) }
	orientation := 1
	readEXIF := func(data []byte) error {
		if bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
			data = data[6:]
		}
		value, err := exifOrientation(data)
		if err != nil {
			return err
		}
		orientation = value
		return nil
	}
	switch mime {
	case "image/jpeg":
		if len(payload) < 2 || payload[0] != 0xff || payload[1] != 0xd8 {
			return invalid()
		}
		for pos := 2; pos < len(payload); {
			if payload[pos] != 0xff {
				return invalid()
			}
			for pos < len(payload) && payload[pos] == 0xff {
				pos++
			}
			if pos >= len(payload) {
				return invalid()
			}
			marker := payload[pos]
			pos++
			if marker == 0xda || marker == 0xd9 {
				break
			}
			if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
				continue
			}
			if pos+2 > len(payload) {
				return invalid()
			}
			length := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
			if length < 2 || length > len(payload)-pos {
				return invalid()
			}
			data := payload[pos+2 : pos+length]
			if marker == 0xe1 && bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
				if err := readEXIF(data); err != nil {
					return 0, err
				}
			}
			pos += length
		}
	case "image/png":
		if len(payload) < 8 || !bytes.Equal(payload[:8], []byte("\x89PNG\r\n\x1a\n")) {
			return invalid()
		}
		for pos := 8; pos < len(payload); {
			if len(payload)-pos < 12 {
				return invalid()
			}
			length := uint64(binary.BigEndian.Uint32(payload[pos : pos+4]))
			if length > uint64(len(payload)-pos-12) {
				return invalid()
			}
			kind := string(payload[pos+4 : pos+8])
			if rejectAnimation && (kind == "acTL" || kind == "fcTL" || kind == "fdAT") {
				return 0, ErrImageUnsupported
			}
			if kind == "eXIf" {
				if err := readEXIF(payload[pos+8 : pos+8+int(length)]); err != nil {
					return 0, err
				}
			}
			pos += int(length) + 12
			if kind == "IEND" {
				break
			}
		}
	case "image/webp":
		if len(payload) < 12 || string(payload[:4]) != "RIFF" || string(payload[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(payload[4:8]))+8 != uint64(len(payload)) {
			return invalid()
		}
		for pos := 12; pos < len(payload); {
			if len(payload)-pos < 8 {
				return invalid()
			}
			length := uint64(binary.LittleEndian.Uint32(payload[pos+4 : pos+8]))
			padded := length + (length & 1)
			if padded > uint64(len(payload)-pos-8) {
				return invalid()
			}
			kind := string(payload[pos : pos+4])
			data := payload[pos+8 : pos+8+int(length)]
			if rejectAnimation && (kind == "ANIM" || kind == "ANMF" || (kind == "VP8X" && len(data) > 0 && data[0]&2 != 0)) {
				return 0, ErrImageUnsupported
			}
			if kind == "EXIF" {
				if err := readEXIF(data); err != nil {
					return 0, err
				}
			}
			pos += int(padded) + 8
		}
	default:
		return 0, ErrImageUnsupported
	}
	return orientation, nil
}

func exifOrientation(data []byte) (int, error) {
	invalid := fmt.Errorf("%w: invalid EXIF orientation", ErrInvalidInput)
	if len(data) < 8 {
		return 0, invalid
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, invalid
	}
	if order.Uint16(data[2:4]) != 42 {
		return 0, invalid
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset+2 > uint64(len(data)) {
		return 0, invalid
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	if offset+2+count*12 > uint64(len(data)) {
		return 0, invalid
	}
	for i := uint64(0); i < count; i++ {
		entry := data[offset+2+i*12 : offset+2+(i+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, invalid
		}
		value := int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 {
			return 0, invalid
		}
		return value, nil
	}
	return 1, nil
}

// ApplyImageOrientation transforms an already bounded image without changing
// its source. Orientation must be a validated EXIF value from 1 through 8.
// Scaling before this call avoids allocating another original-size canvas.
func ApplyImageOrientation(source image.Image, orientation int) image.Image {
	return orientImage(source, orientation)
}

func orientImage(source image.Image, orientation int) image.Image {
	if orientation == 1 {
		return source
	}
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	if src, ok := source.(*webpColorImage); ok && src.orientation == 1 {
		// Keep color conversion and orientation lazy until the crop/resize reads
		// pixels. A rotated WebP needs no original-size RGB canvas either.
		oriented := *src
		oriented.orientation = orientation
		oriented.rect = image.Rect(0, 0, dw, dh)
		return &oriented
	}
	rgba, _ := source.(*image.RGBA)
	var dst *image.NRGBA
	var rgbaDst *image.RGBA
	if rgba != nil {
		rgbaDst = image.NewRGBA(image.Rect(0, 0, dw, dh))
	} else {
		dst = image.NewNRGBA(image.Rect(0, 0, dw, dh))
	}
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			// Preserve RGBA storage, including premultiplied alpha. This avoids
			// per-pixel interface allocations both here and in the JPEG encoder.
			if rgba != nil {
				offset := rgba.PixOffset(bounds.Min.X+sx, bounds.Min.Y+sy)
				target := y*rgbaDst.Stride + x*4
				copy(rgbaDst.Pix[target:target+4], rgba.Pix[offset:offset+4])
				continue
			}
			dst.Set(x, y, source.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	if rgbaDst != nil {
		return rgbaDst
	}
	return dst
}
