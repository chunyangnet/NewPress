package navigation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func navigationJSONFixture(t *testing.T) []byte {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateOnce(t.Context(), Creation{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Actor: "editor", Input: MenuInput{Name: "菜单 🌐 �", Slug: "main", Description: "菜单说明 🌐 �", Locations: []string{LocationPrimary}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceItems(t.Context(), created.Receipt.MenuID, Mutation{ExpectedVersion: 1, Actor: "editor"}, []ItemInput{
		{ID: -1, Label: "入口 🌐 �", URL: "/", Description: "入口说明 🌐 �"},
		{ID: -2, ParentID: -1, Label: "子入口", URL: "/child"},
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func replaceNavigationJSON(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	if from == to || !bytes.Contains(payload, []byte(from)) {
		t.Fatalf("missing distinct fixture replacement %q", from)
	}
	return bytes.Replace(payload, []byte(from), []byte(to), 1)
}

func TestNavigationFileRejectsLossyOrConflictingJSON(t *testing.T) {
	valid := navigationJSONFixture(t)
	for _, mode := range []string{"primary", "backup"} {
		cases := map[string]func(*testing.T, []byte) []byte{}
		for field, value := range map[string]string{"name": "菜单 🌐 �", "description": "菜单说明 🌐 �", "label": "入口 🌐 �", "item_description": "入口说明 🌐 �"} {
			for name, bad := range map[string]string{"invalid_byte": "\xff", "truncated_utf8": "\xe4", "high_surrogate": `\ud800`, "low_surrogate": `\udc00`} {
				cases[field+"/"+name] = func(t *testing.T, p []byte) []byte {
					key := field
					if key == "item_description" {
						key = "description"
					}
					return replaceNavigationJSON(t, p, `"`+key+`": "`+value+`"`, `"`+key+`": "`+strings.Replace(value, "�", bad, 1)+`"`)
				}
			}
		}
		for name, pair := range map[string][2]string{
			"schema_alias":       {`"schema": 2`, `"schema": 1, "SCHEMA": 2`},
			"menus_alias":        {`"menus": [`, `"menus": [], "MENUS": [`},
			"name_alias":         {`"name": "菜单 🌐 �"`, `"name": "other", "NAME": "菜单 🌐 �"`},
			"escaped_name_alias": {`"name": "菜单 🌐 �"`, `"name": "other", "\u004eAME": "菜单 🌐 �"`},
			"locations_alias":    {`"locations": [`, `"locations": [], "LOCATIONS": [`},
			"items_alias":        {`"items": [`, `"items": [], "ITEMS": [`},
			"url_alias":          {`"url": "/"`, `"url": "/other", "URL": "/"`},
			"parent_alias":       {`"parent_id": 1`, `"parent_id": 0, "PARENT_ID": 1`},
			"creations_alias":    {`"creations": [`, `"creations": [], "CREATIONS": [`},
			"duplicate":          {`"schema": 2`, `"schema": 2, "schema": 2`},
			"unknown":            {`"schema": 2`, `"unknown": 1, "schema": 2`},
		} {
			cases[name] = func(t *testing.T, p []byte) []byte { return replaceNavigationJSON(t, p, pair[0], pair[1]) }
		}
		cases["receipt_menu_alias"] = func(t *testing.T, p []byte) []byte {
			index := bytes.Index(p, []byte(`"creations": [`))
			if index < 0 {
				t.Fatal("missing creation receipts")
			}
			return append(append([]byte(nil), p[:index]...), replaceNavigationJSON(t, p[index:], `"menu_id": 1`, `"menu_id": 99, "MENU_ID": 1`)...)
		}
		cases["trailing_json"] = func(_ *testing.T, p []byte) []byte { return append(append([]byte(nil), p...), []byte(` {}`)...) }
		cases["null"] = func(_ *testing.T, _ []byte) []byte { return []byte("null") }
		for name, rewrite := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "navigation.json")
				input := filename
				if mode == "backup" {
					input += ".bak"
				} else if err := os.WriteFile(filename+".bak", valid, 0o600); err != nil {
					t.Fatal(err)
				}
				payload := rewrite(t, valid)
				if err := os.WriteFile(input, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := NewFileStore(filename, true); err == nil {
					t.Error("malformed/conflicting navigation accepted or replaced by backup/seed")
				}
				after, err := os.ReadFile(input)
				if err != nil || !bytes.Equal(after, payload) {
					t.Errorf("rejected document changed: %v", err)
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

func TestNavigationFileJSONUnicodeAndLegacyCompatibility(t *testing.T) {
	valid := navigationJSONFixture(t)
	for _, mode := range []string{"primary", "backup"} {
		for _, name := range []string{"unicode", "escaped_unicode", "single_aliases", "schema_one"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				var expected fileDocument
				if err := json.Unmarshal(valid, &expected); err != nil {
					t.Fatal(err)
				}
				payload := append([]byte(nil), valid...)
				switch name {
				case "escaped_unicode":
					payload = bytes.ReplaceAll(payload, []byte("🌐"), []byte(`\ud83c\udf10`))
					payload = bytes.ReplaceAll(payload, []byte("�"), []byte(`\ufffd`))
				case "single_aliases":
					for _, key := range []string{"schema", "menus", "locations", "items", "label", "url", "creations", "fingerprint"} {
						payload = bytes.ReplaceAll(payload, []byte(`"`+key+`":`), bytes.ToUpper([]byte(`"`+key+`":`)))
					}
				case "schema_one":
					expected.Schema = 1
					expected.Creations = nil
					var err error
					payload, err = json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
				}
				filename := filepath.Join(t.TempDir(), "navigation.json")
				input := filename
				if mode == "backup" {
					input += ".bak"
				}
				if err := os.WriteFile(input, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				decoded, err := loadFileDocument(filename)
				if err != nil || !reflect.DeepEqual(decoded, expected) {
					t.Fatalf("valid JSON changed: %v", err)
				}
				store, err := NewFileStore(filename, true)
				if name == "schema_one" {
					if err == nil {
						t.Fatal("retired schema accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				menu, err := store.Get(t.Context(), expected.Menus[0].ID)
				if err != nil || !reflect.DeepEqual(menu, expected.Menus[0]) {
					t.Fatalf("menu changed after open: %v", err)
				}
				updated, err := store.Update(t.Context(), menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "editor"}, MenuInput{Name: "更新 🌐 �", Slug: menu.Slug, Description: menu.Description, Locations: menu.Locations})
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := NewFileStore(filename, false)
				if err != nil {
					t.Fatal(err)
				}
				got, err := reopened.Get(t.Context(), menu.ID)
				if err != nil || !reflect.DeepEqual(got, updated) || !reflect.DeepEqual(reopened.creations, expected.Creations) {
					t.Fatalf("tree or receipts changed on save/reopen: %v", err)
				}
			})
		}
	}
}
