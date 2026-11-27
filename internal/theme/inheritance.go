package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"newpress.local/newpress/internal/i18n"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

// ParentTheme is a local, single-level package dependency, not a filesystem path.
type ParentTheme struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}
type preparedTheme struct {
	info         Manifest
	templates    map[string]*template.Template
	schema       *settingSchema
	translations *i18n.Catalog
	assets       assetIndex
	owners       map[string]string
}

func prepareTheme(root, slug string, overrides map[string]string) (*preparedTheme, error) {
	directory := func(s string) string {
		if d := overrides[s]; d != "" {
			return d
		}
		return filepath.Join(root, s)
	}
	read := func(s string) (Manifest, error) {
		if err := packagefs.ValidateSlug(s); err != nil {
			return Manifest{}, err
		}
		d := directory(s)
		if st, err := packagefs.LstatNoSymlink(d); err != nil || !st.IsDir() {
			return Manifest{}, fmt.Errorf("theme %s directory unavailable", s)
		}
		m, err := readManifest(filepath.Join(d, "theme.json"))
		if err != nil {
			return m, err
		}
		if err = validateManifest(m); err != nil {
			return m, err
		}
		if m.Slug != s {
			return m, fmt.Errorf("theme manifest slug does not match active directory %s", s)
		}
		return m, nil
	}
	child, err := read(slug)
	if err != nil {
		return nil, err
	}
	p := &preparedTheme{info: cloneManifest(child)}
	if child.Parent == nil {
		p.schema, err = loadSettingSchema(directory(slug), child)
		if err != nil {
			return nil, err
		}
		p.templates, err = compileTemplates(directory(slug), child)
		if err != nil {
			return nil, err
		}
		p.translations, err = loadTranslations(directory(slug), child)
		if err != nil {
			return nil, err
		}
		p.assets, err = hashAssets(directory(slug))
		return p, err
	}
	parent, err := read(child.Parent.Slug)
	if err != nil {
		return nil, fmt.Errorf("parent %s: %w", child.Parent.Slug, err)
	}
	if parent.Parent != nil {
		return nil, fmt.Errorf("multi-level theme inheritance is not supported")
	}
	ok, err := version.Satisfies(parent.Version, child.Parent.Version)
	if err != nil || !ok {
		return nil, fmt.Errorf("parent %s %s does not satisfy %s", parent.Slug, parent.Version, child.Parent.Version)
	}
	if err := version.Compatible(parent.NewPress); err != nil {
		return nil, err
	}
	if child.Engine != parent.Engine || child.View != parent.View {
		return nil, fmt.Errorf("child engine and view must match parent")
	}
	if child.Capabilities != nil {
		a, b := slices.Clone(child.Capabilities), slices.Clone(parent.Capabilities)
		sort.Strings(a)
		sort.Strings(b)
		if !slices.Equal(a, b) {
			return nil, fmt.Errorf("child capabilities must match parent")
		}
	}
	p.info.Capabilities = slices.Clone(parent.Capabilities)
	if p.info.DefaultLocale == "" {
		p.info.DefaultLocale = parent.DefaultLocale
	}
	p.info.Templates = maps.Clone(parent.Templates)
	maps.Copy(p.info.Templates, child.Templates)
	if p.info.Templates["home"] == "" || p.info.Templates["single"] == "" {
		return nil, fmt.Errorf("effective theme requires home and single")
	}
	if len(p.info.Templates) > 128 {
		return nil, fmt.Errorf("effective theme exceeds template limit")
	}
	budget := &overlayBudget{}
	files, _, err := overlayFilesWithBudget([]string{parent.Slug, child.Slug}, directory, "templates", budget)
	if err != nil {
		return nil, err
	}
	base := files["base.html"]
	if base == "" {
		return nil, fmt.Errorf("effective base template missing")
	}
	partialNames := []string{}
	for rel := range files {
		if strings.HasPrefix(rel, "partials/") && strings.Count(rel, "/") == 1 && strings.HasSuffix(rel, ".html") {
			partialNames = append(partialNames, rel)
		}
	}
	sort.Strings(partialNames)
	partials := make([]string, 0, len(partialNames))
	for _, rel := range partialNames {
		partials = append(partials, files[rel])
	}
	p.templates = map[string]*template.Template{}
	sources := map[string]string{}
	for page, rel := range p.info.Templates {
		file := files[rel]
		if file == "" {
			return nil, fmt.Errorf("effective template %s missing: %s", page, rel)
		}
		paths := append([]string{base}, partials...)
		paths = append(paths, file)
		parsed, e := parseInheritedTemplates(paths, sources)
		if e != nil {
			return nil, fmt.Errorf("compile %s: %w", page, e)
		}
		p.templates[page] = parsed
	}
	p.schema, err = loadSettingSchema(directory(parent.Slug), parent)
	if err != nil {
		return nil, err
	}
	if child.SettingsSchema != "" {
		p.schema, err = overlaySettings(directory(child.Slug), child, p.schema)
		if err != nil {
			return nil, err
		}
	}
	p.schema.Parent = &SettingsParent{Slug: parent.Slug, Name: parent.Name, Version: parent.Version, Requires: child.Parent.Version}
	if p.schema.Sources == nil {
		p.schema.Sources = make(map[string]string, len(p.schema.Properties))
		for key := range p.schema.Properties {
			p.schema.Sources[key] = "继承父主题字段"
		}
	}
	p.schema.HomeLayout = p.info.Supports(HomeLayoutV1)
	p.translations, err = i18n.LoadOverlayDirectories(filepath.Join(directory(parent.Slug), "locales"), filepath.Join(directory(child.Slug), "locales"), p.info.DefaultLocale)
	if err != nil {
		return nil, err
	}
	files, p.owners, err = overlayFilesWithBudget([]string{parent.Slug, child.Slug}, directory, "assets", budget)
	if err != nil {
		return nil, err
	}
	p.assets, err = hashOverlayAssets(files)
	if err != nil {
		return nil, err
	}
	identity, _ := json.Marshal(struct {
		Parent Manifest
		Child  Manifest
		Schema *settingSchema
	}{parent, child, p.schema})
	digest := sha256.Sum256(identity)
	p.info.EffectiveRevision = hex.EncodeToString(digest[:])
	return p, nil
}

// overlayFiles validates both trees, including shadowed files and directory aliases.
type overlayBudget struct {
	count int
	total int64
}

func overlayFiles(slugs []string, directory func(string) string, subtree string) (map[string]string, map[string]string, error) {
	return overlayFilesWithBudget(slugs, directory, subtree, &overlayBudget{})
}

func overlayFilesWithBudget(slugs []string, directory func(string) string, subtree string, budget *overlayBudget) (map[string]string, map[string]string, error) {
	files, owners := map[string]string{}, map[string]string{}
	aliases, kinds := map[string]string{}, map[string]bool{}
	limits := packagefs.DefaultLimits()
	for _, slug := range slugs {
		root := filepath.Join(directory(slug), subtree)
		err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) && name == root {
				return nil
			}
			if err != nil {
				return err
			}
			st, e := packagefs.LstatNoSymlink(name)
			if e != nil {
				return e
			}
			if name == root {
				if !st.IsDir() {
					return fmt.Errorf("%s must be a directory", subtree)
				}
				return nil
			}
			rel, e := filepath.Rel(root, name)
			if e != nil {
				return e
			}
			rel = filepath.ToSlash(rel)
			if e = packagefs.ValidateRelativePath(rel); e != nil {
				return e
			}
			key := strings.ToLower(rel)
			if previous, ok := aliases[key]; ok && (previous != rel || kinds[key] != st.IsDir()) {
				return fmt.Errorf("conflicting inherited path %s", rel)
			}
			aliases[key] = rel
			kinds[key] = st.IsDir()
			if st.IsDir() {
				return nil
			}
			if !st.Mode().IsRegular() {
				return fmt.Errorf("nonregular inherited file %s", rel)
			}
			budget.count++
			budget.total += st.Size()
			if budget.count > limits.MaxFiles || st.Size() > limits.MaxFileBytes || budget.total > limits.MaxTotal {
				return fmt.Errorf("combined theme tree exceeds limits")
			}
			files[rel] = name
			owners[rel] = slug
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, owners, nil
}
func hashOverlayAssets(files map[string]string) (assetIndex, error) {
	result := assetIndex{files: map[string][sha256.Size]byte{}}
	h := sha256.New()
	keys := slices.Sorted(maps.Keys(files))
	for _, rel := range keys {
		f, st, err := packagefs.OpenRegularNoSymlink(files[rel])
		if err != nil {
			return result, err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, st.Size())
		fh := sha256.New()
		n, err := io.Copy(io.MultiWriter(h, fh), io.LimitReader(f, packagefs.DefaultLimits().MaxFileBytes+1))
		f.Close()
		if err != nil {
			return result, err
		}
		if n != st.Size() {
			return result, fmt.Errorf("asset changed while loading")
		}
		result.files[rel] = [sha256.Size]byte(fh.Sum(nil))
	}
	result.revision = hex.EncodeToString(h.Sum(nil))[:24]
	return result, nil
}
