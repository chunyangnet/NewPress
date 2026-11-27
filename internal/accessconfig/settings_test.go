package accessconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePersistenceRedactionAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access-settings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sms := DefaultSMS()
	sms.Provider, sms.Enabled, sms.Endpoint, sms.Secret = "custom", true, "https://sms.example.com/send", "stored-sms-secret"
	initial := Settings{Revision: store.Runtime().Revision, Providers: []Provider{{ID: "github", Enabled: true, ClientID: "app", Secret: "stored-oauth-secret"}}, SMS: &sms}
	if _, err = store.Save(initial); err != nil {
		t.Fatal(err)
	}
	view := Redact(store.Runtime())
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), "stored-") || !view.Providers[0].HasSecret || !view.SMS.HasSecret {
		t.Fatal("redaction failed")
	}
	view.CallbackBaseURL = "https://news.example.com/"
	saved, err := store.Save(view)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CallbackBaseURL != "https://news.example.com" || saved.Providers[0].Secret != "" || saved.SMS.Secret != "" {
		t.Fatal("unsafe save response")
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := reopened.Runtime()
	if runtime.Providers[0].Secret != "stored-oauth-secret" || runtime.SMS.Secret != "stored-sms-secret" {
		t.Fatal("credentials were not persisted")
	}
	if _, err = reopened.Save(view); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted", err)
	}
	view = Redact(runtime)
	view.Providers[0].ClientID = "new-app"
	if _, err = reopened.Save(view); err == nil {
		t.Fatal("new app reused old secret")
	}
	view.Providers[0].Secret = "new-oauth-secret"
	view.SMS.Endpoint = "https://other.example.com/send"
	if _, err = reopened.Save(view); err == nil {
		t.Fatal("new endpoint reused old secret")
	}
	view.SMS.Secret = "new-sms-secret"
	saved, err = reopened.Save(view)
	if err != nil {
		t.Fatal(err)
	}
	saved.Providers[0].Enabled = false
	saved.Providers[0].ClearSecret = true
	saved.SMS.Enabled = false
	saved.SMS.ClearSecret = true
	saved, err = reopened.Save(saved)
	if err != nil {
		t.Fatal(err)
	}
	runtime = reopened.Runtime()
	if runtime.Providers[0].Secret != "" || runtime.Providers[0].Enabled || runtime.SMS.Secret != "" || runtime.SMS.Enabled {
		t.Fatal("clearing re-enabled credentials")
	}
	copy := reopened.Runtime()
	copy.Providers[0].Enabled = true
	copy.SMS.Secret = "changed"
	if reopened.Runtime().Providers[0].Enabled || reopened.Runtime().SMS.Secret != "" {
		t.Fatal("runtime alias mutated store")
	}
	if _, err = NewStore(path); err != nil {
		t.Fatal("cleared configuration cannot reopen", err)
	}
}

func TestStoreFailedPersistenceKeepsRevisionAndCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, _ := NewStore(path)
	old := s.Runtime()
	next := old
	next.Providers = []Provider{{ID: "google", Enabled: true, ClientID: "app", Secret: "private"}}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(next); err == nil {
		t.Fatal("directory replaced")
	}
	if s.Runtime().Revision != old.Revision || len(s.Runtime().Providers) != 0 {
		t.Fatal("failed write changed runtime")
	}
}

func TestSettingsRejectUnsafeAndMalformedConfigurations(t *testing.T) {
	for _, base := range []string{"https://user:pass@example.com", "https://example.com/path", "https://example.com?q=x", "http://example.com", "https://example.com#fragment", "javascript:alert(1)"} {
		if ValidCallbackBase(base) {
			t.Fatal("unsafe callback accepted", base)
		}
	}
	for _, base := range []string{"https://example.com", "http://localhost:8080", "http://[::1]:8080"} {
		if !ValidCallbackBase(base) {
			t.Fatal(base)
		}
	}
	base := DefaultSMS()
	base.Enabled = true
	base.Provider = "custom"
	base.Endpoint = "https://sms.example.com/send"
	base.Secret = "private"
	cases := []func(*SMS){
		func(p *SMS) { p.Endpoint = "http://sms.example.com" }, func(p *SMS) { p.Endpoint = "https://127.0.0.1" }, func(p *SMS) { p.Endpoint = "https://10.0.0.1" }, func(p *SMS) { p.Endpoint = "https://example.com:8080" },
		func(p *SMS) { p.Auth = "header"; p.AuthHeader = "Host" }, func(p *SMS) { p.Auth = "header"; p.AuthHeader = "X-Test\r\nAuthorization" }, func(p *SMS) { p.SuccessPath = "data[0]" },
		func(p *SMS) { p.Payload = `{"phone":"{phone}","code":"{bad}"}` }, func(p *SMS) { p.Encoding = "form"; p.Payload = `{"phone":"{phone}","code":["{code}"]}` }, func(p *SMS) { p.TimeoutSeconds = 21 },
		func(p *SMS) {
			p.Provider = "aliyun"
			p.AccessKey = "key"
			p.SignName = "签名"
			p.TemplateID = "id"
			p.TemplateParams = `["{code}"]`
		},
		func(p *SMS) { p.Provider = "twilio"; p.AccessKey = "../../bad"; p.Sender = "+12025550123" },
	}
	for i, change := range cases {
		p := base
		change(&p)
		if _, err := normalizeSMS(p, SMS{}); err == nil {
			t.Fatal("unsafe settings accepted", i)
		}
	}
}
