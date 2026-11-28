package pluginjobs

import "context"

// readState exposes a borrowed snapshot only to package-owned read operations.
// The callback must neither modify state nor retain its maps, slices or pointers.
// FileBackend keeps the lock and verifies external file changes; other backends
// keep their existing Read contract. Public Read still returns an owned copy.
func (q *Queue) readState(ctx context.Context, read func(State) error) error {
	if backend, ok := q.Backend.(*FileBackend); ok {
		return backend.readState(ctx, read)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := read(state); err != nil {
		return err
	}
	return ctx.Err()
}

func (b *FileBackend) readState(ctx context.Context, read func(State) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.unchanged(ctx); err != nil {
		return err
	}
	if err := read(b.state); err != nil {
		return err
	}
	return ctx.Err()
}
