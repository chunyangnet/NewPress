package objectstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

type reviewStorageBackend struct {
	mu                  sync.Mutex
	objects             map[string][]byte
	putErrorAfterCommit bool
	deleteCalls         int
	signEntered         chan struct{}
	signResume          chan struct{}
}

func (b *reviewStorageBackend) Put(ctx context.Context, key string, r io.ReadSeeker, info ObjectInfo) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = data
	if b.putErrorAfterCommit {
		return context.DeadlineExceeded
	}
	return nil
}
func (b *reviewStorageBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	h := sha256.Sum256(data)
	return ObjectInfo{Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])}, nil
}
func (b *reviewStorageBackend) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return nil, ObjectInfo{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), ObjectInfo{Size: int64(len(data))}, nil
}
func (b *reviewStorageBackend) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleteCalls++
	delete(b.objects, key)
	return nil
}
func (b *reviewStorageBackend) SignedURL(context.Context, string, time.Duration) (string, error) {
	if b.signEntered != nil {
		close(b.signEntered)
		<-b.signResume
	}
	return "https://storage.example.test/immutable-object?signature=test", nil
}

func reviewStorageFixture(t *testing.T) (*Service, *media.Manager, *reviewStorageBackend) {
	t.Helper()
	m := plugin.NewManager(t.TempDir())
	if err := m.InstallBundled(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	b := &reviewStorageBackend{objects: map[string][]byte{}}
	s := New(m, library)
	s.factory = func(Profile) (Backend, error) { return b, nil }
	c := Defaults()
	c.ActiveProfile = "review"
	c.Profiles = []Profile{{ID: "review", Name: "Review storage", Provider: "s3-compatible", Endpoint: "https://storage.example.test", Region: "us-east-1", Bucket: "media", AccessKey: "test-key", SecretKey: "test-secret"}}
	if _, err = s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(plugin.ObjectStorage, true); err != nil {
		t.Fatal(err)
	}
	return s, library, b
}

func reviewUploadPDF(t *testing.T, library *media.Manager) media.Item {
	t.Helper()
	item, err := library.Upload(context.Background(), strings.NewReader("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n"), media.UploadInput{Filename: "review.pdf"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestStorageReviewRestoredPendingDeleteBecomesUpload(t *testing.T) {
	s, library, b := reviewStorageFixture(t)
	item := reviewUploadPDF(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	trashed, err := library.Trash(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Synchronize("review"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Cleanup("review"); err != nil || n != 1 {
		t.Fatalf("schedule cleanup: %d, %v", n, err)
	}
	if _, err = library.Restore(context.Background(), item.ID, trashed.Version); err != nil {
		t.Fatal(err)
	}
	if err = s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for _, r := range s.records {
		if r.Operation == "delete" && (r.Status == "pending" || r.Status == "failed" || r.Status == "syncing") {
			t.Errorf("restored media is permanently stuck retrying deletion: %+v", r)
		}
	}
	s.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deleteCalls != 0 {
		t.Fatal("restored live object was deleted")
	}
}

func TestStorageReviewInactiveProfilesReconcileWithoutAutomaticUploads(t *testing.T) {
	s, library, b := reviewStorageFixture(t)
	item := reviewUploadPDF(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	c := s.settings
	s.mu.Unlock()
	other := c.Profiles[0]
	other.ID, other.Name, other.Prefix = "other", "Other destination", "other"
	c.Profiles = append(append([]Profile(nil), c.Profiles...), other)
	c.ActiveProfile, c.AutoSync = "other", false
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	trashed, err := library.Trash(item.ID, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil || status.ProfileCounts["review"]["orphaned"] != 1 || len(status.ProfileCounts["other"]) != 0 {
		t.Fatalf("inactive profile not reconciled: %+v %v", status.ProfileCounts, err)
	}
	if n, err := s.Cleanup("review"); err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	if _, err = library.Restore(context.Background(), item.ID, trashed.Version); err != nil {
		t.Fatal(err)
	}
	// New media must remain unscheduled, but the restored mirrored copy must
	// leave the delete queue, including on an inactive destination.
	if _, err = library.Upload(context.Background(), strings.NewReader("%PDF-1.7\nnew unscheduled object\n%%EOF"), media.UploadInput{Filename: "new.pdf"}, 1024); err != nil {
		t.Fatal(err)
	}
	if err = s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || len(status.Records) != 1 || status.Counts["synced"] != 1 || b.deleteCalls != 0 {
		t.Fatalf("restore/autosync-off: %+v, deletes=%d, err=%v", status.Counts, b.deleteCalls, err)
	}
}

func TestStorageReviewAutoSyncStillReconcilesInactiveProfiles(t *testing.T) {
	s, library, _ := reviewStorageFixture(t)
	item := reviewUploadPDF(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	c := s.settings
	s.mu.Unlock()
	other := c.Profiles[0]
	other.ID, other.Name, other.Prefix = "other", "Other destination", "other"
	c.Profiles = append(append([]Profile(nil), c.Profiles...), other)
	c.ActiveProfile, c.AutoSync = "other", true
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil || status.ProfileCounts["review"]["orphaned"] != 1 || len(status.ProfileCounts["other"]) != 0 {
		t.Fatalf("automatic cycle skipped inactive reconciliation: %+v %v", status.ProfileCounts, err)
	}
}

func TestStorageReviewStaleWorkerCannotClaimReconciledReceipt(t *testing.T) {
	s, library, b := reviewStorageFixture(t)
	item := reviewUploadPDF(t, library)
	if _, err := s.Synchronize("review"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	c, g := s.settings, s.generation
	var stale Record
	for _, r := range s.records {
		stale = r
	}
	s.mu.Unlock()
	if _, err := library.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Synchronize("review"); err != nil {
		t.Fatal(err)
	}
	s.process(context.Background(), g, c, stale)
	status, err := s.Status()
	if err != nil || status.Counts["orphaned"] != 1 || len(b.objects) != 0 {
		t.Fatalf("stale job overwrote reconciliation: %+v %v", status.Counts, err)
	}
}

func TestStorageReviewRecordLimitStillDrainsExistingJobs(t *testing.T) {
	s, library, b := reviewStorageFixture(t)
	reviewUploadPDF(t, library)
	if _, err := s.Synchronize("review"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	var template Record
	for _, r := range s.records {
		template = r
	}
	for i := 1; i < maxRecords; i++ {
		r := template
		r.ItemID = int64(i + 10000)
		r.ID = recordID(r.ProfileID, r.ItemID, r.ObjectKey)
		r.RemoteKey = path.Join(s.namespace, strconv.FormatInt(r.ItemID, 10), r.ObjectKey)
		r.Status, r.Operation = "removed", "delete"
		s.records[r.ID] = r
	}
	s.mu.Unlock()
	if _, err := library.Upload(context.Background(), strings.NewReader("%PDF-1.7\ncapacity overflow\n%%EOF"), media.UploadInput{Filename: "overflow.pdf"}, 1024); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records[template.ID].Status != "synced" || len(s.records) != maxRecords || s.lastError != errRecordLimit.Error() {
		t.Fatalf("capacity stopped existing queue: status=%s, count=%d, error=%q", s.records[template.ID].Status, len(s.records), s.lastError)
	}
	if len(b.objects) != 1 {
		t.Fatalf("unexpected upload count: %d", len(b.objects))
	}
}

func TestStorageReviewProbeCleansUncertainPut(t *testing.T) {
	s, _, b := reviewStorageFixture(t)
	b.putErrorAfterCommit = true
	_, err := s.Test(context.Background(), "review")
	if err == nil {
		t.Fatal("test upload should report its network failure")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.objects) != 0 || b.deleteCalls != 1 {
		t.Fatalf("ambiguous completed upload left an untracked probe: objects=%d deletes=%d", len(b.objects), b.deleteCalls)
	}
}

func TestStorageReviewRedirectRechecksConfigAfterSigning(t *testing.T) {
	s, library, b := reviewStorageFixture(t)
	item := reviewUploadPDF(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	c := s.settings
	s.mu.Unlock()
	c.Mode = "redirect"
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.signEntered = make(chan struct{})
	b.signResume = make(chan struct{})
	result := make(chan bool, 1)
	go func() { _, ok := s.RedirectURL(context.Background(), item); result <- ok }()
	select {
	case <-b.signEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("signing did not start")
	}
	s.mu.Lock()
	c = s.settings
	s.mu.Unlock()
	c.Mode = "mirror"
	if _, err := s.Save(c, nil); err != nil {
		close(b.signResume)
		t.Fatal(err)
	}
	close(b.signResume)
	if <-result {
		t.Fatal("old signed redirect survived a mode/config change")
	}
}
