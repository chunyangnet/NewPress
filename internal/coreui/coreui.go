// Package coreui owns the program's administration and default system screens.
// Its embedded files never resolve through an installed theme directory.
package coreui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/theme"
)

var initialize = sync.OnceValues(func() (map[string]*template.Template, error) {
	var pages map[string]string
	raw, _ := files.ReadFile("pages.json")
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, err
	}
	compiled := map[string]*template.Template{}
	for name, file := range pages {
		base := "public-base.html"
		if IsAdmin(name) {
			base = "admin-base.html"
		}
		parsed, err := template.New(base).Funcs(theme.Functions()).Funcs(template.FuncMap{"coreAsset": AssetURL}).ParseFS(files, "templates/"+base, "templates/partials/*.html", "templates/"+file)
		if err != nil {
			return nil, fmt.Errorf("compile core UI %s: %w", name, err)
		}
		compiled[name] = parsed
	}
	return compiled, nil
})

// Embedded assets are immutable. Compute their validators once, and serve the
// embedded reader directly so even large files and 304s avoid a per-request copy.
var assetETags = make(map[string]string)

var revision = func() string {
	hash := sha256.New()
	_ = fs.WalkDir(files, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			panic(err)
		}
		if entry.IsDir() {
			return nil
		}
		file, err := files.Open(name)
		if err != nil {
			panic(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", name, info.Size())
		digest := sha256.New()
		if _, err := io.Copy(io.MultiWriter(hash, digest), file); err != nil {
			panic(err)
		}
		assetETags[strings.TrimPrefix(name, "assets/")] = `"` + hex.EncodeToString(digest.Sum(nil)[:16]) + `"`
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil))[:24]
}()

func IsAdmin(page string) bool { return page == "admin" || strings.HasPrefix(page, "admin-") }
func Validate() error          { _, err := initialize(); return err }
func HasTemplate(page string) bool {
	compiled, err := initialize()
	return err == nil && compiled[page] != nil
}
func Render(w io.Writer, page string, data any) error {
	compiled, err := initialize()
	if err != nil {
		return err
	}
	if compiled[page] == nil {
		return fmt.Errorf("%w: %s", theme.ErrTemplateMissing, page)
	}
	asset := AssetURL
	if provider, ok := data.(interface{ CoreAssetBase() string }); ok && !IsAdmin(page) {
		base := provider.CoreAssetBase()
		asset = func(name string) string { return base + AssetURL(name) }
	}
	bound, err := compiled[page].Clone()
	if err != nil {
		return err
	}
	bound.Funcs(template.FuncMap{"coreAsset": asset})
	return theme.Execute(w, bound, data, asset)
}
func Revision() string { return revision }
func AssetURL(name string) string {
	return "/core-assets/" + revision + "/" + strings.TrimLeft(name, "/")
}

var defaults = func() struct {
	Values  map[string]any
	Classes string
} {
	var defaults struct {
		Values  map[string]any
		Classes string
	}
	data, _ := files.ReadFile("defaults.json")
	if err := json.Unmarshal(data, &defaults); err != nil {
		panic(err)
	}
	for key, value := range defaults.Values {
		if s, ok := value.(string); ok && strings.HasPrefix(s, "/theme-assets/") {
			defaults.Values[key] = AssetURL(strings.TrimPrefix(s, "/theme-assets/"))
		}
	}
	return defaults
}()

func Defaults() (map[string]any, string) { return maps.Clone(defaults.Values), defaults.Classes }

// Assets supports stable addresses for persisted demo covers and
// the public client, plus immutable URLs used by all embedded templates.
func Assets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/core-assets/")
	versioned := strings.HasPrefix(name, revision+"/")
	if versioned {
		name = strings.TrimPrefix(name, revision+"/")
	}
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") {
		http.NotFound(w, r)
		return
	}
	file, err := files.Open("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	reader, ok := file.(io.ReadSeeker)
	if !ok || assetETags[name] == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", assetETags[name])
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	if versioned {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, reader)
}

var errorTemplate = template.Must(template.New("error").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>{{.Title}}</title><body><main id="main-content"><h1>{{.Title}}</h1><p>{{.Message}}</p><a href="/">返回首页</a> · <a href="/admin/">管理站点</a></main></body></html>`))

func Error(w io.Writer, title, message string) error {
	return errorTemplate.Execute(w, struct{ Title, Message string }{title, message})
}
