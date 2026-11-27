package wechat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HugoSmits86/nativewebp"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

var orientationColors = []color.NRGBA{
	{R: 220, G: 30, B: 30, A: 255}, {R: 30, G: 200, B: 30, A: 255}, {R: 30, G: 30, B: 220, A: 255},
	{R: 220, G: 200, B: 30, A: 255}, {R: 220, G: 30, B: 200, A: 255}, {R: 30, G: 200, B: 220, A: 255},
}

func orientationGrid() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			img.SetNRGBA(x, y, orientationColors[y/32*3+x/32])
		}
	}
	return img
}

func orientationTIFF(value int, order binary.ByteOrder) []byte {
	data := make([]byte, 26)
	copy(data, "II")
	if order == binary.BigEndian {
		copy(data, "MM")
	}
	order.PutUint16(data[2:], 42)
	order.PutUint32(data[4:], 8)
	order.PutUint16(data[8:], 1)
	order.PutUint16(data[10:], 0x0112)
	order.PutUint16(data[12:], 3)
	order.PutUint32(data[14:], 1)
	order.PutUint16(data[18:], uint16(value))
	return append(data, []byte("private-camera-location")...)
}

func orientedPicture(t testing.TB, format string, img image.Image, exif []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95})
	case "png":
		err = png.Encode(&encoded, img)
	case "webp":
		err = nativewebp.Encode(&encoded, img, &nativewebp.Options{CompressionLevel: nativewebp.BestSpeed})
	default:
		t.Fatal("unsupported test format", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	if exif == nil {
		return data
	}
	switch format {
	case "jpeg":
		metadata := append([]byte("Exif\x00\x00"), exif...)
		segment := []byte{0xff, 0xe1, 0, 0}
		binary.BigEndian.PutUint16(segment[2:], uint16(len(metadata)+2))
		segment = append(segment, metadata...)
		return append(append(append([]byte{}, data[:2]...), segment...), data[2:]...)
	case "png":
		chunk := make([]byte, len(exif)+12)
		binary.BigEndian.PutUint32(chunk, uint32(len(exif)))
		copy(chunk[4:], "eXIf")
		copy(chunk[8:], exif)
		binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
		return append(append(append([]byte{}, data[:33]...), chunk...), data[33:]...)
	case "webp":
		if string(data[12:16]) != "VP8L" {
			t.Fatal("expected simple lossless WebP fixture")
		}
		// Add an extended header with EXIF, retaining the original image chunk.
		extended := []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		w, h := img.Bounds().Dx()-1, img.Bounds().Dy()-1
		for i := 0; i < 3; i++ {
			extended[12+i], extended[15+i] = byte(w>>(8*i)), byte(h>>(8*i))
		}
		chunk := make([]byte, 8+len(exif)+(len(exif)&1))
		copy(chunk, "EXIF")
		binary.LittleEndian.PutUint32(chunk[4:], uint32(len(exif)))
		copy(chunk[8:], exif)
		result := append(append(append(append([]byte{}, data[:12]...), extended...), data[12:]...), chunk...)
		binary.LittleEndian.PutUint32(result[4:], uint32(len(result)-8))
		return result
	}
	panic("unreachable")
}

func assertOrientation(t testing.TB, data []byte, orientation int) {
	t.Helper()
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "jpeg" || len(data) >= 1<<20 {
		t.Fatal("invalid output", format, len(data), err)
	}
	width, height := 96, 64
	if orientation >= 5 {
		width, height = height, width
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Fatalf("display size: got %v, want %dx%d", decoded.Bounds(), width, height)
	}
	// Expected reading order of six colored blocks, independent of pixel mapping.
	want := [][]int{{0, 1, 2, 3, 4, 5}, {2, 1, 0, 5, 4, 3}, {5, 4, 3, 2, 1, 0}, {3, 4, 5, 0, 1, 2}, {0, 3, 1, 4, 2, 5}, {3, 0, 4, 1, 5, 2}, {5, 2, 4, 1, 3, 0}, {2, 5, 1, 4, 0, 3}}
	for i, index := range want[orientation-1] {
		x, y := i%(width/32)*32+16, i/(width/32)*32+16
		actual := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
		expected := orientationColors[index]
		for channel, pair := range [][2]uint8{{actual.R, expected.R}, {actual.G, expected.G}, {actual.B, expected.B}} {
			delta := int(pair[0]) - int(pair[1])
			if delta < -15 || delta > 15 {
				t.Fatalf("orientation %d block %d channel %d: got %v, want %v", orientation, i, channel, actual, expected)
			}
		}
	}
	if bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("private-camera-location")) {
		t.Fatal("source metadata retained")
	}
}

func TestJPEGPreservesAllDisplayOrientations(t *testing.T) {
	for _, format := range []string{"jpeg", "png", "webp"} {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			for orientation := 1; orientation <= 8; orientation++ {
				t.Run(fmt.Sprintf("%s/%s/%d", format, order, orientation), func(t *testing.T) {
					data := orientedPicture(t, format, orientationGrid(), orientationTIFF(orientation, order))
					out, err := JPEG(data)
					if err != nil {
						t.Fatal(err)
					}
					assertOrientation(t, out, orientation)
					out, err = JPEG(out)
					if err != nil {
						t.Fatal(err)
					}
					assertOrientation(t, out, orientation)
				})
			}
		}
	}
}

func TestJPEGRejectsBrokenOrientationMetadata(t *testing.T) {
	for _, format := range []string{"jpeg", "png", "webp"} {
		for _, value := range []int{0, 9} {
			t.Run(fmt.Sprintf("%s/%d", format, value), func(t *testing.T) {
				if data, err := JPEG(orientedPicture(t, format, orientationGrid(), orientationTIFF(value, binary.LittleEndian))); err == nil || len(data) != 0 || !strings.Contains(err.Error(), "方向信息") {
					t.Fatal("invalid orientation was discarded silently", len(data), err)
				}
			})
		}
		if data, err := JPEG(orientedPicture(t, format, orientationGrid(), []byte("II\x2a\x00"))); err == nil || len(data) != 0 {
			t.Fatal("truncated EXIF accepted", format, err)
		}
		plain, err := JPEG(orientedPicture(t, format, orientationGrid(), nil))
		if err != nil {
			t.Fatal("metadata-free input rejected", format, err)
		}
		assertOrientation(t, plain, 1)
	}
}

func TestJPEGOrientationKeepsBoundsAndWhiteTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2400, 1200))
	out, err := JPEG(orientedPicture(t, "png", img, orientationTIFF(6, binary.LittleEndian)))
	if err != nil {
		t.Fatal(err)
	}
	result, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" || result.Bounds().Dx() != 800 || result.Bounds().Dy() != 1600 || len(out) >= 1<<20 {
		t.Fatal("rotated image escaped output limits", len(out), err)
	}
	white := color.NRGBAModel.Convert(result.At(400, 800)).(color.NRGBA)
	if white.R < 250 || white.G < 250 || white.B < 250 || white.A != 255 {
		t.Fatal("transparency stopped compositing onto white", white)
	}
	oversized := orientedPicture(t, "png", orientationGrid(), nil)
	binary.BigEndian.PutUint32(oversized[16:], 5000)
	binary.BigEndian.PutUint32(oversized[20:], 4001)
	binary.BigEndian.PutUint32(oversized[29:], crc32.ChecksumIEEE(oversized[12:29]))
	if data, err := JPEG(oversized); err == nil || len(data) != 0 || !strings.Contains(err.Error(), "像素过大") {
		t.Fatal("oversized decoded canvas was not rejected before decode", len(data), err)
	}
}

func TestJPEGFirstFrameCompatibility(t *testing.T) {
	palette := make(color.Palette, len(orientationColors))
	for i, value := range orientationColors {
		palette[i] = value
	}
	frame := image.NewPaletted(image.Rect(0, 0, 96, 64), palette)
	draw.Draw(frame, frame.Bounds(), orientationGrid(), image.Point{}, draw.Src)
	var animated bytes.Buffer
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{frame, image.NewPaletted(frame.Bounds(), palette)}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	data, err := JPEG(animated.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	assertOrientation(t, data, 1)
	// A valid single-frame APNG retains the existing static-frame conversion.
	pngData := orientedPicture(t, "png", orientationGrid(), orientationTIFF(6, binary.LittleEndian))
	chunk := func(name string, payload []byte) []byte {
		out := make([]byte, len(payload)+12)
		binary.BigEndian.PutUint32(out, uint32(len(payload)))
		copy(out[4:], name)
		copy(out[8:], payload)
		binary.BigEndian.PutUint32(out[len(out)-4:], crc32.ChecksumIEEE(out[4:len(out)-4]))
		return out
	}
	control := make([]byte, 26)
	binary.BigEndian.PutUint32(control[4:], 96)
	binary.BigEndian.PutUint32(control[8:], 64)
	binary.BigEndian.PutUint16(control[20:], 1)
	binary.BigEndian.PutUint16(control[22:], 10)
	apng := append(append(append(append([]byte{}, pngData[:33]...), chunk("acTL", []byte{0, 0, 0, 1, 0, 0, 0, 0})...), chunk("fcTL", control)...), pngData[33:]...)
	data, err = JPEG(apng)
	if err != nil {
		t.Fatal(err)
	}
	assertOrientation(t, data, 6)
}

func TestSyncUploadsOrientedPicturesAndPreservesOriginal(t *testing.T) {
	for _, orientation := range []int{6, 9} {
		t.Run(fmt.Sprint(orientation), func(t *testing.T) {
			s, post := fixture(t)
			root := t.TempDir()
			library, err := media.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			s.Media = library
			original := orientedPicture(t, "jpeg", orientationGrid(), orientationTIFF(orientation, binary.LittleEndian))
			item, err := library.Upload(t.Context(), bytes.NewReader(original), media.UploadInput{Filename: "portrait.jpg"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			post, err = s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, CoverURL: item.PublicURL, ContentSource: `<p>照片正文</p><img src="` + item.PublicURL + `">`})
			if err != nil {
				t.Fatal(err)
			}
			requests, uploads, drafts := 0, 0, 0
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				requests++
				switch r.URL.Path {
				case "/cgi-bin/stable_token":
					return reply(r, `{"access_token":"token","expires_in":7200}`), nil
				case "/cgi-bin/material/add_material", "/cgi-bin/media/uploadimg":
					reader, err := r.MultipartReader()
					if err != nil {
						t.Fatal(err)
					}
					part, err := reader.NextPart()
					if err != nil {
						t.Fatal(err)
					}
					payload, err := io.ReadAll(part)
					if err != nil {
						t.Fatal(err)
					}
					assertOrientation(t, payload, 6)
					uploads++
					if r.URL.Path == "/cgi-bin/material/add_material" {
						return reply(r, `{"media_id":"cover-id"}`), nil
					}
					return reply(r, `{"url":"https://mmbiz.qpic.cn/oriented"}`), nil
				case "/cgi-bin/draft/add":
					drafts++
					return reply(r, `{"media_id":"draft-id"}`), nil
				default:
					t.Fatal("unexpected remote request", r.URL.Path)
					return nil, fmt.Errorf("unexpected remote request")
				}
			})}
			state, err := s.Store.View(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
			if err != nil || check.Ready != (orientation == 6) || requests != 0 || check.CheckedImages != 2 {
				t.Fatal("local check did not inspect orientation", check, requests, err)
			}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			job := stateJob(t, s)
			if orientation == 6 {
				if job.Status != "draft" || uploads != 2 || drafts != 1 {
					t.Fatal("oriented photos did not reach the draft", job, uploads, drafts)
				}
			} else if job.Status != "failed" || requests != 0 || !strings.Contains(job.Error, "方向信息") {
				t.Fatal("malformed orientation caused remote writes", job, requests)
			}
			reopened, err := media.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			stored, stream, err := reopened.Open(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			payload, err := io.ReadAll(stream)
			if err != nil || !bytes.Equal(payload, original) || stored.Width != 96 || stored.Height != 64 || stored.Version != item.Version {
				t.Fatal("sync changed original media", stored, err)
			}
		})
	}
}

func BenchmarkWeChatOrientedJPEG(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 3200, 1800))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x / 16), G: uint8(y / 9), B: 80, A: 255})
		}
	}
	for _, orientation := range []int{1, 6} {
		data := orientedPicture(b, "jpeg", img, orientationTIFF(orientation, binary.LittleEndian))
		b.Run(fmt.Sprint(orientation), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := JPEG(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
