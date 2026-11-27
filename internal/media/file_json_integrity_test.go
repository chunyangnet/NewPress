package media

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mediaJSONFixture(t *testing.T) (string, []byte, Item) {
	t.Helper()
	root := t.TempDir()
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	image := testPNG(t, 2, 2)
	item, err := manager.Upload(t.Context(), bytes.NewReader(image), UploadInput{
		Filename: "fixture.png", Title: "标题 🌐 �", AltText: "替代 🌐 �", Caption: "说明 🌐 �", UploadedBy: "编辑 🌐 �",
	}, int64(len(image)+32))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(root, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	return root, payload, item
}

func replaceMediaJSON(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	if from == to || !bytes.Contains(payload, []byte(from)) {
		t.Fatalf("missing distinct fixture replacement %q", from)
	}
	return bytes.Replace(payload, []byte(from), []byte(to), 1)
}

func TestMediaFileRejectsLossyOrConflictingJSON(t *testing.T) {
	for _, mode := range []string{"primary", "backup"} {
		cases := map[string]func(*testing.T, []byte) []byte{}
		for field, value := range map[string]string{"title": "标题 🌐 �", "alt_text": "替代 🌐 �", "caption": "说明 🌐 �", "uploaded_by": "编辑 🌐 �"} {
			for name, bad := range map[string]string{"invalid_byte": "\xff", "truncated_utf8": "\xe4", "high_surrogate": `\ud800`, "low_surrogate": `\udc00`} {
				cases[field+"/"+name] = func(t *testing.T, p []byte) []byte {
					return replaceMediaJSON(t, p, `"`+field+`": "`+value+`"`, `"`+field+`": "`+string(bytes.Replace([]byte(value), []byte("�"), []byte(bad), 1))+`"`)
				}
			}
		}
		for name, pair := range map[string][2]string{
			"schema_alias":        {`"schema": 3`, `"schema": 1, "SCHEMA": 3`},
			"items_alias":         {`"items": [`, `"items": [], "ITEMS": [`},
			"title_alias":         {`"title": "标题 🌐 �"`, `"title": "other", "TITLE": "标题 🌐 �"`},
			"escaped_title_alias": {`"title": "标题 🌐 �"`, `"title": "other", "\u0054ITLE": "标题 🌐 �"`},
			"version_alias":       {`"version": 1`, `"version": 99, "VERSION": 1`},
			"duplicate_key":       {`"schema": 3`, `"schema": 3, "schema": 3`},
			"escaped_duplicate":   {`"schema": 3`, `"schema": 3, "\u0073chema": 3`},
			"unknown_field":       {`"schema": 3`, `"unknown": 1, "schema": 3`},
			"revision_mismatch":   {`"title": "标题 🌐 �"`, `"title": "changed"`},
		} {
			cases[name] = func(t *testing.T, p []byte) []byte { return replaceMediaJSON(t, p, pair[0], pair[1]) }
		}
		cases["trailing_json"] = func(_ *testing.T, p []byte) []byte { return append(append([]byte(nil), p...), []byte(` {}`)...) }
		cases["null"] = func(_ *testing.T, _ []byte) []byte { return []byte("null") }
		for name, rewrite := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) {
				root, valid, item := mediaJSONFixture(t)
				filename := filepath.Join(root, "library.json")
				input := filename
				if mode == "backup" {
					input += ".bak"
					if err := os.Rename(filename, input); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filename+".bak", valid, 0o600); err != nil {
					t.Fatal(err)
				}
				payload := rewrite(t, valid)
				if err := os.WriteFile(input, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				object := filepath.Join(root, filepath.FromSlash(item.ObjectKey))
				objectBefore, err := os.ReadFile(object)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := NewManager(root); err == nil {
					t.Error("malformed/conflicting media index accepted")
				}
				after, err := os.ReadFile(input)
				if err != nil || !bytes.Equal(after, payload) {
					t.Errorf("rejected index changed: %v", err)
				}
				objectAfter, err := os.ReadFile(object)
				if err != nil || !bytes.Equal(objectAfter, objectBefore) {
					t.Errorf("rejected index changed object: %v", err)
				}
				if mode == "backup" {
					if _, err := os.Stat(filename); !os.IsNotExist(err) {
						t.Errorf("rejected backup created primary: %v", err)
					}
				} else {
					backup, err := os.ReadFile(filename + ".bak")
					if err != nil || !bytes.Equal(backup, valid) {
						t.Errorf("rejected primary changed backup: %v", err)
					}
				}
			})
		}
	}
}

func TestMediaFileJSONUnicodeAndRetiredSchemaRejection(t *testing.T) {
	for _, mode := range []string{"primary", "backup"} {
		for _, name := range []string{"unicode", "escaped_unicode", "single_aliases", "schema_one", "schema_two", "missing_source"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				root, payload, item := mediaJSONFixture(t)
				switch name {
				case "escaped_unicode":
					payload = bytes.ReplaceAll(payload, []byte("🌐"), []byte(`\ud83c\udf10`))
					payload = bytes.ReplaceAll(payload, []byte("�"), []byte(`\ufffd`))
				case "single_aliases":
					for _, key := range []string{"schema", "items", "title", "alt_text", "caption", "uploaded_by"} {
						payload = bytes.ReplaceAll(payload, []byte(`"`+key+`":`), bytes.ToUpper([]byte(`"`+key+`":`)))
					}
				case "schema_one":
					payload = replaceMediaJSON(t, payload, `"schema": 3`, `"schema": 1`)
				case "schema_two":
					payload = replaceMediaJSON(t, payload, `"schema": 3`, `"schema": 2`)
				case "missing_source":
					payload = replaceMediaJSON(t, payload, `"upload_source": "media-library"`, `"upload_source": ""`)
				}
				filename := filepath.Join(root, "library.json")
				input := filename
				if mode == "backup" {
					input += ".bak"
					if err := os.Rename(filename, input); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(input, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				manager, err := NewManager(root)
				if name == "schema_one" || name == "schema_two" || name == "missing_source" {
					if err == nil {
						t.Fatal("retired media metadata accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := manager.Get(item.ID)
				if err != nil || loaded != item {
					t.Fatalf("valid index changed metadata: %v", err)
				}
				if manager.Snapshot().Revision != libraryRevision(2, []Item{item}) {
					t.Fatal("valid JSON changed revision")
				}
				updated, err := manager.Update(item.ID, item.Version, UpdateInput{Title: "更新 🌐 �", AltText: item.AltText, Caption: item.Caption})
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := NewManager(root)
				if err != nil {
					t.Fatal(err)
				}
				got, err := reopened.Get(item.ID)
				if err != nil || got != updated {
					t.Fatalf("valid Unicode changed after save/reopen: %v", err)
				}
				var doc document
				data, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &doc); err != nil || doc.Schema != documentSchema {
					t.Fatalf("schema upgrade failed: %v", err)
				}
			})
		}
	}
}
