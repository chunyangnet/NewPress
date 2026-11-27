package plugin

import (
	"fmt"
	"slices"
	"sort"
)

// SettingsRegisterHook is a host-side UI registration point, not a WASM event.
// Rendering navigation or forms never executes plugin code or exposes config values.
const SettingsRegisterHook = "admin.plugin_settings.register"

type SettingsRegistration struct {
	Schema   int               `json:"schema"`
	Sections []SettingsSection `json:"sections"`
}

type SettingsSection struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Order  int      `json:"order,omitempty"`
	Fields []string `json:"fields"`
}

type SettingsGroup struct {
	ID, Title, Icon string
	Fields          []Setting
}

type SettingsPage struct {
	Slug, Title, URL, Adapter string
	Groups                    []SettingsGroup
}

// SettingsRegistry is rebuilt from installed, validated manifests for each read.
// Disabled plugins remain configurable; removal cannot leave a stale registration.
// The host binds the slug, route, renderer and persistence adapter.
type SettingsRegistry struct{ pages map[string]SettingsPage }

func NewSettingsRegistry() *SettingsRegistry {
	return &SettingsRegistry{pages: make(map[string]SettingsPage)}
}

func (r *SettingsRegistry) Register(manifest Manifest) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if _, exists := r.pages[manifest.Slug]; exists {
		return fmt.Errorf("duplicate plugin settings registration: %s", manifest.Slug)
	}
	page, err := SettingsPageFor(manifest)
	if err != nil {
		return err
	}
	if r.pages == nil {
		r.pages = make(map[string]SettingsPage)
	}
	r.pages[manifest.Slug] = page
	return nil
}

func (r *SettingsRegistry) Pages() []SettingsPage {
	pages := make([]SettingsPage, 0, len(r.pages))
	for _, page := range r.pages {
		copy := page
		copy.Groups = slices.Clone(page.Groups)
		for i := range copy.Groups {
			copy.Groups[i].Fields = slices.Clone(copy.Groups[i].Fields)
		}
		pages = append(pages, copy)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	return pages
}

func validateSettingsRegistration(m Manifest) error {
	if m.Admin == nil || m.Admin.Settings == nil {
		return nil
	}
	ui := m.Admin.Settings
	if ui.Schema != 1 || len(ui.Sections) > 16 {
		return fmt.Errorf("invalid plugin settings UI schema")
	}
	fields, sections, used := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range m.Settings {
		fields[f.Key] = true
	}
	for _, section := range ui.Sections {
		if !adminIconPattern.MatchString(section.ID) || sections[section.ID] || section.ID == "permissions" || section.ID == "records" || !validManifestText(section.Title, 80, true) || len(section.Fields) == 0 || len(section.Fields) > 32 {
			return fmt.Errorf("invalid plugin settings section")
		}
		sections[section.ID] = true
		for _, key := range section.Fields {
			if !fields[key] || used[key] {
				return fmt.Errorf("unknown or repeated plugin setting: %s", key)
			}
			used[key] = true
		}
	}
	return nil
}

func SettingsPageFor(m Manifest) (SettingsPage, error) {
	if err := validateSettingsRegistration(m); err != nil {
		return SettingsPage{}, err
	}
	page := SettingsPage{Slug: m.Slug, Title: m.Name, URL: "/admin/plugins/" + m.Slug + "/", Adapter: "configuration"}
	if isBuiltin(m) && m.Slug == CDN {
		page.Adapter = CDN
		page.Groups = []SettingsGroup{{ID: "connections", Title: "服务连接", Icon: "globe-2"}, {ID: "policy", Title: "加速与联动", Icon: "sliders-horizontal"}, {ID: "operations", Title: "缓存任务", Icon: "refresh-cw"}, {ID: "routing", Title: "接入与分流", Icon: "network"}, {ID: "optimizer", Title: "Cloudflare 优选", Icon: "activity"}, {ID: "records", Title: "运行记录", Icon: "database"}}
		return page, nil
	}
	if isBuiltin(m) && m.Slug == ObjectStorage {
		page.Adapter = ObjectStorage
		page.Groups = []SettingsGroup{{ID: "connections", Title: "存储连接", Icon: "database"}, {ID: "policy", Title: "同步与访问", Icon: "sliders-horizontal"}, {ID: "operations", Title: "同步任务", Icon: "refresh-cw"}, {ID: "records", Title: "运行记录", Icon: "activity"}}
		return page, nil
	}
	if isBuiltin(m) && m.Slug == AIProMax {
		page.Adapter = AIProMax
		page.Groups = []SettingsGroup{{ID: "workspace", Title: "AI 工作台", Icon: "sparkles"}, {ID: "coverage", Title: "功能覆盖", Icon: "scan-search"}}
		return page, nil
	}
	if isBuiltin(m) {
		page.Adapter = "comment-origin"
		page.Groups = []SettingsGroup{
			{ID: "display", Title: "展示内容", Icon: "message-circle"},
			{ID: "roles", Title: "作者显示规则", Icon: "users"},
			{ID: "provider", Title: "归属地服务", Icon: "globe-2"},
			{ID: "retention", Title: "记录保留", Icon: "database"},
			{ID: "diagnostics", Title: "预览与运行记录", Icon: "activity"},
		}
		return page, nil
	}
	byKey := map[string]Setting{}
	used := map[string]bool{}
	for _, f := range m.Settings {
		byKey[f.Key] = f
	}
	if m.Admin != nil && m.Admin.Settings != nil {
		sections := slices.Clone(m.Admin.Settings.Sections)
		sort.SliceStable(sections, func(i, j int) bool { return sections[i].Order < sections[j].Order })
		for _, section := range sections {
			group := SettingsGroup{ID: section.ID, Title: section.Title, Icon: "sliders-horizontal"}
			for _, key := range section.Fields {
				group.Fields = append(group.Fields, byKey[key])
				used[key] = true
			}
			page.Groups = append(page.Groups, group)
		}
	}
	remaining := []Setting{}
	for _, f := range m.Settings {
		if !used[f.Key] {
			remaining = append(remaining, f)
		}
	}
	if len(remaining) > 0 {
		general := -1
		for i, group := range page.Groups {
			if group.ID == "general" {
				general = i
			}
		}
		if general >= 0 {
			page.Groups[general].Fields = append(page.Groups[general].Fields, remaining...)
		} else {
			page.Groups = append(page.Groups, SettingsGroup{ID: "general", Title: "常规", Icon: "sliders-horizontal", Fields: remaining})
		}
	}
	return page, nil
}

func (m *Manager) SettingsPages() []SettingsPage {
	items, err := m.List()
	if err != nil {
		return nil
	}
	registry := NewSettingsRegistry()
	for _, item := range items {
		if item.Error != "" || !item.Official {
			continue
		}
		if !isBuiltin(item.Manifest) && (item.Manifest.Runtime != "wasm/core-v1" || len(item.Manifest.Hooks) == 0 || (len(item.Manifest.Settings) == 0 && len(item.Manifest.Capabilities) == 0)) {
			continue
		}
		_ = registry.Register(item.Manifest)
	}
	return registry.Pages()
}
