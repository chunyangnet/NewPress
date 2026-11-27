package site

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRegistrationPolicyPersistenceAndSnapshotIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	m, err := NewManager(path, DefaultSettings("NewPress", ""))
	if err != nil {
		t.Fatal(err)
	}
	initial := m.Snapshot()
	if initial.Settings.AccountRegistration() != "closed" {
		t.Fatal("new sites allow registration")
	}
	settings := initial.Settings
	settings.Accounts = &AccountSettings{Registration: "invite"}
	changed, fields, err := m.Save(initial.Revision, settings)
	if err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	settings.Accounts.Registration = "open"
	changed.Settings.Accounts.Registration = "open"
	if m.Snapshot().Settings.AccountRegistration() != "invite" {
		t.Fatal("snapshot mutated manager")
	}
	reopened, err := NewManager(path, DefaultSettings("Fallback", ""))
	if err != nil || reopened.Snapshot().Settings.AccountRegistration() != "invite" {
		t.Fatal("policy did not persist", err)
	}
	snapshot := m.Snapshot()
	doc := document{Schema: documentSchema, Kind: documentKind, Revision: snapshot.Revision, UpdatedAt: snapshot.UpdatedAt, Settings: snapshot.Settings}
	doc.Settings.Accounts = &AccountSettings{Registration: "open"}
	payload, _ := json.Marshal(doc)
	if _, _, err = m.Import(bytes.NewReader(payload), snapshot.Revision); err == nil {
		t.Fatal("tampered registration revision accepted")
	}
	settings = m.Snapshot().Settings
	settings.Accounts = &AccountSettings{Registration: "anything"}
	if _, fields, err = m.Save(snapshot.Revision, settings); err != nil || fields["account_registration"] == "" {
		t.Fatal("invalid policy allowed", fields, err)
	}
}

func TestRegistrationPolicyFormValidationAndRoundTrip(t *testing.T) {
	p := DefaultRegistrationPolicy()
	p.Fields.InvitationCode = "required"
	rp := p.Referrals()
	rp.Enabled = true
	p.Referral = &rp
	p.OAuthBindings = []string{"email"}
	settings := DefaultSettings("Registration", "")
	settings.Accounts = &AccountSettings{Registration: "open", Policy: &p}
	values := formValues(settings)
	values.Set("account_policy_version", "2")
	parsed, fields, err := ParseForm(values)
	if err != nil || len(fields) != 0 || !parsed.RegistrationPolicy().Referrals().Enabled {
		t.Fatal("policy round trip", fields, err)
	}
	values.Set("account_email_verification", "false")
	_, fields, err = ParseForm(values)
	if err != nil || fields["account_email_verification"] == "" {
		t.Fatal("malformed boolean accepted", fields, err)
	}
	values.Set("account_email_verification", "true")
	values.Set("account_field_email", "hidden")
	_, fields, err = ParseForm(values)
	if err != nil || fields["account_oauth_bind_email"] == "" {
		t.Fatal("hidden mandatory binding accepted", fields, err)
	}
	values.Set("account_policy_version", "3")
	_, fields, err = ParseForm(values)
	if err != nil || fields["account_policy_version"] == "" {
		t.Fatal("unknown policy version accepted", fields, err)
	}
	before := registrationFields(settings)
	for i := 0; i < 20; i++ {
		after := registrationFields(settings)
		for j := range before {
			if before[j].Key != after[j].Key {
				t.Fatal("settings layout is not deterministic")
			}
		}
	}
}

func TestPhoneRegionPolicyValidationAndIsolation(t *testing.T) {
	p := DefaultRegistrationPolicy()
	r := DefaultPhoneRegion()
	r.Selectable = false
	p.PhoneRegion = &r
	settings := DefaultSettings("Regions", "")
	settings.Accounts = &AccountSettings{Registration: "open", Policy: &p}
	values := formValues(settings)
	values.Set("account_policy_version", "2")
	parsed, fields, err := ParseForm(values)
	if err != nil || len(fields) != 0 || parsed.RegistrationPolicy().PhoneRegions().Selectable {
		t.Fatal("fixed region round trip", fields, err)
	}
	if !parsed.RegistrationPolicy().AllowsPhone("+8613800138000") || parsed.RegistrationPolicy().AllowsPhone("+12025550123") {
		t.Fatal("fixed region restriction")
	}
	values.Set("account_phone_region_selectable", "true")
	values.Set("account_phone_default_code", "+1")
	values.Set("account_phone_codes", "+86,+1")
	parsed, fields, err = ParseForm(values)
	if err != nil || len(fields) != 0 || !parsed.RegistrationPolicy().AllowsPhone("+12025550123") || parsed.RegistrationPolicy().PhoneRegions().DefaultCode != "+1" {
		t.Fatal("selectable region round trip", fields, err)
	}
	copy := parsed.RegistrationPolicy()
	copy.PhoneRegion.Codes[0] = "+7"
	copy.PhoneRegion.DefaultCode = "+7"
	if parsed.RegistrationPolicy().PhoneRegions().Codes[0] != "+86" || parsed.RegistrationPolicy().PhoneRegions().DefaultCode != "+1" {
		t.Fatal("region snapshot was shared")
	}
	for _, bad := range []string{"", "+86,+86", "+086,+1", "+44", "+1234,+1"} {
		values.Set("account_phone_codes", bad)
		_, fields, err = ParseForm(values)
		if err != nil || fields["account_phone_codes"] == "" {
			t.Fatalf("accepted codes %q: %v %v", bad, fields, err)
		}
	}
	defaults := Settings{Accounts: &AccountSettings{Policy: &RegistrationPolicy{}}}.RegistrationPolicy().PhoneRegions()
	if !defaults.Selectable || defaults.DefaultCode != "+86" {
		t.Fatal("region defaults", defaults)
	}
}

func TestCodeLoginPolicyPersistenceAndCurrentForm(t *testing.T) {
	settings := DefaultSettings("Code login", "")
	p := DefaultRegistrationPolicy()
	settings.Accounts = &AccountSettings{Registration: "open", Policy: &p}
	values := formValues(settings)
	values.Set("account_policy_version", "2")
	values.Del("account_code_login_enabled") // Unchecked HTML switches are absent.
	parsed, fields, err := ParseForm(values)
	if err != nil || len(fields) != 0 || parsed.RegistrationPolicy().AllowsCodeLogin() {
		t.Fatal("unchecked switch did not disable code login", fields, err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	m, err := NewManager(path, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, fields, err = m.Save(m.Snapshot().Revision, parsed); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	copy := m.Snapshot().Settings.RegistrationPolicy()
	copy.CodeLoginEnabled = true
	if m.Snapshot().Settings.RegistrationPolicy().AllowsCodeLogin() {
		t.Fatal("code login setting shared with snapshot")
	}
	reopened, err := NewManager(path, settings)
	if err != nil || reopened.Snapshot().Settings.RegistrationPolicy().AllowsCodeLogin() {
		t.Fatal("disabled switch did not persist", err)
	}
	values.Set("account_field_password", "optional")
	if _, fields, err = ParseForm(values); err != nil || fields["account_field_password"] == "" {
		t.Fatal("passwordless enrollment without a sign-in method", fields, err)
	}
	values.Set("account_code_login_enabled", "true")
	if _, fields, err = ParseForm(values); err != nil || len(fields) != 0 {
		t.Fatal("verified passwordless registration rejected", fields, err)
	}
	values.Set("account_policy_version", "2")
	values.Del("account_code_login_enabled")
	parsed, fields, err = ParseForm(values)
	if err != nil || fields["account_field_password"] == "" || parsed.RegistrationPolicy().AllowsCodeLogin() || (RegistrationPolicy{}).AllowsCodeLogin() {
		t.Fatal("missing checkbox enabled code login", fields, err)
	}
}

func TestRegistrationPolicyRejectsRetiredSettings(t *testing.T) {
	settings := DefaultSettings("Registration", "")
	for _, key := range []string{"account_invitation_code", "account_registration_code", "account_managed_registration_codes", "account_oauth_github_enabled", "account_oauth_github_client_id"} {
		values := formValues(settings)
		values.Set(key, "retired")
		if _, _, err := ParseForm(values); err == nil {
			t.Fatalf("accepted retired setting %s", key)
		}
	}
	for _, field := range registrationFields(settings) {
		if field.Key == "account_registration_code" || field.Key == "account_managed_registration_codes" {
			t.Fatalf("retired control exposed: %s", field.Key)
		}
	}
	for _, key := range []string{"registration_hash", "managed_registration_codes", "providers"} {
		payload, err := json.Marshal(DefaultRegistrationPolicy())
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields[key]; ok {
			t.Fatalf("retired property exposed: %s", key)
		}
	}
}
