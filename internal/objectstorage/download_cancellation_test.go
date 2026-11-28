package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func TestRemoteDownloadStopsAfterRevocation(t *testing.T) {
	for _, timing := range []string{"before-read", "during-final-read"} {
		for _, change := range []string{"request", "configuration", "uninstall"} {
			t.Run(timing+"/"+change, func(t *testing.T) {
				s, manager, _, memory := storageFixture(t)
				s.configContext = queuedFileCancellation{s.configContext}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				const key = "assets/private.txt"
				backend := &transferSourceBackend{memoryBackend: memory, source: key, data: []byte("private bytes"), info: ObjectInfo{Size: 13}}
				s.factory = func(Profile) (Backend, error) { return backend, nil }
				changeState := func() {
					switch change {
					case "request":
						cancel()
					case "configuration":
						config := s.settings
						config.ScanSeconds++
						if _, err := s.Save(config, nil); err != nil {
							t.Fatal(err)
						}
					case "uninstall":
						if err := manager.Uninstall(plugin.ObjectStorage); err != nil {
							t.Fatal(err)
						}
					}
				}
				if timing == "during-final-read" {
					backend.sourceRead = changeState
				}
				reader, _, err := s.RemoteDownload(ctx, "primary", key, s.settings.Version)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if timing == "before-read" {
					changeState()
				}
				var result bytes.Buffer
				_, err = result.ReadFrom(reader)
				if !errors.Is(err, context.Canceled) || result.Len() != 0 {
					t.Fatalf("revoked download disclosed buffered bytes: %q err=%v", result.String(), err)
				}
				if err := reader.Close(); err != nil || !backend.sourceClosed {
					t.Fatalf("download source was not released: %v", err)
				}
			})
		}
	}
}

func TestFileDownloadAuthorizationReadBoundaries(t *testing.T) {
	for _, timing := range []string{"final-read", "byte-budget", "idle"} {
		t.Run(timing, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			denied := errors.New("test authorization revoked")
			revoked, closed := false, false
			ctx = WithFileAuthorization(ctx, func(context.Context) error {
				if revoked {
					return denied
				}
				return nil
			})
			data := []byte("private final bytes")
			if timing == "byte-budget" {
				data = bytes.Repeat([]byte("x"), 2<<20)
			}
			body := &transferFinalReader{data: data, finish: func() { revoked = true }, closed: &closed}
			reader := newFileDownloadReader(ctx, body, cancel)
			defer reader.Close()
			if timing == "idle" {
				revoked = true
				reader.checkedAt = time.Now().Add(-2 * fileAuthorizationInterval)
			} else if timing == "byte-budget" {
				revoked = true
			}
			var output bytes.Buffer
			_, err := io.Copy(&output, reader)
			if !errors.Is(err, denied) || !closed || ctx.Err() == nil || output.Len() >= fileAuthorizationBytes || timing != "byte-budget" && output.Len() != 0 {
				t.Fatalf("revoked download: bytes=%d err=%v closed=%v cancelled=%v", output.Len(), err, closed, ctx.Err())
			}
		})
	}
}

func TestFileDownloadDoesNotResolveSessionOnEveryRead(t *testing.T) {
	const size = 8 << 20
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checks := 0
	ctx = WithFileAuthorization(ctx, func(context.Context) error { checks++; return nil })
	reader := newFileDownloadReader(ctx, io.NopCloser(strings.NewReader(strings.Repeat("x", size))), cancel)
	defer reader.Close()
	buffer := make([]byte, 4096)
	total, reads := 0, 0
	for {
		n, err := reader.Read(buffer)
		total += n
		reads++
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != size || reads < 1000 || checks < 2 || checks > 16 {
		t.Fatalf("session lookup amplification: bytes=%d reads=%d checks=%d", total, reads, checks)
	}
	t.Logf("8 MiB download: %d read calls, %d authorization checks", reads, checks)
}

func TestFileAuthorizationRechecksCancellationAfterLookup(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = WithFileAuthorization(ctx, func(context.Context) error { cancel(); return nil })
	err := s.RemoteUpload(ctx, "primary", "assets/cancelled.txt", s.settings.Version, strings.NewReader("cancelled"), "text/plain")
	if !errors.Is(err, context.Canceled) || backend.puts != 0 {
		t.Fatalf("cancellation during lookup permitted a write: %v puts=%d", err, backend.puts)
	}
}
