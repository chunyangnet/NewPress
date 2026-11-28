package httpserver

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/coreui"
)

// Account for the initial editor plus optional insertion/draft and media groups.
// Component, resource and AI tools remain separate optional graphs. These
// measurements do not replace the migration plan's outstanding 90 KiB goal.
func TestEditorOptionalToolsResourceContract(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/new/", cookie, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("editor status: %d", response.Code)
	}
	body := response.Body.String()
	scripts := regexp.MustCompile(`<script src="([^"]+)"`).FindAllStringSubmatch(body, -1)
	lazy := regexp.MustCompile(`data-editor-tools-script="([^"]+)"`).FindAllStringSubmatch(body, -1)
	mediaLazy := regexp.MustCompile(`data-media-script="([^"]+)"`).FindAllStringSubmatch(body, -1)
	emojiLazy := regexp.MustCompile(`data-editor-emoji-script="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(lazy) != 1 || len(mediaLazy) != 1 || len(emojiLazy) != 1 {
		t.Fatalf("optional insertion/media/emoji bundle declarations: %d/%d/%d", len(lazy), len(mediaLazy), len(emojiLazy))
	}
	type measuredAsset struct {
		URL    string `json:"url"`
		Gzip   int    `json:"gzip_body_bytes"`
		SHA256 string `json:"sha256"`
	}
	measure := func(target string) measuredAsset {
		t.Helper()
		target = html.UnescapeString(target)
		if !strings.HasPrefix(target, "/core-assets/"+coreui.Revision()+"/") {
			t.Fatalf("unversioned resource: %s", target)
		}
		asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+target, cookie, nil)
		if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") || asset.Header().Get("ETag") == "" || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("resource/cache contract: %s, status %d", target, asset.Code)
		}
		var compressed bytes.Buffer
		writer, err := gzip.NewWriterLevel(&compressed, 5)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(asset.Body.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		count := compressed.Len()
		if asset.Body.Len() < 512 {
			count = asset.Body.Len()
		}
		hash := sha256.Sum256(asset.Body.Bytes())
		return measuredAsset{target, count, hex.EncodeToString(hash[:])}
	}
	var initial []measuredAsset
	initialBytes, documentEditors, pageEntries := 0, 0, 0
	for _, script := range scripts {
		for _, name := range []string{"chunnel-editor-reuse.js", "newpress-editor-snippets.js", "chunnel-editor-widgets.js", "newpress-editor-insert-tools.js", "chunnel-admin-media.js", "newpress-editor-media-dialog.js", "newpress-editor-emoji-picker.js"} {
			if strings.Contains(script[1], name) {
				t.Errorf("optional tool still downloads initially: %s", name)
			}
		}
		for _, name := range []string{"chunnel-admin-workbench.js", "chunnel-admin.js", "chunnel-admin-editor.js", "chunnel-editor-workspace.js", "chunnel-editor-recovery.js", "chunnel-seo.js", "chunnel-emoji-editor.js", "chunnel-emoji.js", "chunnel-article-badge-editor.js", "chunnel-selects.js", "newpress-resource-loader.js", "newpress-editor-media-loader.js", "newpress-component-loader.js", "newpress-editor-tools-loader.js"} {
			if strings.Contains(script[1], "/"+name) {
				t.Errorf("editor page enhancement is duplicated outside its initial bundle: %s", name)
			}
		}
		if strings.Contains(script[1], "/newpress-editor-page.js") {
			pageEntries++
		}
		if strings.Contains(script[1], "/newpress-document-editor.js") {
			documentEditors++
		}
		asset := measure(script[1])
		initial = append(initial, asset)
		initialBytes += asset.Gzip
	}
	if documentEditors != 1 || pageEntries != 1 || len(initial) != 4 {
		t.Fatalf("default document editors/page entries/initial requests = %d/%d/%d", documentEditors, pageEntries, len(initial))
	}
	pageTag := regexp.MustCompile(`<script src="[^"]+/newpress-editor-page\.js[^>]+>`).FindString(body)
	for _, attribute := range []string{"data-editor-media-loader", "data-media-script=", "data-component-loader", "data-component-script=", "data-component-style=", "data-editor-tools-loader", "data-editor-tools-script=", "data-editor-emoji-script="} {
		if !strings.Contains(pageTag, attribute) || strings.Count(body, attribute) != 1 {
			t.Fatalf("tool configuration must occur once on the combined page entry: %s", attribute)
		}
	}
	if strings.Contains(body, "data-choice-entry=") {
		t.Fatal("the editor's necessary choice controls must be included in its initial dependency measurement")
	}
	optional := measure(lazy[0][1])
	media := measure(mediaLazy[0][1])
	emoji := measure(emojiLazy[0][1])
	series := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/series/?edit=new", cookie, nil)
	if series.Code != http.StatusOK || strings.Count(series.Body.String(), "/chunnel-admin-media.js") != 1 || strings.Contains(series.Body.String(), "newpress-editor-media-") {
		t.Fatal("series fields must retain their eager media entry")
	}
	completeBytes := initialBytes + optional.Gzip + media.Gzip
	t.Logf("initial JS=%d B; explicit insertion tools=%d B; explicit media=%d B; JS with both groups=%d B; initial script requests=%d", initialBytes, optional.Gzip, media.Gzip, completeBytes, len(initial))
	t.Logf("explicit emoji=%d B; JS with media, insertion and emoji groups=%d B", emoji.Gzip, completeBytes+emoji.Gzip)
	if report := os.Getenv("NEWPRESS_EDITOR_TOOLS_RESOURCES_REPORT"); report != "" {
		if !filepath.IsAbs(report) {
			t.Fatal("resource report path must be absolute")
		}
		value := map[string]any{"scope": "initial editor plus insertion/draft and media tools; separate component, resource and AI tools excluded", "initial_js": initialBytes, "optional_js": optional.Gzip, "media_js": media.Gzip, "with_media_js": initialBytes + media.Gzip, "complete_js": completeBytes, "initial_scripts": initial, "optional_script": optional, "media_script": media, "original_cap": 92160}
		value["scope"] = "initial editor and explicit insertion/draft, media and emoji groups; separate component, resource and AI tools excluded"
		value["emoji_js"], value["emoji_script"], value["with_emoji_js"], value["with_three_groups_js"] = emoji.Gzip, emoji, initialBytes+emoji.Gzip, completeBytes+emoji.Gzip
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(report, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(append(data, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("resource report write: %v / %v", writeErr, closeErr)
		}
	}
}
