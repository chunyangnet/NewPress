package linkpage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestDirectoryPersistenceConcurrencyAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	s, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	a := Actor{ID: 1, Admin: true}
	cfg := Defaults()
	now := time.Now().UTC()
	create := Command{Action: "create", Key: "create-directory", ExpectedUserID: 1, Settings: &cfg}
	if _, e = s.Apply(t.Context(), "friends", Actor{ID: 2}, create, now); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = s.Apply(t.Context(), "friends", a, create, now); e != nil {
		t.Fatal(e)
	}
	logo, e := NormalizeLogo(testPNG(t, 400, 80))
	if e != nil {
		t.Fatal(e)
	}
	cmd := Command{Action: "save_item", Version: 1, Key: "insert-example", ExpectedUserID: 1, Item: &Item{Name: "Example", URL: "example.com", Logo: logo, Enabled: true}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, e := s.Apply(t.Context(), "friends", a, cmd, now); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	b, e := reopened.Get(t.Context(), "friends")
	if e != nil || b.Version != 2 || len(b.Items) != 1 || b.Items[0].URL != "https://example.com/" {
		t.Fatal(b, e)
	}
	replay, e := reopened.Apply(t.Context(), "friends", a, cmd, now)
	if e != nil || !replay.Replay {
		t.Fatal(e, replay)
	}
	cmd.Key = "different-command"
	cmd.Item.Name = "changed"
	if _, e = reopened.Apply(t.Context(), "friends", a, cmd, now); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	cfg.Style = "wall"
	_, e = reopened.Apply(t.Context(), "friends", a, Command{Action: "configure", Version: 2, Key: "style-directory", ExpectedUserID: 1, Settings: &cfg}, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Ready(t.Context()); !errors.Is(e, ErrChanged) {
		t.Fatal("external file modification ignored", e)
	}
	if _, e = reopened.Apply(t.Context(), "friends", a, Command{Action: "delete_item", Version: 3, ItemID: 1, Key: "delete-link-item", ExpectedUserID: 1}, now); e != nil {
		t.Fatal(e)
	}
	b, _ = reopened.Get(t.Context(), "friends")
	if len(b.Items) != 0 {
		t.Fatal("icon and item not removed")
	}
}
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestMetadataParsingAndImageBoundary(t *testing.T) {
	doc, e := html.Parse(strings.NewReader(`<html><head><title>Example</title><meta property="og:title" content="  一个 &amp; 朋友 "><meta name="description" content="一段简介"><link rel="shortcut icon" href="/logo.png"></head></html>`))
	if e != nil {
		t.Fatal(e)
	}
	m, icons := parseMetadata(doc, "https://example.com/")
	if m.Title != "一个 & 朋友" || m.Description != "一段简介" || len(icons) != 1 || icons[0] != "/logo.png" {
		t.Fatal(m, icons)
	}
	for _, raw := range []string{"http://example.com", "https://127.0.0.1", "https://169.254.169.254/", "https://[::1]", "https://user:pass@example.com", "https://example.com:8080", "javascript:alert(1)", "localhost"} {
		if _, e := NormalizeURL(raw); e == nil {
			t.Fatal("accepted unsafe URL", raw)
		}
	}
	for _, b := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), []byte("not an image"), testPNG(t, 5000, 1)} {
		if _, e := NormalizeLogo(b); e == nil {
			t.Fatal("accepted unsafe image")
		}
	}
	s, e := NormalizeLogo(testPNG(t, 400, 100))
	if e != nil {
		t.Fatal(e)
	}
	b, e := DecodeLogo(s)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(b))
	if cfg.Width != 128 || cfg.Height != 32 {
		t.Fatal(cfg)
	}
	if _, e = DecodeLogo(base64.StdEncoding.EncodeToString(testPNG(t, 200, 200))); e == nil {
		t.Fatal("oversized stored icon")
	}
}
func TestStrictDirectoryDocument(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(p, []byte(`{"schema":1,"schema":1,"boards":[]}`), 0600)
	if _, e := NewFileStore(p); e == nil {
		t.Fatal("duplicate JSON key accepted")
	}
}
