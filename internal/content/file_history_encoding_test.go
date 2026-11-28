package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func historyEncodingSourceBytes(t *testing.T, state *MemoryRepository) []byte {
	t.Helper()
	data, err := json.Marshal([]any{state.posts, state.revisions, state.autosaves})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func historyEncodingExpected(t *testing.T, state *MemoryRepository) []byte {
	t.Helper()
	data, err := json.Marshal(contentDocumentForEncoding(state))
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestContentHistoryEncodingBytesAndOrdering(t *testing.T) {
	random := rand.New(rand.NewSource(20261128))
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for trial := range 40 {
		state := contentFileEncodingFixture(t, 3, 128)
		state.now = func() time.Time { return stamp }
		for i := range random.Intn(70) {
			id := int64(i + 1)
			post := clonePost(state.posts[i%3])
			post.Meta = map[string]any{"exact": json.Number("9007199254740993"), "nested": []any{"<>& 中文\n\"", i}}
			rev := Revision{ID: id, PostID: post.ID, Version: id, Snapshot: post, Actor: "editor", CreatedAt: stamp}
			key := int64(random.Intn(7) + 1)
			state.revisions[key] = append([]Revision{rev}, state.revisions[key]...)
		}
		for i := range random.Intn(50) {
			key := int64(random.Intn(7) + 1)
			if state.autosaves[key] == nil {
				state.autosaves[key] = map[string]Autosave{}
			}
			// Map keys deliberately differ from stored identities: ordering must
			// use the record fields, as it did before reference-based encoding.
			state.autosaves[key][fmt.Sprintf("map-%d", i)] = Autosave{PostID: int64(i%3 + 1), OwnerKey: fmt.Sprintf("user:%04d", 100-i),
				Snapshot: clonePost(state.posts[i%3]), UpdatedAt: stamp.Add(-time.Hour), ExpiresAt: stamp.Add(time.Duration(i%3-1) * time.Hour)}
		}
		state.revisions[99], state.autosaves[99] = nil, nil
		before := historyEncodingSourceBytes(t, state)
		want := historyEncodingExpected(t, state)
		for range 3 {
			var output bytes.Buffer
			if err := encodeContentState(&output, state); err != nil || !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("trial %d changed standard bytes or ordering: %v", trial, err)
			}
		}
		if !bytes.Equal(before, historyEncodingSourceBytes(t, state)) {
			t.Fatal("encoding sorted or changed source collections")
		}
	}
}

func TestContentHistoryEncodingExpiry(t *testing.T) {
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		state := NewMemoryRepository(false)
		calls := 0
		state.now = func() time.Time { calls++; return stamp }
		state.autosaves[1] = map[string]Autosave{"owner": {PostID: 1, OwnerKey: "owner", ExpiresAt: stamp.Add(delta)}}
		var output bytes.Buffer
		if err := encodeContentState(&output, state); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal("expiry check count changed", calls)
		}
		var doc contentDocument
		if err := json.Unmarshal(output.Bytes(), &doc); err != nil || len(doc.Autosaves) != map[bool]int{false: 0, true: 1}[delta > 0] || doc.Autosaves == nil || doc.Revisions == nil {
			t.Fatal("expiry boundary or empty array encoding changed", err)
		}
		if len(state.autosaves[1]) != 1 {
			t.Fatal("encoding deleted an expired source entry")
		}
	}
}

type historyEncodingInterruptedWriter struct {
	buffer    bytes.Buffer
	remaining int
	failure   error
}

type historyEncodingClockWriter struct {
	buffer bytes.Buffer
	first  func()
}

func (w *historyEncodingClockWriter) Write(data []byte) (int, error) {
	if w.first != nil {
		w.first()
		w.first = nil
	}
	return w.buffer.Write(data)
}

func TestContentHistoryEncodingSelectsBeforeWriting(t *testing.T) {
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	state := NewMemoryRepository(false)
	state.autosaves[1] = map[string]Autosave{
		"first": {PostID: 1, OwnerKey: "first", ExpiresAt: stamp.Add(time.Second)},
		"last":  {PostID: 1, OwnerKey: "last", ExpiresAt: stamp.Add(time.Second)},
	}
	calls, writing := 0, false
	state.now = func() time.Time {
		calls++
		if writing {
			t.Error("expiry was rechecked after output started")
		}
		return stamp
	}
	writer := &historyEncodingClockWriter{first: func() {
		if calls != 2 {
			t.Error("selection was not complete before writing", calls)
		}
		writing = true
		stamp = stamp.Add(time.Hour)
	}}
	if err := encodeContentState(writer, state); err != nil {
		t.Fatal(err)
	}
	var doc contentDocument
	if err := json.Unmarshal(writer.buffer.Bytes(), &doc); err != nil || len(doc.Autosaves) != 2 {
		t.Fatal("already selected autosaves were lost during encoding", err)
	}
}

func (w *historyEncodingInterruptedWriter) Write(data []byte) (int, error) {
	n := min(len(data), w.remaining)
	_, _ = w.buffer.Write(data[:n])
	w.remaining -= n
	if n != len(data) {
		return n, w.failure
	}
	return n, nil
}

func TestContentHistoryEncodingWriterErrors(t *testing.T) {
	state := contentCandidateFixture(t)
	state.now = func() time.Time { return state.posts[0].UpdatedAt }
	want := historyEncodingExpected(t, state)
	failure := errors.New("injected history write failure")
	for _, after := range []int{0, bytes.Index(want, []byte(`"revisions":`)) + 50, bytes.Index(want, []byte(`"autosaves":`)) + 50, len(want) - 2} {
		for _, cause := range []error{failure, nil} {
			writer := &historyEncodingInterruptedWriter{remaining: after, failure: cause}
			wantErr := cause
			if wantErr == nil {
				wantErr = io.ErrShortWrite
			}
			if err := encodeContentState(writer, state); !errors.Is(err, wantErr) || !bytes.Equal(writer.buffer.Bytes(), want[:after]) {
				t.Fatal("partial history output or error changed", after, err)
			}
		}
	}
}

func TestContentHistoryEncodingFailureKeepsCommit(t *testing.T) {
	for _, target := range []string{"revision", "autosave", "expired-autosave"} {
		t.Run(target, func(t *testing.T) {
			state := contentCandidateFixture(t)
			stamp := state.posts[0].UpdatedAt
			state.now = func() time.Time { return stamp }
			path := filepath.Join(t.TempDir(), "content.json")
			if _, err := writeContentState(t.Context(), path, state); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			file.state.now = state.now
			original, digest := file.state, file.digest
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fileContentChange(t.Context(), file, func(candidate *MemoryRepository) (struct{}, error) {
				if target == "revision" {
					candidate.revisions[1][0].Snapshot.Meta["invalid"] = make(chan int)
				} else {
					a := candidate.autosaves[1]["user:7"]
					a.Snapshot.Meta["invalid"] = make(chan int)
					if target == "expired-autosave" {
						a.ExpiresAt = stamp
					}
					candidate.autosaves[1]["user:7"] = a
				}
				return struct{}{}, nil
			})
			after, readErr := os.ReadFile(path)
			if target != "expired-autosave" {
				if err == nil || readErr != nil || !bytes.Equal(before, after) || file.state != original || file.searchState.Load() != original || file.digest != digest {
					t.Fatal("invalid history published", err, readErr)
				}
			} else if err != nil || readErr != nil || file.state == original || file.digest != sha256.Sum256(after) {
				t.Fatal("expired invalid autosave must be omitted", err, readErr)
			}
			reopened, err := NewFileRepository(path, false)
			if err != nil || reopened.state.posts[0].Version != 3 || len(reopened.state.revisions[1]) != 3 {
				t.Fatal("history did not survive reopen", err)
			}
			if target == "expired-autosave" && len(reopened.state.autosaves[1]) != 0 {
				t.Fatal("expired autosave was serialized")
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".newpress-content-*.tmp"))
			if err != nil || len(leftovers) != 0 {
				t.Fatal("failed history encoding leaked a temporary file", err)
			}
		})
	}
}

func TestContentHistoryEncodingConcurrentReads(t *testing.T) {
	state := contentCandidateFixture(t)
	state.now = func() time.Time { return state.posts[0].UpdatedAt }
	want := historyEncodingExpected(t, state)
	before := historyEncodingSourceBytes(t, state)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 20 {
				var output bytes.Buffer
				if err := encodeContentState(&output, state); err != nil || !bytes.Equal(want, output.Bytes()) {
					t.Error("concurrent encoding changed bytes", err)
					return
				}
			}
		})
	}
	group.Wait()
	if !bytes.Equal(before, historyEncodingSourceBytes(t, state)) {
		t.Fatal("concurrent reads changed retained source")
	}
}
