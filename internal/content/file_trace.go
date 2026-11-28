package content

import (
	"context"
	"io"
	"runtime/trace"
)

// Only install this wrapper while an execution trace is enabled. Region names
// are fixed, with no document bytes, record identifiers, or file paths.
type contentFileTraceWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contentFileTraceWriter) Write(data []byte) (int, error) {
	defer trace.StartRegion(w.ctx, "content.file.write").End()
	return w.writer.Write(data)
}
