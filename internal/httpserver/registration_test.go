package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/site"
)

func setEnhancedRegistration(t *testing.T, s *Server, mode string, p site.RegistrationPolicy) {
	t.Helper()
	snap := s.siteSettings.Snapshot()
	settings := snap.Settings
	settings.Accounts = &site.AccountSettings{Registration: mode, Policy: &p}
	if _, fields, err := s.siteSettings.Save(snap.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
}
func registrationBrowser(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	r := serveContentAPI(h, nil, "GET", "https://example.test/api/v1/social/access", "", nil)
	for _, cookie := range r.Result().Cookies() {
		if cookie.Name == accessBrowserCookie {
			return cookie
		}
	}
	t.Fatal("browser cookie missing")
	return nil
}
func registrationBody(t *testing.T, input registrationInput) string {
	t.Helper()
	b, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

var registrationHeaders = map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"}

func TestCodeLoginSwitchRejectsIssuedProofAndHidesEntrypoints(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	setEnhancedRegistration(t, s, "open", p)
	mail := &accessMailbox{}
	s.mailSender = mail
	cookie := registrationBrowser(t, h)
	send := func(purpose, target string) (string, string) {
		t.Helper()
		body := fmt.Sprintf(`{"channel":"email","target":%q,"purpose":%q}`, target, purpose)
		response := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code", body, registrationHeaders)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var data struct{ Challenge string }
		decodeAPIResponse(t, response, &data)
		return data.Challenge, regexp.MustCompile(`[0-9]{6}`).FindString(mail.messages[len(mail.messages)-1].body)
	}
	challenge, code := send("register", "switch@example.test")
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Username: "code-switch", DisplayName: "读者", Email: "switch@example.test", EmailChallenge: challenge, EmailCode: code, Password: "Registration password 2026", ConfirmPassword: "Registration password 2026"}
	response := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	challenge, code = send("login", input.Email)
	login := fmt.Sprintf(`{"channel":"email","target":%q,"challenge":%q,"code":%q}`, input.Email, challenge, code)
	enabled := false
	p.CodeLoginEnabled = enabled
	setEnhancedRegistration(t, s, "open", p)
	response = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/access", "", nil)
	if !strings.Contains(response.Body.String(), `"code_login_enabled":false`) {
		t.Fatal("disabled policy was not public", response.Body.String())
	}
	for _, path := range []string{"/", "/account/access/"} {
		response = serveContentAPI(h, nil, "GET", "https://example.test"+path, "", nil)
		if response.Code != 200 || strings.Contains(response.Body.String(), `data-account-register="login"`) || strings.Contains(response.Body.String(), `data-access-flow="login"`) {
			t.Fatal("disabled entry rendered", path, response.Code)
		}
	}
	assertCoreEntry := func(want bool) {
		t.Helper()
		var rendered strings.Builder
		view := s.baseView("账号", "", "/account/access/")
		view.BodyClass = "is-account-access"
		if err := coreui.Render(&rendered, "account-access", view); err != nil {
			t.Fatal("core account entry", err)
		}
		for _, marker := range []string{`data-account-register="login"`, `data-access-flow="login"`} {
			if strings.Contains(rendered.String(), marker) != want {
				t.Fatal("core account entry ignored switch", marker, want)
			}
		}
	}
	assertCoreEntry(false)
	for _, channel := range []string{"email", "phone"} {
		body := fmt.Sprintf(`{"channel":%q,"target":"switch@example.test","purpose":"login"}`, channel)
		response = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code", body, registrationHeaders)
		if response.Code != 403 || !strings.Contains(response.Body.String(), "auth.code_login_disabled") {
			t.Fatal("disabled code issuance accepted", channel, response.Code, response.Body.String())
		}
	}
	response = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code-login", login, registrationHeaders)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "auth.code_login_disabled") {
		t.Fatal("previously issued proof bypassed switch", response.Code, response.Body.String())
	}
	if len(mail.messages) != 2 {
		t.Fatal("disabled code login sent mail")
	}
	send("register", "enrollment@example.test")
	enabled = true
	p.CodeLoginEnabled = enabled
	setEnhancedRegistration(t, s, "open", p)
	assertCoreEntry(true)
	response = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code-login", login, registrationHeaders)
	if response.Code != 200 {
		t.Fatal("re-enabled code login failed", response.Code, response.Body.String())
	}
}

func TestRegistrationPhoneRegionRestrictions(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.Fields.Email = "hidden"
	p.Fields.Phone = "required"
	p.PhoneRegion = &site.PhoneRegionPolicy{Selectable: false, DefaultCode: "+86", Codes: []string{"+86", "+1"}}
	setEnhancedRegistration(t, s, "open", p)
	cookie := registrationBrowser(t, h)
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Username: "region-reader", DisplayName: "读者", Phone: "+12025550123", Password: "Registration password 2026", ConfirmPassword: "Registration password 2026"}
	response := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if response.Code != 422 || !strings.Contains(response.Body.String(), `"phone"`) {
		t.Fatal("forged fixed prefix", response.Code, response.Body.String())
	}
	for _, purpose := range []string{"register", "login"} {
		response = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code", `{"channel":"phone","target":"+12025550123","purpose":"`+purpose+`"}`, registrationHeaders)
		want := 422
		if purpose == "login" {
			want = 503
		} // No SMS adapter, but old verified accounts retain their region.
		if response.Code != want {
			t.Fatal(purpose, response.Code, response.Body.String())
		}
	}
	p.PhoneRegion.Selectable = true
	p.PhoneRegion.DefaultCode = "+1"
	setEnhancedRegistration(t, s, "open", p)
	response = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/access", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"default_code":"+1"`) {
		t.Fatal("public region", response.Code, response.Body.String())
	}
	response = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if response.Code != 200 {
		t.Fatal("allowed selectable prefix", response.Code, response.Body.String())
	}
	for _, binding := range []bool{false, true} {
		p.OAuthBindings = []string{"phone"}
		input.Phone = "+441234567890"
		if validateRegistrationInput(p, &input, binding)["phone"] == "" {
			t.Fatal("foreign prefix accepted", binding)
		}
	}
}

func TestEnhancedRegistrationRulesAndDurableReplay(t *testing.T) {
	s, h, m, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.Fields.Email = "hidden"
	p.Fields.DisplayName = "optional"
	p.Fields.Phone = "optional"
	p.Fields.Username = "hidden"
	p.EmailVerification = false
	setEnhancedRegistration(t, s, "open", p)
	cookie := registrationBrowser(t, h)
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Username: "forged-name", Email: "forged@example.test", Phone: "+8613800138000", Password: "Registration password 2026", ConfirmPassword: "different"}
	post := func() int {
		r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
		return r.Code
	}
	if code := post(); code != 422 {
		t.Fatalf("password confirmation = %d", code)
	}
	input.ConfirmPassword = input.Password
	if code := post(); code != 200 {
		t.Fatalf("registration = %d", code)
	}
	users, err := m.ListUsers(t.Context(), auth.UserQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var created auth.User
	for _, u := range users.Items {
		if u.Phone == input.Phone {
			created = u
		}
	}
	if created.ID == 0 || created.Email != "" || created.Username == input.Username || created.HasRole("administrator") || created.PhoneVerifiedAt != nil {
		t.Fatalf("unexpected identity: %#v", created)
	}
	if _, _, err = m.Authenticate("phone-test", input.Phone, input.Password); err != nil {
		t.Fatal("phone login", err)
	}
	if code := post(); code != 200 {
		t.Fatalf("replay = %d", code)
	}
	after, _ := m.ListUsers(t.Context(), auth.UserQuery{})
	if after.Total != users.Total {
		t.Fatal("duplicate account")
	}
	input.DisplayName = "changed"
	if code := post(); code != 409 {
		t.Fatalf("different replay = %d", code)
	}
	input.RequestID, _ = auth.RandomCredential()
	if code := post(); code != 409 {
		t.Fatalf("duplicate phone = %d", code)
	}
}

func TestEnhancedRegistrationVerificationAndInvitationCannotBeForged(t *testing.T) {
	s, h, m, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	mail := &accessMailbox{}
	s.mailSender = mail
	setEnhancedRegistration(t, s, "open", p)
	cookie := registrationBrowser(t, h)
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Username: "proof-reader", DisplayName: "读者", Email: "proof@example.test", Password: "Verified password 2026", ConfirmPassword: "Verified password 2026"}
	post := func() *http.Response {
		r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
		return r.Result()
	}
	if r := post(); r.StatusCode != 422 {
		t.Fatal(r.StatusCode)
	}
	r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code", `{"channel":"email","target":"proof@example.test","purpose":"register"}`, registrationHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var code struct {
		Challenge string `json:"challenge"`
	}
	decodeAPIResponse(t, r, &code)
	input.EmailChallenge = code.Challenge
	input.EmailCode = regexp.MustCompile(`[0-9]{6}`).FindString(mail.messages[0].body)
	other := registrationBrowser(t, h)
	r = serveContentAPI(h, other, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if r.Code != 422 {
		t.Fatal("cross-browser code accepted", r.Code)
	}
	if r := post(); r.StatusCode != 200 {
		data, _ := io.ReadAll(r.Body)
		t.Fatal(r.StatusCode, string(data))
	}
	u, err := m.FindRegistration(t.Context(), "email", input.Email)
	if err != nil || u.EmailVerifiedAt == nil {
		t.Fatal("email proof absent", err)
	}
	setEnhancedRegistration(t, s, "invite", p)
	input.RequestID, _ = auth.RandomCredential()
	input.Kind = "invite"
	input.Token = ""
	input.Username = "forged-invite"
	input.Email = "other@example.test"
	if r := post(); r.StatusCode != 422 {
		t.Fatal("kind forged an invitation", r.StatusCode)
	}
	r = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/social/access/complete", `{"kind":"register","token":"anything","username":"legacy-bypass","password":"Registration password 2026"}`, registrationHeaders)
	if r.Code != 400 {
		t.Fatal("legacy endpoint bypass", r.Code)
	}
}

func TestEnhancedInvitationLinkHonorsExtraFields(t *testing.T) {
	s, h, m, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.Fields.Phone = "required"
	p.PhoneVerification = false
	p.Fields.InvitationCode = "required"
	rp := p.Referrals()
	rp.Enabled = true
	p.Referral = &rp
	setEnhancedRegistration(t, s, "invite", p)
	raw, _, err := m.IssueAccess(t.Context(), auth.AccessInvite, "invite-enhanced@example.test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Token: raw, Kind: auth.AccessInvite, Username: "enhanced-invite", DisplayName: "被邀请者", Phone: "+8613800138001", Password: "Invite password 2026", ConfirmPassword: "Invite password 2026"}
	r := serveContentAPI(h, registrationBrowser(t, h), "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	u, err := m.FindRegistration(t.Context(), "phone", input.Phone)
	if err != nil || u.EmailVerifiedAt == nil || u.Email != "invite-enhanced@example.test" {
		t.Fatal(u, err)
	}
}

type oauthTestTransport func(*http.Request) (*http.Response, error)

func (fn oauthTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func oauthTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestOAuthProvidersProtocolsAndBindingGate(t *testing.T) {
	for _, id := range site.LoginProviderOrder {
		t.Run(id, func(t *testing.T) {
			s, h, m, _, _, _ := newPersistentAdminTestServer(t)

			p := site.DefaultRegistrationPolicy()
			configureTestLoginProvider(t, s, id, "client-fixture", "provider-test-secret")
			p.OAuthBindings = []string{"email"}
			setEnhancedRegistration(t, s, "open", p)
			calls := 0
			s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() == oauthEndpoints[id].Token || r.URL.Path == "/oauth2.0/token" || r.URL.Path == "/sns/oauth2/access_token" {
					r.ParseForm()
					if r.Form.Get("code") != "authorization-code" {
						t.Error("authorization code missing")
					}
					if oauthEndpoints[id].PKCE && r.Form.Get("code_verifier") == "" {
						t.Error("PKCE verifier missing")
					}
					if id == "linuxdo" {
						client, secret, ok := r.BasicAuth()
						if !ok || client != "client-fixture" || secret != "provider-test-secret" || r.Form.Get("client_secret") != "" {
							t.Error("Linux DO client authentication missing or duplicated")
						}
					}
					return oauthTestResponse(`{"access_token":"access-fixture","openid":"subject-fixture"}`), nil
				}
				if r.URL.Path == "/oauth2.0/me" {
					return oauthTestResponse(`{"client_id":"client-fixture","openid":"subject-fixture"}`), nil
				}
				return oauthTestResponse(`{"id":1234,"sub":"subject-fixture","openid":"subject-fixture","name":"OAuth reader","nickname":"OAuth reader","ret":0,"email":"owner@example.test"}`), nil
			})}
			start := serveContentAPI(h, nil, "GET", "https://example.test/auth/"+id+"/start", "", nil)
			if start.Code != 302 {
				t.Fatal(start.Code, start.Body.String())
			}
			redirect, _ := url.Parse(start.Header().Get("Location"))
			state := redirect.Query().Get("state")
			if state == "" || oauthEndpoints[id].PKCE && redirect.Query().Get("code_challenge") == "" {
				t.Fatal("missing state/PKCE")
			}
			var cookie *http.Cookie
			for _, c := range start.Result().Cookies() {
				if c.Name == accessBrowserCookie {
					cookie = c
				}
			}
			callback := "https://example.test/auth/" + id + "/callback?code=authorization-code&state=" + url.QueryEscape(state)
			foreign := serveContentAPI(h, nil, "GET", callback, "", nil)
			if foreign.Code != 303 || calls != 0 {
				t.Fatal("foreign callback exchanged credentials")
			}
			response := serveContentAPI(h, cookie, "GET", callback, "", nil)
			if response.Code != 303 || response.Header().Get("Location") != "/account/access/?flow=bind" {
				t.Fatal(response.Code, response.Header())
			}
			for _, c := range response.Result().Cookies() {
				if c.Name == auth.CookieName && c.MaxAge > 0 {
					t.Fatal("session issued before required binding")
				}
			}
			before := calls
			serveContentAPI(h, cookie, "GET", callback, "", nil)
			if calls != before {
				t.Fatal("OAuth state replayed")
			}
			users, _ := m.ListUsers(t.Context(), auth.UserQuery{Search: "OAuth reader"})
			if users.Total != 0 {
				t.Fatal("provider email auto-linked or created a user")
			}
			pending := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/access/pending", "", nil)
			if pending.Code != 200 || strings.Contains(pending.Body.String(), "subject-fixture") || strings.Contains(pending.Body.String(), "owner@example.test") {
				t.Fatal("pending identity leaked", pending.Body.String())
			}
		})
	}
}

func TestOAuthExistingAccountRequiresNewBinding(t *testing.T) {
	s, h, m, _, _, _ := newPersistentAdminTestServer(t)

	p := site.DefaultRegistrationPolicy()
	p.Fields.Phone = "required"
	p.Fields.Email = "hidden"
	p.EmailVerification = false
	p.OAuthBindings = []string{"phone"}
	configureTestLoginProvider(t, s, "github", "fixture", "fixture-secret")
	setEnhancedRegistration(t, s, "open", p)
	key, _ := auth.RandomCredential()
	u, err := m.RegisterAccount(t.Context(), auth.UserInput{Username: "bound-existing", DisplayName: "绑定读者", OAuthProvider: "github", ExternalKey: secretDigest("github:fixture:123"), RegistrationKey: secretDigest(key), RegistrationDigest: secretDigest(key), DisablePassword: true}, key)
	if err != nil {
		t.Fatal(err)
	}
	cookie := registrationBrowser(t, h)
	browser := secretDigest(cookie.Value)
	s.registration.mu.Lock()
	s.registration.prepare()
	s.registration.pending[browser] = oauthPending{Provider: "github", ExternalKey: secretDigest("github:fixture:123"), Name: "绑定读者", Fingerprint: secretDigest("fixture:fixture-secret"), BindingID: "binding-fixture", UserID: u.ID, Expires: time.Now().Add(time.Minute)}
	s.registration.mu.Unlock()
	request, _ := auth.RandomCredential()
	input := registrationInput{RequestID: request, BindingID: "other-tab-binding", Phone: "+8613800138002"}
	stale := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/bind", registrationBody(t, input), registrationHeaders)
	if stale.Code != 401 {
		t.Fatal("an older tab bound a newer OAuth identity", stale.Code)
	}
	input.BindingID = "binding-fixture"
	r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/bind", registrationBody(t, input), registrationHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	updated, err := m.GetUser(t.Context(), u.ID)
	if err != nil || updated.Phone != input.Phone || updated.Username != u.Username || updated.PasswordLogin {
		t.Fatal(updated, err)
	}
	replay := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/bind", registrationBody(t, input), registrationHeaders)
	if replay.Code != 200 {
		t.Fatal("binding response cannot be reconciled", replay.Code, replay.Body.String())
	}
	unchanged, _ := m.GetUser(t.Context(), u.ID)
	if unchanged.Version != updated.Version {
		t.Fatal("binding replay changed the account twice")
	}
}

func TestEnhancedPasswordlessRegistrationAndCodeLogin(t *testing.T) {
	s, h, m, _, _, owner := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.Fields.Password = "hidden"
	p.Fields.Username = "hidden"
	p.Fields.DisplayName = "optional"
	setEnhancedRegistration(t, s, "open", p)
	mail := &accessMailbox{}
	s.mailSender = mail
	cookie := registrationBrowser(t, h)
	sendCode := func(purpose string) (string, string) {
		t.Helper()
		r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code", `{"channel":"email","target":"passwordless@example.test","purpose":"`+purpose+`"}`, registrationHeaders)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var response struct {
			Challenge string `json:"challenge"`
		}
		decodeAPIResponse(t, r, &response)
		return response.Challenge, regexp.MustCompile(`[0-9]{6}`).FindString(mail.messages[len(mail.messages)-1].body)
	}
	challenge, code := sendCode("register")
	request, _ := auth.RandomCredential()
	input := registrationInput{RequestID: request, Email: "passwordless@example.test", Password: "Forged hidden password 2026", EmailChallenge: challenge, EmailCode: code}
	r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	u, err := m.FindRegistration(t.Context(), "email", input.Email)
	if err != nil || u.PasswordLogin || u.EmailVerifiedAt == nil {
		t.Fatal("passwordless verification missing", err)
	}
	if _, _, err = m.Authenticate("passwordless-test", u.Username, input.Password); err == nil {
		t.Fatal("hidden password allowed login")
	}
	challenge, code = sendCode("login")
	login := fmt.Sprintf(`{"channel":"email","target":"passwordless@example.test","challenge":%q,"code":%q}`, challenge, code)
	r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code-login", login, registrationHeaders)
	if r.Code != 200 {
		t.Fatal("code login", r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/code-login", login, registrationHeaders)
	if r.Code != 422 {
		t.Fatal("used contact proof accepted", r.Code)
	}
	// Any later account mutation invalidates the receipt used to reconcile a
	// lost registration response, including a password reset.
	if _, err = m.UpdateUserPassword(t.Context(), owner, u.ID, u.Version, "Updated passwordless password 2026"); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
	if r.Code != 409 {
		t.Fatal("stale receipt logged in after password change", r.Code)
	}
}
