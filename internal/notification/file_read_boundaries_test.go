package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type notificationReadSink func([]byte) (int, error)

func (f notificationReadSink) Write(p []byte) (int, error) { return f(p) }

func TestNotificationFileReadCancellationAndFailure(t *testing.T) {
	for _, size := range []int{16, 1 << 20} {
		for _, failure := range []string{"cancel", "write-error", "short-write", "changed-length"} {
			t.Run(failure+"/"+map[bool]string{true: "small", false: "large"}[size == 16], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "state.json")
				if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, size), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				written := 0
				err := readFileTo(ctx, path, notificationReadSink(func(p []byte) (int, error) {
					written += len(p)
					switch failure {
					case "cancel":
						cancel()
					case "write-error":
						return 0, io.ErrClosedPipe
					case "short-write":
						return 0, nil
					case "changed-length":
						if err := os.Truncate(path, 0); err != nil {
							t.Fatal(err)
						}
					}
					return len(p), nil
				}))
				if err == nil || written == 0 || size > 128<<10 && written >= size {
					t.Fatalf("failed read was not stopped: bytes=%d size=%d error=%v", written, size, err)
				}
				switch failure {
				case "cancel":
					if !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation lost", err)
					}
				case "write-error":
					if !errors.Is(err, io.ErrClosedPipe) {
						t.Fatal("destination error lost", err)
					}
				case "short-write":
					if !errors.Is(err, io.ErrShortWrite) {
						t.Fatal("short write accepted", err)
					}
				}
				if err := os.Remove(path); err != nil {
					t.Fatal("failed read retained its file handle", err)
				}
			})
		}
	}
}

func TestNotificationFileReadSizeAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := bytes.Repeat([]byte("notification fixture\n"), 10000)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("stable read changed bytes", err)
	}
	if err := os.Truncate(path, maxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = readFileTo(t.Context(), path, notificationReadSink(func(p []byte) (int, error) { calls++; return len(p), nil }))
	if err == nil || calls != 0 {
		t.Fatalf("oversized file reached the reader: calls=%d error=%v", calls, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := readFileTo(t.Context(), path, io.Discard); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing file lost its identity", err)
	}
}

func TestNotificationIdleClaimStillDetectsFileTampering(t *testing.T) {
	s, path, _, _ := notificationStorageFixture(t, true)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(raw, []byte(`"user_id":2`), []byte(`"user_id":3`), 1)
	if bytes.Equal(changed, raw) || len(changed) != len(raw) {
		t.Fatal("tamper fixture did not preserve size")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(t.Context()); err == nil {
		t.Error("same-size same-time edit passed readiness")
	}
	if _, found, err := s.ClaimMail(t.Context(), time.Now()); err == nil || found {
		t.Fatal("idle fast path skipped file integrity", found, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, changed) {
		t.Fatal("tampered source was replaced", err)
	}
}

func TestNotificationWriteChecksAfterStaging(t *testing.T) {
	for _, mode := range []string{"cancel", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			before := []byte("preserve previous bytes")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("commit guard refused")
			called := false
			err := writeFile(ctx, path, []byte("candidate bytes"), func() error {
				called = true
				files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".notifications-*.tmp"))
				if err != nil || len(files) != 1 {
					t.Fatal("guard did not follow staging", files, err)
				}
				if mode == "cancel" {
					cancel()
					return nil
				}
				return failure
			})
			want := failure
			if mode == "cancel" {
				want = context.Canceled
			}
			if !called || !errors.Is(err, want) {
				t.Fatal("failed staging was committed", called, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
				t.Fatal("previous file changed", err)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".notifications-*.tmp"))
			if err != nil || len(files) != 0 {
				t.Fatal("temporary file was not cleaned", files, err)
			}
		})
	}
}

func TestNotificationClaimFastPathPreservesTransitions(t *testing.T) {
	for _, mode := range []string{"empty", "future", "pending", "sent", "failed", "uncertain", "cancelled", "live-lease", "expired-lease", "expired-and-pending"} {
		t.Run(mode, func(t *testing.T) {
			s, path, now, delivery := notificationStorageFixture(t, true)
			if mode != "empty" {
				if err := s.Project(t.Context(), "claim-boundary", []Delivery{delivery}, now); err != nil {
					t.Fatal(err)
				}
			}
			key := JobKey("claim-boundary", delivery)
			if err := s.change(t.Context(), func(d *document) error {
				if mode == "empty" {
					return nil
				}
				job := d.Jobs[key]
				switch mode {
				case "future":
					job.AvailableAt = now.Add(time.Hour)
				case "live-lease", "expired-lease", "expired-and-pending":
					lease := now.Add(-time.Second)
					if mode == "live-lease" {
						lease = now.Add(time.Minute)
					}
					job.State, job.Attempts, job.LeaseUntil = "sending", 1, &lease
				case "sent", "failed", "uncertain", "cancelled":
					job.State, job.Attempts = mode, 1
				}
				d.Jobs[key] = job
				if mode == "expired-and-pending" {
					d.Receipts["next-claim"] = now
					next := JobKey("next-claim", delivery)
					d.Jobs[next] = NewJob(next, delivery, now)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			job, found, err := s.ClaimMail(t.Context(), now)
			wantFound := mode == "pending" || mode == "expired-and-pending"
			if err != nil || found != wantFound || found && (job.State != "sending" || job.LeaseUntil == nil) {
				t.Fatalf("wrong claim: found=%v want=%v state=%s error=%v", found, wantFound, job.State, err)
			}
			reloaded, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired-lease" || mode == "expired-and-pending" {
				if j := reloaded.state.Jobs[key]; j.State != "uncertain" || j.LeaseUntil != nil {
					t.Fatal("expired lease was not durably recovered", j)
				}
			} else if !found {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("idle claim rewrote the document", err)
				}
			}
			current, _ := json.Marshal(s.state)
			durable, _ := json.Marshal(reloaded.state)
			if !bytes.Equal(current, durable) {
				t.Fatal("memory and durable state disagree")
			}
		})
	}
}

func TestNotificationConcurrentReadinessAndIdleClaims(t *testing.T) {
	s, path, now, _ := notificationStorageFixture(t, true)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 10 {
				if err := s.Ready(t.Context()); err != nil {
					t.Error(err)
				}
				if _, found, err := s.ClaimMail(t.Context(), now); err != nil || found {
					t.Error(found, err)
				}
			}
		})
	}
	p, err := s.Preferences(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		p.Reply = !p.Reply
		p, err = s.SavePreferences(t.Context(), p, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Preferences(t.Context(), 2)
	if err != nil || got != p {
		t.Fatal("concurrent readers lost a committed preference", err)
	}
}
