package httpserver

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Write runs on the handler goroutine even when ServeContent produces ranges
// on another goroutine. Abort there instead of silently returning a truncated
// file or appending an error document to bytes already transmitted.
type privateResponseWriter struct {
	http.ResponseWriter
	ctx          context.Context
	access       func() error
	cacheControl string
	checkedAt    time.Time
	checkBytes   int
	written      int64
}

const privateResponseCheckBytes = 1 << 20

func privateResponse(w http.ResponseWriter, r *http.Request, access func() error) *privateResponseWriter {
	return &privateResponseWriter{ResponseWriter: w, ctx: r.Context(), access: access, cacheControl: w.Header().Get("Cache-Control"), checkedAt: time.Now()}
}

func (w *privateResponseWriter) WriteHeader(status int) {
	// ServeContent otherwise removes this policy on range error responses.
	if w.cacheControl != "" {
		w.Header().Set("Cache-Control", w.cacheControl)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *privateResponseWriter) check(final bool) {
	if w.ctx.Err() != nil {
		panic(http.ErrAbortHandler)
	}
	if final || w.checkBytes >= privateResponseCheckBytes || time.Since(w.checkedAt) >= time.Second {
		if w.access() != nil {
			panic(http.ErrAbortHandler)
		}
		w.checkedAt, w.checkBytes = time.Now(), 0
	}
}

func (w *privateResponseWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		w.check(false)
		chunk := min(len(p), privateResponseCheckBytes-w.checkBytes)
		length, lengthErr := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
		w.check(lengthErr == nil && w.written+int64(chunk) >= length)
		n, err := w.ResponseWriter.Write(p[:chunk])
		w.written += int64(n)
		w.checkBytes += n
		written += n
		w.check(false)
		if err != nil {
			return written, err
		}
		if n != chunk {
			return written, io.ErrShortWrite
		}
		p = p[n:]
	}
	return written, nil
}
