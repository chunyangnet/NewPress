package objectstorage

import (
	"context"
	"io"
	"sync"
	"time"
)

type fileAuthorizationKey struct{}

// WithFileAuthorization binds a file request to its original caller. The check
// runs at remote read/write boundaries, and periodically during a download.
// It must use the supplied context and return errors without writing a response.
func WithFileAuthorization(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, fileAuthorizationKey{}, check)
}

func checkFileAccess(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, _ := ctx.Value(fileAuthorizationKey{}).(func(context.Context) error); check != nil {
		err := check(ctx)
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

const fileAuthorizationBytes = 1 << 20
const fileAuthorizationInterval = time.Second

type fileDownloadReader struct {
	reader    io.ReadCloser
	ctx       context.Context
	cancel    context.CancelFunc
	guarded   bool
	checkedAt time.Time
	readBytes int
	closeOnce sync.Once
	closeErr  error
}

func newFileDownloadReader(ctx context.Context, reader io.ReadCloser, cancel context.CancelFunc) *fileDownloadReader {
	check, _ := ctx.Value(fileAuthorizationKey{}).(func(context.Context) error)
	return &fileDownloadReader{reader: reader, ctx: ctx, cancel: cancel, guarded: check != nil, checkedAt: time.Now()}
}

func (r *fileDownloadReader) check(final bool) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.guarded && (final || r.readBytes >= fileAuthorizationBytes || time.Since(r.checkedAt) >= fileAuthorizationInterval) {
		if err := checkFileAccess(r.ctx); err != nil {
			return err
		}
		r.checkedAt, r.readBytes = time.Now(), 0
	}
	return nil
}

func (r *fileDownloadReader) Read(p []byte) (int, error) {
	if err := r.check(false); err != nil {
		_ = r.Close()
		return 0, err
	}
	if r.guarded {
		p = p[:min(len(p), fileAuthorizationBytes-r.readBytes)]
	}
	n, err := r.reader.Read(p)
	r.readBytes += n
	// A reader may return bytes and EOF together, or return buffered bytes after
	// its context was cancelled. Do not release those bytes across a revoked
	// boundary. Session lookups are bounded by bytes/time instead of read calls.
	if accessErr := r.check(err != nil); accessErr != nil {
		_ = r.Close()
		return 0, accessErr
	}
	if err != nil && err != io.EOF {
		return n, safeStorageError("读取", err)
	}
	return n, err
}

func (r *fileDownloadReader) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		r.closeErr = safeStorageError("关闭连接", r.reader.Close())
	})
	return r.closeErr
}
