package theme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

type Release struct {
	Manifest Manifest            `json:"manifest"`
	Path     string              `json:"path"`
	SHA256   string              `json:"sha256"`
	Size     int64               `json:"size"`
	Notes    string              `json:"notes"`
	Release  *extension.Metadata `json:"release,omitempty"`
	Cover    *CoverFile          `json:"cover,omitempty"`
}

type UpdateTarget struct {
	manifest   Manifest
	state      extension.VersionState
	generation uint64
}

func (m *Manager) Recover() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return extension.Recover(m.root)
}

func (m *Manager) VersionState(slug string) (extension.VersionState, error) {
	target, err := m.CaptureUpdate(slug, "", "")
	return target.state, err
}

func (m *Manager) CaptureUpdate(slug, expected, revision string) (UpdateTarget, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.captureUpdate(slug, expected, revision)
}

func (m *Manager) captureUpdate(slug, expected, revision string) (UpdateTarget, error) {
	if err := packagefs.ValidateSlug(slug); err != nil {
		return UpdateTarget{}, err
	}
	manifest, err := readManifest(filepath.Join(m.root, slug, "theme.json"))
	if err != nil || validateManifest(manifest) != nil || manifest.Slug != slug {
		return UpdateTarget{}, errors.New("已安装主题信息不可用")
	}
	state, err := extension.State(filepath.Join(m.root, slug), manifest.Version)
	if err != nil {
		return UpdateTarget{}, err
	}
	if state.Revision, err = m.dependencyRevision(manifest, state.Revision); err != nil {
		return UpdateTarget{}, err
	}
	if expected != "" && expected != manifest.Version || revision != "" && revision != state.Revision {
		return UpdateTarget{}, extension.ErrConflict
	}
	return UpdateTarget{manifest, state, m.generation[slug]}, nil
}

func (m *Manager) InstallRelease(ctx context.Context, archive string, release Release, progress extension.Progress) error {
	if err := version.Compatible(release.Manifest.NewPress); err != nil {
		return err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	progress.Report("validating", 0, 0)
	manifest, err := packagefs.ReadJSON[Manifest](archive, "theme.json")
	if err != nil || !reflect.DeepEqual(manifest, release.Manifest) {
		return errors.New("主题包与官方目录不一致")
	}
	if info, err := packagefs.LstatNoSymlink(m.root); err != nil || !info.IsDir() {
		return errors.New("主题目录不可用")
	}
	staging, err := os.MkdirTemp(m.root, ".install-official-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := installArchive(staging, manifest, archive, m.root); err != nil {
		return err
	}
	prepared := filepath.Join(staging, manifest.Slug)
	if err := extension.WriteJSON(filepath.Join(prepared, ".official.json"), release); err != nil {
		return err
	}
	target := filepath.Join(m.root, manifest.Slug)
	if _, err := packagefs.LstatNoSymlink(target); !os.IsNotExist(err) {
		return extension.ErrConflict
	}
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.Rename(prepared, target); err != nil {
		return err
	}
	m.generation[manifest.Slug]++
	return nil
}

func (m *Manager) UpdatePackage(ctx context.Context, archive string, release Release, target UpdateTarget, progress extension.Progress) error {
	next := release.Manifest
	if next.Slug != target.manifest.Slug || next.Engine != target.manifest.Engine {
		return errors.New("主题更新不能更改标识或模板引擎")
	}
	comparison, err := version.Compare(next.Version, target.manifest.Version)
	if err != nil || comparison <= 0 {
		return errors.New("在线更新只接受更高的语义版本")
	}
	if release.Release == nil || !release.Release.DataCompatible {
		return errors.New("发布者尚未声明数据兼容，不能在线覆盖已有主题")
	}
	if err := version.Compatible(next.NewPress); err != nil {
		return err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	current, err := m.captureUpdate(next.Slug, target.manifest.Version, target.state.Revision)
	if err != nil {
		return err
	}
	if current.generation != target.generation {
		return extension.ErrConflict
	}
	transaction, err := extension.Begin(m.root, next.Slug)
	if err != nil {
		return err
	}
	defer transaction.Close()
	progress.Report("validating", 0, 0)
	probe := filepath.Join(filepath.Dir(transaction.Stage), "probe")
	if err := os.Mkdir(probe, 0700); err != nil {
		return err
	}
	if err := validateManifest(next); err != nil {
		return err
	}
	if err := installArchive(probe, next, archive, m.root); err != nil {
		return err
	}
	if err := os.Remove(transaction.Stage); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(probe, next.Slug), transaction.Stage); err != nil {
		return err
	}
	if err := extension.WriteJSON(filepath.Join(transaction.Stage, ".official.json"), release); err != nil {
		return err
	}
	return m.replacePrepared(ctx, transaction, next, target, "update", release.SHA256, progress)
}

func (m *Manager) Rollback(ctx context.Context, slug, expected, revision, previous string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	target, err := m.captureUpdate(slug, expected, revision)
	if err != nil {
		return err
	}
	if previous == "" || target.state.Previous != previous {
		return extension.ErrConflict
	}
	dir := filepath.Join(m.root, slug)
	if err := extension.VerifyRollback(dir); err != nil {
		return err
	}
	transaction, err := extension.Begin(m.root, slug)
	if err != nil {
		return err
	}
	defer transaction.Close()
	progress.Report("validating", 0, 0)
	if err := extension.CopyTree(ctx, filepath.Join(dir, extension.RollbackDir), transaction.Stage); err != nil {
		return err
	}
	next, err := readManifest(filepath.Join(transaction.Stage, "theme.json"))
	if err != nil || validateManifest(next) != nil || next.Slug != slug || next.Version != previous || next.Engine != target.manifest.Engine {
		return errors.New("主题回退快照无效")
	}
	if err := version.Compatible(next.NewPress); err != nil {
		return err
	}
	return m.replacePrepared(ctx, transaction, next, target, "rollback", "", progress)
}

// The operation lock also protects SaveSettings. Existing settings are read
// only, so an unsuccessful update cannot modify the settings document.
func (m *Manager) replacePrepared(ctx context.Context, transaction *extension.Transaction, next Manifest, target UpdateTarget, action, digest string, progress extension.Progress) error {
	if next.View != target.manifest.View || parentSlug(next) != parentSlug(target.manifest) {
		return errors.New("主题更新不能更改父主题或展示契约")
	}
	overrides := map[string]string{next.Slug: transaction.Stage}
	prepared, err := prepareTheme(m.root, next.Slug, overrides)
	if err != nil {
		return err
	}
	affected, err := m.dependents(next.Slug)
	if err != nil {
		return err
	}
	activePrepared := prepared
	for _, slug := range affected {
		child, err := prepareTheme(m.root, slug, overrides)
		if err != nil {
			return fmt.Errorf("子主题 %s 不兼容: %w", slug, err)
		}
		if _, _, _, err = m.loadSettings(child.info, child.schema, false); err != nil {
			return fmt.Errorf("子主题 %s 设置不兼容: %w", slug, err)
		}
		if slug == m.active {
			activePrepared = child
		}
	}
	publish := next.Slug == m.active || activePrepared.info.Slug == m.active
	if publish {
		for key := range m.info.Templates {
			if key == "admin" || strings.HasPrefix(key, "admin-") {
				continue
			}
			if activePrepared.info.Templates[key] == "" {
				return fmt.Errorf("目标主题缺少正在使用的模板入口 %s", key)
			}
		}
	}
	if _, _, _, err = m.loadSettings(prepared.info, prepared.schema, false); err != nil {
		return fmt.Errorf("现有主题设置与目标版本不兼容: %w", err)
	}
	settings, updatedAt, storeVersion, err := m.loadSettings(activePrepared.info, activePrepared.schema, false)
	if err != nil {
		return err
	}
	progress.Report("preparing", 0, 0)
	dir := filepath.Join(m.root, next.Slug)
	if err := extension.CopyTree(ctx, dir, filepath.Join(transaction.Stage, extension.RollbackDir), extension.RollbackDir, extension.HistoryFile); err != nil {
		return err
	}
	if err := extension.Record(dir, transaction.Stage, target.manifest.Version, next.Version, action, digest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := transaction.Commit(); err != nil {
		return err
	}
	m.generation[next.Slug]++
	for _, slug := range affected {
		m.generation[slug]++
	}
	if publish {
		m.info, m.templates, m.settingsSchema = activePrepared.info, activePrepared.templates, activePrepared.schema
		m.translations = activePrepared.translations
		m.assetRevision, m.assetHashes = activePrepared.assets.revision, activePrepared.assets.files
		m.assetOwners = activePrepared.owners
		m.settings, m.settingsUpdatedAt, m.settingsStoreVersion = settings, updatedAt, storeVersion
	}

	return nil
}

// OpenAsset and the version used for cache headers share the same lock as a
// theme switch, preventing an asset open in the gap between directory renames.
func (m *Manager) OpenAsset(relative string) (*os.File, os.FileInfo, string, error) {
	if err := packagefs.ValidateRelativePath(relative); err != nil {
		return nil, nil, "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	file, info, err := packagefs.OpenRegularNoSymlink(m.assetPath(relative))
	return file, info, m.info.Version, err
}
