package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagerPersistsAtomicallyAndRejectsStaleRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings", "site.json")
	manager, err := NewManager(path, DefaultSettings("NewPress", "测试站点"))
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()
	if initial.Revision == "" || initial.FieldCount != 107 || len(initial.Groups) != 14 {
		t.Fatalf("initial snapshot = %#v", initial)
	}
	if initial.Settings.Discussion != defaultDiscussionSettings() {
		t.Fatalf("discussion defaults = %#v", initial.Settings.Discussion)
	}
	updated := initial.Settings
	updated.Identity.Title = "隧道出版"
	updated.Reading.HomePosts = 12
	updated.Operations.SecurityContact = "mailto:security@example.com"
	saved, fields, err := manager.Save(initial.Revision, updated)
	if err != nil || len(fields) != 0 {
		t.Fatalf("save fields=%v err=%v", fields, err)
	}
	if saved.Revision == initial.Revision || saved.Settings.Identity.Title != "隧道出版" || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved snapshot = %#v", saved)
	}
	if _, _, err := manager.Save(initial.Revision, initial.Settings); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	if _, _, err := manager.Save("", updated); !errors.Is(err, ErrRevisionRequired) {
		t.Fatalf("missing revision error = %v", err)
	}
	invalid := saved.Settings
	invalid.Reading.HomePosts = 999
	preview, invalidFields, err := manager.Save(saved.Revision, invalid)
	if err != nil || invalidFields["home_posts"] == "" || preview.Revision != saved.Revision {
		t.Fatalf("invalid preview=%#v fields=%v err=%v", preview, invalidFields, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o", info.Mode().Perm())
	}
	reloaded, err := NewManager(path, DefaultSettings("Ignored", "Ignored"))
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()
	if got.Revision != saved.Revision || got.Settings.Identity.Title != "隧道出版" || got.Settings.Reading.HomePosts != 12 {
		t.Fatalf("reloaded snapshot = %#v", got)
	}
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewManager(path, DefaultSettings("Fallback", "Fallback"))
	if err != nil || recovered.Snapshot().Revision != saved.Revision {
		t.Fatalf("rollback recovery = %#v, err=%v", recovered, err)
	}
}

func TestWriteDocumentRejectsOversizedEncodingWithoutChangingExistingFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "site.json")
	baseline := document{
		Schema: documentSchema, Kind: documentKind, Revision: "baseline",
		UpdatedAt: time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC), Settings: DefaultSettings("Baseline", "Baseline"),
	}
	if err := writeDocument(path, baseline); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oversized := baseline
	oversized.Settings.Identity.Description = strings.Repeat("x", maxDocumentBytes)
	if err := writeDocument(path, oversized); err == nil {
		t.Fatal("oversized site settings document was written")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("oversized site settings write changed the existing file")
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".site-settings-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("oversized write left temporary files: %v", temporary)
	}
}

func TestParseFormAndValidationBoundary(t *testing.T) {
	settings := DefaultSettings("NewPress", "测试站点")
	p := settings.RegistrationPolicy()
	settings.Accounts = &AccountSettings{Registration: "closed", Policy: &p}
	values := formValues(settings)
	parsed, fields, err := ParseForm(values)
	if err != nil || len(fields) != 0 || !reflect.DeepEqual(parsed, settings) {
		t.Fatalf("parsed=%#v fields=%v err=%v", parsed, fields, err)
	}

	unknown := cloneValues(values)
	unknown.Set("is_admin", "true")
	if _, _, err := ParseForm(unknown); !errors.Is(err, ErrInvalidForm) {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicate := cloneValues(values)
	duplicate["site_title"] = []string{"First", "Second"}
	if _, _, err := ParseForm(duplicate); !errors.Is(err, ErrInvalidForm) {
		t.Fatalf("duplicate field error = %v", err)
	}

	invalid := cloneValues(values)
	invalid.Set("site_timezone", "Mars/Olympus")
	invalid.Set("site_icon_url", "javascript:alert(1)")
	invalid.Set("maintenance_enabled", "true")
	invalid.Set("maintenance_message", "")
	invalid.Set("public_cache_seconds", "99999")
	invalid.Set("comment_max_length", "99")
	invalid.Set("comments_per_page", "201")
	invalid.Set("comment_rate_limit", "0")
	invalid.Set("comment_auto_close_days", "3651")
	invalid.Set("autosave_interval_seconds", "9")
	invalid.Set("autosave_retention_hours", "721")
	invalid.Set("preview_token_minutes", "61")
	_, fields, err = ParseForm(invalid)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"site_timezone", "site_icon_url", "maintenance_message", "public_cache_seconds",
		"comment_max_length", "comments_per_page", "comment_rate_limit", "comment_auto_close_days",
		"autosave_interval_seconds", "autosave_retention_hours", "preview_token_minutes",
	} {
		if fields[key] == "" {
			t.Fatalf("missing validation error for %s: %v", key, fields)
		}
	}
}

func TestCommentLoginPolicyRoundTripAndExplicitDefault(t *testing.T) {
	settings := DefaultSettings("Comments", "评论设置")
	payload, err := json.Marshal(settings)
	if err != nil || !bytes.Contains(payload, []byte(`"require_login":false`)) {
		t.Fatal("default must explicitly declare the comment login policy", err)
	}
	path := filepath.Join(t.TempDir(), "site.json")
	manager, err := NewManager(path, settings)
	if err != nil {
		t.Fatal(err)
	}
	values := formValues(settings)
	values.Set("comments_require_login", "true")
	parsed, fields, err := ParseForm(values)
	if err != nil || len(fields) > 0 || !parsed.Discussion.RequireLogin {
		t.Fatal(parsed, fields, err)
	}
	saved, fields, err := manager.Save(manager.Snapshot().Revision, parsed)
	if err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	reloaded, err := NewManager(path, settings)
	if err != nil || !reloaded.Snapshot().Settings.Discussion.RequireLogin || reloaded.Snapshot().Revision != saved.Revision {
		t.Fatal("policy did not survive reload", err)
	}
}

func TestSettingsRejectIncompleteStoredDocuments(t *testing.T) {
	for _, group := range []string{"discussion", "writing", "integrations"} {
		t.Run(group, func(t *testing.T) {
			settings := DefaultSettings("Incomplete", "")
			payload, _ := json.Marshal(document{Schema: documentSchema, Kind: documentKind, Settings: settings, Revision: settingsRevision(settings), UpdatedAt: time.Now().UTC()})
			var raw map[string]any
			if err := json.Unmarshal(payload, &raw); err != nil {
				t.Fatal(err)
			}
			delete(raw["settings"].(map[string]any), group)
			payload, _ = json.Marshal(raw)
			path := filepath.Join(t.TempDir(), "site-settings.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewManager(path, settings); err == nil {
				t.Fatal("incomplete settings loaded")
			}
			target, _ := NewManager("", settings)
			if _, _, err := target.Import(bytes.NewReader(payload), target.Snapshot().Revision); err == nil {
				t.Fatal("incomplete settings imported")
			}
		})
	}
}

func TestExportImportResetAndStrictJSON(t *testing.T) {
	manager, err := NewManager("", DefaultSettings("NewPress", "测试站点"))
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()
	changed := initial.Settings
	changed.Identity.Description = "面向创作者的高性能内容系统"
	changed.API.PublicIncludeBody = false
	saved, _, err := manager.Save(initial.Revision, changed)
	if err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if err := manager.Export(&exported); err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(exported.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Kind != documentKind || doc.Revision != saved.Revision || doc.Settings.API.PublicIncludeBody {
		t.Fatalf("exported document = %#v", doc)
	}

	target, _ := NewManager("", DefaultSettings("Target", "Target"))
	targetInitial := target.Snapshot()
	imported, fields, err := target.Import(bytes.NewReader(exported.Bytes()), targetInitial.Revision)
	if err != nil || len(fields) != 0 || imported.Settings.Identity.Description != changed.Identity.Description {
		t.Fatalf("imported=%#v fields=%v err=%v", imported, fields, err)
	}
	reset, err := target.Reset(imported.Revision)
	if err != nil || !reflect.DeepEqual(reset.Settings, DefaultSettings("", "")) {
		t.Fatalf("reset=%#v err=%v", reset, err)
	}

	duplicate := strings.Replace(exported.String(), `"schema": 1,`, `"schema": 1, "schema": 1,`, 1)
	if _, _, err := target.Import(strings.NewReader(duplicate), reset.Revision); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate import error = %v", err)
	}
	unknown := strings.Replace(exported.String(), `"kind": "newpress/site-settings",`, `"kind": "newpress/site-settings", "secret": true,`, 1)
	if _, _, err := target.Import(strings.NewReader(unknown), reset.Revision); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown import error = %v", err)
	}
	tampered := strings.Replace(exported.String(), "面向创作者的高性能内容系统", "被修改的内容", 1)
	if _, _, err := target.Import(strings.NewReader(tampered), reset.Revision); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("tampered import error = %v", err)
	}
}

func formValues(settings Settings) url.Values {
	values := make(url.Values)
	values.Set("account_policy_version", "2")
	for _, group := range settingGroups(settings) {
		for _, field := range group.Fields {
			if field.Control == "toggle" {
				if field.BoolValue {
					values.Set(field.Key, "true")
				}
				continue
			}
			values.Set(field.Key, field.StringValue)
		}
	}
	return values
}

func cloneValues(values url.Values) url.Values {
	copyValues := make(url.Values, len(values))
	for key, entries := range values {
		copyValues[key] = append([]string(nil), entries...)
	}
	return copyValues
}
