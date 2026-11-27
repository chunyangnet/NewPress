package objectstorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

type memoryBackend struct {
	mu       sync.Mutex
	objects  map[string][]byte
	metadata map[string]ObjectInfo
	puts     int
	deletes  int
	fail     bool
	corrupt  bool
	started  chan struct{}
	release  chan struct{}
}

func (b *memoryBackend) Put(ctx context.Context, k string, r io.ReadSeeker, i ObjectInfo) error {
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.release:
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	if b.fail {
		return &OperationError{"存储暂时不可用"}
	}
	v, e := io.ReadAll(r)
	if e != nil {
		return e
	}
	if b.corrupt {
		v = append(v, 0)
	}
	b.objects[k] = v
	b.metadata[k] = i
	return nil
}
func (b *memoryBackend) Head(_ context.Context, k string) (ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.metadata[k]
	if !ok {
		return i, ErrNotFound
	}
	return i, nil
}
func (b *memoryBackend) Open(_ context.Context, k string) (io.ReadCloser, ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.objects[k]
	if !ok {
		return nil, ObjectInfo{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), v...))), b.metadata[k], nil
}
func (b *memoryBackend) Delete(_ context.Context, k string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, k)
	delete(b.metadata, k)
	b.deletes++
	return nil
}
func (b *memoryBackend) SignedURL(_ context.Context, k string, _ time.Duration) (string, error) {
	return "https://cdn.example/" + k + "?temporary=1", nil
}

func storageFixture(t *testing.T) (*Service, *plugin.Manager, *media.Manager, *memoryBackend) {
	t.Helper()
	manager := plugin.NewManager(t.TempDir())
	if err := manager.InstallBundled(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(plugin.ObjectStorage, true); err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	b := &memoryBackend{objects: map[string][]byte{}, metadata: map[string]ObjectInfo{}}
	s := New(manager, library)
	s.factory = func(Profile) (Backend, error) { return b, nil }
	c := Defaults()
	c.Profiles = []Profile{{ID: "primary", Name: "测试存储", Provider: "s3-compatible", Endpoint: "https://storage.example", Region: "us-east-1", Bucket: "media", Prefix: "assets", AccessKey: "test-ak", SecretKey: "secret-must-stay-private", SessionToken: "session-must-stay-private", TimeoutSeconds: 10, PathStyle: true}}
	c.ActiveProfile = "primary"
	if _, err = s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	return s, manager, library, b
}
func storageUpload(t *testing.T, m *media.Manager) media.Item {
	t.Helper()
	i, e := m.Upload(context.Background(), strings.NewReader("%PDF-1.4\nObject storage regression fixture\n%%EOF"), media.UploadInput{Filename: "中文 文件.pdf", Title: "存储测试"}, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	return i
}

func TestStorageSyncPersistsAndKeepsLocal(t *testing.T) {
	s, manager, library, b := storageFixture(t)
	item := storageUpload(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil || status.Counts["synced"] != 1 || status.SyncedBytes != item.SizeBytes {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if b.puts != 1 {
		t.Fatal(b.puts)
	}
	if err = s.cycle(context.Background()); err != nil || b.puts != 1 {
		t.Fatalf("duplicate upload %d %v", b.puts, err)
	}
	_, r, e := library.Open(item.ID)
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	restarted := New(manager, library)
	restarted.factory = func(Profile) (Backend, error) { return b, nil }
	v, e := restarted.Status()
	if e != nil || v.Counts["synced"] != 1 {
		t.Fatalf("recovery %+v %v", v, e)
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), "secret-must-stay-private") || strings.Contains(string(data), "session-must-stay-private") {
		t.Fatal("secret in status")
	}
}

func TestStorageProfilesSecretsAndConflicts(t *testing.T) {
	s, _, _, _ := storageFixture(t)
	v, _ := s.Status()
	c := v.Settings.Settings
	if c.Profiles[0].SecretKey != "" || !v.Settings.ProfileViews[0].SecretSaved {
		t.Fatal("credential projection")
	}
	newView, e := s.Save(c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(c, nil); !errors.Is(e, ErrConflict) {
		t.Fatal("missing version conflict", e)
	}
	c = newView.Settings
	c.Profiles[0].Endpoint = "https://other.example"
	if _, e = s.Save(c, nil); e == nil {
		t.Fatal("secret retained across endpoint change")
	}
	c = newView.Settings
	c.Profiles = append([]Profile(nil), newView.Profiles...)
	c.Profiles[0].AccessKey = "different"
	if _, e = s.Save(c, nil); e == nil {
		t.Fatal("secret retained across identity change")
	}
	if _, e = s.Synchronize("primary", 1); !errors.Is(e, ErrConflict) {
		t.Fatal("stale schedule", e)
	}
}

func TestStorageFailureRetryChecksumAndPause(t *testing.T) {
	s, _, library, b := storageFixture(t)
	storageUpload(t, library)
	b.fail = true
	v, _ := s.Status()
	c := v.Settings.Settings
	c.MaxAttempts = 1
	if _, e := s.Save(c, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Status()
	if v.Counts["failed"] != 1 {
		t.Fatal(v.Counts)
	}
	b.fail = false
	b.corrupt = true
	if n, e := s.Retry("primary"); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e := s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Status()
	if v.Counts["failed"] != 1 {
		t.Fatal("corruption marked synced", v.Counts)
	}
	b.corrupt = false
	c = v.Settings.Settings
	c.Paused = true
	if _, e := s.Save(c, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Retry("primary"); e != nil {
		t.Fatal(e)
	}
	puts := b.puts
	if e := s.cycle(context.Background()); e != nil || b.puts != puts {
		t.Fatal("paused worker uploaded", e)
	}
	v, _ = s.Status()
	c = v.Settings.Settings
	c.Paused = false
	if _, e := s.Save(c, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Status()
	if v.Counts["synced"] != 1 {
		t.Fatal(v.Counts)
	}
}

func TestStorageProbeCleansOnlyItsOwnObject(t *testing.T) {
	s, _, _, b := storageFixture(t)
	b.objects["unrelated"] = []byte("keep")
	v, e := s.Test(context.Background(), "primary")
	if e != nil || !v.OK || !v.CleanupOK || len(v.Stages) != 3 {
		t.Fatalf("%+v %v", v, e)
	}
	if len(b.objects) != 1 || string(b.objects["unrelated"]) != "keep" {
		t.Fatal("test affected unrelated object")
	}
}

func TestStorageCleanupAndRestore(t *testing.T) {
	s, _, library, b := storageFixture(t)
	i := storageUpload(t, library)
	if e := s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	trashed, e := library.Trash(i.ID, i.Version)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Status()
	if v.Counts["orphaned"] != 1 || b.deletes != 0 {
		t.Fatal("unexpected automatic delete", v.Counts, b.deletes)
	}
	if n, e := s.Cleanup("primary"); n != 1 || e != nil {
		t.Fatal(n, e)
	}
	if _, e = library.Restore(context.Background(), i.ID, trashed.Version); e != nil {
		t.Fatal(e)
	}
	if e = s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	if b.deletes != 0 {
		t.Fatal("cleanup deleted restored media")
	}
}

func TestStorageDisableCancelsAndFallsBack(t *testing.T) {
	s, manager, library, b := storageFixture(t)
	i := storageUpload(t, library)
	v, _ := s.Status()
	c := v.Settings.Settings
	c.Mode = "redirect"
	if _, e := s.Save(c, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.cycle(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.RedirectURL(context.Background(), i); !ok {
		t.Fatal("no synced URL")
	}
	if e := manager.SetEnabled(plugin.ObjectStorage, false); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.RedirectURL(context.Background(), i); ok {
		t.Fatal("disabled plugin still redirects")
	}
	if e := manager.SetEnabled(plugin.ObjectStorage, true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Synchronize("primary"); e != nil {
		t.Fatal(e)
	}
	// Force verification with a blocking upload by adding a different object.
	_, e := library.Upload(context.Background(), strings.NewReader("%PDF-1.5\nanother document\n%%EOF"), media.UploadInput{Filename: "other.pdf"}, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	b.started = make(chan struct{}, 1)
	b.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.cycle(context.Background()) }()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not started")
	}
	if e := manager.Uninstall(plugin.ObjectStorage); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall didn't cancel upload")
	}
	if _, e := s.Status(); !errors.Is(e, plugin.ErrInactive) {
		t.Fatal("uninstall recreated data", e)
	}
}
