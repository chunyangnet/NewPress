package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"
)

func TestAvatarBoundsCropAndReencoding(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 80, B: 20, A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	input.WriteString("private source metadata")
	prepared, err := PrepareAvatar(bytes.NewReader(input.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	out, format, err := image.Decode(prepared)
	if err != nil || format != "jpeg" || out.Bounds().Dx() != 256 || out.Bounds().Dy() != 256 {
		t.Fatal(format, err)
	}
	for _, bad := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), input.Bytes()[:24]} {
		if _, err := PrepareAvatar(bytes.NewReader(bad)); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	input.Reset()
	_ = png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 2049, 1)))
	if _, err := PrepareAvatar(bytes.NewReader(input.Bytes())); err == nil {
		t.Fatal("oversized decoded image accepted")
	}
}

type pausedAvatarReader struct {
	*bytes.Reader
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (r *pausedAvatarReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.resume
	})
	return r.Reader.Read(p)
}

func TestAvatarProcessingRejectsConcurrentWorkAndReleasesCapacity(t *testing.T) {
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA64(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	paused := &pausedAvatarReader{Reader: bytes.NewReader(input.Bytes()), entered: make(chan struct{}), resume: make(chan struct{})}
	var release sync.Once
	resume := func() { release.Do(func() { close(paused.resume) }) }
	defer resume()
	finished := make(chan error, 1)
	go func() {
		_, err := PrepareAvatar(paused)
		finished <- err
	}()
	select {
	case <-paused.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first avatar did not start reading")
	}
	if _, err := PrepareAvatar(bytes.NewReader(input.Bytes())); !errors.Is(err, ErrImageBusy) {
		t.Fatalf("concurrent avatar = %v, want busy", err)
	}
	resume()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first avatar did not finish")
	}
	if _, err := PrepareAvatar(bytes.NewReader([]byte("invalid PNG"))); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("invalid avatar = %v", err)
	}
	if _, err := PrepareAvatar(bytes.NewReader(input.Bytes())); err != nil {
		t.Fatal("processing did not recover after successful and invalid inputs", err)
	}
}
