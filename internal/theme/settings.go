package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"time"
)

const (
	settingsDocumentSchema   = 1
	maxSettingsDocumentBytes = 256 << 10
)

var (
	settingKeyPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)
	cssVariablePattern = regexp.MustCompile(`^--[a-z][a-z0-9-]{0,63}$`)
	cssClassPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)
)

type ManagerOption func(*Manager)

func WithSettingsDir(directory string) ManagerOption {
	return func(manager *Manager) {
		manager.settingsDir = directory
	}
}

type SettingOption struct {
	Value       string
	Label       string
	Description string
}

type SettingField struct {
	Source      string
	DefaultHint string
	Key         string
	Group       string
	Title       string
	Description string
	Control     string
	Placeholder string
	Unit        string
	Required    bool
	StringValue string
	BoolValue   bool
	Min         string
	Max         string
	Step        string
	Options     []SettingOption
}

type SettingGroup struct {
	ID          string
	Title       string
	Description string
	Icon        string
	Fields      []SettingField
}

type SettingsParent struct {
	Slug     string
	Name     string
	Version  string
	Requires string
}

type SettingsSnapshot struct {
	ThemeName    string                 `json:"-"`
	Parent       *SettingsParent        `json:"-"`
	Theme        string                 `json:"theme"`
	ThemeVersion string                 `json:"theme_version"`
	Revision     string                 `json:"revision"`
	UpdatedAt    time.Time              `json:"updated_at,omitempty"`
	Values       map[string]any         `json:"values"`
	Groups       []SettingGroup         `json:"-"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

type settingsDocument struct {
	Schema       int            `json:"schema"`
	Theme        string         `json:"theme"`
	ThemeVersion string         `json:"theme_version"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Values       map[string]any `json:"values"`
}

type settingSchema struct {
	Parent               *SettingsParent            `json:"-"`
	Sources              map[string]string          `json:"-"`
	HomeLayout           bool                       `json:"-"`
	JSONSchema           string                     `json:"$schema"`
	Title                string                     `json:"title"`
	Description          string                     `json:"description"`
	Type                 string                     `json:"type"`
	AdditionalProperties bool                       `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]settingProperty `json:"properties"`
	UI                   schemaUI                   `json:"x-newpress"`
}

type schemaUI struct {
	Version int         `json:"version"`
	Groups  []groupSpec `json:"groups"`
}

type groupSpec struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Order       int    `json:"order"`
}

type settingProperty struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Default     any             `json:"default"`
	Enum        []any           `json:"enum"`
	OneOf       []settingChoice `json:"oneOf"`
	Minimum     *float64        `json:"minimum"`
	Maximum     *float64        `json:"maximum"`
	MinLength   *int            `json:"minLength"`
	MaxLength   *int            `json:"maxLength"`
	Pattern     string          `json:"pattern"`
	Format      string          `json:"format"`
	UI          fieldUI         `json:"x-newpress"`
}

type settingChoice struct {
	Const       any    `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type fieldUI struct {
	Hidden         bool              `json:"hidden"`
	Group          string            `json:"group"`
	Control        string            `json:"control"`
	Placeholder    string            `json:"placeholder"`
	Unit           string            `json:"unit"`
	Order          int               `json:"order"`
	Step           float64           `json:"step"`
	CSSVariable    string            `json:"css_variable"`
	CSSUnit        string            `json:"css_unit"`
	CSSMap         map[string]string `json:"css_map"`
	ClassPrefix    string            `json:"class_prefix"`
	ClassWhenTrue  string            `json:"class_when_true"`
	ClassWhenFalse string            `json:"class_when_false"`
}

func (m *Manager) SettingsSnapshot() SettingsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	values := cloneSettings(m.settings)
	snapshot := m.settingsSnapshot(values)
	snapshot.UpdatedAt = m.settingsUpdatedAt
	return snapshot
}

// settingsSnapshot projects the already-published schema; caller holds mu.
func (m *Manager) settingsSnapshot(values map[string]any) SettingsSnapshot {
	snapshot := SettingsSnapshot{
		Theme: m.info.Slug, ThemeName: m.info.Name, ThemeVersion: m.info.Version,
		Revision: settingsRevision(m.info, values), Values: values,
		Groups: m.settingsSchema.groups(values),
	}
	if m.settingsSchema.Parent != nil {
		parent := *m.settingsSchema.Parent
		snapshot.Parent = &parent
	}
	return snapshot
}

func (m *Manager) ParseSettingsForm(form url.Values) (map[string]any, map[string]string) {
	m.mu.RLock()
	schema := m.settingsSchema
	m.mu.RUnlock()
	if schema == nil {
		return map[string]any{}, nil
	}
	return parseSettingsForm(schema, form)
}

var ErrSettingsRevisionConflict = errors.New("theme settings revision conflict")

func (m *Manager) SaveSettings(values map[string]any) (SettingsSnapshot, map[string]string, error) {
	return m.saveSettings("", values)
}

func (m *Manager) SaveSettingsRevision(revision string, values map[string]any) (SettingsSnapshot, map[string]string, error) {
	if revision == "" {
		return m.SettingsSnapshot(), nil, ErrSettingsRevisionConflict
	}
	return m.saveSettings(revision, values)
}

func (m *Manager) saveSettings(revision string, values map[string]any) (SettingsSnapshot, map[string]string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	schema := m.settingsSchema
	manifest := m.info
	directory := m.settingsDir
	repository := m.settingsRepository
	storeVersion := m.settingsStoreVersion
	guard := m.saveGuard
	currentRevision := settingsRevision(m.info, m.settings)
	m.mu.RUnlock()
	if revision != "" && revision != currentRevision {
		return m.SettingsSnapshot(), nil, ErrSettingsRevisionConflict
	}
	if schema == nil {
		return SettingsSnapshot{}, nil, errors.New("active theme has no settings schema")
	}
	if directory == "" && repository == nil {
		return SettingsSnapshot{}, nil, errors.New("theme settings storage is not configured")
	}
	normalized, errorsByField := schema.validate(values)
	if len(errorsByField) > 0 {
		return SettingsSnapshot{}, errorsByField, nil
	}
	if guard != nil {
		release, fields, err := guard(normalized)
		if release != nil {
			defer release()
		}
		if err != nil || len(fields) > 0 {
			return SettingsSnapshot{}, fields, err
		}
	}
	now := time.Now().UTC()
	document := settingsDocument{
		Schema: settingsDocumentSchema, Theme: manifest.Slug, ThemeVersion: manifest.Version,
		UpdatedAt: now, Values: normalized,
	}
	if repository != nil {
		if err := m.persistSettings(document, storeVersion, manifest, schema); err != nil {
			return m.SettingsSnapshot(), nil, err
		}
	} else {
		if err := writeSettingsDocument(directory, document); err != nil {
			return SettingsSnapshot{}, nil, err
		}
	}
	m.mu.Lock()
	m.settings = normalized
	m.settingsUpdatedAt = now
	if repository != nil {
		m.settingsStoreVersion = storeVersion + 1
	}
	m.mu.Unlock()
	return m.SettingsSnapshot(), nil, nil
}

// SetSaveGuard installs a resource lease that spans each settings mutation.
// It is configured during assembly; import and reset also pass through it.
func (m *Manager) SetSaveGuard(guard func(map[string]any) (func(), map[string]string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveGuard = guard
}

func (m *Manager) ResetSettings() (SettingsSnapshot, error) {
	return m.ResetSettingsRevision("")
}

func (m *Manager) ResetSettingsRevision(revision string) (SettingsSnapshot, error) {
	m.mu.RLock()
	schema := m.settingsSchema
	currentRevision := settingsRevision(m.info, m.settings)
	m.mu.RUnlock()
	if revision == "" {
		revision = currentRevision
	}
	if schema == nil {
		return SettingsSnapshot{}, errors.New("active theme has no settings schema")
	}
	snapshot, fields, err := m.SaveSettingsRevision(revision, schema.defaults())
	if err == nil && len(fields) > 0 {
		err = errors.New("default theme settings reference unavailable resources")
	}
	return snapshot, err
}

func (m *Manager) ImportSettings(reader io.Reader) (SettingsSnapshot, map[string]string, error) {
	return m.ImportSettingsRevision(reader, "")
}

func (m *Manager) ImportSettingsRevision(reader io.Reader, revision string) (SettingsSnapshot, map[string]string, error) {
	var document settingsDocument
	if err := decodeJSONDocument(reader, maxSettingsDocumentBytes, &document, true); err != nil {
		return SettingsSnapshot{}, nil, fmt.Errorf("decode imported theme settings: %w", err)
	}
	m.mu.RLock()
	active := m.info.Slug
	currentRevision := settingsRevision(m.info, m.settings)
	m.mu.RUnlock()
	if revision == "" {
		revision = currentRevision
	}
	if document.Schema != settingsDocumentSchema || document.Theme != active {
		return SettingsSnapshot{}, nil, errors.New("settings file does not belong to the active theme")
	}
	return m.SaveSettingsRevision(revision, document.Values)
}

func (m *Manager) ExportSettings(writer io.Writer) error {
	snapshot := m.SettingsSnapshot()
	document := settingsDocument{
		Schema: settingsDocumentSchema, Theme: snapshot.Theme, ThemeVersion: snapshot.ThemeVersion,
		UpdatedAt: snapshot.UpdatedAt, Values: snapshot.Values,
	}
	var formatted bytes.Buffer
	encoder := json.NewEncoder(&formatted)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return err
	}
	payload := formatted.Bytes()
	if len(payload) > maxSettingsDocumentBytes {
		// Repository-backed documents can fit when compact but exceed the
		// import limit after indentation. Always export an importable file.
		var err error
		payload, err = encodePersistentSettings(document)
		if err != nil {
			return err
		}
	}
	_, err := io.Copy(writer, bytes.NewReader(payload))
	return err
}
