package wechat

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func TestSyncUploadsWebPDisplayColors(t *testing.T) {
	for _, name := range []string{"palette", "alpha-rotated"} {
		t.Run(name, func(t *testing.T) {
			s, post := fixture(t)
			library, err := media.NewManager("")
			if err != nil {
				t.Fatal(err)
			}
			s.Media = library
			original, reference := webpColorFixture(t, name)
			item, err := s.Media.Upload(t.Context(), bytes.NewReader(original), media.UploadInput{Filename: name + ".webp"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			post, err = s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, CoverURL: item.PublicURL, ContentSource: `<p>色彩测试</p><img src="` + item.PublicURL + `">`})
			if err != nil {
				t.Fatal(err)
			}
			requests, uploads := 0, 0
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
					data, err := io.ReadAll(part)
					if err != nil {
						t.Fatal(err)
					}
					assertWebPJPEGColor(t, data, reference)
					uploads++
					if r.URL.Path == "/cgi-bin/material/add_material" {
						return reply(r, `{"media_id":"cover-id"}`), nil
					}
					return reply(r, `{"url":"https://mmbiz.qpic.cn/color-image"}`), nil
				case "/cgi-bin/draft/add":
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
			if err != nil || !check.Ready || check.CheckedImages != 2 || requests != 0 {
				t.Fatal("local check did not prepare the WebP images", check, requests, err)
			}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if job := stateJob(t, s); job.Status != "draft" || uploads != 2 {
				t.Fatal("corrected images did not reach the draft", job, uploads)
			}
		})
	}
}

func TestJPEGWebPColors(t *testing.T) {
	for _, name := range []string{"palette", "alpha", "alpha-rotated", "gradient", "lossless", "narrow"} {
		t.Run(name, func(t *testing.T) {
			original, reference := webpColorFixture(t, name)
			got, err := JPEG(original)
			if err != nil {
				t.Fatal(err)
			}
			assertWebPJPEGColor(t, got, reference)
		})
	}
}

func BenchmarkWebPJPEGColors(b *testing.B) {
	for _, name := range []string{"large", "large-rotated", "large-alpha"} {
		data, err := os.ReadFile(filepath.Join("..", "media", "testdata", "webp-color", name+".webp"))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := JPEG(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func webpColorFixture(t testing.TB, name string) ([]byte, []byte) {
	t.Helper()
	read := func(extension string) []byte {
		data, err := os.ReadFile(filepath.Join("..", "media", "testdata", "webp-color", name+extension))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return read(".webp"), read(".png")
}

func assertWebPJPEGColor(t testing.TB, payload, reference []byte) {
	t.Helper()
	// Both outputs get the same JPEG compression and white background. The
	// reference pixels themselves were decoded independently by libwebp.
	wantBytes, err := JPEG(reference)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := image.Decode(bytes.NewReader(wantBytes))
	if err != nil {
		t.Fatal(err)
	}
	got, format, err := image.Decode(bytes.NewReader(payload))
	if err != nil || format != "jpeg" || len(payload) >= 1<<20 {
		t.Fatal("invalid bounded JPEG", format, len(payload), err)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatal("display dimensions differ", got.Bounds(), want.Bounds())
	}
	var sums [3]uint64
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < got.Bounds().Dx(); x++ {
			gr, gg, gb, _ := got.At(x, y).RGBA()
			wr, wg, wb, _ := want.At(x, y).RGBA()
			for i, pair := range [][2]uint32{{gr, wr}, {gg, wg}, {gb, wb}} {
				a, b := pair[0], pair[1]
				if a > b {
					a, b = b, a
				}
				sums[i] += uint64(b - a)
			}
		}
	}
	for channel, sum := range sums {
		mean := float64(sum) / float64(got.Bounds().Dx()*got.Bounds().Dy()) / 257
		if mean > 2.5 {
			t.Errorf("channel %d mean error %.3f exceeds 2.5", channel, mean)
		}
	}
}
