package theme

import (
	"fmt"
	"path/filepath"

	"newpress.local/newpress/internal/version"
)

type DependencyStatus struct {
	Parent           string   `json:"parent,omitempty"`
	Requires         string   `json:"requires,omitempty"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	Error            string   `json:"error,omitempty"`
	Dependents       []string `json:"dependents,omitempty"`
}

// DependencyStatus is a cheap metadata check for admin cards. Lifecycle methods
// always repeat authoritative composition validation under the operation lock.
func (m *Manager) DependencyStatus(info Manifest) DependencyStatus {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	status := DependencyStatus{}
	if info.Parent != nil {
		status.Parent, status.Requires = info.Parent.Slug, info.Parent.Version
		parent, err := readManifest(filepath.Join(m.root, info.Parent.Slug, "theme.json"))
		if err != nil {
			status.Error = "请先安装父主题 " + info.Parent.Slug
			return status
		}
		status.InstalledVersion = parent.Version
		if validateManifest(parent) != nil || parent.Slug != info.Parent.Slug || parent.Parent != nil {
			status.Error = "父主题不可用或使用了多层继承"
			return status
		}
		ok, err := version.Satisfies(parent.Version, info.Parent.Version)
		if err != nil || !ok {
			status.Error = fmt.Sprintf("父主题 %s %s 不满足 %s", parent.Slug, parent.Version, info.Parent.Version)
		}
		if parent.View != info.View || parent.Engine != info.Engine {
			status.Error = "父子主题展示契约或引擎不一致"
		}
	}
	dependents, err := m.dependents(info.Slug)
	if err != nil {
		status.Error = err.Error()
	} else {
		status.Dependents = dependents
	}
	return status
}
