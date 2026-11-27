package commentorigin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/plugin"
)

func TestFlagModesAndCountryAllowlist(t *testing.T) {
	for _, tc := range []struct{ mode, code, want string }{
		{"off", "US", ""}, {"", "US", ""}, {"invalid", "US", ""},
		{"all", "CN", "cn"}, {"all", " jp ", "jp"}, {"foreign", "US", "us"},
		{"foreign", "CN", ""}, {"foreign", "HK", ""}, {"foreign", "MO", ""}, {"foreign", "TW", ""},
		{"all", "", ""}, {"all", "ZZ", ""}, {"all", "EU", ""}, {"all", "../us", ""}, {"all", "中国", ""},
	} {
		if got := flagCode(tc.mode, tc.code); got != tc.want {
			t.Errorf("flagCode(%q, %q)=%q, want %q", tc.mode, tc.code, got, tc.want)
		}
	}
	for _, code := range []string{"cn", "us", "jp", "hk", "mo", "tw"} {
		data, err := FlagSVG(code)
		if err != nil || !strings.Contains(string(data), "<svg") {
			t.Fatalf("flag %s: %v", code, err)
		}
	}
	for _, code := range []string{"../LICENSE", "LICENSE", "zz", "US", "us.svg", "", "us?x=1"} {
		if _, err := FlagSVG(code); err == nil {
			t.Errorf("accepted asset %q", code)
		}
	}
}

func TestFlagsFollowPrivacyAndNeverGuessHistoricalCountries(t *testing.T) {
	s, m, root := originFixture(t)
	if err := s.Capture(1, "8.8.8.8", "Windows NT 10.0"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.PrivateRecord(1)
	r.Status, r.Location = "resolved", Location{Country: "Japan", CountryCode: "JP", City: "Tokyo", Precision: "city"}
	path := filepath.Join(root, plugin.CommentOrigin, "data", "records", "1.json")
	if err := plugin.WritePrivate(path, r); err != nil {
		t.Fatal(err)
	}
	if b := s.Badges(1, nil); b.Flag != "jp" {
		t.Fatalf("missing foreign flag: %+v", b)
	}
	for _, roles := range [][]string{{"administrator"}, {"unknown"}, {"guest", "administrator"}} {
		if b := s.Badges(1, roles); b != (Badges{}) {
			t.Fatalf("private role exposed: %+v", b)
		}
	}
	config, _ := s.Settings()
	config.Location = false
	config, _ = s.Save(config, false)
	if b := s.Badges(1, nil); b.Flag != "" || b.Location != "" || b.Device == "" {
		t.Fatalf("global location privacy: %+v", b)
	}
	config.Location, config.FlagMode = true, "off"
	config, _ = s.Save(config, false)
	if b := s.Badges(1, nil); b.Flag != "" || b.Location == "" {
		t.Fatalf("flag off hid location: %+v", b)
	}
	config.FlagMode = "all"
	if _, err := s.Save(config, false); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", "ZZ", "../jp"} {
		r.Location.CountryCode = code
		if err := plugin.WritePrivate(path, r); err != nil {
			t.Fatal(err)
		}
		if b := s.Badges(1, nil); b.Flag != "" || b.Location == "" {
			t.Fatalf("guessed historical/invalid country: %+v", b)
		}
	}
	r.Location.CountryCode, r.Status = "JP", "pending"
	if err := plugin.WritePrivate(path, r); err != nil {
		t.Fatal(err)
	}
	if b := s.Badges(1, nil); b.Flag != "" || b.Location != "" {
		t.Fatalf("unresolved location: %+v", b)
	}
	if err := m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	if b := s.Badges(1, nil); b != (Badges{}) {
		t.Fatalf("disabled plugin: %+v", b)
	}
}

func TestFlagSettingsPersistenceAndRejectsMissingMode(t *testing.T) {
	s, _, root := originFixture(t)
	config, _ := s.Settings()
	if config.FlagMode != "foreign" {
		t.Fatal(config.FlagMode)
	}
	config.FlagMode, config.CountryCodePath = "all", "data.country_code"
	if _, err := s.Save(config, false); err != nil {
		t.Fatal(err)
	}
	reloaded := New(plugin.NewManager(root))
	got, err := reloaded.Settings()
	if err != nil || got.FlagMode != "all" || got.CountryCodePath != "data.country_code" {
		t.Fatalf("persistence: %+v %v", got, err)
	}
	got.FlagMode = "unexpected"
	if _, err := reloaded.Save(got, false); err == nil {
		t.Fatal("accepted unknown mode")
	}
	// Simulate a config written before these fields existed.
	data, _ := json.Marshal(Defaults())
	var legacy map[string]any
	_ = json.Unmarshal(data, &legacy)
	delete(legacy, "flag_mode")
	delete(legacy, "country_code_path")
	data, _ = json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(root, plugin.CommentOrigin, "data", "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = reloaded.Settings()
	if err == nil {
		t.Fatal("accepted settings without an explicit flag mode")
	}
	got = Defaults()
	got.Provider, got.Endpoint, got.CountryCodePath = "custom", "https://geo.example.test/?ip={ip}", "../../secret"
	if validate(got) == nil {
		t.Fatal("accepted invalid country code path")
	}
}
