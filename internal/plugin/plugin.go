package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

var (
	pluginVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	hookNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,95}$`)
	exportNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,95}$`)
	adminIconPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

type Manifest struct {
	Schema        int                 `json:"schema"`
	Slug          string              `json:"slug"`
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	Description   string              `json:"description"`
	Category      string              `json:"category,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
	Author        string              `json:"author"`
	License       string              `json:"license"`
	NewPress      string              `json:"newpress"`
	Runtime       string              `json:"runtime"`
	Entry         string              `json:"entry"`
	Capabilities  []string            `json:"capabilities"`
	OutboundHosts []string            `json:"outbound_hosts,omitempty"`
	Hooks         map[string][]string `json:"hooks,omitempty"`
	Admin         *AdminContribution  `json:"admin,omitempty"`
	Settings      []Setting           `json:"settings,omitempty"`
}

type AdminContribution struct {
	Navigation []AdminNav            `json:"navigation,omitempty"`
	Settings   *SettingsRegistration `json:"settings,omitempty"`
}

type AdminNav struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Icon  string `json:"icon,omitempty"`
}

type Installed struct {
	// Slug comes from the directory, so damaged manifests can still be removed.
	Slug         string                 `json:"slug"`
	Manifest     Manifest               `json:"manifest"`
	Enabled      bool                   `json:"enabled"`
	Error        string                 `json:"error,omitempty"`
	Official     bool                   `json:"official"`
	VersionState extension.VersionState `json:"version_state"`
}

type Manager struct {
	root         string
	mu           sync.Mutex
	generation   map[string]uint64
	contexts     map[string]context.Context
	cancels      map[string]context.CancelFunc
	execution    map[string]ExecutionStatus
	slots        chan struct{}
	taskControls TaskControlStore
}

func NewManager(root string, options ...ManagerOption) *Manager {
	m := &Manager{root: root, generation: make(map[string]uint64), contexts: make(map[string]context.Context), cancels: make(map[string]context.CancelFunc), execution: make(map[string]ExecutionStatus), slots: make(chan struct{}, 8)}
	for _, option := range options {
		option(m)
	}
	return m
}

func (m *Manager) List() ([]Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, err := packagefs.LstatNoSymlink(m.root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("plugin root is missing, linked, or not a directory")
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	plugins := make([]Installed, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		directory := filepath.Join(m.root, entry.Name())
		if info, err := packagefs.LstatNoSymlink(directory); err != nil || !info.IsDir() {
			continue
		}
		manifest, err := readManifest(filepath.Join(directory, "plugin.json"))
		item := Installed{Slug: entry.Name(), Manifest: manifest}
		_, receiptErr := packagefs.LstatNoSymlink(filepath.Join(directory, ".official.json"))
		item.Official = receiptErr == nil || isBuiltin(manifest)
		if err != nil {
			item.Error = err.Error()
		} else if err := ValidateManifest(manifest); err != nil {
			item.Error = err.Error()
		} else if manifest.Slug != entry.Name() {
			item.Error = "plugin manifest slug does not match its directory"
		} else if info, err := packagefs.LstatNoSymlink(filepath.Join(directory, filepath.FromSlash(manifest.Entry))); err != nil || !info.Mode().IsRegular() {
			item.Error = "plugin entry is missing or not a regular file"
		} else {
			marker := filepath.Join(directory, ".disabled")
			markerInfo, markerErr := packagefs.LstatNoSymlink(marker)
			switch {
			case errors.Is(markerErr, os.ErrNotExist):
				item.Enabled = true
			case markerErr != nil:
				item.Error = "plugin disabled marker is missing, linked, or unreadable"
			case !markerInfo.Mode().IsRegular():
				item.Error = "plugin disabled marker is not a regular file"
			}
		}
		if item.Error == "" {
			target, stateErr := m.captureUpdate(item.Slug, "", "")
			if stateErr != nil {
				item.Error = "plugin version state is unreadable"
			} else {
				item.VersionState = target.state
			}
		}
		if item.Error == "" && item.Enabled && m.taskControls != nil && manifest.Runtime == "wasm/core-v1" && len(manifest.Hooks) > 0 {
			ctx, cancel := taskControlContext()
			module, _, controlErr := readModule(filepath.Join(directory, filepath.FromSlash(manifest.Entry)))
			if controlErr == nil {
				controlErr = verifyIntegrity(directory, manifest, module)
			}
			if controlErr == nil {
				_, controlErr = m.activeTaskControl(ctx, manifest, module)
				if controlErr != nil && !errors.Is(controlErr, ErrInactive) {
					cancel()
					return nil, controlErr
				}
			}
			cancel()
			if controlErr != nil {
				item.Enabled = false
				if !errors.Is(controlErr, ErrInactive) {
					item.Error = controlErr.Error()
				}
			}
		}
		plugins = append(plugins, item)
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Slug < plugins[j].Slug })
	return plugins, nil
}

func (m *Manager) InstallPackage(archivePath string) (Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installPackage(archivePath)
}

func (m *Manager) installPackage(archivePath string) (Manifest, error) {
	if info, err := packagefs.LstatNoSymlink(m.root); err != nil || !info.IsDir() {
		return Manifest{}, fmt.Errorf("plugin root is missing, linked, or not a directory")
	}
	manifest, err := packagefs.ReadJSON[Manifest](archivePath, "plugin.json")
	if err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	target := filepath.Join(m.root, manifest.Slug)
	if _, err := packagefs.LstatNoSymlink(target); err == nil {
		return Manifest{}, fmt.Errorf("plugin %s is already installed", manifest.Slug)
	} else if !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("plugin install target is missing, linked, or unavailable: %w", err)
	}
	temporary, err := os.MkdirTemp(m.root, ".install-"+manifest.Slug+"-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(temporary)
	if err := packagefs.Extract(archivePath, temporary, packagefs.DefaultLimits()); err != nil {
		return Manifest{}, err
	}
	for _, reserved := range []string{"data", ".official.json", ".disabled", ".integrity.json", extension.HistoryFile, extension.RollbackDir, ".review-required", ".rollback-configuration.json"} {
		if _, err := os.Lstat(filepath.Join(temporary, reserved)); !os.IsNotExist(err) {
			return Manifest{}, fmt.Errorf("plugin package contains reserved state %s", reserved)
		}
	}
	installedManifest, err := readManifest(filepath.Join(temporary, "plugin.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("read extracted plugin manifest: %w", err)
	}
	if err := ValidateManifest(installedManifest); err != nil {
		return Manifest{}, fmt.Errorf("validate extracted plugin manifest: %w", err)
	}
	if !reflect.DeepEqual(installedManifest, manifest) {
		return Manifest{}, fmt.Errorf("plugin manifest changed while installing")
	}
	entry := filepath.Join(temporary, filepath.FromSlash(installedManifest.Entry))
	if info, err := packagefs.LstatNoSymlink(entry); err != nil || !info.Mode().IsRegular() {
		return Manifest{}, fmt.Errorf("plugin entry %s is missing or not a regular file", installedManifest.Entry)
	}
	probeContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := probeEntry(probeContext, entry, installedManifest); err != nil {
		return Manifest{}, fmt.Errorf("validate plugin runtime: %w", err)
	}
	if err := recordIntegrity(temporary, installedManifest); err != nil {
		return Manifest{}, err
	}
	if err := writeMarker(filepath.Join(temporary, ".disabled"), []byte("installed but not enabled\n")); err != nil {
		return Manifest{}, fmt.Errorf("create disabled marker: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		return Manifest{}, err
	}
	m.nextGeneration(manifest.Slug)
	return manifest, nil
}

func (m *Manager) SetEnabled(slug string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := packagefs.ValidateSlug(slug); err != nil {
		return err
	}
	directory := filepath.Join(m.root, slug)
	if info, err := packagefs.LstatNoSymlink(directory); err != nil || !info.IsDir() {
		return fmt.Errorf("installed plugin directory is missing, linked, or not a directory")
	}
	manifest, err := readManifest(filepath.Join(directory, "plugin.json"))
	if err != nil {
		return fmt.Errorf("read installed plugin: %w", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if manifest.Slug != slug {
		return fmt.Errorf("plugin manifest slug %q does not match directory %q", manifest.Slug, slug)
	}
	marker := filepath.Join(directory, ".disabled")
	if !enabled {
		if manifest.Runtime == "wasm/core-v1" && len(manifest.Hooks) > 0 {
			ctx, cancel := taskControlContext()
			defer cancel()
			if err := m.revokeTaskControl(ctx, slug); err != nil {
				return err
			}
		}
		if err := writeMarker(marker, []byte("disabled by administrator\n")); err != nil {
			return fmt.Errorf("disable plugin: %w", err)
		}
		m.nextGeneration(slug)
		return nil
	}
	if _, err := packagefs.LstatNoSymlink(filepath.Join(directory, ".review-required")); !os.IsNotExist(err) {
		return errors.New("新版插件需要重新确认配置与权限，请先在设置页保存")
	}
	if !isBuiltin(manifest) {
		if err := version.Compatible(manifest.NewPress); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entry := filepath.Join(directory, filepath.FromSlash(manifest.Entry))
	if len(manifest.Hooks) > 0 {
		c, err := m.configurationForData(ctx, filepath.Join(directory, "data"), manifest)
		if err != nil {
			return err
		}
		if c.Version == 0 {
			return errors.New("请先在设置页保存配置并确认权限，再启用插件")
		}
		b, _, err := readModule(entry)
		if err != nil {
			return err
		}
		if err = verifyIntegrity(directory, manifest, b); err != nil {
			return err
		}
	}
	if err := probeEntry(ctx, entry, manifest); err != nil {
		return fmt.Errorf("probe plugin before enabling: %w", err)
	}
	markerInfo, markerErr := packagefs.LstatNoSymlink(marker)
	if markerErr == nil && !markerInfo.Mode().IsRegular() {
		return fmt.Errorf("enable plugin: disabled marker is not a regular file")
	}
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return fmt.Errorf("enable plugin: disabled marker is missing, linked, or unreadable: %w", markerErr)
	}
	// Rotate before enabling. A crash or failed marker removal can invalidate
	// old tasks, but can never revive a previous authorization lifetime.
	// In shared mode the database commit enables matching participating instances;
	// removing this local marker afterwards is a separate filesystem operation.
	if manifest.Runtime == "wasm/core-v1" && len(manifest.Hooks) > 0 {
		if m.taskControls != nil {
			module, _, err := readModule(entry)
			if err != nil {
				return err
			}
			if err := m.enableTaskControl(ctx, manifest, module); err != nil {
				return err
			}
		} else {
			if err := rotateTaskEpoch(filepath.Join(directory, "data")); err != nil {
				return err
			}
		}
	}
	if markerErr == nil {
		if err := os.Remove(marker); err != nil {
			return fmt.Errorf("enable plugin: %w", err)
		}
	}
	m.nextGeneration(slug)
	return nil
}

func ValidateManifest(manifest Manifest) error {
	if manifest.Schema != 1 {
		return fmt.Errorf("unsupported plugin schema %d", manifest.Schema)
	}
	if err := packagefs.ValidateSlug(manifest.Slug); err != nil {
		return err
	}
	if err := validateDiscoveryMetadata(manifest); err != nil {
		return err
	}
	if isBuiltin(manifest) {
		return nil
	}
	if err := validateBusinessManifest(manifest); err != nil {
		return err
	}
	if !validManifestText(manifest.Name, 160, true) || !pluginVersionPattern.MatchString(manifest.Version) || manifest.Runtime != "wasm/core-v1" {
		return fmt.Errorf("plugin name, version and runtime=wasm/core-v1 are required")
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "description", value: manifest.Description, limit: 2000},
		{name: "author", value: manifest.Author, limit: 160},
		{name: "license", value: manifest.License, limit: 80},
		{name: "newpress", value: manifest.NewPress, limit: 128},
	} {
		if !validManifestText(field.value, field.limit, false) {
			return fmt.Errorf("plugin %s is invalid", field.name)
		}
	}
	if err := packagefs.ValidateRelativePath(manifest.Entry); err != nil {
		return fmt.Errorf("plugin entry must be a clean relative path")
	}
	if filepath.Ext(filepath.FromSlash(manifest.Entry)) != ".wasm" {
		return fmt.Errorf("plugin entry must be a .wasm module")
	}
	if len(manifest.Capabilities) > 64 || len(manifest.OutboundHosts) > 64 || len(manifest.Hooks) > 128 || manifest.Admin != nil && len(manifest.Admin.Navigation) > 64 {
		return fmt.Errorf("plugin manifest contains too many capabilities, hosts, hooks or navigation entries")
	}
	allowedCapabilities := map[string]bool{
		"content.read": true, "content.write": true, "settings.read": true,
		"settings.write": true, "cache": true, "jobs.enqueue": true,
		"http.outbound": true, "http.send": true, "render.fragment": true,
	}
	grants := make(map[string]bool, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		if !allowedCapabilities[capability] {
			return fmt.Errorf("unknown plugin capability %q", capability)
		}
		if grants[capability] {
			return fmt.Errorf("duplicate plugin capability %q", capability)
		}
		grants[capability] = true
	}
	if len(manifest.OutboundHosts) > 0 && !grants["http.outbound"] && !grants["http.send"] {
		return fmt.Errorf("outbound_hosts requires http.outbound or http.send capability")
	}
	if grants["http.send"] && len(manifest.OutboundHosts) == 0 {
		return fmt.Errorf("http.send requires explicit outbound_hosts")
	}
	hosts := make(map[string]struct{}, len(manifest.OutboundHosts))
	for _, host := range manifest.OutboundHosts {
		if !validOutboundHost(host) {
			return fmt.Errorf("outbound host %q must be a hostname without scheme, path or port", host)
		}
		if _, exists := hosts[host]; exists {
			return fmt.Errorf("outbound host %q is duplicated", host)
		}
		hosts[host] = struct{}{}
	}
	for hook, exports := range manifest.Hooks {
		if !hookNamePattern.MatchString(hook) || len(exports) == 0 || len(exports) > 64 {
			return fmt.Errorf("plugin hook %q is invalid", hook)
		}
		seen := make(map[string]struct{}, len(exports))
		for _, export := range exports {
			if !exportNamePattern.MatchString(export) {
				return fmt.Errorf("plugin hook %q has invalid export %q", hook, export)
			}
			if _, exists := seen[export]; exists {
				return fmt.Errorf("plugin hook %q repeats export %q", hook, export)
			}
			seen[export] = struct{}{}
		}
	}
	if manifest.Admin != nil {
		seenPaths := make(map[string]struct{}, len(manifest.Admin.Navigation))
		for _, item := range manifest.Admin.Navigation {
			if !validManifestText(item.Label, 80, true) || !validAdminPath(item.Path) || item.Icon != "" && !adminIconPattern.MatchString(item.Icon) {
				return fmt.Errorf("plugin admin navigation entry is invalid")
			}
			if _, exists := seenPaths[item.Path]; exists {
				return fmt.Errorf("plugin admin navigation path %q is duplicated", item.Path)
			}
			seenPaths[item.Path] = struct{}{}
		}
	}
	return nil
}

func validManifestText(value string, maximum int, required bool) bool {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || !utf8.ValidString(value) || required && value == "" || len([]rune(value)) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 && character != '\n' && character != '\r' && character != '\t' || character == 0x7f {
			return false
		}
	}
	return true
}

func validOutboundHost(host string) bool {
	if host == "" || host != strings.ToLower(host) || len(host) > 253 || strings.ContainsAny(host, "/\\:@ ") || strings.HasSuffix(host, ".") {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validAdminPath(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && strings.HasPrefix(value, "/extensions/") && path.Clean(value) == value && parsed.Path == value && parsed.RawQuery == "" && parsed.Fragment == ""
}

func readManifest(path string) (Manifest, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin manifest is missing, linked, or not a regular file: %w", err)
	}
	defer file.Close()
	return packagefs.DecodeJSON[Manifest](file, 1<<20, "plugin manifest")
}

func writeMarker(path string, content []byte) error {
	if err := packagefs.ValidateRegularFileTargetNoSymlink(path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".marker-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := packagefs.ReplaceRegularFileNoSymlink(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
