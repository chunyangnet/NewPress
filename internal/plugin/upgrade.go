package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

type updateTarget struct {
	manifest   Manifest
	state      extension.VersionState
	generation uint64
}

func (m *Manager) VersionState(slug string) (extension.VersionState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, err := m.captureUpdate(slug, "", "")
	return target.state, err
}

func (m *Manager) captureUpdate(slug, expected, revision string) (updateTarget, error) {
	if err := packagefs.ValidateSlug(slug); err != nil {
		return updateTarget{}, err
	}
	dir := filepath.Join(m.root, slug)
	manifest, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err != nil || ValidateManifest(manifest) != nil || manifest.Slug != slug {
		return updateTarget{}, errors.New("已安装插件信息不可用")
	}
	state, err := extension.State(dir, manifest.Version)
	if err != nil {
		return updateTarget{}, err
	}
	info, markerErr := packagefs.LstatNoSymlink(filepath.Join(dir, ".review-required"))
	if markerErr == nil && info.Mode().IsRegular() {
		state.ReviewRequired = true
	} else if !os.IsNotExist(markerErr) {
		return updateTarget{}, errors.New("插件权限确认记录不可用")
	}
	if expected != "" && expected != manifest.Version || revision != "" && revision != state.Revision {
		return updateTarget{}, extension.ErrConflict
	}
	return updateTarget{manifest: manifest, state: state, generation: m.generationID(slug)}, nil
}

func (m *Manager) updatePackage(ctx context.Context, archive string, release Release, target updateTarget, progress extension.Progress) error {
	next := release.Manifest
	if next.Slug != target.manifest.Slug || next.Runtime != "wasm/core-v1" || target.manifest.Runtime != next.Runtime {
		return errors.New("内置插件随 NewPress 更新；独立更新不能更换运行时")
	}
	comparison, err := version.Compare(next.Version, target.manifest.Version)
	if err != nil || comparison <= 0 {
		return errors.New("在线更新只接受更高的语义版本")
	}
	if release.Release == nil || !release.Release.DataCompatible {
		return errors.New("发布者尚未声明数据兼容，不能在线覆盖已有插件")
	}
	if err := version.Compatible(next.NewPress); err != nil {
		return err
	}
	transaction, err := extension.Begin(m.root, next.Slug)
	if err != nil {
		return err
	}
	defer transaction.Close()
	progress.Report("validating", 0, 0)
	probeRoot := filepath.Join(filepath.Dir(transaction.Stage), "probe")
	if err := os.Mkdir(probeRoot, 0700); err != nil {
		return err
	}
	manifest, err := NewManager(probeRoot).InstallPackage(archive)
	if err != nil {
		return err
	}
	if !reflectManifest(manifest, next) {
		return errors.New("插件包与已选择发行版不一致")
	}
	if err := os.Remove(transaction.Stage); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(probeRoot, next.Slug), transaction.Stage); err != nil {
		return err
	}
	if err := WritePrivate(filepath.Join(transaction.Stage, ".official.json"), release); err != nil {
		return err
	}
	return m.replacePrepared(ctx, transaction, next, target, "update", release.SHA256, progress)
}

func (m *Manager) Rollback(ctx context.Context, slug, expected, revision, previous string, progress extension.Progress) error {
	m.mu.Lock()
	target, err := m.captureUpdate(slug, expected, revision)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if previous == "" || target.state.Previous != previous {
		return extension.ErrConflict
	}
	if target.manifest.Runtime != "wasm/core-v1" {
		return errors.New("内置插件随 NewPress 更新")
	}
	transaction, err := extension.Begin(m.root, slug)
	if err != nil {
		return err
	}
	defer transaction.Close()
	progress.Report("validating", 0, 0)
	// Hold the lifecycle lock while reading the retained snapshot. A concurrent
	// uninstall/update must not exchange the source while it is being copied.
	m.mu.Lock()
	current, err := m.captureUpdate(slug, expected, revision)
	if err == nil && current.generation != target.generation {
		err = extension.ErrConflict
	}
	if err == nil {
		err = extension.VerifyRollback(filepath.Join(m.root, slug))
	}
	if err == nil {
		err = extension.CopyTree(ctx, filepath.Join(m.root, slug, extension.RollbackDir), transaction.Stage)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	next, err := readManifest(filepath.Join(transaction.Stage, "plugin.json"))
	if err != nil || ValidateManifest(next) != nil || next.Slug != slug || next.Version != previous || next.Runtime != target.manifest.Runtime {
		return errors.New("插件回退快照无效")
	}
	if err := version.Compatible(next.NewPress); err != nil {
		return err
	}
	module, _, err := readModule(filepath.Join(transaction.Stage, filepath.FromSlash(next.Entry)))
	if err != nil {
		return err
	}
	if err := verifyIntegrity(transaction.Stage, next, module); err != nil {
		return err
	}
	if err := probeEntry(ctx, filepath.Join(transaction.Stage, filepath.FromSlash(next.Entry)), next); err != nil {
		return err
	}
	receipt, err := ReadPrivate[Release](filepath.Join(transaction.Stage, ".official.json"), 256<<10)
	if err != nil {
		return err
	}
	return m.replacePrepared(ctx, transaction, next, target, "rollback", receipt.SHA256, progress)
}

func (m *Manager) replacePrepared(ctx context.Context, transaction *extension.Transaction, next Manifest, target updateTarget, action, digest string, progress extension.Progress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	slug := next.Slug
	current, err := m.captureUpdate(slug, target.manifest.Version, target.state.Revision)
	if err != nil {
		return err
	}
	if current.generation != target.generation {
		return extension.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Join(m.root, slug)
	// An official receipt alone is not enough: verify the actual running bytes
	// before keeping them as a trusted rollback candidate.
	module, _, err := readModule(filepath.Join(dir, filepath.FromSlash(current.manifest.Entry)))
	if err != nil {
		return err
	}
	if err := verifyIntegrity(dir, current.manifest, module); err != nil {
		return err
	}
	progress.Report("preparing", 0, 0)
	rollback := filepath.Join(transaction.Stage, extension.RollbackDir)
	if err := extension.CopyTree(ctx, dir, rollback, "data", extension.RollbackDir, extension.HistoryFile, ".rollback-configuration.json"); err != nil {
		return err
	}
	oldConfiguration, err := m.configurationForData(ctx, filepath.Join(dir, "data"), current.manifest)
	if err != nil {
		return err
	}
	var sharedBefore TaskControl
	var sharedFound bool
	if m.taskControls != nil {
		sharedBefore, sharedFound, err = m.loadTaskControl(ctx, slug)
		if err != nil {
			return err
		}
		if sharedFound {
			if sharedBefore.TransitionID != "" || !taskControlPackageMatches(sharedBefore, current.manifest, module) {
				return ErrTaskControlConflict
			}
			oldConfiguration = sharedBefore.Configuration
		}
	}
	if err := writeConfiguration(filepath.Join(rollback, ".rollback-configuration.json"), oldConfiguration); err != nil {
		return err
	}
	dataDir := filepath.Join(dir, "data")
	if _, err := packagefs.LstatNoSymlink(dataDir); err == nil {
		if err := extension.CopyTree(ctx, dataDir, filepath.Join(transaction.Stage, "data")); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	baseline := defaultConfiguration(next)
	if action == "rollback" {
		baseline, err = ReadPrivate[Configuration](filepath.Join(transaction.Stage, ".rollback-configuration.json"), businessBytes)
		if err != nil || validateConfiguration(next, baseline) != nil {
			return errors.New("回退版本配置快照无效")
		}
		if err := os.Remove(filepath.Join(transaction.Stage, ".rollback-configuration.json")); err != nil {
			return err
		}
	}
	review := len(next.Hooks) > 0 && oldConfiguration.Version == 0 || current.state.ReviewRequired || expanded(current.manifest.Capabilities, next.Capabilities) ||
		expanded(current.manifest.OutboundHosts, next.OutboundHosts) || !reflect.DeepEqual(current.manifest.Settings, next.Settings)
	configuration := defaultConfiguration(next)
	configuration.Version = oldConfiguration.Version + 1
	if configuration.Version < 1 {
		return errors.New("插件配置版本超出范围")
	}
	for _, setting := range next.Settings {
		value, exists := oldConfiguration.Values[setting.Key]
		if !exists {
			if saved, ok := baseline.Values[setting.Key]; ok {
				value = saved
			} else {
				value = setting.Default
			}
		}
		if err := validateValue(setting, value); err != nil {
			return fmt.Errorf("已有配置 %s 不符合目标版本要求，请先调整配置: %w", setting.Key, err)
		}
		configuration.Values[setting.Key] = value
	}
	for _, grant := range oldConfiguration.Grants {
		if slices.Contains(next.Capabilities, grant) && !((grant == "http.outbound" || grant == "http.send") && expanded(current.manifest.OutboundHosts, next.OutboundHosts)) {
			configuration.Grants = append(configuration.Grants, grant)
		}
	}
	if len(next.Hooks) > 0 {
		if err := packagefs.MkdirAllNoSymlink(filepath.Join(transaction.Stage, "data"), 0700); err != nil {
			return err
		}
		if err := writeConfiguration(filepath.Join(transaction.Stage, "data", "runtime.json"), configuration); err != nil {
			return err
		}
		// Copied state must not revive old tasks, even on code rollback.
		if err := rotateTaskEpoch(filepath.Join(transaction.Stage, "data")); err != nil {
			return err
		}
	}
	if err := clearReview(transaction.Stage); err != nil {
		return err
	}
	if review {
		if err := writeMarker(filepath.Join(transaction.Stage, ".review-required"), []byte("review configuration and permissions before enabling\n")); err != nil {
			return err
		}
	}
	_, disabledErr := packagefs.LstatNoSymlink(filepath.Join(dir, ".disabled"))
	if disabledErr != nil && !os.IsNotExist(disabledErr) {
		return disabledErr
	}
	if !review && !sharedFound && os.IsNotExist(disabledErr) {
		if err := os.Remove(filepath.Join(transaction.Stage, ".disabled")); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := writeMarker(filepath.Join(transaction.Stage, ".disabled"), []byte("disabled until administrator enables\n")); err != nil {
		return err
	}
	if err := extension.Record(dir, transaction.Stage, current.manifest.Version, next.Version, action, digest); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	var transition *taskControlTransition
	if sharedFound {
		preparedModule, _, err := readModule(filepath.Join(transaction.Stage, filepath.FromSlash(next.Entry)))
		if err != nil {
			return err
		}
		desired := TaskControl{Schema: 1, Slug: slug, Manifest: next, ModuleDigest: digestBytes(preparedModule), Configuration: configuration, Retired: sharedBefore.Retired}
		transition, err = m.beginTaskControlTransition(ctx, sharedBefore, desired, action)
		if err != nil {
			return err
		}
	}
	m.nextGeneration(slug)
	if err := transaction.Commit(); err != nil {
		return err
	}
	return m.finishTaskControlTransition(ctx, transition)
}

func expanded(before, after []string) bool {
	for _, value := range after {
		if !slices.Contains(before, value) {
			return true
		}
	}
	return false
}
