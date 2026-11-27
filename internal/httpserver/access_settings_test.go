package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/site"
)

func TestAccessConsoleAuthorizationPersistencePrivacyAndHotReload(t *testing.T) {
	s, h, m, _, cookie, owner := newPersistentAdminTestServer(t)
	path := filepath.Join(t.TempDir(), "access-settings.json")
	store, err := accessconfig.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAccessSettings(store)
	_, reader, rp := createAndLoginTestUser(t, m, owner, "access-reader", "access-reader@example.test", "读者", "subscriber")
	input := s.accessConfig()
	input.Providers = []accessconfig.Provider{{ID: "github", Enabled: true, ClientID: "online-id", Secret: "private-online-secret"}}
	input.CallbackBaseURL = "https://example.test"
	sms := accessconfig.DefaultSMS()
	sms.Enabled = true
	sms.Provider = "custom"
	sms.Endpoint = "https://sms.example.com/send"
	sms.Secret = "private-sms-token"
	sms.SuccessPath = "code"
	sms.SuccessValue = "0"
	input.SMS = &sms
	b, _ := json.Marshal(input)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	for _, c := range []struct {
		cookie       *http.Cookie
		csrf, origin string
	}{{nil, "", "https://example.test"}, {reader, rp.CSRFToken, "https://example.test"}, {cookie, "", "https://example.test"}, {cookie, owner.CSRFToken, "https://evil.test"}} {
		r := serveContentAPI(h, c.cookie, "POST", "https://example.test/api/v1/admin/access/settings", string(b), map[string]string{"Content-Type": "application/json", "Origin": c.origin, "X-CSRF-Token": c.csrf})
		if r.Code != 401 && r.Code != 403 {
			t.Fatal("unauthorized mutation", r.Code)
		}
	}
	r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/access/settings", string(b), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, ok := s.loginProvider("github"); !ok || !s.smsReady() {
		t.Fatal("online settings did not become active")
	}
	for _, path := range []string{"/api/v1/admin/access/settings", "/admin/settings/", "/admin/settings/export/", "/api/v1/social/access"} {
		r = serveContentAPI(h, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), "private-online-secret") || strings.Contains(r.Body.String(), "private-sms-token") {
			t.Fatal("secret exposure or broken route", path, r.Code)
		}
		if fixtureDir := os.Getenv("NEWPRESS_ACCESS_UI_FIXTURE"); fixtureDir != "" {
			name := ""
			if path == "/admin/settings/" {
				name = "settings.html"
			}
			if path == "/api/v1/admin/access/settings" {
				name = "settings.json"
			}
			if name != "" {
				if err := os.WriteFile(filepath.Join(fixtureDir, name), r.Body.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if r = serveContentAPI(h, reader, "GET", "https://example.test/api/v1/admin/access/settings", "", nil); r.Code != 403 {
		t.Fatal("reader can inspect settings")
	}
	if r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/access/settings", string(b), headers); r.Code != 409 {
		t.Fatal("stale save accepted", r.Code)
	}
	reopened, err := accessconfig.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAccessSettings(reopened)
	start := serveContentAPI(h, nil, "GET", "https://example.test/auth/github/start", "", nil)
	if start.Code != 302 {
		t.Fatal("restart lost provider", start.Code)
	}
	u, _ := url.Parse(start.Header().Get("Location"))
	if u.Query().Get("client_id") != "online-id" || u.Query().Get("redirect_uri") != "https://example.test/auth/github/callback" {
		t.Fatal("wrong authorization request")
	}
	input = accessconfig.Redact(s.accessConfig())
	input.Providers[0].Secret = "rotated-secret"
	if _, err = s.accessSettings.Save(input); err != nil {
		t.Fatal(err)
	}
	var browser *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == accessBrowserCookie {
			browser = c
		}
	}
	s.registration.client = &http.Client{Transport: oauthTestTransport(func(*http.Request) (*http.Response, error) { t.Fatal("stale state reached provider"); return nil, nil })}
	r = serveContentAPI(h, browser, "GET", "https://example.test/auth/github/callback?state="+url.QueryEscape(u.Query().Get("state"))+"&code=old-code", "", nil)
	if r.Header().Get("Location") != "/account/access/#oauth_error=1" {
		t.Fatal("rotation accepted old authorization")
	}
	input = accessconfig.Redact(s.accessConfig())
	input.Providers[0].Enabled = false
	input.Providers[0].ClearSecret = true
	input.SMS.Enabled = false
	input.SMS.ClearSecret = true
	if _, err = s.accessSettings.Save(input); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_OAUTH_GITHUB_SECRET", "legacy-again")
	t.Setenv("NEWPRESS_SMS_ENDPOINT", "https://sms.example.com/send")
	t.Setenv("NEWPRESS_SMS_TOKEN", "legacy-sms-again")
	if _, ok := s.loginProvider("github"); ok || s.smsReady() {
		t.Fatal("disabled config fell back to environment")
	}
}

func TestAccessSMSDeliveryFailureCooldownAndVerification(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.Fields.Email = "hidden"
	p.Fields.Phone = "required"
	p.PhoneVerification = true
	setEnhancedRegistration(t, s, "open", p)
	sms := accessconfig.DefaultSMS()
	sms.Enabled = true
	sms.Provider = "custom"
	sms.Endpoint = "https://sms.example.com/send"
	sms.Secret = "gateway-private"
	sms.SuccessPath = "code"
	sms.SuccessValue = "0"
	cfg := s.accessConfig()
	cfg.SMS = &sms
	if _, err := s.accessSettings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deliveredCode := ""
	s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var input map[string]string
		json.NewDecoder(r.Body).Decode(&input)
		deliveredCode = input["code"]
		if r.Header.Get("Authorization") != "Bearer gateway-private" || input["purpose"] != "register" {
			t.Fatal("gateway request invalid")
		}
		if calls == 1 {
			return oauthTestResponse(`{"code":500,"error":"gateway-private"}`), nil
		}
		return oauthTestResponse(`{"code":0}`), nil
	})}
	browser := registrationBrowser(t, h)
	body := `{"channel":"phone","target":"+8613800138000","purpose":"register"}`
	r := serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/code", body, registrationHeaders)
	if r.Code != 503 || strings.Contains(r.Body.String(), "gateway-private") {
		t.Fatal("business failure accepted or exposed", r.Code)
	}
	r = serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/code", body, registrationHeaders)
	if r.Code != 429 || calls != 1 {
		t.Fatal("failed request evaded cooldown", r.Code, calls)
	}
	s.registration.mu.Lock()
	for _, entry := range s.registration.codes {
		if !entry.Failed || entry.Ready || entry.Digest != "" {
			t.Fatal("failed code remained valid")
		}
		entry.Expires = time.Now().Add(-time.Second)
	}
	s.registration.mu.Unlock()
	r = serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/code", body, registrationHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var data struct{ Challenge string }
	decodeAPIResponse(t, r, &data)
	s.registration.mu.Lock()
	defer s.registration.mu.Unlock()
	if err := s.checkContact(secretDigest(browser.Value), "phone", "+8613800138000", "register", data.Challenge, deliveredCode); err != nil {
		t.Fatal("delivered code invalid", err)
	}
	if err := s.checkContact(secretDigest(browser.Value), "phone", "+8613800138000", "login", data.Challenge, deliveredCode); err == nil {
		t.Fatal("purpose bypass")
	}
}

func TestAccessSMSTestRequiresSavedRevisionAndNeverCreatesLoginProof(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	sms := accessconfig.DefaultSMS()
	sms.Enabled = true
	sms.Provider = "custom"
	sms.Endpoint = "https://sms.example.com/send"
	sms.Secret = "secret"
	cfg := s.accessConfig()
	cfg.SMS = &sms
	saved, err := s.accessSettings.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": owner.CSRFToken}
	calls := 0
	s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"purpose":"test"`) {
			t.Fatal("test purpose missing")
		}
		return oauthTestResponse(`{}`), nil
	})}
	for _, rev := range []string{"", cfg.Revision} {
		b, _ := json.Marshal(map[string]string{"revision": rev, "phone": "+8613800138000"})
		r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/access/sms-test", string(b), headers)
		if r.Code != 409 {
			t.Fatal("stale test allowed", r.Code)
		}
	}
	b, _ := json.Marshal(map[string]string{"revision": saved.Revision, "phone": "+8613800138000"})
	for i := 0; i < 4; i++ {
		r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/access/sms-test", string(b), headers)
		if i < 3 && r.Code != 200 || i == 3 && r.Code != 429 {
			t.Fatal("test send or rate limit", i, r.Code, r.Body.String())
		}
	}
	if calls != 3 || len(s.registration.codes) != 0 {
		t.Fatal("test generated login proof or failed to limit calls")
	}
}

func TestAccessAllOAuthProtocolsUseConsoleCredentials(t *testing.T) {
	for _, id := range site.LoginProviderOrder {
		t.Run(id, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			cfg := s.accessConfig()
			cfg.Providers = []accessconfig.Provider{{ID: id, Enabled: true, ClientID: "console-id", Secret: "console-secret"}}
			if _, err := s.accessSettings.Save(cfg); err != nil {
				t.Fatal(err)
			}
			p, ok := s.loginProvider(id)
			if !ok {
				t.Fatal("console provider missing")
			}
			s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
				r.ParseForm()
				if strings.Contains(r.URL.Path, "token") {
					secret := r.Form.Get("client_secret")
					if id == "wechat" {
						secret = r.Form.Get("secret")
					}
					if id == "linuxdo" {
						_, secret, _ = r.BasicAuth()
					}
					if secret != "console-secret" {
						t.Fatal("wrong client secret", id)
					}
					return oauthTestResponse(`{"access_token":"access","openid":"subject"}`), nil
				}
				if r.URL.Path == "/oauth2.0/me" {
					return oauthTestResponse(`{"client_id":"console-id","openid":"subject"}`), nil
				}
				return oauthTestResponse(`{"id":123,"sub":"subject","openid":"subject","ret":0,"name":"Reader"}`), nil
			})}
			req, _ := http.NewRequest("GET", "https://example.test", nil)
			subject, _, err := s.exchangeOAuth(req, oauthState{Provider: id, ClientID: p.ClientID, Redirect: s.oauthRedirect(id), Verifier: "verifier"}, "code", p.Secret)
			if err != nil || subject == "" {
				t.Fatal(id, err)
			}
		})
	}
}

func configureTestLoginProvider(t *testing.T, s *Server, id, clientID, secret string) {
	t.Helper()
	input := s.accessConfig()
	input.Providers = append(input.Providers, accessconfig.Provider{ID: id, Enabled: true, ClientID: clientID, Secret: secret})
	if _, err := s.accessSettings.Save(input); err != nil {
		t.Fatal(err)
	}
}
