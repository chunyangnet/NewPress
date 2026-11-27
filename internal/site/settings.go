// Package site owns the runtime-editable, non-secret settings for a NewPress
// installation. Security-sensitive bootstrap values (public URL, credentials,
// storage paths and network listeners) deliberately remain environment-owned.
package site

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"newpress.local/newpress/internal/module"
)

const (
	documentSchema   = 1
	documentKind     = "newpress/site-settings"
	maxDocumentBytes = 256 << 10
)

var (
	ErrRevisionRequired = errors.New("site settings revision is required")
	ErrRevisionConflict = errors.New("site settings revision conflict")
	ErrInvalidForm      = errors.New("site settings form shape is invalid")

	localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
)

type Settings struct {
	Modules      map[string]bool       `json:"modules,omitempty"`
	Identity     IdentitySettings      `json:"identity"`
	Writing      WritingSettings       `json:"writing"`
	Discussion   DiscussionSettings    `json:"discussion"`
	Reading      ReadingSettings       `json:"reading"`
	API          APISettings           `json:"api"`
	SEO          SEOSettings           `json:"seo"`
	Operations   OperationsSettings    `json:"operations"`
	Accounts     *AccountSettings      `json:"accounts,omitempty"`
	Integrations *IntegrationsSettings `json:"integrations"`
}

type AccountSettings struct {
	Registration string              `json:"registration"`
	Policy       *RegistrationPolicy `json:"policy,omitempty"`
}

func (s Settings) AccountRegistration() string {
	if s.Accounts == nil {
		return "closed"
	}
	return s.Accounts.Registration
}

type IdentitySettings struct {
	Title              string `json:"title"`
	Tagline            string `json:"tagline"`
	Description        string `json:"description"`
	Locale             string `json:"locale"`
	Timezone           string `json:"timezone"`
	TitleSeparator     string `json:"title_separator"`
	IconURL            string `json:"icon_url"`
	SocialDefaultImage string `json:"social_default_image"`
}

type WritingSettings struct {
	MaxUploadMiB            int    `json:"max_upload_mib,omitempty"`
	PostPermalink           string `json:"post_permalink,omitempty"`
	PagePermalink           string `json:"page_permalink,omitempty"`
	SlugMode                string `json:"slug_mode,omitempty"`
	DefaultStatus           string `json:"default_status"`
	DefaultCategory         string `json:"default_category"`
	PostCommentsEnabled     bool   `json:"post_comments_enabled"`
	PageCommentsEnabled     bool   `json:"page_comments_enabled"`
	AutosaveEnabled         bool   `json:"autosave_enabled"`
	AutosaveIntervalSeconds int    `json:"autosave_interval_seconds"`
	AutosaveRetentionHours  int    `json:"autosave_retention_hours"`
	PreviewTokenMinutes     int    `json:"preview_token_minutes"`
	BrowserRecoveryEnabled  bool   `json:"browser_recovery_enabled"`
}

type DiscussionSettings struct {
	RequireLogin             bool   `json:"require_login"`
	PublicSubmissionsEnabled bool   `json:"public_submissions_enabled"`
	RequireModeration        bool   `json:"require_moderation"`
	AllowReplies             bool   `json:"allow_replies"`
	MaxLength                int    `json:"max_length"`
	CommentsPerPage          int    `json:"comments_per_page"`
	RateLimitPerTenMinutes   int    `json:"rate_limit_per_ten_minutes"`
	AutoCloseDays            int    `json:"auto_close_days"`
	ModerationNotice         string `json:"moderation_notice"`
	PrivacyNotice            string `json:"privacy_notice"`
}

type ReadingSettings struct {
	HomePosts     int `json:"home_posts"`
	ExplorePosts  int `json:"explore_posts"`
	AdminPageSize int `json:"admin_page_size"`
	RevisionLimit int `json:"revision_limit"`
}

type APISettings struct {
	PublicPostsEnabled  bool `json:"public_posts_enabled"`
	PublicDefaultLimit  int  `json:"public_default_limit"`
	PublicIncludeBody   bool `json:"public_include_body"`
	AdminContentEnabled bool `json:"admin_content_enabled"`
}

type SEOSettings struct {
	SearchEngineVisible bool   `json:"search_engine_visible"`
	SitemapEnabled      bool   `json:"sitemap_enabled"`
	RobotsText          string `json:"robots_text"`
}

type OperationsSettings struct {
	MaintenanceEnabled bool   `json:"maintenance_enabled"`
	MaintenanceMessage string `json:"maintenance_message"`
	PublicCacheSeconds int    `json:"public_cache_seconds"`
	GeneratorMeta      bool   `json:"generator_meta"`
	SecurityContact    string `json:"security_contact"`
}

type SettingOption struct {
	Value       string
	Label       string
	Description string
}

type SettingField struct {
	Key         string
	Group       string
	Title       string
	Description string
	Control     string
	Placeholder string
	Unit        string
	InputMode   string
	Required    bool
	Critical    bool
	StringValue string
	BoolValue   bool
	Min         string
	Max         string
	Step        string
	MaxLength   int
	Options     []SettingOption
}

type SettingGroup struct {
	ID          string
	Title       string
	Description string
	Icon        string
	Fields      []SettingField
}

type SettingsSnapshot struct {
	Revision   string         `json:"revision"`
	UpdatedAt  time.Time      `json:"updated_at,omitempty"`
	Settings   Settings       `json:"settings"`
	Groups     []SettingGroup `json:"-"`
	FieldCount int            `json:"-"`
}

type Manager struct {
	mu           sync.RWMutex
	moduleLeases map[string]moduleLease
	saveGuard    func(Settings) (func(), map[string]string, error)
	repository   DocumentRepository
	path         string
	settings     Settings
	updatedAt    time.Time
}

type moduleLease struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// ModuleContext is cancelled on the first committed settings change that disables id.
// Re-enabling creates a new lease, so an interrupted operation cannot resume silently.
func (manager *Manager) ModuleContext(id string) (context.Context, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !module.Allowed(manager.settings.Modules, id) {
		return nil, false
	}
	if lease, ok := manager.moduleLeases[id]; ok {
		return lease.ctx, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	if manager.moduleLeases == nil {
		manager.moduleLeases = make(map[string]moduleLease)
	}
	manager.moduleLeases[id] = moduleLease{ctx, cancel}
	return ctx, true
}

func DefaultSettings(title, tagline string) Settings {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "NewPress"
	}
	tagline = strings.TrimSpace(tagline)
	if tagline == "" {
		tagline = "AI 原生 · 高性能建站引擎"
	}
	return Settings{
		Identity: IdentitySettings{
			Title: title, Tagline: tagline, Description: tagline,
			Locale: "zh-CN", Timezone: "Asia/Shanghai", TitleSeparator: "·",
		},
		Writing: WritingSettings{
			DefaultStatus: "draft", DefaultCategory: "未分类",
			PostCommentsEnabled: true, PageCommentsEnabled: false,
			AutosaveEnabled: true, AutosaveIntervalSeconds: 30,
			AutosaveRetentionHours: 72, PreviewTokenMinutes: 10,
		},
		Discussion: defaultDiscussionSettings(),
		Reading: ReadingSettings{
			HomePosts: 24, ExplorePosts: 20, AdminPageSize: 20, RevisionLimit: 20,
		},
		API: APISettings{
			PublicPostsEnabled: true, PublicDefaultLimit: 20,
			PublicIncludeBody: true, AdminContentEnabled: true,
		},
		SEO: SEOSettings{
			SearchEngineVisible: true, SitemapEnabled: true,
			RobotsText: "User-agent: *\nAllow: /",
		},
		Operations: OperationsSettings{
			MaintenanceMessage: "站点正在进行计划维护，请稍后再来。",
			PublicCacheSeconds: 0, GeneratorMeta: false,
		},
		Integrations: &IntegrationsSettings{},
	}
}

func defaultDiscussionSettings() DiscussionSettings {
	return DiscussionSettings{
		PublicSubmissionsEnabled: true,
		RequireModeration:        true,
		AllowReplies:             true,
		MaxLength:                2000,
		CommentsPerPage:          50,
		RateLimitPerTenMinutes:   5,
		AutoCloseDays:            0,
		ModerationNotice:         "评论已进入审核队列，通过后会公开显示。",
		PrivacyNotice:            "邮箱仅用于反滥用与必要联系，不会公开显示。",
	}
}

// NewManager uses bootstrap values only when no saved document exists. The app
// holds the data-directory lock while assembling this manager.
func NewManager(path string, bootstrap Settings) (*Manager, error) {
	manager := &Manager{path: path}
	if path != "" {
		loaded, updatedAt, err := loadDocument(path)
		if err == nil {
			manager.settings, manager.updatedAt = loaded, updatedAt
			return manager, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	normalized, fields := Validate(bootstrap)
	if len(fields) > 0 {
		return nil, fmt.Errorf("invalid initial site settings: %v", fields)
	}
	manager.settings = normalized
	if path != "" {
		manager.updatedAt = time.Now().UTC().Truncate(time.Second)
		if err := writeDocument(path, document{
			Schema: documentSchema, Kind: documentKind, Revision: settingsRevision(normalized),
			UpdatedAt: manager.updatedAt, Settings: normalized,
		}); err != nil {
			return nil, fmt.Errorf("persist initial site settings: %w", err)
		}
	}
	return manager, nil
}

func (manager *Manager) ModuleEnabled(id string) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return module.Allowed(manager.settings.Modules, id)
}

func (manager *Manager) Snapshot() SettingsSnapshot {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return snapshot(manager.settings, manager.updatedAt)
}

func (manager *Manager) Preview(settings Settings) SettingsSnapshot {
	manager.mu.RLock()
	updatedAt := manager.updatedAt
	currentRevision := settingsRevision(manager.settings)
	manager.mu.RUnlock()
	preview := snapshot(settings, updatedAt)
	preview.Revision = currentRevision
	return preview
}

func (manager *Manager) Save(expectedRevision string, settings Settings) (SettingsSnapshot, map[string]string, error) {
	normalized, fields := Validate(settings)
	if len(fields) > 0 {
		return manager.Preview(settings), fields, nil
	}
	return manager.saveValidated(expectedRevision, normalized)
}

func (manager *Manager) Reset(expectedRevision string) (SettingsSnapshot, error) {
	snapshot, fields, err := manager.saveValidated(expectedRevision, DefaultSettings("", ""))
	if err == nil && len(fields) > 0 {
		err = errors.New("default site settings reference unavailable resources")
	}
	return snapshot, err
}

func (manager *Manager) Import(reader io.Reader, expectedRevision string) (SettingsSnapshot, map[string]string, error) {
	doc, err := decodeDocument(reader)
	if err != nil {
		return manager.Snapshot(), nil, fmt.Errorf("decode site settings: %w", err)
	}
	if doc.Schema != documentSchema || doc.Kind != documentKind {
		return manager.Snapshot(), nil, errors.New("site settings metadata is not supported")
	}
	normalized, fields, revisionMatches := normalizeDocumentSettings(doc)
	if len(fields) > 0 {
		return manager.Preview(doc.Settings), fields, errors.New("imported site settings failed validation")
	}
	if doc.Revision == "" || !revisionMatches {
		return manager.Snapshot(), nil, errors.New("imported site settings revision does not match its values")
	}
	return manager.saveValidated(expectedRevision, normalized)
}

func (manager *Manager) Export(writer io.Writer) error {
	snapshot := manager.Snapshot()
	doc := document{
		Schema: documentSchema, Kind: documentKind, Revision: snapshot.Revision,
		UpdatedAt: snapshot.UpdatedAt, Settings: snapshot.Settings,
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}

func (manager *Manager) saveValidated(expectedRevision string, settings Settings) (SettingsSnapshot, map[string]string, error) {
	if strings.TrimSpace(expectedRevision) == "" {
		return manager.Snapshot(), nil, ErrRevisionRequired
	}
	manager.mu.RLock()
	guard := manager.saveGuard
	manager.mu.RUnlock()
	if guard != nil {
		release, fields, err := guard(settings)
		if release != nil {
			defer release()
		}
		if err != nil || len(fields) > 0 {
			return manager.Preview(settings), fields, err
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	currentRevision := settingsRevision(manager.settings)
	if expectedRevision != currentRevision {
		return snapshot(manager.settings, manager.updatedAt), nil, ErrRevisionConflict
	}
	if settingsRevision(settings) == currentRevision {
		return snapshot(manager.settings, manager.updatedAt), nil, nil
	}
	updatedAt := time.Now().UTC().Truncate(time.Second)
	doc := document{
		Schema: documentSchema, Kind: documentKind, Revision: settingsRevision(settings),
		UpdatedAt: updatedAt, Settings: settings,
	}
	if manager.path != "" {
		if err := writeDocument(manager.path, doc); err != nil {
			return snapshot(manager.settings, manager.updatedAt), nil, err
		}
	}
	if manager.repository != nil {
		payload, err := encodeDocument(doc)
		if err != nil {
			return snapshot(manager.settings, manager.updatedAt), nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		updated, saveErr := manager.repository.CompareAndSwap(ctx, currentRevision, doc.Revision, payload)
		if saveErr != nil {
			cancel()
			return snapshot(manager.settings, manager.updatedAt), nil, fmt.Errorf("persist site settings: %w", saveErr)
		}
		if !updated {
			latest, found, loadErr := manager.repository.Load(ctx)
			if loadErr != nil {
				cancel()
				return snapshot(manager.settings, manager.updatedAt), nil, fmt.Errorf("reload site settings after revision conflict: %w", loadErr)
			}
			if !found {
				cancel()
				return snapshot(manager.settings, manager.updatedAt), nil, errors.New("persistent site settings disappeared during update")
			}
			current, savedAt, decodeErr := decodeStoredDocument(latest)
			if decodeErr != nil {
				cancel()
				return snapshot(manager.settings, manager.updatedAt), nil, fmt.Errorf("validate site settings after revision conflict: %w", decodeErr)
			}
			manager.settings, manager.updatedAt = current, savedAt
			cancel()
			return snapshot(manager.settings, manager.updatedAt), nil, ErrRevisionConflict
		}
		cancel()
	}
	manager.settings = settings
	for id, lease := range manager.moduleLeases {
		if !module.Allowed(settings.Modules, id) {
			lease.cancel()
			delete(manager.moduleLeases, id)
		}
	}
	manager.updatedAt = updatedAt
	return snapshot(manager.settings, manager.updatedAt), nil, nil
}

// SetSaveGuard attaches application-level resource validation during assembly.
// The returned lease spans validation through persistence, before the settings
// mutex is acquired. Save, import and reset all use this boundary.
func (manager *Manager) SetSaveGuard(guard func(Settings) (func(), map[string]string, error)) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.saveGuard = guard
}

func TimeLocation(settings Settings) *time.Location {
	zone := strings.TrimSpace(settings.Identity.Timezone)
	if zone == "" || zone == "Local" {
		return time.UTC
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return location
}

func snapshot(settings Settings, updatedAt time.Time) SettingsSnapshot {
	settings.Modules = maps.Clone(settings.Modules)
	if settings.Integrations != nil {
		value := *settings.Integrations
		settings.Integrations = &value
	}
	if settings.Accounts != nil {
		value := *settings.Accounts
		if value.Policy != nil {
			p := settings.RegistrationPolicy()
			value.Policy = &p
		}
		settings.Accounts = &value
	}
	groups := settingGroups(settings)
	fieldCount := 0
	for _, group := range groups {
		fieldCount += len(group.Fields)
	}
	return SettingsSnapshot{
		Revision: settingsRevision(settings), UpdatedAt: updatedAt,
		Settings: settings, Groups: groups, FieldCount: fieldCount,
	}
}
