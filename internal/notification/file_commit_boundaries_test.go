package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cancel just after the caller observes a live context, as another goroutine
// can do while the file is being opened or read. No timing sleeps are needed.
type notificationCancelAfterCheck struct {
	context.Context
	cancel context.CancelFunc
}

func (c *notificationCancelAfterCheck) Err() error {
	err := c.Context.Err()
	if c.cancel != nil {
		cancel := c.cancel
		c.cancel = nil
		cancel()
	}
	return err
}

func TestNotificationReadyObservesCancellationAfterEntry(t *testing.T) {
	for _, mode := range []string{"memory", "missing", "existing"} {
		t.Run(mode, func(t *testing.T) {
			path := ""
			if mode != "memory" {
				path = filepath.Join(t.TempDir(), "notifications.json")
			}
			s, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "existing" {
				if _, err := s.SavePreferences(t.Context(), Defaults(2), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := s.Ready(&notificationCancelAfterCheck{Context: ctx, cancel: cancel}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled readiness appeared healthy: %v", err)
			}
			if err := s.Ready(t.Context()); err != nil {
				t.Fatal("cancellation changed storage state", err)
			}
		})
	}
}

func TestNotificationCommitPreservesConcurrentFileChange(t *testing.T) {
	for _, mode := range []string{"rewrite", "replace", "remove", "appeared"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "notifications.json")
			s, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if mode != "appeared" {
				if _, err := s.SavePreferences(t.Context(), Defaults(2), now); err != nil {
					t.Fatal(err)
				}
			}
			before, err := json.Marshal(s.state)
			if err != nil {
				t.Fatal(err)
			}
			external := emptyDocument()
			external.Receipts["external-change"] = now
			externalBytes, err := json.Marshal(external)
			if err != nil {
				t.Fatal(err)
			}
			err = s.change(t.Context(), func(d *document) error {
				d.Receipts["candidate-must-not-commit"] = now
				if mode == "replace" || mode == "remove" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "remove" {
					if err := os.WriteFile(path, externalBytes, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			if err == nil {
				t.Error("file change during mutation was silently overwritten")
			}
			after, err := json.Marshal(s.state)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("failed commit changed the in-memory state", err)
			}
			retained, readErr := os.ReadFile(path)
			if mode == "remove" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("removed file was recreated", readErr)
				}
			} else if readErr != nil || !bytes.Equal(retained, externalBytes) {
				t.Fatal("external bytes were replaced", readErr)
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".notifications-*.tmp"))
			if err != nil || len(files) != 0 {
				t.Fatal("rejected commit retained temporary files", files, err)
			}
		})
	}
}

func TestNotificationFailedCandidateOwnsAllNestedValues(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, path, now, delivery := notificationStorageFixture(t, true)
			if err := s.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateNotices(t.Context(), 2, 1, "read", now); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateNotices(t.Context(), 2, 1, "clear", now); err != nil {
				t.Fatal(err)
			}
			job, found, err := s.ClaimMail(t.Context(), now)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("candidate rejected")
			err = s.change(ctx, func(d *document) error {
				// Compare against the former JSON-roundtrip snapshot contract.
				encoded, err := json.Marshal(d)
				if err != nil || !bytes.Equal(encoded, before) {
					t.Fatal("candidate clone changed persisted values", err)
				}
				p := d.Preferences[2]
				p.Email = false
				d.Preferences[2] = p
				d.Receipts[delivery.Event.Key] = now.Add(time.Hour)
				d.Receipts["extra"] = now
				d.NextID++
				n := &d.Notices[0]
				n.Event.Mentions[0], n.Event.Recipients[0] = "changed notice", 999
				*n.ReadAt, *n.ClearedAt = now.Add(time.Hour), now.Add(time.Hour)
				n.UserID = 999
				j := d.Jobs[job.Key]
				j.Event.Mentions[0], j.Event.Recipients[0] = "changed job", 999
				*j.LeaseUntil = now.Add(time.Hour)
				j.State = "uncertain"
				d.Jobs[job.Key] = j
				delete(d.Jobs, job.Key)
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
			if !errors.Is(err, want) {
				t.Fatal("rejected candidate committed", err)
			}
			current, err := json.Marshal(s.state)
			if err != nil || !bytes.Equal(current, before) {
				t.Fatal("candidate mutated the live document", err)
			}
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(retained, before) {
				t.Fatal("candidate mutated durable bytes", err)
			}
			if err := s.FinishMail(t.Context(), job, "sent", now); err != nil {
				t.Fatal("failed candidate invalidated the real mail lease", err)
			}
		})
	}
}
