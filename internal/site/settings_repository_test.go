package site

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memoryDocumentRepository struct {
	mu        sync.Mutex
	payload   []byte
	exists    bool
	loadErr   error
	createErr error
	saveErr   error
}

func (r *memoryDocumentRepository) Load(context.Context) ([]byte, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.payload...), r.exists, r.loadErr
}

func (r *memoryDocumentRepository) Create(_ context.Context, _ string, payload []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return false, r.createErr
	}
	if r.exists {
		return false, nil
	}
	r.payload = append([]byte(nil), payload...)
	r.exists = true
	return true, nil
}

func (r *memoryDocumentRepository) CompareAndSwap(_ context.Context, expected, _ string, payload []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return false, r.saveErr
	}
	if !r.exists {
		return false, nil
	}
	current, _, err := decodeStoredDocument(r.payload)
	if err != nil {
		return false, err
	}
	if settingsRevision(current) != expected {
		return false, nil
	}
	r.payload = append([]byte(nil), payload...)
	return true, nil
}

func TestRepositoryManagerImportsFileOnceAndSurvivesLocalFileRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	legacy, err := NewManager(path, DefaultSettings("Legacy site", "Legacy tagline"))
	if err != nil {
		t.Fatal(err)
	}
	initial := legacy.Snapshot()
	changed := initial.Settings
	changed.Identity.Title = "Persisted cloud site"
	if _, fields, err := legacy.Save(initial.Revision, changed); err != nil || len(fields) != 0 {
		t.Fatalf("save legacy document: fields=%v err=%v", fields, err)
	}

	repository := &memoryDocumentRepository{}
	cloud, err := NewManagerWithRepository(t.Context(), repository, path, DefaultSettings("Bootstrap", "Bootstrap"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cloud.Snapshot().Settings.Identity.Title; got != "Persisted cloud site" {
		t.Fatalf("migrated title = %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewManagerWithRepository(t.Context(), repository, path, DefaultSettings("New defaults", "New defaults"))
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot().Settings.Identity.Title; got != "Persisted cloud site" {
		t.Fatalf("reloaded title after local storage removal = %q", got)
	}
}

func TestRepositoryManagerRevisionConflictReloadsCommittedSettings(t *testing.T) {
	repository := &memoryDocumentRepository{}
	first, err := NewManagerWithRepository(t.Context(), repository, "", DefaultSettings("Initial", "Initial"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewManagerWithRepository(t.Context(), repository, "", DefaultSettings("Ignored", "Ignored"))
	if err != nil {
		t.Fatal(err)
	}

	firstSnapshot := first.Snapshot()
	firstSettings := firstSnapshot.Settings
	firstSettings.Identity.Title = "First update"
	if _, fields, err := first.Save(firstSnapshot.Revision, firstSettings); err != nil || len(fields) != 0 {
		t.Fatalf("first save: fields=%v err=%v", fields, err)
	}
	stale := second.Snapshot()
	staleSettings := stale.Settings
	staleSettings.Identity.Title = "Stale update"
	if _, _, err := second.Save(stale.Revision, staleSettings); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	if got := second.Snapshot().Settings.Identity.Title; got != "First update" {
		t.Fatalf("conflict did not reload committed settings: %q", got)
	}
}

func TestRepositoryManagerDoesNotPublishSettingsWhenPersistenceFails(t *testing.T) {
	repository := &memoryDocumentRepository{}
	manager, err := NewManagerWithRepository(t.Context(), repository, "", DefaultSettings("Initial", "Initial"))
	if err != nil {
		t.Fatal(err)
	}
	before := manager.Snapshot()
	wantErr := errors.New("database unavailable")
	repository.mu.Lock()
	repository.saveErr = wantErr
	repository.mu.Unlock()
	changed := before.Settings
	changed.Identity.Title = "Not committed"
	if _, _, err := manager.Save(before.Revision, changed); !errors.Is(err, wantErr) {
		t.Fatalf("save error = %v", err)
	}
	if after := manager.Snapshot(); after.Revision != before.Revision || after.Settings.Identity.Title != before.Settings.Identity.Title {
		t.Fatalf("failed persistence changed in-memory state: before=%#v after=%#v", before, after)
	}
}

func TestRepositoryManagerRejectsCorruptStoredDocument(t *testing.T) {
	manager, err := NewManager("", DefaultSettings("Initial", "Initial"))
	if err != nil {
		t.Fatal(err)
	}
	settings := manager.Snapshot().Settings
	settings.Identity.Title = "Saved"
	revision := settingsRevision(settings)
	payload, err := encodeDocument(document{Schema: documentSchema, Kind: documentKind, Revision: revision, UpdatedAt: time.Now().UTC(), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryDocumentRepository{payload: payload, exists: true}
	repository.payload = append([]byte(nil), []byte(`{"schema":1,"kind":"newpress/site-settings","revision":"0000000000000000","updated_at":"2026-01-01T00:00:00Z","settings":{}}`)...)
	if _, err := NewManagerWithRepository(t.Context(), repository, "", DefaultSettings("Fallback", "Fallback")); err == nil {
		t.Fatal("corrupt repository document was accepted")
	}
}

func TestRepositoryManagerConcurrentInitializationKeepsFirstCommittedSettings(t *testing.T) {
	repository := &memoryDocumentRepository{}
	start := make(chan struct{})
	results := make(chan *Manager, 2)
	errorsFound := make(chan error, 2)
	for _, title := range []string{"First bootstrap", "Second bootstrap"} {
		go func(title string) {
			<-start
			manager, err := NewManagerWithRepository(t.Context(), repository, "", DefaultSettings(title, title))
			if err != nil {
				errorsFound <- err
				return
			}
			results <- manager
		}(title)
	}
	close(start)
	var winner string
	for range 2 {
		select {
		case err := <-errorsFound:
			t.Fatal(err)
		case manager := <-results:
			title := manager.Snapshot().Settings.Identity.Title
			if winner == "" {
				winner = title
			} else if title != winner {
				t.Fatalf("concurrent instances loaded different defaults: %q and %q", winner, title)
			}
		}
	}
}
