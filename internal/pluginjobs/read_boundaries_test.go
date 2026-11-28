package pluginjobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type readBoundaryBackend struct {
	Backend
	after func()
	err   error
}

func (b readBoundaryBackend) Read(ctx context.Context) (State, error) {
	state, err := b.Backend.Read(ctx)
	if b.after != nil {
		b.after()
	}
	if b.err != nil {
		return State{}, b.err
	}
	return state, err
}

func TestTaskReadPageMatchesSortedSnapshot(t *testing.T) {
	for _, disk := range []bool{false, true} {
		q, _, _ := taskReadFixture(t, 1200, disk)
		state, err := q.Backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, fallback := range []bool{false, true} {
			queue := *q
			if fallback {
				queue.Backend = readBoundaryBackend{Backend: q.Backend}
			}
			for _, archived := range []bool{false, true} {
				for _, status := range []string{"", "running", "succeeded", "failed"} {
					for _, offset := range []int{0, 20, 995, 1200} {
						query := Query{Plugin: "fixture-0", Status: status, Limit: 20, Offset: offset, Archived: archived}
						got, err := queue.List(t.Context(), query)
						if err != nil {
							t.Fatal(err)
						}
						all := []Summary{}
						if archived {
							for _, job := range state.ArchivedJobs {
								all = append(all, job.Summary)
							}
						} else {
							for _, job := range state.Jobs {
								all = append(all, Summarize(job))
							}
						}
						matched := []Summary{}
						for _, job := range all {
							if job.Plugin == query.Plugin && (status == "" || job.Status == status) {
								matched = append(matched, job)
							}
						}
						sort.Slice(matched, func(i, j int) bool {
							if matched[i].UpdatedAt.Equal(matched[j].UpdatedAt) {
								return matched[i].ID < matched[j].ID
							}
							return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
						})
						start := min(offset, len(matched))
						want := Page{Total: len(matched), Items: matched[start:min(start+20, len(matched))]}
						if !reflect.DeepEqual(got, want) || got.Items == nil {
							t.Fatalf("page changed: disk=%v fallback=%v query=%+v", disk, fallback, query)
						}
					}
				}
			}
		}
	}
}

func TestTaskReadPageOwnsReviewSummaries(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprint("archived=", archived), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			q := testQueue(t, path)
			now := time.Now().UTC()
			source, request := cancelledReviewSource(t, q, now)
			child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if archived {
				if err := q.Change(t.Context(), child.Plugin, child.ID, child.Version, "cancel", now.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := q.Archive(t.Context(), ArchiveQuery{Plugin: child.Plugin, Tasks: true, Before: now.Add(4 * time.Second)}, now.Add(4*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			query := Query{Plugin: child.Plugin, Archived: archived}
			page, err := q.List(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			for i := range page.Items {
				if page.Items[i].ID == child.ID {
					page.Items[i].Reissue.SourceID = strings.Repeat("f", 64)
					page.Items[i].Status = "tampered"
					changed = true
				}
			}
			if !changed {
				t.Fatal("missing child summary")
			}
			in := testInput()
			in.Key = "unrelated-save"
			if _, err := q.Enqueue(t.Context(), in, now.Add(5*time.Second)); err != nil {
				t.Fatal("returned summary changed stored state", err)
			}
			for _, reader := range []*Queue{q, testQueue(t, path)} {
				page, err := reader.List(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					if item.ID == child.ID && (item.Status == "tampered" || item.Reissue.SourceID != source.ID) {
						t.Fatal("caller mutated durable review summary")
					}
				}
			}
		})
	}
}

func TestTaskReadsDetectSameSizeSameTimeFileChanges(t *testing.T) {
	q, claim, now := taskReadFixture(t, 10, true)
	path := q.Backend.(*FileBackend).path
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(body, []byte("xxxxxxxx"), []byte("yyyyyyyy"), 1)
	if bytes.Equal(body, changed) || len(body) != len(changed) {
		t.Fatal("fixture did not change bytes at the same length")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.List(t.Context(), Query{Plugin: claim.Plugin}); !errors.Is(err, ErrConflict) {
		t.Fatal("list trusted stale state after external edit", err)
	}
	if err := q.CheckClaim(t.Context(), claim, now); !errors.Is(err, ErrConflict) {
		t.Fatal("authorization trusted stale state after external edit", err)
	}
	if _, err := q.Backend.Read(t.Context()); !errors.Is(err, ErrConflict) {
		t.Fatal("snapshot trusted stale state after external edit", err)
	}
}

type taskReadSink struct{ write func([]byte) (int, error) }

func (s taskReadSink) Write(p []byte) (int, error) { return s.write(p) }

func TestTaskStateHashReadStopsOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	written := 0
	exists, err := readStateFileTo(ctx, path, taskReadSink{write: func(p []byte) (int, error) {
		written += len(p)
		cancel()
		return len(p), nil
	}})
	if exists || !errors.Is(err, context.Canceled) || written == 0 || written >= 1<<20 {
		t.Fatalf("cancelled digest continued: bytes=%d exists=%v err=%v", written, exists, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal("cancelled reader retained its file handle", err)
	}
}

func TestTaskReadFallbackPropagatesCancellationAndFailure(t *testing.T) {
	q, claim, now := taskReadFixture(t, 5, false)
	for _, operation := range []string{"list", "check"} {
		for _, mode := range []string{"cancelled", "storage-error"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				backend := readBoundaryBackend{Backend: q.Backend}
				want := error(context.Canceled)
				if mode == "cancelled" {
					backend.after = cancel
				} else {
					want = io.ErrUnexpectedEOF
					backend.err = want
				}
				reader := Queue{Backend: backend}
				var err error
				if operation == "list" {
					_, err = reader.List(ctx, Query{Plugin: claim.Plugin})
				} else {
					err = reader.CheckClaim(ctx, claim, now)
				}
				if !errors.Is(err, want) {
					t.Fatal("read failure lost", err)
				}
			})
		}
	}
}

func TestTaskReadConcurrentCancellation(t *testing.T) {
	q, claim, now := taskReadFixture(t, 30, false)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := q.List(t.Context(), Query{Plugin: claim.Plugin}); err != nil {
					t.Error(err)
				}
				if err := q.CheckClaim(t.Context(), claim, now); err != nil && !errors.Is(err, ErrAuthorization) {
					t.Error(err)
				}
			}
		})
	}
	if err := q.Change(t.Context(), claim.Plugin, claim.ID, claim.Version, "cancel", now); err != nil {
		t.Error(err)
	}
	wg.Wait()
	if err := q.CheckClaim(t.Context(), claim, now); !errors.Is(err, ErrAuthorization) {
		t.Fatal("cancelled claim authorized after concurrent reads", err)
	}
}
