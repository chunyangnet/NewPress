package pluginjobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"newpress.local/newpress/internal/packagefs"
)

// FileBackend is single-process storage. The application must hold its data
// directory lock for the entire lifetime of this backend. An empty path gives
// an isolated in-memory backend with the same transaction semantics.
type FileBackend struct {
	mu     sync.Mutex
	path   string
	state  State
	digest [32]byte
	exists bool
}

func NewFileBackend(path string) (*FileBackend, error) {
	b := &FileBackend{path: path, state: InitialState()}
	if path == "" {
		return b, nil
	}
	body, exists, err := readStateFile(path)
	if err != nil {
		return nil, err
	}
	if exists {
		s, err := packagefs.DecodeJSON[State](bytes.NewReader(body), MaxSnapshotBytes, "plugin task state")
		if err != nil {
			return nil, err
		}
		if err = ValidateState(s); err != nil {
			return nil, err
		}
		if err = checkStorageSize(s, false); err != nil {
			return nil, err
		}
		b.state = s
		b.digest = sha256.Sum256(body)
		b.exists = true
	}
	return b, nil
}

func readStateFile(path string) ([]byte, bool, error) {
	var body bytes.Buffer
	exists, err := readStateFileTo(context.Background(), path, &body)
	if err != nil {
		return nil, false, err
	}
	return body.Bytes(), exists, nil
}

// Stream integrity checks into a hash without allocating the entire snapshot.
// Startup/maintenance readers can still collect the same verified bytes.
func readStateFileTo(ctx context.Context, path string, destination io.Writer) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if info.Size() > MaxSnapshotBytes {
		return false, ErrCapacity
	}
	if buffer, ok := destination.(*bytes.Buffer); ok {
		buffer.Grow(int(info.Size()) + bytes.MinRead)
	}
	var size int64
	reader := io.LimitReader(&stateFileReader{ctx: ctx, reader: f}, MaxSnapshotBytes+1)
	if _, ok := destination.(io.ReaderFrom); ok {
		size, err = io.Copy(destination, reader)
	} else {
		// A larger fixed buffer reduces small file reads during hash checks,
		// without retaining or allocating a queue-sized byte slice.
		size, err = io.CopyBuffer(destination, reader, make([]byte, 128<<10))
	}
	if err != nil {
		return false, err
	}
	if size > MaxSnapshotBytes {
		return false, ErrCapacity
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	after, err := f.Stat()
	if err != nil {
		return false, err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, after) || !os.SameFile(info, current) || info.Size() != after.Size() || info.Size() != size || !info.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) || after.Size() != current.Size() {
		return false, ErrConflict
	}
	return true, ctx.Err()
}

type stateFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *stateFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func cloneState(s State) State {
	out := State{Schema: s.Schema, Kind: s.Kind, Jobs: make(map[string]Job, len(s.Jobs))}
	if s.Quarantines != nil {
		out.Quarantines = make(map[string]QuarantinedRecord, len(s.Quarantines))
		for key, record := range s.Quarantines {
			out.Quarantines[key] = CloneQuarantine(record)
		}
	}
	if s.ArchivedJobs != nil {
		out.ArchivedJobs = make(map[string]ArchivedJob, len(s.ArchivedJobs))
		for id, job := range s.ArchivedJobs {
			job.Review = CloneReview(job.Review)
			if job.Reissue != nil {
				summary := *job.Reissue
				job.Reissue = &summary
			}
			out.ArchivedJobs[id] = job
		}
	}
	if s.ArchivedFanouts != nil {
		out.ArchivedFanouts = make(map[string]ArchivedFanout, len(s.ArchivedFanouts))
		for key, archived := range s.ArchivedFanouts {
			archived.Receipt.Subscriptions = append([]Subscription{}, archived.Receipt.Subscriptions...)
			out.ArchivedFanouts[key] = archived
		}
	}
	if s.Fanouts != nil {
		out.Fanouts = make(map[string]FanoutReceipt, len(s.Fanouts))
		for key, receipt := range s.Fanouts {
			receipt.Subscriptions = append([]Subscription{}, receipt.Subscriptions...)
			out.Fanouts[key] = receipt
		}
	}
	for id, j := range s.Jobs {
		j.Payload = append(json.RawMessage(nil), j.Payload...)
		j.Review = CloneReview(j.Review)
		out.Jobs[id] = j
	}
	return out
}
func (b *FileBackend) unchanged(ctx context.Context) error {
	if b.path == "" {
		return ctx.Err()
	}
	hash := sha256.New()
	exists, err := readStateFileTo(ctx, b.path, hash)
	if err != nil {
		return err
	}
	if exists != b.exists || (exists && !bytes.Equal(hash.Sum(nil), b.digest[:])) {
		return ErrConflict
	}
	return nil
}
func (b *FileBackend) Read(ctx context.Context) (State, error) {
	var state State
	if err := b.readState(ctx, func(current State) error { state = cloneState(current); return nil }); err != nil {
		return State{}, err
	}
	return state, nil
}
func (b *FileBackend) Update(ctx context.Context, fn func(*State) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.unchanged(ctx); err != nil {
		return err
	}
	next := cloneState(b.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := ValidateState(next); err != nil {
		return err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(body) > MaxSnapshotBytes {
		return ErrCapacity
	}
	// Reserve execution metadata when admitting any new task or event receipt.
	// Existing transitions consume that reservation; replay and completion must
	// not reserve the same space again or become stuck at the size boundary.
	if err := checkStorageSize(next, admitsEntries(b.state, next)); err != nil {
		return err
	}
	// RawMessage can be valid alone but exceed JSON nesting limits after the
	// state/job envelopes are added. Never commit a snapshot we cannot reopen.
	if !json.Valid(body) {
		return ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	previous, err := json.Marshal(b.state)
	if err != nil {
		return err
	}
	if bytes.Equal(body, previous) {
		return nil
	}
	if b.path != "" {
		dir := filepath.Dir(b.path)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if _, err = packagefs.LstatNoSymlink(dir); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".pluginjobs-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(body)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = b.unchanged(ctx); err != nil {
			return err
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), b.path); err != nil {
			return err
		}
		b.digest = sha256.Sum256(body)
		b.exists = true
	}
	b.state = cloneState(next)
	return nil
}
