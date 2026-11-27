package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/licensing"
	"newpress.local/newpress/internal/packagefs"
)

const CommentOrigin = "comment-origin"

var ErrInactive = errors.New("plugin is not installed, enabled, or generation changed")

// BuiltinManifest describes reviewed host code shipped with this executable.
// Installing this package activates a registered extension, never arbitrary Go code.
func BuiltinManifest() Manifest {
	return Manifest{Schema: 1, Slug: CommentOrigin, Name: "评论来源", Version: "1.0.1", Description: "为评论展示城市归属地与设备信息，按作者角色独立控制显示。", Author: "NewPress 官方", License: "Apache-2.0", NewPress: "builtin/comment-origin-v1", Runtime: "builtin/comment-origin-v1", Entry: "builtin.json", Capabilities: []string{"settings.read", "settings.write", "http.outbound", "render.fragment"}, OutboundHosts: []string{"ipwho.is", "restapi.amap.com", "api.map.baidu.com"}}
}

func isBuiltin(m Manifest) bool {
	for _, definition := range BundledManifests() {
		if reflect.DeepEqual(m, definition) {
			return true
		}
	}
	return false
}

func probeEntry(ctx context.Context, entry string, manifest Manifest) error {
	if isBuiltin(manifest) {
		f, _, err := packagefs.OpenRegularNoSymlink(entry)
		if err != nil {
			return err
		}
		defer f.Close()
		v, err := packagefs.DecodeJSON[struct {
			Runtime string `json:"runtime"`
		}](f, 1024, "builtin descriptor")
		if err != nil || v.Runtime != manifest.Runtime {
			return errors.New("unsupported builtin descriptor")
		}
		return nil
	}
	_, err := ProbeFile(ctx, entry, manifest.Slug, DefaultSandboxOptions())
	if err == nil && len(manifest.Hooks) > 0 {
		var b []byte
		b, _, err = readModule(entry)
		if err == nil {
			probe, cancel := context.WithTimeout(ctx, defaultCallTimeout)
			defer cancel()
			_, err = executeBusiness(probe, b, manifest, "", nil, nil)
		}
	}
	return err
}

func (m *Manager) InstallBuiltin() error {
	return m.InstallBundled(CommentOrigin)
}

func (m *Manager) InstallBundled(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var definition Manifest
	for _, candidate := range BundledManifests() {
		if candidate.Slug == slug {
			definition = candidate
			break
		}
	}
	if definition.Slug == "" {
		return errors.New("unknown bundled plugin")
	}
	if info, err := packagefs.LstatNoSymlink(m.root); err != nil || !info.IsDir() {
		return errors.New("plugin root unavailable")
	}
	target := filepath.Join(m.root, slug)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return errors.New("plugin is already installed or unavailable")
	}
	temp, err := os.MkdirTemp(m.root, ".install-"+slug+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	manifest, _ := json.MarshalIndent(definition, "", "  ")
	descriptor, _ := json.Marshal(map[string]string{"runtime": definition.Runtime})
	for name, data := range map[string][]byte{
		"plugin.json": manifest, "builtin.json": descriptor,
		"LICENSE":                []byte(licensing.Apache20),
		"THIRD_PARTY_NOTICES.md": []byte(licensing.HostPluginNotices),
		"flag-icons-MIT.txt":     []byte(licensing.FlagIconsMIT),
		".disabled":              []byte("installed but not enabled\n"),
	} {
		if err = writeMarker(filepath.Join(temp, name), data); err != nil {
			return err
		}
	}
	if err = os.Rename(temp, target); err != nil {
		return err
	}
	m.nextGeneration(slug)
	return nil
}

// WithData serializes short local operations against disable/uninstall. A worker
// must supply its captured generation before committing an external lookup.
// Data is kept inside the installation so a clean uninstall removes every fact.
func (m *Manager) WithData(slug string, enabled bool, generation uint64, fn func(string, uint64) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := packagefs.ValidateSlug(slug); err != nil {
		return err
	}
	dir := filepath.Join(m.root, slug)
	manifest, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err != nil || manifest.Slug != slug || ValidateManifest(manifest) != nil {
		return ErrInactive
	}
	if IsBundledSlug(slug) && !isBuiltin(manifest) {
		return ErrInactive
	}
	if generation != 0 && generation != m.generationID(slug) {
		return ErrInactive
	}
	if enabled {
		if _, err := os.Lstat(filepath.Join(dir, ".disabled")); !os.IsNotExist(err) {
			return ErrInactive
		}
	}
	data := filepath.Join(dir, "data")
	if err := packagefs.MkdirAllNoSymlink(data, 0o700); err != nil {
		return err
	}
	return fn(data, m.generationID(slug))
}

func (m *Manager) generationID(slug string) uint64 {
	if m.generation[slug] == 0 {
		m.generation[slug] = 1
	}
	return m.generation[slug]
}

func (m *Manager) nextGeneration(slug string) {
	if cancel := m.cancels[slug]; cancel != nil {
		cancel()
	}
	delete(m.cancels, slug)
	delete(m.contexts, slug)
	businessCompilations.invalidate(m.root, slug)
	m.generation[slug]++
	delete(m.execution, slug)
}

// ActiveContext cancels outbound jobs immediately when an installation is
// disabled, removed or replaced. A stale generation cannot obtain a new lease.
func (m *Manager) ActiveContext(slug string, generation uint64) (context.Context, error) {
	return m.installationContext(slug, generation, true)
}

// InstalledContext also permits diagnostics before enabling a plugin. Lifecycle
// changes still cancel the request and stale generations cannot acquire a lease.
func (m *Manager) InstalledContext(slug string, generation uint64) (context.Context, error) {
	return m.installationContext(slug, generation, false)
}

func (m *Manager) installationContext(slug string, generation uint64, enabled bool) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if packagefs.ValidateSlug(slug) != nil || generation != m.generationID(slug) {
		return nil, ErrInactive
	}
	dir := filepath.Join(m.root, slug)
	manifest, err := readManifest(filepath.Join(dir, "plugin.json"))
	if err != nil || manifest.Slug != slug || ValidateManifest(manifest) != nil {
		return nil, ErrInactive
	}
	if enabled {
		if _, err = os.Lstat(filepath.Join(dir, ".disabled")); !os.IsNotExist(err) {
			return nil, ErrInactive
		}
	}
	if ctx := m.contexts[slug]; ctx != nil {
		return ctx, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.contexts[slug] = ctx
	m.cancels[slug] = cancel
	return ctx, nil
}

// Uninstall first detaches the whole namespace under the same lock as data
// writes. If cleanup is interrupted, Recover finishes it before serving traffic.
func (m *Manager) Uninstall(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := packagefs.ValidateSlug(slug); err != nil {
		return err
	}
	target := filepath.Join(m.root, slug)
	info, err := packagefs.LstatNoSymlink(target)
	if os.IsNotExist(err) {
		ctx, cancel := taskControlContext()
		defer cancel()
		transition, err := m.beginTaskControlRetirement(ctx, slug)
		if err != nil {
			return err
		}
		return m.finishTaskControlTransition(ctx, transition)
	}
	if err != nil || !info.IsDir() {
		return errors.New("invalid plugin directory")
	}
	tomb := filepath.Join(m.root, ".removed-"+slug)
	if _, err = os.Lstat(tomb); err == nil {
		return errors.New("previous cleanup is pending")
	}
	ctx, cancel := taskControlContext()
	defer cancel()
	transition, err := m.beginTaskControlRetirement(ctx, slug)
	if err != nil {
		return err
	}
	if err = os.Rename(target, tomb); err != nil {
		return err
	}
	m.nextGeneration(slug)
	if err = os.RemoveAll(tomb); err != nil {
		return errors.New("plugin removed; file cleanup is pending until restart")
	}
	return m.finishTaskControlTransition(ctx, transition)
}

func (m *Manager) Recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := extension.Recover(m.root); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".download-") && strings.HasSuffix(entry.Name(), ".zip") {
			p := filepath.Join(m.root, entry.Name())
			info, err := packagefs.LstatNoSymlink(p)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("invalid interrupted plugin download")
			}
			if err = os.Remove(p); err != nil {
				return err
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".removed-") && !strings.HasPrefix(entry.Name(), ".install-") {
			continue
		}
		p := filepath.Join(m.root, entry.Name())
		info, err := packagefs.LstatNoSymlink(p)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("invalid interrupted plugin operation")
		}
		if err = os.RemoveAll(p); err != nil {
			return err
		}
	}
	return m.recoverTaskControlTransitions()
}

func WritePrivate(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeMarker(path, b)
}

func ReadPrivate[T any](path string, limit int64) (T, error) {
	var zero T
	f, _, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	return packagefs.DecodeJSON[T](f, limit, "plugin private data")
}
