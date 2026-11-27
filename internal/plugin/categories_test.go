package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginCategoryCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest Manifest
		want     string
	}{
		{"builtin legacy", BuiltinManifest(), "comments"},
		{"WASM legacy", Manifest{Slug: "content-age"}, "content"},
		{"unclassified legacy", Manifest{Slug: "older-plugin"}, "other"},
		{"explicit category", Manifest{Slug: "content-age", Category: "tools"}, "tools"},
		{"future category", Manifest{Category: "commerce"}, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			category := CategoryFor(tc.manifest)
			if category.Slug != tc.want || category.Name == "" || category.Icon == "" {
				t.Fatalf("category = %#v, want %s", category, tc.want)
			}
		})
	}
	// Discovery defaults must never change the serialized identity of signed
	// legacy packages or the strictly matched built-in activation manifest.
	builtin := BuiltinManifest()
	_ = CategoryFor(builtin)
	b, err := json.Marshal(builtin)
	if err != nil || bytes.Contains(b, []byte(`"category"`)) || bytes.Contains(b, []byte(`"tags"`)) || !isBuiltin(builtin) {
		t.Fatalf("legacy manifest changed: %s, %v", b, err)
	}
	if err := ValidateManifest(builtin); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDiscoveryMetadataValidation(t *testing.T) {
	base := Manifest{Schema: 1, Slug: "discovery-test", Name: "Discovery", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm"}
	for _, tc := range []struct {
		name, category string
		tags           []string
		valid          bool
	}{
		{"optional", "", nil, true},
		{"Chinese labels", "content", []string{"写作", "时效提醒", "SEO"}, true},
		{"future category", "commerce", []string{"商店"}, true},
		{"case in category", "Content", nil, false},
		{"invalid ID", "content--tools", nil, false},
		{"long ID", strings.Repeat("a", 41), nil, false},
		{"blank tag", "tools", []string{""}, false},
		{"untrimmed tag", "tools", []string{" SEO"}, false},
		{"multiline tag", "tools", []string{"one\ntwo"}, false},
		{"long tag", "tools", []string{strings.Repeat("字", 25)}, false},
		{"duplicate tag", "seo", []string{"SEO", "seo"}, false},
		{"too many tags", "tools", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := base
			manifest.Category, manifest.Tags = tc.category, tc.tags
			if err := ValidateManifest(manifest); (err == nil) != tc.valid {
				t.Fatalf("valid = %t, error = %v", tc.valid, err)
			}
			if tc.valid {
				b, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				var decoded Manifest
				if err := json.Unmarshal(b, &decoded); err != nil || !reflectManifest(manifest, decoded) {
					t.Fatalf("metadata lost in manifest round trip: %v", err)
				}
			}
		})
	}
}
