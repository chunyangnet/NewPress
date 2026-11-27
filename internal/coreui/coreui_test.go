package coreui

import (
	"io/fs"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedAssetGraphIsIndependent(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	assetCall := regexp.MustCompile(`(?:asset|assetOr\s+[^\n{}]+?)\s+"([^"\n]+)"`)
	cssURL := regexp.MustCompile(`url\(\s*["']?([^\s"')]+)["']?\s*\)`)
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !strings.HasSuffix(name, ".html") && !strings.HasSuffix(name, ".css") {
			return nil
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".html") {
			for _, match := range assetCall.FindAllStringSubmatch(string(data), -1) {
				if _, err := files.ReadFile("assets/" + match[1]); err != nil {
					t.Errorf("%s references missing core asset %s", name, match[1])
				}
			}
		} else {
			for _, match := range cssURL.FindAllStringSubmatch(string(data), -1) {
				parsed, err := url.Parse(match[1])
				if err != nil {
					t.Errorf("%s has invalid URL %q", name, match[1])
					continue
				}
				if parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
					continue
				}
				target := path.Join(path.Dir(name), parsed.Path)
				if strings.HasPrefix(parsed.Path, "/") {
					t.Errorf("%s has unscoped resource %s", name, match[1])
					continue
				}
				if _, err := files.ReadFile(target); err != nil {
					t.Errorf("%s references missing resource %s", name, target)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoreAssetCacheIdentity(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		target := "/core-assets/js/newpress-client.js"
		if versioned {
			target = AssetURL("js/newpress-client.js")
		}
		w := httptest.NewRecorder()
		Assets(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "NewPressClient") || w.Header().Get("ETag") == "" {
			t.Fatal("missing standalone client", w.Code)
		}
		if strings.Contains(w.Header().Get("Cache-Control"), "immutable") != versioned {
			t.Fatal("incorrect core asset cache policy")
		}
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("If-None-Match", w.Header().Get("ETag"))
		conditional := httptest.NewRecorder()
		Assets(conditional, r)
		if conditional.Code != 304 || conditional.Body.Len() != 0 {
			t.Fatal("core conditional request failed")
		}
	}
}
