package site

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func siteJSONFixture(t *testing.T) ([]byte, SettingsSnapshot) {
	t.Helper()
	settings := DefaultSettings("站点 🌐 �", "标签 🌐 �")
	settings.Discussion.ModerationNotice = "审核 🌐 �"
	settings.Discussion.RequireLogin = true
	settings.Writing.AutosaveEnabled = false
	settings.Writing.AutosaveIntervalSeconds = 45
	settings.Writing.AutosaveRetentionHours = 96
	settings.Writing.PreviewTokenMinutes = 15
	settings.Accounts = &AccountSettings{Registration: "invite"}
	settings.Integrations = &IntegrationsSettings{FooterCopyright: "版权 🌐 �", Analytics: AnalyticsSettings{Enabled: true, GoogleID: "G-ABCDE12345"}}
	manager, err := NewManager("", settings)
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	if err := manager.Export(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes(), manager.Snapshot()
}

func replaceSiteJSON(t *testing.T, payload []byte, from, to string) []byte {
	t.Helper()
	if from == to || !bytes.Contains(payload, []byte(from)) {
		t.Fatalf("missing distinct fixture replacement %q", from)
	}
	return bytes.Replace(payload, []byte(from), []byte(to), 1)
}

func assertSiteJSONRejected(t *testing.T, mode string, payload, valid []byte) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "site.json")
	bootstrap := DefaultSettings("Fallback", "Fallback")
	switch mode {
	case "primary", "backup":
		input := filename
		if mode == "backup" {
			input += ".bak"
		} else if err := os.WriteFile(filename+".bak", valid, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewManager(filename, bootstrap); err == nil {
			t.Error("malformed/conflicting settings accepted or replaced by backup/defaults")
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
	case "import":
		manager, err := NewManager(filename, bootstrap)
		if err != nil {
			t.Fatal(err)
		}
		before := manager.Snapshot()
		disk, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := manager.Import(bytes.NewReader(payload), before.Revision); err == nil {
			t.Error("malformed/conflicting settings imported")
		}
		if !reflect.DeepEqual(before, manager.Snapshot()) {
			t.Error("rejected import changed in-memory settings or revision")
		}
		after, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(after, disk) {
			t.Errorf("rejected import changed durable settings: %v", err)
		}
	case "repository":
		repository := &memoryDocumentRepository{payload: append([]byte(nil), payload...), exists: true}
		if _, err := NewManagerWithRepository(t.Context(), repository, "", bootstrap); err == nil {
			t.Error("malformed/conflicting repository settings accepted")
		}
		if !repository.exists || !bytes.Equal(repository.payload, payload) {
			t.Error("rejected repository settings overwritten")
		}
	case "migration":
		if err := os.WriteFile(filename, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		repository := &memoryDocumentRepository{}
		if _, err := NewManagerWithRepository(t.Context(), repository, filename, bootstrap); err == nil {
			t.Error("malformed/conflicting settings migrated")
		}
		if repository.exists || len(repository.payload) != 0 {
			t.Error("rejected migration initialized repository")
		}
		after, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(after, payload) {
			t.Errorf("rejected migration changed source: %v", err)
		}
	default:
		t.Fatal("unsupported test mode", mode)
	}
}

func TestSiteSettingsRejectLossyOrConflictingJSON(t *testing.T) {
	valid, _ := siteJSONFixture(t)
	cases := map[string]func(*testing.T, []byte) []byte{}
	for field, value := range map[string]string{"title": "站点 🌐 �", "moderation_notice": "审核 🌐 �", "footer_copyright": "版权 🌐 �"} {
		for name, bad := range map[string]string{"invalid_byte": "\xff", "truncated_utf8": "\xe4", "high_surrogate": `\ud800`, "low_surrogate": `\udc00`} {
			cases[field+"/"+name] = func(t *testing.T, p []byte) []byte {
				return replaceSiteJSON(t, p, `"`+field+`": "`+value+`"`, `"`+field+`": "`+strings.Replace(value, "�", bad, 1)+`"`)
			}
		}
	}
	for name, pair := range map[string][2]string{
		"schema_alias":        {`"schema": 1`, `"schema": 2, "SCHEMA": 1`},
		"settings_alias":      {`"settings": {`, `"settings": {}, "SETTINGS": {`},
		"identity_alias":      {`"identity": {`, `"identity": {}, "IDENTITY": {`},
		"writing_alias":       {`"writing": {`, `"WRITING": {}, "writing": {`},
		"discussion_alias":    {`"discussion": {`, `"DISCUSSION": {}, "discussion": {`},
		"title_alias":         {`"title": "站点 🌐 �"`, `"title": "other", "TITLE": "站点 🌐 �"`},
		"escaped_title_alias": {`"title": "站点 🌐 �"`, `"title": "other", "\u0054ITLE": "站点 🌐 �"`},
		"moderation_alias":    {`"require_moderation": true`, `"require_moderation": false, "REQUIRE_MODERATION": true`},
		"autosave_alias":      {`"autosave_enabled": false`, `"autosave_enabled": true, "AUTOSAVE_ENABLED": false`},
		"registration_alias":  {`"registration": "invite"`, `"registration": "open", "REGISTRATION": "invite"`},
		"analytics_alias":     {`"enabled": true`, `"enabled": false, "ENABLED": true`},
		"duplicate":           {`"schema": 1`, `"schema": 1, "schema": 1`},
		"escaped_duplicate":   {`"schema": 1`, `"schema": 1, "\u0073chema": 1`},
		"unknown":             {`"schema": 1`, `"unknown": 1, "schema": 1`},
		"revision_mismatch":   {`"title": "站点 🌐 �"`, `"title": "changed"`},
	} {
		cases[name] = func(t *testing.T, p []byte) []byte { return replaceSiteJSON(t, p, pair[0], pair[1]) }
	}
	cases["trailing_json"] = func(_ *testing.T, p []byte) []byte { return append(append([]byte(nil), p...), []byte(` {}`)...) }
	cases["null"] = func(_ *testing.T, _ []byte) []byte { return []byte("null") }
	cases["array"] = func(_ *testing.T, _ []byte) []byte { return []byte("[]") }
	for _, mode := range []string{"primary", "backup", "import", "repository", "migration"} {
		for name, rewrite := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) { assertSiteJSONRejected(t, mode, rewrite(t, valid), valid) })
		}
	}
}

func TestSiteSettingsJSONUnicodeAndCaseCompatibility(t *testing.T) {
	valid, expected := siteJSONFixture(t)
	cases := map[string]func(*testing.T, []byte) []byte{
		"unicode": func(_ *testing.T, p []byte) []byte { return p },
		"escaped_unicode": func(_ *testing.T, p []byte) []byte {
			p = bytes.ReplaceAll(p, []byte("🌐"), []byte(`\ud83c\udf10`))
			return bytes.ReplaceAll(p, []byte("�"), []byte(`\ufffd`))
		},
	}
	for _, key := range []string{"settings", "discussion", "writing", "autosave_enabled", "autosave_interval_seconds", "autosave_retention_hours", "preview_token_minutes"} {
		cases["case_"+key] = func(t *testing.T, p []byte) []byte {
			return replaceSiteJSON(t, p, `"`+key+`":`, `"`+strings.ToUpper(key)+`":`)
		}
	}
	cases["unicode_case_fold"] = func(t *testing.T, p []byte) []byte { return replaceSiteJSON(t, p, `"discussion":`, `"diſcuſſion":`) }
	for _, mode := range []string{"primary", "backup", "import", "repository", "migration"} {
		for name, rewrite := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) {
				payload := rewrite(t, valid)
				filename := filepath.Join(t.TempDir(), "site.json")
				bootstrap := DefaultSettings("Fallback", "Fallback")
				var manager *Manager
				var err error
				switch mode {
				case "primary", "backup":
					input := filename
					if mode == "backup" {
						input += ".bak"
					}
					if err = os.WriteFile(input, payload, 0o600); err != nil {
						t.Fatal(err)
					}
					manager, err = NewManager(filename, bootstrap)
				case "import":
					manager, err = NewManager(filename, bootstrap)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]string
					_, fields, err = manager.Import(bytes.NewReader(payload), manager.Snapshot().Revision)
					if len(fields) != 0 {
						t.Fatal(fields)
					}
				case "repository":
					manager, err = NewManagerWithRepository(t.Context(), &memoryDocumentRepository{payload: payload, exists: true}, "", bootstrap)
				case "migration":
					if err = os.WriteFile(filename, payload, 0o600); err != nil {
						t.Fatal(err)
					}
					manager, err = NewManagerWithRepository(t.Context(), &memoryDocumentRepository{}, filename, bootstrap)
				}
				if err != nil {
					t.Fatalf("valid document rejected: %v", err)
				}
				got := manager.Snapshot()
				if !reflect.DeepEqual(got.Settings, expected.Settings) || got.Revision != expected.Revision {
					t.Fatal("valid Unicode/aliases changed settings, safety values or revision")
				}
				var exported bytes.Buffer
				if err := manager.Export(&exported); err != nil {
					t.Fatal(err)
				}
				decoded, _, err := decodeStoredDocument(exported.Bytes())
				if err != nil || !reflect.DeepEqual(decoded, expected.Settings) {
					t.Fatalf("valid settings changed after export/reopen: %v", err)
				}
			})
		}
	}
}

func TestSiteSettingsShapeKeepsNullAndAbsenceDistinct(t *testing.T) {
	for name, test := range map[string]struct {
		json            string
		discussion, all bool
	}{
		"missing":      {`{"settings":{}}`, false, false},
		"null_groups":  {`{"SETTINGS":{"DISCUSSION":null,"WRITING":null}}`, true, false},
		"partial_null": {`{"SETTINGS":{"WRITING":{"AUTOSAVE_ENABLED":null}}}`, false, false},
		"all_null":     {`{"settings":{"writing":{"AUTOSAVE_ENABLED":null,"AUTOSAVE_INTERVAL_SECONDS":null,"AUTOSAVE_RETENTION_HOURS":null,"PREVIEW_TOKEN_MINUTES":null,"BROWSER_RECOVERY_ENABLED":null}}}`, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeDocument(strings.NewReader(test.json))
			if err != nil {
				t.Fatal(err)
			}
			if doc.discussionPresent != test.discussion || doc.writingSafetyPresent != test.all {
				t.Fatalf("wrong settings shape: discussion=%v all=%v", doc.discussionPresent, doc.writingSafetyPresent)
			}
		})
	}
}
