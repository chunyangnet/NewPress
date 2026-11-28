package aieditor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSettingsSaveCheckedPreservesStateOnDenial(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, denyAt := range []int{1, 2} {
			name := "memory/locked"
			if durable {
				name = "disk/locked"
			}
			if denyAt == 2 {
				name = strings.Replace(name, "locked", "prepared", 1)
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				path := ""
				if durable {
					path = filepath.Join(t.TempDir(), "ai-settings.json")
				}
				store, err := NewSettingsStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Save(testSettings(store.Snapshot().Revision)); err != nil {
					t.Fatal(err)
				}
				before := store.Snapshot()
				input := store.Snapshot()
				input.Providers[0].APIKey = "unauthorized-replacement-key"
				var disk []byte
				if durable {
					disk, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				denied, checks := errors.New("authority revoked"), 0
				result, err := store.SaveChecked(input, func() error {
					checks++
					if checks == denyAt {
						return denied
					}
					return nil
				})
				if !errors.Is(err, denied) || result.Revision != "" || store.Snapshot().Revision != before.Revision {
					t.Fatal("denied save changed state", checks, result.Revision, err)
				}
				connection, err := store.Resolve(Connection{ProviderID: "main"}, true)
				if err != nil || connection.APIKey != "first-test-key" {
					t.Fatal("denied save replaced credential", err)
				}
				if durable {
					after, err := os.ReadFile(path)
					if err != nil || string(after) != string(disk) {
						t.Fatal("denied save changed disk", err)
					}
				}
				// The same optimistic revision remains usable after a rejected save.
				if _, err := store.SaveChecked(input, func() error { return nil }); err != nil {
					t.Fatal("denied save consumed revision", err)
				}
				if durable {
					reopened, err := NewSettingsStore(path)
					if err != nil {
						t.Fatal(err)
					}
					connection, err = reopened.Resolve(Connection{ProviderID: "main"}, true)
					if err != nil || connection.APIKey != "unauthorized-replacement-key" {
						t.Fatal("authorized retry did not persist", err)
					}
				}
			})
		}
	}
}

func TestSettingsSaveCheckedAfterLockWait(t *testing.T) {
	store, err := NewSettingsStore("")
	if err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot()
	input := testSettings(before.Revision)
	var revoked atomic.Bool
	denied := errors.New("authority revoked while waiting")
	store.mu.Lock()
	entered, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		close(entered)
		_, err := store.SaveChecked(input, func() error {
			if revoked.Load() {
				return denied
			}
			return nil
		})
		finished <- err
	}()
	<-entered
	revoked.Store(true)
	store.mu.Unlock()
	if err := <-finished; !errors.Is(err, denied) || store.Snapshot().Revision != before.Revision {
		t.Fatal("revoked waiter committed settings", err)
	}
}
