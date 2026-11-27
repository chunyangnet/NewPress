package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

func taskControlContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (m *Manager) loadTaskControl(ctx context.Context, slug string) (TaskControl, bool, error) {
	c, found, err := m.taskControls.LoadTaskControl(ctx, slug)
	if err != nil || !found {
		return TaskControl{}, found, err
	}
	if c.Slug != slug || ValidateTaskControl(c) != nil {
		return TaskControl{}, false, ErrTaskControlCorrupt
	}
	return c, true, nil
}

// Local runtime.json remains authoritative only in directory-owned file mode.
// A different manifest may preview defaults for explicit review; its old grants
// cannot execute, and its save still compares the shared configuration version.
func (m *Manager) configurationForData(ctx context.Context, dir string, manifest Manifest) (Configuration, error) {
	if m.taskControls == nil {
		return loadConfiguration(dir, manifest)
	}
	c, found, err := m.loadTaskControl(ctx, manifest.Slug)
	if err != nil {
		return Configuration{}, err
	}
	if !found {
		return defaultConfiguration(manifest), nil
	}
	if !taskControlManifestMatches(c, manifest) {
		preview := defaultConfiguration(manifest)
		preview.Version = c.Configuration.Version
		return preview, nil
	}
	return c.Configuration, nil
}

func (m *Manager) activeTaskControl(ctx context.Context, manifest Manifest, module []byte) (TaskControl, error) {
	c, found, err := m.loadTaskControl(ctx, manifest.Slug)
	if err != nil {
		return TaskControl{}, err
	}
	if !found || !c.Enabled || c.Retired || c.Epoch.StartedAt.After(time.Now()) || !taskControlPackageMatches(c, manifest, module) {
		return TaskControl{}, ErrInactive
	}
	return c, nil
}

// A captured sandbox runs verified bytes. Recheck shared authority before each
// host call without retaining an authorization transaction across guest code.
func (m *Manager) checkCapturedTaskControl(ctx context.Context, captured TaskControl) error {
	c, found, err := m.loadTaskControl(ctx, captured.Slug)
	if err != nil {
		return err
	}
	if !found || !c.Enabled || c.Retired || c.Epoch.Token != captured.Epoch.Token ||
		c.ModuleDigest != captured.ModuleDigest || !taskControlManifestMatches(c, captured.Manifest) {
		return ErrInactive
	}
	return nil
}

func (m *Manager) configureTaskControl(dir string, manifest Manifest, config Configuration, actorID int64) error {
	ctx, cancel := taskControlContext()
	defer cancel()
	old, found, err := m.loadTaskControl(ctx, manifest.Slug)
	if err != nil {
		return err
	}
	if found && old.TransitionID != "" {
		return ErrTaskControlConflict
	}
	expected, expectedConfiguration := int64(0), int64(0)
	if found {
		expected, expectedConfiguration = old.Revision, old.Configuration.Version
	}
	if config.Version != expectedConfiguration {
		return ErrConfigurationConflict
	}
	config.Version++
	raw, err := json.Marshal(config)
	if config.Version < 1 {
		return errors.New("插件配置版本超出范围")
	}
	if err != nil || len(raw) > businessBytes {
		return errors.New("插件配置总大小不能超过 64 KiB，请缩短配置内容")
	}
	module, _, err := readModule(filepath.Join(filepath.Dir(dir), filepath.FromSlash(manifest.Entry)))
	if err != nil {
		return err
	}
	if err = verifyIntegrity(filepath.Dir(dir), manifest, module); err != nil {
		return err
	}
	epoch, err := freshTaskEpoch(actorID)
	if err != nil {
		return err
	}
	next := TaskControl{Schema: 1, Slug: manifest.Slug, Revision: expected, Manifest: manifest,
		ModuleDigest: digestBytes(module), Configuration: config, Epoch: epoch,
		Enabled: found && old.Enabled && !old.Retired && taskControlPackageMatches(old, manifest, module)}
	if next, err = NextTaskControl(next); err != nil {
		return err
	}
	// Removing a local review marker cannot grant shared execution. The shared
	// CAS below is the commit point. A lost commit response requires a fresh
	// shared read; an error alone does not prove that old authority survived.
	if err = clearReview(filepath.Dir(dir)); err != nil {
		return err
	}
	if err = m.taskControls.SaveTaskControl(ctx, expected, next); err != nil {
		if errors.Is(err, ErrTaskControlConflict) {
			return ErrConfigurationConflict
		}
		return err
	}
	m.nextGeneration(manifest.Slug)
	delete(m.execution, manifest.Slug)
	return nil
}

func (m *Manager) enableTaskControl(ctx context.Context, manifest Manifest, module []byte) error {
	old, found, err := m.loadTaskControl(ctx, manifest.Slug)
	if err != nil {
		return err
	}
	if !found || old.Retired || old.TransitionID != "" || old.Configuration.Version < 1 || !taskControlPackageMatches(old, manifest, module) {
		return errors.New("请先在设置页保存共享配置并确认权限，再启用插件")
	}
	next, err := NextTaskControl(old)
	if err != nil {
		return err
	}
	next.Epoch, err = freshTaskEpoch(0)
	if err != nil {
		return err
	}
	next.Enabled = true
	return m.taskControls.SaveTaskControl(ctx, old.Revision, next)
}

// Revoke first in shared storage, then change local package files. A failed
// local disable/update/uninstall cannot revive another instance's old work.
func (m *Manager) revokeTaskControl(ctx context.Context, slug string) error {
	if m.taskControls == nil {
		return nil
	}
	old, found, err := m.loadTaskControl(ctx, slug)
	if err != nil || !found {
		return err
	}
	if !old.Enabled && old.Epoch.ActorID == 0 {
		return nil
	}
	next, err := NextTaskControl(old)
	if err != nil {
		return err
	}
	next.Enabled = false
	next.Epoch, err = freshTaskEpoch(0)
	if err != nil {
		return err
	}
	return m.taskControls.SaveTaskControl(ctx, old.Revision, next)
}

// Guest settings.set keeps its approved grant lifetime, but values/version are
// shared and CAS-protected. It remains restricted to authenticated admin.action.
func (m *Manager) writeSharedTaskValues(ctx context.Context, manifest Manifest, module []byte, epoch string, config Configuration) error {
	old, err := m.activeTaskControl(ctx, manifest, module)
	if err != nil {
		return err
	}
	if epoch == "" || old.Epoch.Token != epoch {
		return ErrInactive
	}
	if config.Version != old.Configuration.Version+1 || config.Version < 1 ||
		!sameTaskGrants(config.Grants, old.Configuration.Grants) || validateConfiguration(manifest, config) != nil {
		return ErrConfigurationConflict
	}
	next, err := NextTaskControl(old)
	if err != nil {
		return err
	}
	next.Configuration = config
	err = m.taskControls.SaveTaskControl(ctx, old.Revision, next)
	if errors.Is(err, ErrTaskControlConflict) {
		return ErrConfigurationConflict
	}
	return err
}

func sameTaskGrants(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
