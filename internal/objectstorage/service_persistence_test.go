package objectstorage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

type receiptFailureBackend struct {
	*memoryBackend
	afterPut func() error
}

func (b *receiptFailureBackend) Put(ctx context.Context, key string, reader io.ReadSeeker, info ObjectInfo) error {
	if err := b.memoryBackend.Put(ctx, key, reader, info); err != nil {
		return err
	}
	if b.afterPut != nil {
		return b.afterPut()
	}
	return nil
}

// Replace only a receipt inside the test's temporary plugin directory. A
// directory cannot be atomically replaced with a regular file on any platform.
func obstructStorageReceipt(path string) (func() error, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, err
	}
	return func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, original, 0600)
	}, nil
}

func storageReceiptPath(t *testing.T, s *Service, manager *plugin.Manager) string {
	t.Helper()
	status, err := s.Status()
	if err != nil || len(status.Records) != 1 {
		t.Fatalf("expected one queued receipt: %+v, %v", status.Counts, err)
	}
	var result string
	if err := manager.WithData(plugin.ObjectStorage, true, 0, func(dir string, _ uint64) error {
		result = filepath.Join(dir, "records", status.Records[0].ID+".json")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func clearStorageWake(s *Service) {
	for {
		select {
		case <-s.wake:
		default:
			return
		}
	}
}

func TestStorageReceiptWriteFailuresStopCycleAndRecover(t *testing.T) {
	for _, phase := range []string{"claim", "completion"} {
		t.Run(phase, func(t *testing.T) {
			s, manager, library, memory := storageFixture(t)
			settings, err := s.Status()
			if err != nil {
				t.Fatal(err)
			}
			config := settings.Settings.Settings
			config.Profiles[0].PublicURL = "https://cdn.example.test"
			if _, err := s.Save(config, nil); err != nil {
				t.Fatal(err)
			}
			storageUpload(t, library)
			if _, err := s.Synchronize("primary"); err != nil {
				t.Fatal(err)
			}
			path := storageReceiptPath(t, s, manager)
			var restore func() error
			backend := &receiptFailureBackend{memoryBackend: memory}
			s.factory = func(Profile) (Backend, error) { return backend, nil }
			if phase == "claim" {
				var err error
				restore, err = obstructStorageReceipt(path)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				backend.afterPut = func() error {
					var err error
					restore, err = obstructStorageReceipt(path)
					return err
				}
			}
			changes := 0
			s.OnDeliveryChange = func(string) { changes++ }
			clearStorageWake(s)
			err = s.cycle(t.Context())
			if err == nil {
				t.Error("receipt write failure was reported as a successful cycle")
			} else if safeError(err) != "同步记录保存失败" {
				t.Errorf("missing safe storage diagnostic: %q", safeError(err))
			}
			select {
			case <-s.wake:
				t.Error("failed receipt immediately scheduled another cycle")
			default:
			}
			wantPuts, wantStatus := 0, "pending"
			if phase == "completion" {
				wantPuts, wantStatus = 1, "syncing"
			}
			status, err := s.Status()
			if err != nil || status.Counts[wantStatus] != 1 || memory.puts != wantPuts || changes != 0 {
				t.Fatalf("failed write published uncommitted state: status=%+v, puts=%d, changes=%d, err=%v", status.Counts, memory.puts, changes, err)
			}
			if restore == nil {
				t.Fatal("storage failure was not injected")
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			backend.afterPut = nil
			if err := s.cycle(t.Context()); err != nil {
				t.Fatal("retry after storage recovery", err)
			}
			status, err = s.Status()
			if err != nil || status.Counts["synced"] != 1 || memory.puts != wantPuts+1 || len(memory.objects) != 1 || changes != 1 {
				t.Fatalf("recovery did not finish the same object: status=%+v, puts=%d, objects=%d, changes=%d, err=%v", status.Counts, memory.puts, len(memory.objects), changes, err)
			}
			reopened := New(manager, library)
			status, err = reopened.Status()
			if err != nil || status.Counts["synced"] != 1 {
				t.Fatalf("recovery was not durable: %+v, %v", status.Counts, err)
			}
		})
	}
}

func TestStorageRunReportsReceiptFailureAndResumesAfterWake(t *testing.T) {
	s, manager, library, memory := storageFixture(t)
	storageUpload(t, library)
	if _, err := s.Synchronize("primary"); err != nil {
		t.Fatal(err)
	}
	restore, err := obstructStorageReceipt(storageReceiptPath(t, s, manager))
	if err != nil {
		t.Fatal(err)
	}
	clearStorageWake(s)
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() { defer close(stopped); s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("storage worker did not stop")
		}
	})
	waitFor := func(ready func(Status) bool) Status {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			status, err := s.Status()
			if err != nil {
				t.Fatal(err)
			}
			if ready(status) {
				return status
			}
			if time.Now().After(deadline) {
				t.Fatalf("worker status did not reach expected state: %+v", status.Counts)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	status := waitFor(func(status Status) bool { return status.LastError != "" })
	if status.LastError != "同步记录保存失败" {
		t.Fatalf("unsafe or missing diagnostic: %q", status.LastError)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	s.Wake()
	waitFor(func(status Status) bool { return status.Counts["synced"] == 1 })
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if memory.puts != 1 {
		t.Fatalf("recovery uploaded %d times", memory.puts)
	}
}

func TestStorageBatchJoinsOtherWorkersBeforeReportingReceiptFailure(t *testing.T) {
	s, manager, library, memory := storageFixture(t)
	storageUpload(t, library)
	if _, err := library.Upload(t.Context(), strings.NewReader("%PDF-1.4\nSecond storage object\n%%EOF"), media.UploadInput{Filename: "second.pdf"}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Synchronize("primary"); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil || len(status.Records) != 2 {
		t.Fatal("expected two records", err)
	}
	var restore func() error
	if err := manager.WithData(plugin.ObjectStorage, true, 0, func(dir string, _ uint64) error {
		var err error
		restore, err = obstructStorageReceipt(filepath.Join(dir, "records", status.Records[0].ID+".json"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clearStorageWake(s)
	if err := s.cycle(t.Context()); err == nil {
		t.Error("batch swallowed a receipt failure")
	}
	status, err = s.Status()
	if err != nil || status.Counts["synced"] != 1 || status.Counts["pending"] != 1 || status.Running || memory.puts != 1 {
		t.Fatalf("batch left a worker unfinished: counts=%+v, running=%v, puts=%d, err=%v", status.Counts, status.Running, memory.puts, err)
	}
	select {
	case <-s.wake:
		t.Error("failed concurrent batch scheduled an immediate retry")
	default:
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || status.Counts["synced"] != 2 || memory.puts != 2 {
		t.Fatalf("recovery repeated the successful peer: counts=%+v, puts=%d, err=%v", status.Counts, memory.puts, err)
	}
}
