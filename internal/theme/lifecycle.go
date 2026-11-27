package theme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

const SelectionFile = ".active-theme.json"

type selection struct {
	Schema int    `json:"schema"`
	Slug   string `json:"slug"`
}

// SelectedSlug is also used by offline backup so its runtime manifest reflects
// the selection saved by the administration UI, including after a restart.
func SelectedSlug(root, fallback string) (string, error) {
	document, err := extension.ReadJSON[selection](filepath.Join(root, SelectionFile), 1024)
	if os.IsNotExist(err) {
		return fallback, packagefs.ValidateSlug(fallback)
	}
	if err != nil {
		return "", err
	}
	if document.Schema != 1 || packagefs.ValidateSlug(document.Slug) != nil {
		return "", errors.New("saved theme selection is invalid")
	}
	return document.Slug, nil
}

func (m *Manager) Activate(ctx context.Context, slug, expected, revision, expectedActive string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if expectedActive == "" || m.Active().Slug != expectedActive {
		return extension.ErrConflict
	}
	target, err := m.captureUpdate(slug, expected, revision)
	if err != nil {
		return err
	}
	if err := version.Compatible(target.manifest.NewPress); err != nil {
		return err
	}
	if slug == expectedActive {
		return errors.New("该主题已在使用")
	}
	progress.Report("validating", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Switching is the non-cancellable commit phase. loadSelected publishes the
	// persisted selection and prepared templates together under the reader lock.
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.loadSelected(slug, true)
}

func (m *Manager) Uninstall(ctx context.Context, slug, expected, revision, expectedActive string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	active := m.Active().Slug
	if expectedActive == "" || active != expectedActive {
		return extension.ErrConflict
	}
	if slug == active {
		return errors.New("正在使用的主题不能卸载，请先切换到其他主题")
	}
	if _, err := m.captureUpdate(slug, expected, revision); err != nil {
		return err
	}
	dependents, err := m.dependents(slug)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return fmt.Errorf("父主题仍被子主题依赖: %v", dependents)
	}
	target := filepath.Join(m.root, slug)
	if info, err := packagefs.LstatNoSymlink(target); err != nil || !info.IsDir() {
		return errors.New("主题安装目录不可用")
	}
	trash, err := os.MkdirTemp(m.root, ".uninstall-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(trash)
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	err = os.Rename(target, filepath.Join(trash, slug))
	if err == nil {
		m.generation[slug]++
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("主题已卸载，但临时文件清理失败: %w", err)
	}
	// Settings belong to the site, not to the installed package. Keep them so a
	// reinstall can restore customization without touching content or navigation.
	return nil
}
