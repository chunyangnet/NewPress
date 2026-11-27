package theme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryThemeSettingsRecord struct {
	payload []byte
	version int64
}

type memoryThemeSettingsRepository struct {
	mu      sync.Mutex
	records map[string]memoryThemeSettingsRecord
	loadErr error
	saveErr error
}

func (r *memoryThemeSettingsRepository) Load(_ context.Context, slug string) ([]byte, int64, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return nil, 0, false, r.loadErr
	}
	record, ok := r.records[slug]
	return bytes.Clone(record.payload), record.version, ok, nil
}

func (r *memoryThemeSettingsRepository) Create(_ context.Context, slug string, payload []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return false, r.saveErr
	}
	if _, exists := r.records[slug]; exists {
		return false, nil
	}
	if r.records == nil {
		r.records = make(map[string]memoryThemeSettingsRecord)
	}
	r.records[slug] = memoryThemeSettingsRecord{bytes.Clone(payload), 1}
	return true, nil
}

func (r *memoryThemeSettingsRepository) CompareAndSwap(_ context.Context, slug string, expected int64, payload []byte) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return false, r.saveErr
	}
	current, exists := r.records[slug]
	if !exists || expected != current.version {
		return false, nil
	}
	r.records[slug] = memoryThemeSettingsRecord{bytes.Clone(payload), expected + 1}
	return true, nil
}

func persistentThemeFixture(t *testing.T) (*Manager, *memoryThemeSettingsRepository, Manifest) {
	t.Helper()
	local, manifest := themeOTAFixture(t)
	repository := &memoryThemeSettingsRepository{}
	manager := NewManager(local.root, manifest.Slug, WithSettingsDir(local.settingsDir), WithSettingsRepository(repository))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	return manager, repository, manifest
}

func TestThemeRepositoryImportsLocalOnceAndSurvivesSettingsDirectoryLoss(t *testing.T) {
	m, repository, manifest := persistentThemeFixture(t)
	if m.SettingsSnapshot().Values["headline"] != "自定义外观" {
		t.Fatal("local settings were not imported")
	}
	// A corrupt leftover file must not override an already committed document.
	if err := os.WriteFile(settingsFilePath(m.settingsDir, manifest.Slug), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded := NewManager(m.root, manifest.Slug, WithSettingsDir(m.settingsDir), WithSettingsRepository(repository))
	if err := reloaded.Load(); err != nil {
		t.Fatal("existing repository fell back to a local file", err)
	}
	saved, fields, err := reloaded.SaveSettings(map[string]any{"headline": "持久外观"})
	if err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	var exported bytes.Buffer
	if err := reloaded.ExportSettings(&exported); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.ResetSettings(); err != nil {
		t.Fatal(err)
	}
	if reloaded.SettingsSnapshot().Values["headline"] != "default" {
		t.Fatal("reset did not use schema defaults")
	}
	if _, fields, err := reloaded.ImportSettings(&exported); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	// No local settings directory is needed once a repository is configured.
	restarted := NewManager(m.root, manifest.Slug, WithSettingsRepository(repository))
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if snapshot := restarted.SettingsSnapshot(); snapshot.Revision != saved.Revision || snapshot.Values["headline"] != "持久外观" {
		t.Fatal("restart lost committed settings", snapshot)
	}
	local, err := os.ReadFile(settingsFilePath(m.settingsDir, manifest.Slug))
	if err != nil || string(local) != "invalid" {
		t.Fatal("repository writes unexpectedly rewrote the local import source", err)
	}
}

func TestThemeRepositoryConflictRefreshAndFailedSaveKeepCommittedState(t *testing.T) {
	first, repository, manifest := persistentThemeFixture(t)
	second := NewManager(first.root, manifest.Slug, WithSettingsRepository(repository))
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	stale := second.SettingsSnapshot()
	if _, fields, err := first.SaveSettings(map[string]any{"headline": "first writer"}); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	current, fields, err := second.SaveSettingsRevision(stale.Revision, map[string]any{"headline": "stale overwrite"})
	if !errors.Is(err, ErrSettingsRevisionConflict) || len(fields) != 0 || current.Values["headline"] != "first writer" {
		t.Fatal("stale writer did not refresh without overwriting", current, fields, err)
	}
	if _, fields, err := second.SaveSettingsRevision(current.Revision, map[string]any{"headline": "retry"}); err != nil || len(fields) != 0 {
		t.Fatal("fresh revision could not retry", fields, err)
	}
	before := second.SettingsSnapshot()
	wantErr := errors.New("database unavailable")
	repository.saveErr = wantErr
	if _, _, err := second.SaveSettings(map[string]any{"headline": "must not publish"}); !errors.Is(err, wantErr) {
		t.Fatal(err)
	}
	if after := second.SettingsSnapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("failed write changed the visible settings")
	}
	repository.loadErr = wantErr
	if err := second.Load(); !errors.Is(err, wantErr) {
		t.Fatal("repository outage was silently replaced with local defaults", err)
	}
	if after := second.SettingsSnapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("failed reload changed the visible settings")
	}
}

func TestThemeRepositoryCorruptDocumentFailsClosed(t *testing.T) {
	for _, payload := range []string{
		`{"schema":1,"theme":"other-theme","values":{"headline":"wrong"}}`,
		`{"schema":1,"theme":"sample-theme","values":{"headline":5}}`,
		`{"schema":1,"theme":"sample-theme","values":{},"unknown":true}`,
		`{"schema":1,"theme":"sample-theme","values":{},"values":{}}`,
		`{"schema":1,"theme":"sample-theme","values":null}`,
	} {
		t.Run(payload, func(t *testing.T) {
			m, repository, manifest := persistentThemeFixture(t)
			before := m.SettingsSnapshot()
			repository.records[manifest.Slug] = memoryThemeSettingsRecord{[]byte(payload), 1}
			if err := m.Load(); err == nil {
				t.Fatal("corrupt repository document was accepted")
			}
			if after := m.SettingsSnapshot(); !reflect.DeepEqual(before, after) {
				t.Fatal("corrupt reload changed current settings")
			}
		})
	}
}

func TestThemeRepositoryStoredVersionDetectsChangeAndRevert(t *testing.T) {
	first, repository, manifest := persistentThemeFixture(t)
	second := NewManager(first.root, manifest.Slug, WithSettingsRepository(repository))
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	stale := second.SettingsSnapshot()
	for _, headline := range []string{"temporary change", "自定义外观"} {
		if _, fields, err := first.SaveSettings(map[string]any{"headline": headline}); err != nil || len(fields) != 0 {
			t.Fatal(fields, err)
		}
	}
	if first.SettingsSnapshot().Revision != stale.Revision {
		t.Fatal("fixture did not restore the original public revision")
	}
	if _, _, err := second.SaveSettingsRevision(stale.Revision, map[string]any{"headline": "stale edit"}); !errors.Is(err, ErrSettingsRevisionConflict) {
		t.Fatal("change-and-revert escaped persistent version checks", err)
	}
}

func TestThemeRepositoryLargeExportRemainsImportable(t *testing.T) {
	repository := &memoryThemeSettingsRepository{}
	properties := make(map[string]settingProperty, 65)
	values := make(map[string]any, 65)
	for i := range 65 {
		key := fmt.Sprintf("field_%02d", i)
		properties[key] = settingProperty{Type: "string", Default: ""}
		values[key] = ""
	}
	manifest := Manifest{Slug: "large-settings", Version: "1.0.0"}
	schema := &settingSchema{Type: "object", Properties: properties}
	m := &Manager{info: manifest, settingsSchema: schema, settings: schema.defaults(), settingsRepository: repository}
	payload, err := encodePersistentSettings(settingsDocument{Schema: 1, Theme: manifest.Slug, ThemeVersion: manifest.Version, UpdatedAt: time.Now().UTC(), Values: values})
	if err != nil {
		t.Fatal(err)
	}
	remaining := maxSettingsDocumentBytes - 128 - len(payload)
	for i := range 64 {
		size := min(remaining, 4096)
		values[fmt.Sprintf("field_%02d", i)] = strings.Repeat("x", size)
		remaining -= size
	}
	if remaining != 0 {
		t.Fatal("fixture did not reach the size boundary")
	}
	saved, fields, err := m.SaveSettings(values)
	if err != nil || len(fields) != 0 {
		t.Fatal("near-limit settings were rejected", fields, err)
	}
	var exported bytes.Buffer
	if err := m.ExportSettings(&exported); err != nil {
		t.Fatal(err)
	}
	if exported.Len() > maxSettingsDocumentBytes || exported.Len() < maxSettingsDocumentBytes-256 {
		t.Fatalf("exported file missed the importable boundary: %d bytes", exported.Len())
	}
	if _, err := m.ResetSettings(); err != nil {
		t.Fatal(err)
	}
	imported, fields, err := m.ImportSettings(&exported)
	if err != nil || len(fields) != 0 || imported.Revision != saved.Revision {
		t.Fatal("export could not restore the large settings", fields, err)
	}
	before := m.SettingsSnapshot()
	tooLarge := cloneSettings(before.Values)
	tooLarge["field_64"] = strings.Repeat("x", 4096)
	if _, fields, err := m.SaveSettings(tooLarge); err == nil || len(fields) != 0 {
		t.Fatal("oversized valid values did not hit the document limit", fields, err)
	}
	oversized := strings.Repeat(" ", maxSettingsDocumentBytes+1)
	if _, _, err := m.ImportSettings(strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized import was accepted")
	}
	if !reflect.DeepEqual(before, m.SettingsSnapshot()) {
		t.Fatal("oversized save/import changed the current settings")
	}
	stored, version, found, err := repository.Load(t.Context(), manifest.Slug)
	if err != nil || !found {
		t.Fatal(err)
	}
	decoded, _, _, err := decodePersistentSettings(stored, version, manifest, schema)
	if err != nil || !reflect.DeepEqual(before.Values, decoded) {
		t.Fatal("oversized write changed persisted settings", err)
	}
}

func TestThemeRepositoryConcurrentInitializationConverges(t *testing.T) {
	local, manifest := themeOTAFixture(t)
	repository := &memoryThemeSettingsRepository{}
	const count = 6
	managers := make([]*Manager, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for i := range managers {
		directory := t.TempDir()
		if err := writeSettingsDocument(directory, settingsDocument{Schema: 1, Theme: manifest.Slug, ThemeVersion: manifest.Version, Values: map[string]any{"headline": directory}}); err != nil {
			t.Fatal(err)
		}
		managers[i] = NewManager(local.root, manifest.Slug, WithSettingsDir(directory), WithSettingsRepository(repository))
		group.Go(func() { errs <- managers[i].Load() })
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winner := managers[0].SettingsSnapshot()
	for _, manager := range managers[1:] {
		if !reflect.DeepEqual(winner, manager.SettingsSnapshot()) {
			t.Fatal("instances published different bootstrap settings")
		}
	}
}

func TestThemeRepositorySwitchUpdateAndRollbackPreserveSettings(t *testing.T) {
	m, repository, first := persistentThemeFixture(t)
	second := first
	second.Slug, second.Name = "second-theme", "Second"
	_, archive := themeOTAArchive(t, second, otaSettings, `{{define "content"}}second{{end}}`)
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	state, _ := m.VersionState(second.Slug)
	if err := m.Activate(t.Context(), second.Slug, second.Version, state.Revision, first.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Values["headline"] != "default" {
		t.Fatal("other theme settings leaked into the selected theme")
	}
	if _, fields, err := m.SaveSettings(map[string]any{"headline": "second custom"}); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	firstState, _ := m.VersionState(first.Slug)
	if err := m.Activate(t.Context(), first.Slug, first.Version, firstState.Revision, second.Slug, nil); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Values["headline"] != "自定义外观" {
		t.Fatal("switch did not restore the first theme settings")
	}
	before, version, _, _ := repository.Load(t.Context(), first.Slug)
	target, _ := m.CaptureUpdate(first.Slug, "", "")
	next := first
	next.Version = "1.1.0"
	release, archive := themeOTAArchive(t, next, otaSettings, `{{define "content"}}updated{{end}}`)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	after, updatedVersion, _, _ := repository.Load(t.Context(), first.Slug)
	if !bytes.Equal(before, after) || version != updatedVersion {
		t.Fatal("package upgrade rewrote stored settings")
	}
	if _, fields, err := m.SaveSettings(map[string]any{"headline": "after upgrade"}); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	state, _ = m.VersionState(first.Slug)
	if err := m.Rollback(t.Context(), first.Slug, next.Version, state.Revision, first.Version, nil); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Values["headline"] != "after upgrade" {
		t.Fatal("rollback lost settings saved on the newer package")
	}
	restarted := NewManager(m.root, first.Slug, WithSettingsRepository(repository))
	if err := restarted.Load(); err != nil || restarted.SettingsSnapshot().Values["headline"] != "after upgrade" {
		t.Fatal("restart after rollback lost settings", err)
	}
	if _, err := os.Stat(filepath.Join(m.settingsDir, second.Slug+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new theme settings were stored on the ephemeral filesystem", err)
	}
}
