package extensionupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

type fakeMarket struct {
	mu   sync.Mutex
	view plugin.MarketView
	work func(context.Context, extension.Progress) error
}

func (m *fakeMarket) View() plugin.MarketView {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.view)
	var v plugin.MarketView
	_ = json.Unmarshal(b, &v)
	return v
}
func (m *fakeMarket) Refresh(ctx context.Context) error {
	if m.work != nil {
		return m.work(ctx, nil)
	}
	return nil
}
func (m *fakeMarket) InstallWithProgress(ctx context.Context, _, _ string, p extension.Progress) error {
	return m.work(ctx, p)
}
func (m *fakeMarket) Update(ctx context.Context, _, _, _, _ string, p extension.Progress) error {
	return m.work(ctx, p)
}
func (m *fakeMarket) InstallTheme(ctx context.Context, _ *theme.Manager, _, _ string, p extension.Progress) error {
	return m.work(ctx, p)
}
func (m *fakeMarket) UpdateTheme(ctx context.Context, _ *theme.Manager, _, _, _, _ string, p extension.Progress) error {
	return m.work(ctx, p)
}

func serviceFixture(t *testing.T) (*Service, *fakeMarket, Request) {
	t.Helper()
	pm := plugin.NewManager(t.TempDir())
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "plugins", "content-age", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest plugin.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join("..", "..", "examples", "plugins", "content-age", "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{"plugin.json": b, "plugin.wasm": code} {
		f, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "plugin.zip")
	if err := os.WriteFile(file, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.InstallPackage(file); err != nil {
		t.Fatal(err)
	}
	items, _ := pm.List()
	manifest = items[0].Manifest
	if err := plugin.WritePrivate(filepath.Join(itemsRoot(pm, t), manifest.Slug, ".official.json"), plugin.Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	next := manifest
	next.Version = "1.1.0"
	release := plugin.Release{Manifest: next, Path: "packages/new.zip", SHA256: strings.Repeat("a", 64), Size: 100, Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}
	market := &fakeMarket{view: plugin.MarketView{Configured: true, Schema: 2, Expires: time.Now().Add(time.Hour), Plugins: []plugin.Release{release}}}
	market.work = func(context.Context, extension.Progress) error { return nil }
	service, err := New(filepath.Join(t.TempDir(), "state.json"), market, pm, theme.NewManager(t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	state, err := pm.VersionState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return service, market, Request{Kind: "plugin", Action: "update", Slug: manifest.Slug, Version: "1.1.0", Digest: release.SHA256, ExpectedVersion: manifest.Version, Revision: state.Revision, Channel: "stable"}
}

// WithData supplies the installation directory without adding a production
// accessor solely for tests.
func itemsRoot(m *plugin.Manager, t *testing.T) string {
	t.Helper()
	var root string
	if err := m.WithData("content-age", false, 0, func(dir string, _ uint64) error { root = filepath.Dir(filepath.Dir(dir)); return nil }); err != nil {
		t.Fatal(err)
	}
	return root
}

func awaitJob(t *testing.T, s *Service, kind string) Job {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		j := s.Job(kind)
		if !j.Busy() {
			return j
		}
		select {
		case <-deadline:
			t.Fatal("job did not finish")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBackgroundUpdateProgressCancellationAndKindIsolation(t *testing.T) {
	s, market, request := serviceFixture(t)
	started := make(chan struct{})
	market.work = func(ctx context.Context, p extension.Progress) error {
		p.Report("downloading", 40, 100)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	job, err := s.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	status := s.Job("plugin")
	if !status.Busy() || status.Downloaded != 40 || status.Total != 100 {
		t.Fatal(status)
	}
	private := s.Job("theme")
	if private.Phase != "other" || private.ID != "" || private.Slug != "" || private.Target != "" {
		t.Fatal("cross-kind job details leaked", private)
	}
	if _, err := s.Start(Request{Kind: "theme", Action: "check", Channel: "stable"}); !errors.Is(err, extension.ErrBusy) {
		t.Fatal(err)
	}
	if err := s.Cancel("theme", job.ID); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("cross-kind cancellation", err)
	}
	if err := s.Cancel("plugin", "stale"); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("stale cancellation", err)
	}
	if err := s.Cancel("plugin", job.ID); err != nil {
		t.Fatal(err)
	}
	final := awaitJob(t, s, "plugin")
	if final.Phase != "cancelled" {
		t.Fatal(final)
	}
	stored, err := extension.ReadJSON[Job](s.stateFile, 16<<10)
	if err != nil || stored.ID != job.ID || stored.Phase != "cancelled" {
		t.Fatal(stored, err)
	}
}

func TestCommittingJobCannotBeCancelled(t *testing.T) {
	s, market, request := serviceFixture(t)
	switching, finish := make(chan struct{}), make(chan struct{})
	market.work = func(ctx context.Context, p extension.Progress) error {
		p.Report("switching", 0, 0)
		close(switching)
		<-finish
		return nil
	}
	job, err := s.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	<-switching
	if err := s.Cancel("plugin", job.ID); err == nil {
		t.Fatal("committing job cancelled")
	}
	close(finish)
	if job := awaitJob(t, s, "plugin"); job.Phase != "succeeded" {
		t.Fatal(job)
	}
}

func TestTaskPersistenceFailureCancelsBeforeCommit(t *testing.T) {
	s, market, request := serviceFixture(t)
	started, advance := make(chan struct{}), make(chan struct{})
	committed := false
	market.work = func(ctx context.Context, p extension.Progress) error {
		close(started)
		<-advance
		p.Report("switching", 0, 0)
		if err := ctx.Err(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if _, err := s.Start(request); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := os.Remove(s.stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.stateFile, 0700); err != nil {
		t.Fatal(err)
	}
	close(advance)
	if job := awaitJob(t, s, "plugin"); job.Phase != "failed" {
		t.Fatal(job)
	}
	s.Close()
	if committed {
		t.Fatal("update committed without durable task state")
	}
}

func TestVersionSelectionHonorsChannelsCompatibilityAndRevision(t *testing.T) {
	s, market, request := serviceFixture(t)
	base := market.view.Plugins[0]
	preview := base
	preview.Manifest.Version = "1.2.0-rc.1"
	preview.SHA256 = strings.Repeat("b", 64)
	preview.Release = &extension.Metadata{Channel: "preview", PublishedAt: time.Now().UTC(), DataCompatible: true}
	future := base
	future.Manifest.Version = "2.0.0"
	future.Manifest.NewPress = ">=99.0.0"
	future.SHA256 = strings.Repeat("c", 64)
	offline := base
	offline.Manifest.Version = "1.5.0"
	offline.SHA256 = strings.Repeat("d", 64)
	offline.Release = &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC()}
	market.view.Plugins = append(market.view.Plugins, preview, future, offline)
	snapshot, err := s.Snapshot("plugin", "stable")
	if err != nil || len(snapshot.Cards) != 1 || len(snapshot.Cards[0].Releases) != 3 || snapshot.Updates != 1 {
		t.Fatal(snapshot, err)
	}
	if snapshot.Cards[0].Releases[0].Allowed || !strings.Contains(snapshot.Cards[0].Releases[0].Reason, "99.0.0") {
		t.Fatal("core constraint ignored")
	}
	previewRequest := request
	previewRequest.Version = preview.Manifest.Version
	previewRequest.Digest = preview.SHA256
	if err := s.validate(previewRequest); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("preview available in stable", err)
	}
	previewRequest.Channel = "preview"
	if err := s.validate(previewRequest); err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.Revision = "stale"
	if _, err := s.Start(stale); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	wrong := request
	wrong.Digest = strings.Repeat("f", 64)
	if _, err := s.Start(wrong); !errors.Is(err, extension.ErrConflict) {
		t.Fatal("unbound digest accepted", err)
	}
	market.view.Expires = time.Now().Add(-time.Second)
	if err := s.validate(request); err == nil {
		t.Fatal("expired release accepted")
	}
}

func TestInterruptedJobIsPersistedAsInterruptedAtStartup(t *testing.T) {
	s, market, _ := serviceFixture(t)
	job := Job{ID: "interrupted", Kind: "plugin", Action: "update", Slug: "content-age", Phase: "downloading", StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := extension.WriteJSON(s.stateFile, job); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.stateFile, market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if j := restarted.Job("plugin"); j.Phase != "interrupted" || j.Busy() {
		t.Fatal(j)
	}
}
