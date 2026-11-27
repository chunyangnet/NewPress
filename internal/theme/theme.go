package theme

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/i18n"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

var themeVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

type Manifest struct {
	Parent            *ParentTheme      `json:"parent,omitempty"`
	EffectiveRevision string            `json:"-"`
	Schema            int               `json:"schema"`
	Slug              string            `json:"slug"`
	Name              string            `json:"name"`
	Version           string            `json:"version"`
	Description       string            `json:"description"`
	Author            string            `json:"author"`
	License           string            `json:"license"`
	Engine            string            `json:"engine"`
	NewPress          string            `json:"newpress"`
	Templates         map[string]string `json:"templates"`
	SettingsSchema    string            `json:"settings_schema,omitempty"`
	View              string            `json:"view,omitempty"`
	Capabilities      []string          `json:"capabilities,omitempty"`
	Cover             string            `json:"cover,omitempty"`
	DefaultLocale     string            `json:"default_locale,omitempty"`
}

type Manager struct {
	assetOwners          map[string]string
	opMu                 sync.Mutex
	generation           map[string]uint64
	saveGuard            func(map[string]any) (func(), map[string]string, error)
	root                 string
	active               string
	settingsDir          string
	settingsRepository   SettingsRepository
	settingsStoreVersion int64
	mu                   sync.RWMutex
	info                 Manifest
	templates            map[string]*template.Template
	translations         *i18n.Catalog
	assetRevision        string
	assetHashes          map[string][sha256.Size]byte
	settingsSchema       *settingSchema
	settings             map[string]any
	settingsUpdatedAt    time.Time
}

func NewManager(root, active string, options ...ManagerOption) *Manager {
	manager := &Manager{root: root, active: active, generation: map[string]uint64{}}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func (m *Manager) Load() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	selected, err := SelectedSlug(m.root, m.active)
	if err != nil {
		return err
	}
	return m.loadSelected(selected, false)
}

// LoadExplicit loads the named theme without consulting or changing the saved
// site selection. Offline validators use it to check the package they were given.
func (m *Manager) LoadExplicit(slug string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.loadSelected(slug, false)
}

// Caller holds opMu. Prepare everything before changing the published snapshot.
func (m *Manager) loadSelected(selected string, persist bool) error {
	prepared, err := prepareTheme(m.root, selected, nil)
	if err != nil {
		return err
	}
	manifest, settingsSchema, compiled, translations, assetRevision := prepared.info, prepared.schema, prepared.templates, prepared.translations, prepared.assets
	settings, settingsUpdatedAt, storeVersion, err := m.loadSettings(manifest, settingsSchema, true)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if persist {
		if err := extension.WriteJSON(filepath.Join(m.root, SelectionFile), selection{Schema: 1, Slug: selected}); err != nil {
			return err
		}
	}
	m.assetOwners = prepared.owners
	m.active = selected
	m.info = manifest
	m.templates = compiled
	m.translations = translations
	m.assetRevision, m.assetHashes = assetRevision.revision, assetRevision.files
	m.settingsSchema = settingsSchema
	m.settings = settings
	m.settingsUpdatedAt = settingsUpdatedAt
	m.settingsStoreVersion = storeVersion
	m.generation[m.active]++
	return nil
}

func (m *Manager) Active() Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneManifest(m.info)
}

func (m *Manager) AssetDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return filepath.Join(m.root, m.active, "assets")
}

// TimezoneProvider exposes the site's IANA timezone to theme helpers. The
// server page model implements it so templates that call the bare date helper
// still render in 站点设置 timezone instead of the host's local zone.
type TimezoneProvider interface{ SiteTimezone() string }
type AssetBaseProvider interface{ ThemeAssetBase() string }

func (m *Manager) Render(writer io.Writer, page string, data any) error {
	return m.render(writer, page, data, nil)
}

// RenderPublic captures the selected contract and compiled template together,
// so an OTA switch cannot pair a new contract with an older adapter.
func (m *Manager) RenderPublic(writer io.Writer, page string, chunnel, public any) error {
	return m.render(writer, page, chunnel, public)
}

func (m *Manager) render(writer io.Writer, page string, data, public any) error {
	m.mu.RLock()
	parsed := m.templates[page]
	if public != nil && m.info.View == PublicViewV1 {
		data = public
	}
	assetBase := "/theme-assets/" + m.info.Slug + "/" + m.assetRevision + "/"
	translations := m.translations
	m.mu.RUnlock()
	if parsed == nil {
		return fmt.Errorf("%w: %s", ErrTemplateMissing, page)
	}
	if provider, ok := data.(AssetBaseProvider); ok {
		assetBase = provider.ThemeAssetBase() + assetBase
	}
	return Execute(writer, parsed, data, func(name string) string { return assetBase + strings.TrimLeft(name, "/") }, translations)
}

var ErrTemplateMissing = errors.New("theme template missing")

// Execute binds request-local helpers to a clone. Prototypes are never executed.
// Both the embedded core UI and themes use the same escaping/timezone rules.
func Execute(writer io.Writer, parsed *template.Template, data any, asset func(string) string, catalogs ...*i18n.Catalog) error {
	// The shared prototype must never execute: html/template cannot clone a
	// template after execution, even when that render had no timezone provider.
	bound, err := parsed.Clone()
	if err != nil {
		return fmt.Errorf("clone theme template: %w", err)
	}
	location := time.UTC
	if provider, ok := data.(TimezoneProvider); ok {
		location = themeLocation(provider.SiteTimezone())
	}
	locale := i18n.DefaultLocale
	if provider, ok := data.(LocaleProvider); ok {
		locale = provider.SiteLocale()
	}
	translator := i18n.NewTranslator(locale, slices.Concat(catalogs, []*i18n.Catalog{i18n.Core()})...)
	bound.Funcs(template.FuncMap{
		"t":     translator.Text,
		"date":  func(value time.Time) string { return formatThemeTime(value, location, "2006-01-02") },
		"asset": asset,
		"assetOr": func(value, path string) string {
			if strings.TrimSpace(value) != "" {
				return value
			}
			return asset(path)
		},
	})
	if err := bound.ExecuteTemplate(writer, "base", data); err != nil {
		return fmt.Errorf("render theme: %w", err)
	}
	return nil
}

func (m *Manager) InstallPackage(archivePath string) (Manifest, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	manifest, err := packagefs.ReadJSON[Manifest](archivePath, "theme.json")
	if err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	err = installArchive(m.root, manifest, archivePath)
	if err == nil {
		m.generation[manifest.Slug]++
	}
	return manifest, err
}

func (m *Manager) List() ([]Manifest, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if info, err := packagefs.LstatNoSymlink(m.root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("theme root is missing, linked, or not a directory")
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	items := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		manifest, err := readManifest(filepath.Join(m.root, entry.Name(), "theme.json"))
		if err == nil && validateManifest(manifest) == nil && manifest.Slug == entry.Name() {
			items = append(items, manifest)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Slug < items[j].Slug })
	return items, nil
}

func readManifest(path string) (Manifest, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("theme manifest is missing, linked, or not a regular file: %w", err)
	}
	defer file.Close()
	manifest, err := packagefs.DecodeJSON[Manifest](file, 1<<20, "theme manifest")
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Cover != "" && !validCoverPath(manifest.Cover) {
		return errors.New("theme cover must be a PNG, JPEG or WebP file under assets/")
	}
	if manifest.Schema != 1 && manifest.Schema != 2 {
		return fmt.Errorf("unsupported theme schema %d", manifest.Schema)
	}
	if manifest.Parent != nil {
		if manifest.Schema != 2 || manifest.Parent.Slug == manifest.Slug {
			return errors.New("child themes require schema=2 and a different parent")
		}
		if err := packagefs.ValidateSlug(manifest.Parent.Slug); err != nil {
			return err
		}
		if manifest.Parent.Version == "" {
			return errors.New("parent version range is required")
		}
		if err := version.ValidateRange(manifest.Parent.Version); err != nil {
			return err
		}
	}
	if manifest.View != PublicViewV1 && (manifest.View != ChunnelViewV1 || (manifest.Slug != "chunnel" && (manifest.Parent == nil || manifest.Parent.Slug != "chunnel"))) {
		return fmt.Errorf("unsupported theme view %q", manifest.View)
	}
	if manifest.DefaultLocale != "" {
		if _, err := i18n.CanonicalLocale(manifest.DefaultLocale); err != nil {
			return fmt.Errorf("invalid theme default_locale: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		if capability != "home-layout/v1" || seen[capability] {
			return fmt.Errorf("unsupported or duplicate theme capability %q", capability)
		}
		seen[capability] = true
	}
	if err := packagefs.ValidateSlug(manifest.Slug); err != nil {
		return err
	}
	if !validThemeText(manifest.Name, 160, true) || !themeVersionPattern.MatchString(manifest.Version) || manifest.Engine != "go-template/v1" {
		return fmt.Errorf("theme name, version and engine=go-template/v1 are required")
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
		if !validThemeText(field.value, field.limit, false) {
			return fmt.Errorf("theme %s is invalid", field.name)
		}
	}
	if manifest.Parent == nil && (manifest.Templates["home"] == "" || manifest.Templates["single"] == "") {
		return fmt.Errorf("theme must provide home and single templates")
	}
	if len(manifest.Templates) > 128 {
		return fmt.Errorf("theme cannot declare more than 128 templates")
	}
	for pageName, pageFile := range manifest.Templates {
		if !validThemePageName(pageName) {
			return fmt.Errorf("invalid theme template key %q", pageName)
		}
		if err := packagefs.ValidateRelativePath(pageFile); err != nil || path.Ext(pageFile) != ".html" {
			return fmt.Errorf("invalid theme template path %q", pageFile)
		}
	}
	if manifest.SettingsSchema != "" {
		if err := packagefs.ValidateRelativePath(manifest.SettingsSchema); err != nil || path.Ext(manifest.SettingsSchema) != ".json" {
			return fmt.Errorf("invalid theme settings schema path %q", manifest.SettingsSchema)
		}
	}
	return nil
}

// ValidateManifest exposes the contract to the signed release publisher.
func ValidateManifest(manifest Manifest) error { return validateManifest(manifest) }

func validThemeText(value string, maximum int, required bool) bool {
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

func validThemePageName(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func installArchive(root string, manifest Manifest, archivePath string, dependencyRoots ...string) error {
	if info, err := packagefs.LstatNoSymlink(root); err != nil || !info.IsDir() {
		return fmt.Errorf("theme root is missing, linked, or not a directory")
	}
	slug := manifest.Slug
	target := filepath.Join(root, slug)
	if _, err := packagefs.LstatNoSymlink(target); err == nil {
		return fmt.Errorf("package %s is already installed", slug)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("theme install target is missing, linked, or unavailable: %w", err)
	}
	temporary, err := os.MkdirTemp(root, ".install-"+slug+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := packagefs.Extract(archivePath, temporary, packagefs.DefaultLimits()); err != nil {
		return err
	}
	for _, reserved := range []string{extension.HistoryFile, extension.RollbackDir, ".official.json"} {
		if _, err := os.Lstat(filepath.Join(temporary, reserved)); !os.IsNotExist(err) {
			return fmt.Errorf("theme package contains reserved state %s", reserved)
		}
	}
	installedManifest, err := readManifest(filepath.Join(temporary, "theme.json"))
	if err != nil {
		return err
	}
	if err := validateManifest(installedManifest); err != nil {
		return fmt.Errorf("validate extracted theme manifest: %w", err)
	}
	if !reflect.DeepEqual(installedManifest, manifest) {
		return fmt.Errorf("theme manifest changed while installing")
	}
	dependencyRoot := root
	if len(dependencyRoots) > 0 {
		dependencyRoot = dependencyRoots[0]
	}
	if _, err := prepareTheme(dependencyRoot, installedManifest.Slug, map[string]string{installedManifest.Slug: temporary}); err != nil {
		return fmt.Errorf("validate theme combination: %w", err)
	}
	if installedManifest.Cover != "" {
		if _, _, err := readCover(temporary, installedManifest); err != nil {
			return err
		}
	}
	return os.Rename(temporary, target)
}

func compileTemplates(directory string, manifest Manifest) (map[string]*template.Template, error) {
	base := filepath.Join(directory, "templates", "base.html")
	if info, err := packagefs.LstatNoSymlink(base); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("theme base template is missing, linked, or not a regular file")
	}
	partialsDirectory := filepath.Join(directory, "templates", "partials")
	if info, err := packagefs.LstatNoSymlink(partialsDirectory); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("theme partials path is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect theme partials directory: %w", err)
	}
	partials, err := filepath.Glob(filepath.Join(partialsDirectory, "*.html"))
	if err != nil {
		return nil, err
	}
	for _, partial := range partials {
		if info, err := packagefs.LstatNoSymlink(partial); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("theme partial is linked or not a regular file: %s", filepath.Base(partial))
		}
	}
	functions := Functions()
	// Each page gets its own template set, but all pages in this load use the
	// same verified source bytes. A later load reads and checks the files again.
	sources := make(map[string]string, len(partials)+len(manifest.Templates)+1)
	compiled := make(map[string]*template.Template, len(manifest.Templates))
	for page, pageFile := range manifest.Templates {
		if err := packagefs.ValidateRelativePath(pageFile); err != nil {
			return nil, fmt.Errorf("unsafe template path %q", pageFile)
		}
		cleanPage := filepath.FromSlash(pageFile)
		pagePath := filepath.Join(directory, "templates", cleanPage)
		if info, err := packagefs.LstatNoSymlink(pagePath); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("theme page template is missing, linked, or not a regular file: %s", pageFile)
		}
		files := append([]string{base}, partials...)
		files = append(files, pagePath)
		parsed, err := parseVerifiedTemplates(files, functions, sources)
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", page, err)
		}
		compiled[page] = parsed
	}
	return compiled, nil
}

// Functions is the restricted presentation-only template helper set.
func Functions() template.FuncMap {
	return template.FuncMap{
		"t":     i18n.NewTranslator(i18n.DefaultLocale, i18n.Core()).Text,
		"dict":  templateDictionary,
		"add":   func(left, right int) int { return left + right },
		"asset": func(path string) string { return "/theme-assets/" + path },
		"assetOr": func(value, path string) string {
			if strings.TrimSpace(value) != "" {
				return value
			}
			return "/theme-assets/" + strings.TrimLeft(path, "/")
		},
		// date falls back to UTC until Render binds the site timezone for the
		// current request. Pages that know their zone should use dateIn.
		"date": func(value time.Time) string { return formatThemeTime(value, time.UTC, "2006-01-02") },
		"dateIn": func(value time.Time, timezone string) string {
			return formatThemeTime(value, themeLocation(timezone), "2006-01-02")
		},
		"datetimeIn": func(value time.Time, timezone string) string {
			return formatThemeTime(value, themeLocation(timezone), "2006-01-02 15:04")
		},
		"datetimeSecondsIn": func(value time.Time, timezone string) string {
			return formatThemeTime(value, themeLocation(timezone), "2006-01-02 15:04:05")
		},
		"timeIn": func(value time.Time, timezone string) string {
			return formatThemeTime(value, themeLocation(timezone), "15:04")
		},
		"safeHTML": func(value string) template.HTML {
			return template.HTML(contenthtml.Sanitize(value))
		},
		"responsiveHTML": func(value string, images map[string]contenthtml.ResponsiveImage) template.HTML {
			return template.HTML(contenthtml.WithResponsiveImages(value, images))
		},
	}
}

// templateDictionary passes named properties to shared template components.
// Values remain ordinary template data and are escaped by html/template.
func templateDictionary(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict requires key/value pairs")
	}
	values := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok || key == "" {
			return nil, fmt.Errorf("dict keys must be non-empty strings")
		}
		values[key] = pairs[i+1]
	}
	return values, nil
}

// themeLocation resolves an IANA zone for template helpers, falling back to UTC.
// Local is a host setting, not a site timezone.
func themeLocation(timezone string) *time.Location {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || timezone == "Local" {
		return time.UTC
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return location
}

func formatThemeTime(value time.Time, location *time.Location, layout string) string {
	if value.IsZero() {
		return ""
	}
	return value.In(location).Format(layout)
}

// parseVerifiedTemplates verifies each distinct file once per load and parses
// its captured bytes into the same named templates ParseFiles would create.
// sources belongs to one compileTemplates call, never to a persistent cache.
func parseVerifiedTemplates(files []string, functions template.FuncMap, sources map[string]string) (*template.Template, error) {
	var parsed *template.Template
	for _, filePath := range files {
		contents, captured := sources[filePath]
		if !captured {
			file, _, err := packagefs.OpenRegularNoSymlink(filePath)
			if err != nil {
				return nil, err
			}
			raw, readErr := io.ReadAll(file)
			closeErr := file.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			contents = string(raw)
			sources[filePath] = contents
		}
		name := filepath.Base(filePath)
		if parsed == nil {
			parsed = template.New(name).Funcs(functions)
		} else {
			parsed = parsed.New(name)
		}
		if _, err := parsed.Parse(contents); err != nil {
			return nil, err
		}
	}
	return parsed, nil
}
