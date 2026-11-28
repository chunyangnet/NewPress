package notification

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"newpress.local/newpress/internal/packagefs"
)

func readFile(path string) ([]byte, error) {
	var body bytes.Buffer
	if err := readFileTo(context.Background(), path, &body); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// Hash checks stream the complete file into a fixed buffer. Startup uses the
// same identity, length and timestamp checks while collecting its JSON bytes.
func readFileTo(ctx context.Context, path string, destination io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, before, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if before.Size() > maxFileBytes {
		return errors.New("notification store exceeds size limit")
	}
	if body, ok := destination.(*bytes.Buffer); ok {
		// ReaderFrom reserves MinRead for the final EOF read as well.
		body.Grow(int(before.Size()) + bytes.MinRead)
	}
	reader := io.LimitReader(&notificationFileReader{ctx: ctx, reader: f}, before.Size()+1)
	var size int64
	if _, ok := destination.(io.ReaderFrom); ok {
		size, err = io.Copy(destination, reader)
	} else {
		size, err = io.CopyBuffer(destination, reader, make([]byte, min(before.Size()+1, 128<<10)))
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, opened) || !os.SameFile(before, current) || before.Size() != size ||
		opened.Size() != before.Size() || current.Size() != before.Size() ||
		!opened.ModTime().Equal(before.ModTime()) || !current.ModTime().Equal(before.ModTime()) {
		return errors.New("notification file changed during read")
	}
	return ctx.Err()
}

type notificationFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *notificationFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func writeFile(ctx context.Context, path string, data []byte, beforeCommit func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > maxFileBytes {
		return errors.New("notification store exceeds size limit")
	}
	dir := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".notifications-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Serialization and durable staging can take time. Recheck the original
	// file and cancellation before publishing; discard only our temporary file
	// on failure. This is still a single-writer store, not cross-process CAS.
	if err = beforeCommit(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if dirFile, err := packagefs.OpenDirectoryNoSymlink(dir); err == nil {
			_ = dirFile.Sync()
			_ = dirFile.Close()
		}
	}
	return nil
}
