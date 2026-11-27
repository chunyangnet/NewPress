package theme

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"slices"
)

const PublicViewV1 = "public/v1"
const ChunnelViewV1 = "chunnel/v1"
const HomeLayoutV1 = "home-layout/v1"

func cloneManifest(info Manifest) Manifest {
	if info.Parent != nil {
		parent := *info.Parent
		info.Parent = &parent
	}
	info.Templates = maps.Clone(info.Templates)
	info.Capabilities = slices.Clone(info.Capabilities)
	return info
}

func (m Manifest) Supports(capability string) bool {
	return slices.Contains(m.Capabilities, capability)
}

func (m *Manager) HasTemplate(page string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.templates[page] != nil
}

// RenderBuffer bounds memory before an HTTP status/body can be committed.
// It deliberately reports short writes as errors to html/template.
type RenderBuffer struct{ bytes.Buffer }

const MaxRenderBytes = 8 << 20

var ErrRenderLimit = errors.New("rendered page exceeds 8 MiB")

func (b *RenderBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxRenderBytes-b.Len() {
		return 0, ErrRenderLimit
	}
	return b.Buffer.Write(p)
}
func (b *RenderBuffer) WriteString(s string) (int, error) { return b.Write([]byte(s)) }

var _ io.Writer = (*RenderBuffer)(nil)
