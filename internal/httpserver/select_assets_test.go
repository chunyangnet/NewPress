package httpserver

import (
	"bytes"
	"testing"

	"newpress.local/newpress/internal/coreui"
)

func TestNativeSelectAssetsAreAddedOnceForInstalledThemes(t *testing.T) {
	input := []byte(`<!doctype html><html lang="zh-CN"><head><title>文档</title></head><body class="docshub"><select><option>全部</option></select></body></html>`)
	output := withSelectAssets(input, "explore")

	for _, asset := range []string{coreui.AssetURL("css/chunnel-selects.css"), coreui.AssetURL("js/chunnel-selects.js")} {
		if count := bytes.Count(output, []byte(asset)); count != 1 {
			t.Errorf("shared asset %q appeared %d times", asset, count)
		}
	}
	if !bytes.Contains(output, []byte(`<html data-choice-controls lang="zh-CN">`)) {
		t.Fatal("select stylesheet is not enabled on the installed theme document")
	}
	if !bytes.Contains(output, []byte(`</head><body`)) || !bytes.Contains(output, []byte(`defer></script></body>`)) {
		t.Fatal("select assets were inserted outside the document head or body")
	}
}

func TestNativeSelectAssetsPreserveExistingThemeAssets(t *testing.T) {
	input := []byte(`<html data-choice-controls><head><link rel="stylesheet" href="/theme-assets/css/chunnel-selects.css"></head><body><script src="/theme-assets/js/chunnel-selects.js"></script></body></html>`)
	output := withSelectAssets(input, "explore")
	if count := bytes.Count(output, []byte("chunnel-selects.css")); count != 1 {
		t.Errorf("existing theme stylesheet count = %d", count)
	}
	if count := bytes.Count(output, []byte("chunnel-selects.js")); count != 1 {
		t.Errorf("existing theme script count = %d", count)
	}
}

func TestHomepageSelectAssetsFollowRenderedFields(t *testing.T) {
	shell := []byte(`<html><head></head><body><dialog data-choice-style="/theme-assets/css/chunnel-selects.css" data-choice-script="/theme-assets/js/chunnel-selects.js"></dialog></body></html>`)
	if !bytes.Equal(withSelectAssets(shell, "home"), shell) {
		t.Fatal("homepage without selects loaded extra assets")
	}
	withField := bytes.Replace(shell, []byte("</body>"), []byte("<select></select></body>"), 1)
	got := withSelectAssets(withField, "home")
	if !selectStyles.Match(got) || !selectScript.Match(got) {
		t.Fatal("homepage with a visible select did not get the shared assets")
	}
}
