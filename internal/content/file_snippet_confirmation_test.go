package content

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetConfirmationFixture(t *testing.T) (*FileRepository, SnippetUpdate) {
	t.Helper()
	r, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	a := snippetManager()
	s, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{Title: "first", ContentSource: "<p>one</p>", Visibility: SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), PostInput{Title: "article", Slug: "article", AuthorID: 7, Status: StatusPublish,
		ContentSource: "<p>original</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion}), Actor: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{Title: "second", ContentSource: "<p>two</p>", Visibility: SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	u := SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	return r, u
}

func snippetConfirmationUnchanged(t *testing.T, r *FileRepository) func() {
	t.Helper()
	before, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.path)
	if err != nil {
		t.Fatal(err)
	}
	state, digest, published := r.state, r.digest, r.searchState.Load()
	return func() {
		t.Helper()
		after, err := os.ReadFile(r.path)
		if err != nil {
			t.Fatal(err)
		}
		current, err := os.Stat(r.path)
		if err != nil || !bytes.Equal(before, after) || !os.SameFile(info, current) || !info.ModTime().Equal(current.ModTime()) ||
			r.state != state || r.digest != digest || r.searchState.Load() != published {
			t.Fatal("confirmation replaced or changed the committed file/state", err)
		}
		files, err := filepath.Glob(filepath.Join(filepath.Dir(r.path), ".newpress-content-*.tmp"))
		if err != nil || len(files) != 0 {
			t.Fatal("confirmation left temporary files", files, err)
		}
	}
}

func TestSnippetFileConfirmationKeepsFile(t *testing.T) {
	for _, operation := range []string{"withdraw", "receipt"} {
		t.Run(operation, func(t *testing.T) {
			r, u := snippetConfirmationFixture(t)
			a := snippetManager()
			var receipt SnippetReceipt
			if operation == "receipt" {
				result, err := r.ApplySnippetUpdate(t.Context(), u, a)
				if err != nil || result.Replayed {
					t.Fatal(result, err)
				}
				receipt = result.Receipt
			}
			s, err := r.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, a)
			if err != nil {
				t.Fatal(err)
			}
			// Reopening proves the confirmation uses durable facts. An unchanged
			// file's identity and original formatting must remain intact too.
			raw, err := os.ReadFile(r.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(r.path, append(raw, '\n', ' ', '\t'), 0600); err != nil {
				t.Fatal(err)
			}
			r, err = NewFileRepository(r.path, false)
			if err != nil {
				t.Fatal(err)
			}
			unchanged := snippetConfirmationUnchanged(t, r)
			for range 2 {
				if operation == "withdraw" {
					got, err := r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a)
					if err != nil || !reflect.DeepEqual(got, s) {
						t.Fatal("withdrawal confirmation changed", got, err)
					}
				} else {
					got, err := r.ApplySnippetUpdate(t.Context(), u, a)
					if err != nil || !got.Replayed || !reflect.DeepEqual(got.Receipt, receipt) {
						t.Fatal("receipt confirmation changed", got, err)
					}
					got.Receipt.Items[0].Version = 999 // result must be detached
				}
				unchanged()
			}
			if err := os.Mkdir(r.path+".bak", 0700); err != nil {
				t.Fatal(err)
			}
			// A blocked replacement must not prevent a read-only confirmation.
			if operation == "withdraw" {
				_, err = r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a)
			} else {
				_, err = r.ApplySnippetUpdate(t.Context(), u, a)
			}
			if err != nil {
				t.Fatal("confirmation attempted a replacement", err)
			}
			unchanged()
		})
	}
}

func TestSnippetFileConfirmationRejects(t *testing.T) {
	for _, operation := range []string{"withdraw", "receipt"} {
		t.Run(operation, func(t *testing.T) {
			for _, failure := range []string{"permission", "invalid", "mismatch", "cancelled", "changed-file", "missing-file", "directory", "foreign-identity"} {
				t.Run(failure, func(t *testing.T) {
					r, u := snippetConfirmationFixture(t)
					a := snippetManager()
					if _, err := r.ApplySnippetUpdate(t.Context(), u, a); err != nil {
						t.Fatal(err)
					}
					s, err := r.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, a)
					if err != nil {
						t.Fatal(err)
					}
					state, digest := r.state, r.digest
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					id, version := s.ID, s.Version
					var want error
					switch failure {
					case "permission":
						a.Manage, a.WriteOwn = false, false
						want = ErrSnippetPermission
					case "invalid":
						id, u.RequestKey = 999, "bad"
						if operation == "withdraw" {
							want = ErrNotFound
						}
					case "mismatch":
						version--
						u.Digest = strings.Repeat("f", 64)
						want = ErrSnippetReplay
						if operation == "withdraw" {
							want = ErrVersionConflict
						}
					case "cancelled":
						cancel()
						want = context.Canceled
					case "changed-file":
						raw, err := os.ReadFile(r.path)
						if err != nil {
							t.Fatal(err)
						}
						// Same-length external edit; metadata alone cannot prove identity.
						raw = bytes.Replace(raw, []byte("article"), []byte("changed"), 1)
						if err := os.WriteFile(r.path, raw, 0600); err != nil {
							t.Fatal(err)
						}
					case "missing-file", "directory":
						if err := os.Remove(r.path); err != nil {
							t.Fatal(err)
						}
						if failure == "directory" {
							if err := os.Mkdir(r.path, 0700); err != nil {
								t.Fatal(err)
							}
						}
					case "foreign-identity":
						if operation == "withdraw" {
							// Invalid identity must not be enough to confirm withdrawal.
							a.Identity = ""
							want = ErrSnippetPermission
						} else {
							a.Identity = "other:editor"
							want = ErrVersionConflict
						}
					}
					if operation == "withdraw" {
						_, err = r.WithdrawSnippet(ctx, id, Mutation{ExpectedVersion: version}, a)
					} else {
						_, err = r.ApplySnippetUpdate(ctx, u, a)
					}
					if err == nil || want != nil && !errors.Is(err, want) || r.state != state || r.digest != digest || r.searchState.Load() != state {
						t.Fatal("invalid confirmation accepted or state changed", err, want)
					}
				})
			}
		})
	}
}

func TestSnippetFileConfirmationChangesPersist(t *testing.T) {
	for _, operation := range []string{"withdraw", "update"} {
		t.Run(operation, func(t *testing.T) {
			r, u := snippetConfirmationFixture(t)
			before, err := os.ReadFile(r.path)
			if err != nil {
				t.Fatal(err)
			}
			state, digest := r.state, r.digest
			if err := os.Mkdir(r.path+".bak", 0700); err != nil {
				t.Fatal(err)
			}
			change := func() error {
				if operation == "withdraw" {
					_, err := r.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, snippetManager())
					return err
				}
				_, err := r.ApplySnippetUpdate(t.Context(), u, snippetManager())
				return err
			}
			if err := change(); err == nil {
				t.Fatal("new work bypassed failed persistence")
			}
			after, err := os.ReadFile(r.path)
			if err != nil || !bytes.Equal(before, after) || r.state != state || r.digest != digest || r.searchState.Load() != state {
				t.Fatal("failed work published", err)
			}
			if err := os.Remove(r.path + ".bak"); err != nil {
				t.Fatal(err)
			}
			if err := change(); err != nil {
				t.Fatal(err)
			}
			if r.state == state || r.digest == digest || r.searchState.Load() != r.state {
				t.Fatal("new work did not publish after persistence")
			}
			reopened, err := NewFileRepository(r.path, false)
			if err != nil {
				t.Fatal(err)
			}
			left, _ := encodeContentDocument(r.state)
			right, _ := encodeContentDocument(reopened.state)
			if !bytes.Equal(left, right) {
				t.Fatal("new work did not survive reopen")
			}
		})
	}
}

func TestSnippetFileConfirmationConcurrent(t *testing.T) {
	r, u := snippetConfirmationFixture(t)
	a := snippetManager()
	var group sync.WaitGroup
	results, failures := make(chan SnippetResult, 12), make(chan error, 12)
	for range 12 {
		group.Go(func() { result, err := r.ApplySnippetUpdate(t.Context(), u, a); results <- result; failures <- err })
	}
	group.Wait()
	close(results)
	close(failures)
	fresh := 0
	for result := range results {
		if !result.Replayed {
			fresh++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fresh != 1 || r.state.posts[0].Version != u.Items[0].Version+1 || len(r.state.snippetReceipts) != 1 {
		t.Fatal("concurrent confirmation repeated work", fresh)
	}
	// A real withdrawal and old-result confirmation serialize under the same
	// file lock. The old receipt remains confirmable in either order.
	for range 8 {
		group.Go(func() {
			result, err := r.ApplySnippetUpdate(t.Context(), u, a)
			if err != nil || !result.Replayed {
				t.Error(result, err)
			}
		})
	}
	group.Go(func() {
		if _, err := r.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, a); err != nil {
			t.Error(err)
		}
	})
	group.Wait()
	reopened, err := NewFileRepository(r.path, false)
	if err != nil || reopened.state.snippets[0].Visibility != SnippetWithdrawn || len(reopened.state.snippetReceipts) != 1 {
		t.Fatal("concurrent result did not persist", err)
	}
	if result, err := reopened.ApplySnippetUpdate(t.Context(), u, a); err != nil || !result.Replayed {
		t.Fatal("reopened receipt confirmation failed", result, err)
	}
}

func TestSnippetFileConfirmationCancelled(t *testing.T) {
	for _, operation := range []string{"withdraw", "receipt"} {
		t.Run(operation, func(t *testing.T) {
			r, u := snippetConfirmationFixture(t)
			a := snippetManager()
			if _, err := r.ApplySnippetUpdate(t.Context(), u, a); err != nil {
				t.Fatal(err)
			}
			s, err := r.WithdrawSnippet(t.Context(), 1, Mutation{ExpectedVersion: 2}, a)
			if err != nil {
				t.Fatal(err)
			}
			unchanged := snippetConfirmationUnchanged(t, r)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r.mu.Lock()
			done := make(chan error, 1)
			go func() {
				if operation == "withdraw" {
					_, err := r.WithdrawSnippet(ctx, s.ID, Mutation{ExpectedVersion: s.Version}, a)
					done <- err
				} else {
					_, err := r.ApplySnippetUpdate(ctx, u, a)
					done <- err
				}
			}()
			cancel()
			r.mu.Unlock()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled confirmation did not return")
			}
			unchanged()
		})
	}
}

func TestSnippetFileConfirmationRejectsMaximumVersion(t *testing.T) {
	r, _ := snippetConfirmationFixture(t)
	s := &r.state.snippets[0]
	s.Version, s.Visibility, s.SharedSince = math.MaxInt64, SnippetWithdrawn, 0
	// Fault injection only; an actual file loader rejects invalid metadata.
	if _, err := r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, snippetManager()); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("maximum version bypassed withdrawal checks", err)
	}
}

func TestSnippetFileConfirmationKeepsRequestIdentity(t *testing.T) {
	r, u := snippetConfirmationFixture(t)
	a := snippetManager()
	p, err := r.Create(t.Context(), PostInput{Title: "other", Slug: "other", AuthorID: 8, Status: StatusPublish,
		ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1}), Actor: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	u.Items = append(u.Items, SnippetSelection{ID: p.ID, Version: p.Version})
	plan, err := r.PreviewSnippetUpdate(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest = plan.Digest
	result, err := r.ApplySnippetUpdate(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Items[0], u.Items[1] = u.Items[1], u.Items[0]
	original := append([]SnippetSelection(nil), u.Items...)
	unchanged := snippetConfirmationUnchanged(t, r)
	replayed, err := r.ApplySnippetUpdate(t.Context(), u, a)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Receipt, result.Receipt) || !reflect.DeepEqual(u.Items, original) {
		t.Fatal("normalization changed request identity or caller slice", err)
	}
	unchanged()
	// Same current identity may confirm past work after losing management but
	// retaining write permission; it need not own every previously updated post.
	a.Manage, a.WriteOwn, a.UserID = false, true, 7
	replayed, err = r.ApplySnippetUpdate(t.Context(), u, a)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Receipt, result.Receipt) {
		t.Fatal("current write permission could not confirm historical work", err)
	}
	unchanged()
}
