package theme

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

// Inspect each selected source before combining it. Explicit definitions in
// different selected files must not silently replace each other.
func parseInheritedTemplates(files []string, sources map[string]string) (*template.Template, error) {
	definitions := map[string]string{}
	for _, name := range files {
		source, ok := sources[name]
		if !ok {
			f, _, err := packagefs.OpenRegularNoSymlink(name)
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(io.LimitReader(f, packagefs.DefaultLimits().MaxFileBytes+1))
			f.Close()
			if err != nil {
				return nil, err
			}
			if int64(len(raw)) > packagefs.DefaultLimits().MaxFileBytes {
				return nil, fmt.Errorf("template exceeds size limit")
			}
			source = string(raw)
			sources[name] = source
		}
		parsed, err := template.New(filepath.Base(name)).Funcs(Functions()).Parse(source)
		if err != nil {
			return nil, err
		}
		for _, definition := range parsed.Templates() {
			if definition.Tree == nil || strings.TrimSpace(definition.Tree.Root.String()) == "" {
				continue
			}
			key := definition.Name()
			if previous, exists := definitions[key]; exists && previous != name {
				return nil, fmt.Errorf("template definition %q conflicts in %s and %s", key, previous, name)
			}
			definitions[key] = name
		}
	}
	return parseVerifiedTemplates(files, Functions(), sources)
}
