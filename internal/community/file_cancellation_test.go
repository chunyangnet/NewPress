package community

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type attachmentCancelReader struct {
	io.Reader
	cancel context.CancelFunc
	reads  int
}

func (r *attachmentCancelReader) Read(p []byte) (int, error) {
	r.reads++
	n, err := r.Reader.Read(p[:min(len(p), 10)])
	r.cancel()
	return n, err
}

func TestCircleFileStageCancellationStopsInput(t *testing.T) {
	for _, alreadyCancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-read", true: "before-read"}[alreadyCancelled], func(t *testing.T) {
			files, err := NewFiles("")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if alreadyCancelled {
				cancel()
			}
			reader := &attachmentCancelReader{Reader: strings.NewReader(strings.Repeat("private", 1000)), cancel: cancel}
			_, err = files.Stage(ctx, "cancel", "attachment-cancel-key", 1, "private.txt", reader)
			wantReads := 1
			if alreadyCancelled {
				wantReads = 0
			}
			if !errors.Is(err, context.Canceled) || reader.reads != wantReads || len(files.memory) != 0 {
				t.Fatalf("cancelled staging consumed input: reads=%d want=%d objects=%d error=%v", reader.reads, wantReads, len(files.memory), err)
			}
		})
	}
}

// Signal after taking the initial cancellation snapshot, so the test cancels
// between that check and acquisition of the already-held object lock.
type attachmentObservedContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
	observe func() bool
}

func (c *attachmentObservedContext) Err() error {
	err := c.Context.Err()
	if c.observe == nil || c.observe() {
		c.once.Do(func() { close(c.checked) })
	}
	return err
}

type attachmentObservedReader struct {
	io.Reader
	done atomic.Bool
}

func (r *attachmentObservedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil {
		r.done.Store(true)
	}
	return n, err
}

func TestCircleFileStageRechecksCancellationAfterLock(t *testing.T) {
	files, err := NewFiles("")
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &attachmentObservedReader{Reader: strings.NewReader("private attachment")}
	ctx := &attachmentObservedContext{Context: parent, checked: make(chan struct{}), observe: reader.done.Load}
	files.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := files.Stage(ctx, "cancel", "attachment-stage-lock", 1, "private.txt", reader)
		done <- err
	}()
	<-ctx.checked
	cancel()
	files.mu.Unlock()
	err = <-done
	if !errors.Is(err, context.Canceled) || len(files.memory) != 0 {
		t.Fatalf("cancelled staging retained bytes: objects=%d error=%v", len(files.memory), err)
	}
}

func TestCircleFileReadRechecksCancellationAfterLock(t *testing.T) {
	files, err := NewFiles("")
	if err != nil {
		t.Fatal(err)
	}
	file, err := files.Stage(t.Context(), "cancel", "attachment-lock-key", 1, "private.txt", strings.NewReader("private attachment"))
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &attachmentObservedContext{Context: parent, checked: make(chan struct{})}
	files.mu.Lock()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := files.Read(ctx, file)
		done <- result{data, err}
	}()
	<-ctx.checked
	cancel()
	files.mu.Unlock()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || len(got.data) != 0 {
		t.Fatalf("cancelled locked read returned bytes: bytes=%d error=%v", len(got.data), got.err)
	}
}
