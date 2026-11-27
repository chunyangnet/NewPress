package httpserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/plugin"
)

func installDirectorySample(t *testing.T, manager *plugin.Manager, tags []string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range []string{"plugin.json", "plugin.wasm"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "examples", "plugins", "content-age", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "plugin.json" {
			var manifest plugin.Manifest
			if err := json.Unmarshal(b, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Category, manifest.Tags = "content", tags
			b, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "directory-sample.zip")
	if err := os.WriteFile(archive, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDirectoryCategoriesAndEscapedMetadata(t *testing.T) {
	s, handler, _, cookie, _ := newCommentHTTPTestServer(t)
	installDirectorySample(t, s.plugins, []string{"写作", `<img src=x onerror=1>`})
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/", "", "", "8.8.8.8:80")
	body := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("directory status: %d %s", page.Code, body)
	}
	for _, marker := range []string{
		`data-plugin-category="comments"`, `data-plugin-category="content"`,
		`data-plugin-card="content-age" data-category="content" data-installed="true"`,
		`data-plugin-card="ai-pro-max" data-category="content" data-installed="false"`,
		`data-plugin-card="comment-origin" data-category="comments" data-installed="false"`,
		`内容创作 <span data-category-count>2</span>`, `评论互动 <span data-category-count>1</span>`,
		`data-plugin-search`, `data-plugin-status-filter`, `data-plugin-empty`,
		`&lt;img src=x onerror=1&gt;`, `data-plugin-remove="content-age"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("missing directory markup %q", marker)
		}
	}
	if strings.Contains(body, `<img src=x onerror=1>`) || strings.Contains(body, `data-plugin-category="seo"`) {
		t.Fatal("unsafe metadata or empty category rendered")
	}
}
