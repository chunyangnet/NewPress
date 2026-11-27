package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/dirlock"
)

type preparationTransport func(*http.Request) (*http.Response, error)

func (f preparationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func preparationState(phase string) State {
	return State{ID: "interrupted-preparation", Phase: phase, Target: "0.2.0", Percent: 31, Downloaded: 42, Total: 100,
		StartedAt: time.Now().UTC().Add(-time.Minute), UpdatedAt: time.Now().UTC().Add(-time.Second),
		Logs: []Log{{Time: time.Now().UTC().Add(-time.Second), Level: "info", Message: "last committed progress"}}}
}

func preparationStateBytes(t *testing.T, root string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPreparationRecoveryAfterRestart(t *testing.T) {
	for _, managed := range []bool{false, true} {
		mode := "serve"
		if managed {
			mode = "run"
		}
		for _, phase := range []string{"checking", "downloading", "verifying", "staging"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				fixtureKey(t)
				previous := managerFixture(t)
				before := preparationState(phase)
				if err := previous.save(before); err != nil {
					t.Fatal(err)
				}
				previous.Close()
				reopened, err := New(previous.root, previous.current, previous.themes, managed)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				reopened.client.client = &http.Client{Transport: preparationTransport(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("simulated source unavailable")
				})}
				view, err := reopened.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				after := view.State
				if after.Phase != "failed" || after.BlocksChanges() {
					t.Errorf("abandoned preparation still blocks updates after restart: %+v", after)
				}
				if after.ID != before.ID || after.Target != before.Target || after.Percent != before.Percent || after.Downloaded != before.Downloaded || after.Total != before.Total || !after.StartedAt.Equal(before.StartedAt) {
					t.Error("recovery lost task identity or committed progress", after)
				}
				if len(after.Logs) != len(before.Logs)+1 || !reflect.DeepEqual(after.Logs[:len(before.Logs)], before.Logs) || !strings.Contains(after.Logs[len(after.Logs)-1].Message, "中断") {
					t.Error("recovery did not preserve history and report interruption", after.Logs)
				}
				stored, err := reopened.state()
				if err != nil || !reflect.DeepEqual(after, stored) {
					t.Fatal("recovered status is not durable", stored, err)
				}
				if err := reopened.Configure(testSource(), false); err != nil {
					t.Errorf("recovered task prevents saving the update source: %v", err)
				}
				if err := reopened.Check(); err != nil {
					t.Errorf("recovered task prevents a new explicit check: %v", err)
				}
			})
		}
	}
}

func TestPreparationRecoveryPreservesLiveOwner(t *testing.T) {
	m := managerFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	if err := m.launch("checking", "", nil, func(ctx context.Context, state *State) error {
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			state.step("current", 100, "check completed")
			return m.save(*state)
		}
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	before := preparationStateBytes(t, m.root)
	other, err := New(m.root, m.current, m.themes, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if !bytes.Equal(preparationStateBytes(t, m.root), before) {
		t.Error("opening another manager changed an actively owned task")
	}
	if err := other.Check(); !errors.Is(err, ErrBusy) {
		t.Error("active operation lost exclusion", err)
	}
	close(release)
	m.wg.Wait()
	state, err := other.state()
	if err != nil || state.Phase != "current" {
		t.Fatal("original task did not finish normally", state, err)
	}
}

func TestPreparationRecoveryPreservesCutoverAndResults(t *testing.T) {
	for _, phase := range []string{"idle", "available", "current", "failed", "cancelled", "queued", "stopping", "backup", "starting", "observing", "rollback", "rolled_back", "completed"} {
		t.Run(phase, func(t *testing.T) {
			m := managerFixture(t)
			state := preparationState(phase)
			if phase == "completed" {
				state.ProtectedUntil = time.Now().Add(time.Minute).UTC()
			}
			if err := m.save(state); err != nil {
				t.Fatal(err)
			}
			before := preparationStateBytes(t, m.root)
			other, err := New(m.root, m.current, m.themes, false)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if !bytes.Equal(before, preparationStateBytes(t, m.root)) {
				t.Fatal("startup rewrote a supervisor phase or completed result")
			}
		})
	}
	for _, name := range []string{"request.json", "transaction.json"} {
		t.Run(name, func(t *testing.T) {
			m := managerFixture(t)
			if err := m.save(preparationState("staging")); err != nil {
				t.Fatal(err)
			}
			// Even an unreadable cutover receipt must be left for the supervisor.
			if err := os.WriteFile(filepath.Join(m.root, name), []byte("incomplete cutover receipt"), 0600); err != nil {
				t.Fatal(err)
			}
			before := preparationStateBytes(t, m.root)
			other, err := New(m.root, m.current, m.themes, false)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			if !bytes.Equal(before, preparationStateBytes(t, m.root)) {
				t.Fatal("startup rewrote a task with a cutover receipt")
			}
		})
	}
}

func TestPreparationRecoveryAfterResultSaveFailure(t *testing.T) {
	m := managerFixture(t)
	statePath := filepath.Join(m.root, "state.json")
	if err := m.launch("checking", "", nil, func(context.Context, *State) error {
		if err := os.Rename(statePath, statePath+".saved"); err != nil {
			return err
		}
		if err := os.Mkdir(statePath, 0700); err != nil {
			return err
		}
		return errors.New("check failed while the result file is unavailable")
	}); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	m.Close()
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(statePath+".saved", statePath); err != nil {
		t.Fatal(err)
	}
	other, err := New(m.root, m.current, m.themes, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	view, err := other.Snapshot()
	if err != nil || view.State.Phase != "failed" || view.State.BlocksChanges() {
		t.Error("failed final save left update operations permanently busy after restart", view.State, err)
	}
	lock, err := dirlock.Acquire(filepath.Join(m.root, "operation"))
	if err != nil {
		t.Fatal("startup recovery retained the operation lock", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
