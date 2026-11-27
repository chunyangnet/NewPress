package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

type stagedUpload struct {
	path    string
	payload []byte
	prefix  []byte
	digest  [32]byte
	size    int64
}

func (s *stagedUpload) discard() {
	if s.path != "" {
		_ = os.Remove(s.path)
	}
	s.payload = nil
}

func (s *stagedUpload) dimensions(mediaType string) (int, int, error) {
	if !strings.HasPrefix(mediaType, "image/") {
		return 0, 0, nil
	}
	var (
		width  int
		height int
		err    error
	)
	if mediaType == "image/webp" {
		width, height, err = webPDimensions(s.prefix)
	} else {
		reader, openErr := s.open()
		if openErr != nil {
			return 0, 0, openErr
		}
		config, _, decodeErr := image.DecodeConfig(reader)
		_ = reader.Close()
		if decodeErr != nil {
			return 0, 0, fmt.Errorf("decode image dimensions: %w", ErrInvalidInput)
		}
		width, height = config.Width, config.Height
	}
	if err != nil || width <= 0 || height <= 0 || width > maxImageDimension || height > maxImageDimension || int64(width)*int64(height) > maxImagePixels {
		return 0, 0, fmt.Errorf("image dimensions are outside safety limits: %w", ErrInvalidInput)
	}
	return width, height, nil
}

func (s *stagedUpload) open() (ReadSeekCloser, error) {
	if s.path != "" {
		file, _, err := packagefs.OpenRegularNoSymlink(s.path)
		return file, err
	}
	return &memoryObject{Reader: bytes.NewReader(s.payload)}, nil
}

type memoryObject struct{ *bytes.Reader }

func (m *memoryObject) Close() error { return nil }

type prefixWriter struct {
	maximum int
	data    []byte
}

func (w *prefixWriter) Write(payload []byte) (int, error) {
	if len(w.data) < w.maximum {
		remaining := w.maximum - len(w.data)
		w.data = append(w.data, payload[:min(len(payload), remaining)]...)
	}
	return len(payload), nil
}

type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (r contextReader) Read(payload []byte) (int, error) {
	select {
	case <-r.context.Done():
		return 0, r.context.Err()
	default:
		return r.reader.Read(payload)
	}
}

func (m *Manager) stageUpload(ctx context.Context, reader io.Reader, maximum int64) (*stagedUpload, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	staged := &stagedUpload{}
	prefix := &prefixWriter{maximum: 512}
	hash := sha256.New()
	var destination io.Writer
	var file *os.File
	if m.root == "" {
		buffer := &bytes.Buffer{}
		destination = buffer
		defer func() { staged.payload = append([]byte(nil), buffer.Bytes()...) }()
	} else {
		var err error
		if _, err := packagefs.LstatNoSymlink(filepath.Join(m.root, "tmp")); err != nil {
			return nil, fmt.Errorf("inspect media staging directory: %w", err)
		}
		file, err = os.CreateTemp(filepath.Join(m.root, "tmp"), ".upload-*.tmp")
		if err != nil {
			return nil, fmt.Errorf("create media staging file: %w", err)
		}
		staged.path = file.Name()
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			staged.discard()
			return nil, fmt.Errorf("secure media staging file: %w", err)
		}
		destination = file
	}
	limited := &io.LimitedReader{R: contextReader{context: ctx, reader: reader}, N: maximum + 1}
	size, err := io.Copy(io.MultiWriter(destination, hash, prefix), limited)
	if file != nil {
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		staged.discard()
		return nil, fmt.Errorf("read media upload: %w", err)
	}
	if size > maximum {
		staged.discard()
		return nil, ErrTooLarge
	}
	if size == 0 {
		staged.discard()
		return nil, fmt.Errorf("media upload is empty: %w", ErrInvalidInput)
	}
	copy(staged.digest[:], hash.Sum(nil))
	staged.prefix = append([]byte(nil), prefix.data...)
	staged.size = size
	return staged, nil
}
