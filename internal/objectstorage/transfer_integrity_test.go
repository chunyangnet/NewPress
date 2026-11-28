package objectstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

type transferFinalReader struct {
	data   []byte
	finish func()
	closed *bool
}

func (r *transferFinalReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		if r.finish != nil {
			r.finish()
			r.finish = nil
		}
		return n, io.EOF
	}
	return n, nil
}

func (r *transferFinalReader) Close() error {
	if r.closed != nil {
		*r.closed = true
	}
	return nil
}

type transferSourceBackend struct {
	*memoryBackend
	source                 string
	data                   []byte
	info                   ObjectInfo
	sourceRead, targetRead func()
	sourceClosed           bool
}

// Keep asynchronous cancellation callbacks queued until the operation closes.
// A configuration boundary must not rely on the callback already being scheduled.
type queuedFileCancellation struct{ context.Context }

func (c queuedFileCancellation) Value(any) any                { return nil }
func (c queuedFileCancellation) AfterFunc(func()) func() bool { return func() bool { return true } }

func (b *transferSourceBackend) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if key == b.source {
		return &transferFinalReader{data: bytes.Clone(b.data), finish: b.sourceRead, closed: &b.sourceClosed}, b.info, nil
	}
	reader, info, err := b.memoryBackend.Open(ctx, key)
	if err != nil || b.targetRead == nil {
		return reader, info, err
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		return nil, info, err
	}
	return &transferFinalReader{data: data, finish: b.targetRead}, info, nil
}

func transferChecksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestRemoteTransferValidatesSourceBeforeCopyOrMove(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		for _, state := range []string{"short", "extra", "checksum", "cancel-source", "cancel-verification", "valid", "unknown-length", "no-checksum", "empty"} {
			t.Run(action+"/"+state, func(t *testing.T) {
				s, _, _, memory := storageFixture(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				const source, target = "assets/source.txt", "assets/target.txt"
				original := []byte("complete source data")
				if state == "empty" {
					original = []byte{}
				}
				info := ObjectInfo{Size: int64(len(original)), ContentType: "text/plain", SHA256: transferChecksum(original)}
				memory.objects[source], memory.metadata[source] = bytes.Clone(original), info
				backend := &transferSourceBackend{memoryBackend: memory, source: source, data: bytes.Clone(original), info: info}
				switch state {
				case "short":
					backend.data = backend.data[:4]
				case "extra":
					backend.data = append(backend.data, 'x')
				case "checksum":
					backend.data[0] ^= 1
				case "cancel-source":
					backend.sourceRead = cancel
				case "cancel-verification":
					backend.targetRead = cancel
				case "unknown-length":
					backend.info.Size = -1
				case "no-checksum":
					backend.info.SHA256 = ""
				}
				s.factory = func(Profile) (Backend, error) { return backend, nil }
				err := s.RemoteAction(ctx, "primary", source, "primary", target, action, s.settings.Version)
				valid := state == "valid" || state == "unknown-length" || state == "no-checksum" || state == "empty"
				if valid {
					if err != nil || !bytes.Equal(memory.objects[target], original) || memory.puts != 1 {
						t.Fatalf("valid transfer failed: err=%v puts=%d target=%q", err, memory.puts, memory.objects[target])
					}
					if action == "move" && memory.deletes != 1 {
						t.Fatal("valid move did not delete its source")
					}
				} else {
					if err == nil {
						t.Error("invalid or cancelled source was accepted")
					}
					if strings.HasPrefix(state, "cancel") && !errors.Is(err, context.Canceled) {
						t.Errorf("cancellation lost: %v", err)
					}
					if memory.deletes != 0 || !bytes.Equal(memory.objects[source], original) {
						t.Errorf("source lost after failed transfer: deletes=%d source=%q", memory.deletes, memory.objects[source])
					}
					if state != "cancel-verification" && memory.puts != 0 {
						t.Errorf("invalid source reached destination: puts=%d target=%q", memory.puts, memory.objects[target])
					}
				}
				if !backend.sourceClosed {
					t.Fatal("source stream was not closed")
				}
			})
		}
	}
}

func TestRemoteUploadStopsAfterCancelledFinalRead(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &transferFinalReader{data: []byte("upload fixture"), finish: cancel}
	err := s.RemoteUpload(ctx, "primary", "assets/cancelled.txt", s.settings.Version, reader, "text/plain")
	if !errors.Is(err, context.Canceled) || backend.puts != 0 {
		t.Fatalf("cancelled upload continued: err=%v puts=%d", err, backend.puts)
	}
}

func TestRemoteTransferStopsAfterConfigurationChange(t *testing.T) {
	for _, stage := range []string{"source", "verification"} {
		t.Run(stage, func(t *testing.T) {
			s, _, _, memory := storageFixture(t)
			s.configContext = queuedFileCancellation{s.configContext}
			const source, target = "assets/source.txt", "assets/target.txt"
			original := []byte("configuration change fixture")
			info := ObjectInfo{Size: int64(len(original)), ContentType: "text/plain", SHA256: transferChecksum(original)}
			memory.objects[source], memory.metadata[source] = bytes.Clone(original), info
			backend := &transferSourceBackend{memoryBackend: memory, source: source, data: bytes.Clone(original), info: info}
			change := func() {
				config := s.settings
				config.ScanSeconds++
				if _, err := s.Save(config, nil); err != nil {
					t.Error("save new configuration:", err)
				}
			}
			if stage == "source" {
				backend.sourceRead = change
			} else {
				backend.targetRead = change
			}
			s.factory = func(Profile) (Backend, error) { return backend, nil }
			err := s.RemoteAction(t.Context(), "primary", source, "primary", target, "move", s.settings.Version)
			if err == nil || memory.deletes != 0 || !bytes.Equal(memory.objects[source], original) {
				t.Fatalf("changed configuration still moved source: err=%v deletes=%d", err, memory.deletes)
			}
			if stage == "source" && memory.puts != 0 {
				t.Fatalf("stale config uploaded destination %d times", memory.puts)
			}
		})
	}
}
