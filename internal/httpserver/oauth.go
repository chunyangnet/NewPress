package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

type oauthState struct {
	BindingUser                                                  int64
	BindingSession                                               string
	Browser, Provider, ClientID, Fingerprint, Verifier, Redirect string
	Expires                                                      time.Time
}
type oauthPending struct {
	Provider, ExternalKey, Name, Fingerprint, BindingID string
	UserID                                              int64
	Expires                                             time.Time
}
type oauthEndpoint struct {
	Authorize, Token, User, Scope string
	PKCE                          bool
}

type loginProviderView struct{ ID, Name, Mark string }

func (s *Server) publicLoginProviders() []loginProviderView {
	items := []loginProviderView{}
	marks := map[string]string{"qq": "QQ", "github": "GH", "wechat": "微", "microsoft": "M", "google": "G", "linuxdo": "L"}
	for _, id := range site.LoginProviderOrder {
		if _, ok := s.loginProvider(id); ok && s.oauthRedirect(id) != "" {
			items = append(items, loginProviderView{id, site.LoginProviderNames[id], marks[id]})
		}
	}
	return items
}

var oauthEndpoints = map[string]oauthEndpoint{
	"github":    {"https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com/user", "read:user", true},
	"google":    {"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://openidconnect.googleapis.com/v1/userinfo", "openid profile", true},
	"microsoft": {"https://login.microsoftonline.com/common/oauth2/v2.0/authorize", "https://login.microsoftonline.com/common/oauth2/v2.0/token", "https://graph.microsoft.com/oidc/userinfo", "openid profile", true},
	"linuxdo":   {"https://connect.linux.do/oauth2/authorize", "https://connect.linux.do/oauth2/token", "https://connect.linux.do/api/user", "", false},
	"qq":        {"https://graph.qq.com/oauth2.0/authorize", "https://graph.qq.com/oauth2.0/token", "https://graph.qq.com/user/get_user_info", "get_user_info", false},
	"wechat":    {"https://open.weixin.qq.com/connect/qrconnect", "https://api.weixin.qq.com/sns/oauth2/access_token", "https://api.weixin.qq.com/sns/userinfo", "snsapi_login", false},
}

func (s *Server) loginProvider(id string) (accessconfig.Provider, bool) {
	if oauthEndpoints[id].Authorize != "" && s.auth.Configured() {
		for _, p := range s.accessConfig().Providers {
			if p.ID == id && p.Enabled && p.ClientID != "" && p.Secret != "" {
				return p, true
			}
		}
	}
	return accessconfig.Provider{}, false
}
func (s *Server) oauthRedirect(id string) string {
	base := s.accessConfig().CallbackBaseURL
	if base == "" {
		base = s.config.PublicURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return ""
	}
	u.Path = "/auth/" + id + "/callback"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func (s *Server) oauthFailure(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/account/access/#oauth_error=1", http.StatusSeeOther)
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var bindingUser int64
	var bindingSession string
	if r.Method == http.MethodPost {
		p, ok := s.socialPrincipal(w, r, true)
		if !ok {
			return
		}
		_, token, ok := s.currentAdminSession(r)
		if !ok {
			return
		}
		bindingUser, bindingSession = p.UserID, secretDigest(token)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	id := r.PathValue("provider")
	provider, ok := s.loginProvider(id)
	if !ok || s.oauthRedirect(id) == "" {
		http.NotFound(w, r)
		return
	}
	if allowed, _ := s.accessRate.allow("oauth:"+s.commentGuard.sourceDigest(s.clientIPs.string(r)), 30, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "auth.rate_limited", "尝试次数过多，请稍后再试", nil)
		return
	}
	raw, err := auth.RandomCredential()
	if err != nil {
		s.oauthFailure(w, r)
		return
	}
	verifier, err := auth.RandomCredential()
	if err != nil {
		s.oauthFailure(w, r)
		return
	}
	browser := s.accessBrowser(w, r, true)
	if browser == "" {
		s.oauthFailure(w, r)
		return
	}
	s.registration.mu.Lock()
	s.registration.prepare()
	if len(s.registration.states) >= 1000 {
		s.registration.mu.Unlock()
		s.oauthFailure(w, r)
		return
	}
	state := oauthState{Browser: browser, Provider: id, ClientID: provider.ClientID, Fingerprint: secretDigest(provider.ClientID + ":" + provider.Secret), Verifier: verifier, Redirect: s.oauthRedirect(id), Expires: time.Now().Add(10 * time.Minute), BindingUser: bindingUser, BindingSession: bindingSession}
	s.registration.states[secretDigest(raw)] = state
	s.registration.mu.Unlock()
	endpoint := oauthEndpoints[id]
	u, _ := url.Parse(endpoint.Authorize)
	q := url.Values{"client_id": {provider.ClientID}, "redirect_uri": {state.Redirect}, "response_type": {"code"}, "state": {raw}}
	if endpoint.Scope != "" {
		q.Set("scope", endpoint.Scope)
	}
	if endpoint.PKCE {
		d := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(d[:]))
		q.Set("code_challenge_method", "S256")
	}
	if id == "wechat" {
		q.Del("client_id")
		q.Set("appid", provider.ClientID)
		u.Fragment = "wechat_redirect"
	}
	u.RawQuery = q.Encode()
	if bindingUser > 0 {
		writeJSON(w, 200, map[string]string{"url": u.String()})
		return
	}
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"code": true, "state": true, "error": true, "error_description": true, "error_uri": true, "scope": true, "authuser": true, "prompt": true, "session_state": true})
	if !ok {
		return
	}
	id := r.PathValue("provider")
	provider, ok := s.loginProvider(id)
	if !ok {
		s.oauthFailure(w, r)
		return
	}
	browser := s.accessBrowser(w, r, false)
	s.registration.mu.Lock()
	s.registration.prepare()
	state, valid := s.registration.states[secretDigest(q.Get("state"))]
	if valid && browser != "" && state.Browser == browser && state.Provider == id {
		delete(s.registration.states, secretDigest(q.Get("state")))
	} else {
		valid = false
	}
	s.registration.mu.Unlock()
	if valid && state.BindingUser > 0 {
		p, token, ok := s.currentAdminSession(r)
		if !ok || p.UserID != state.BindingUser || !secretEqual(state.BindingSession, secretDigest(token)) {
			http.Redirect(w, r, "/account/?view=security#binding_error", http.StatusSeeOther)
			return
		}
	}
	if !valid || state.Redirect != s.oauthRedirect(id) || !secretEqual(state.Fingerprint, secretDigest(provider.ClientID+":"+provider.Secret)) || q.Get("error") != "" || q.Get("code") == "" || len(q.Get("code")) > 4096 {
		s.oauthFailure(w, r)
		return
	}
	subject, name, err := s.exchangeOAuth(r, state, q.Get("code"), provider.Secret)
	if err != nil {
		s.oauthFailure(w, r)
		return
	}
	current, enabled := s.loginProvider(id)
	if !enabled || state.Redirect != s.oauthRedirect(id) || !secretEqual(state.Fingerprint, secretDigest(current.ClientID+":"+current.Secret)) {
		s.oauthFailure(w, r)
		return
	}
	external := secretDigest(id + ":" + provider.ClientID + ":" + subject)
	if state.BindingUser > 0 {
		p, token, ok := s.currentAdminSession(r)
		if !ok || p.UserID != state.BindingUser || !secretEqual(state.BindingSession, secretDigest(token)) {
			http.Redirect(w, r, "/account/?view=security#binding_error", http.StatusSeeOther)
			return
		}
		user, err := s.auth.GetUser(r.Context(), state.BindingUser)
		if err == nil {
			_, err = s.auth.UpdateOAuthBinding(r.Context(), user, id, external)
		}
		result := "binding_success"
		if err != nil {
			result = "binding_error"
		}
		http.Redirect(w, r, "/account/?view=security#"+result, http.StatusSeeOther)
		return
	}
	u, err := s.auth.FindRegistration(r.Context(), "external", external)
	if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
		s.oauthFailure(w, r)
		return
	}
	if err == nil && u.Status != auth.UserStatusActive {
		s.oauthFailure(w, r)
		return
	}
	settings := s.siteSettings.Snapshot().Settings
	p := settings.RegistrationPolicy()
	if u.ID == 0 && settings.AccountRegistration() == "closed" {
		s.oauthFailure(w, r)
		return
	}
	needs := u.ID == 0 && (len(p.OAuthBindings) > 0 || settings.AccountRegistration() == "invite" || p.Fields.InvitationCode == "required" || p.Referrals().Enabled && p.Fields.InvitationCode == "optional" || p.Fields.RegistrationCode == "required") || u.ID > 0 && len(missingOAuthBindings(p, u)) > 0
	if needs {
		bindingID, err := auth.RandomCredential()
		if err != nil {
			s.oauthFailure(w, r)
			return
		}
		s.registration.mu.Lock()
		s.registration.prepare()
		if len(s.registration.pending) >= 1000 {
			s.registration.mu.Unlock()
			s.oauthFailure(w, r)
			return
		}
		s.registration.pending[browser] = oauthPending{Provider: id, ExternalKey: external, Name: name, Fingerprint: state.Fingerprint, BindingID: bindingID, UserID: u.ID, Expires: time.Now().Add(15 * time.Minute)}
		s.registration.mu.Unlock()
		http.Redirect(w, r, "/account/access/?flow=bind", http.StatusSeeOther)
		return
	}
	if u.ID == 0 {
		random, err := auth.RandomCredential()
		if err != nil {
			s.oauthFailure(w, r)
			return
		}
		u, err = s.auth.RegisterAccount(r.Context(), auth.UserInput{Username: "member_" + strings.ToLower(random[:16]), DisplayName: name, Locale: "zh-CN", ExternalKey: external, OAuthProvider: id, RegistrationKey: secretDigest("oauth:" + external), RegistrationDigest: secretDigest("oauth:" + external), DisablePassword: true}, random)
		if err != nil {
			s.oauthFailure(w, r)
			return
		}
	}
	token, err := s.auth.LoginVerifiedUser(r.Context(), u.ID, auth.SessionMetadata{IPAddress: s.clientIPs.string(r), UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, auth.ErrSessionUnavailable) {
			s.sessionStorageError(w, r)
			return
		}
		s.oauthFailure(w, r)
		return
	}
	if !s.replaceBrowserSession(w, r, token) {
		return
	}
	if s.currencyRewards != nil {
		_ = s.currencyRewards.RegistrationAll(r.Context(), u.ID)
	}
	if s.progression != nil {
		_, _ = s.progression.Registration(r.Context(), u.ID)
	}
	http.Redirect(w, r, "/account/", http.StatusSeeOther)
}

func missingOAuthBindings(p site.RegistrationPolicy, u auth.User) []string {
	missing := []string{}
	for _, key := range p.OAuthBindings {
		empty := false
		switch key {
		case "username":
			empty = u.Username == ""
		case "display_name":
			empty = u.DisplayName == ""
		case "password":
			empty = !u.PasswordLogin
		case "email":
			empty = u.Email == "" || p.EmailVerification && u.EmailVerifiedAt == nil
		case "phone":
			empty = u.Phone == "" || p.PhoneVerification && u.PhoneVerifiedAt == nil
		}
		if empty {
			missing = append(missing, key)
		}
	}
	return missing
}
func (s *Server) socialOAuthPending(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	browser := s.accessBrowser(w, r, false)
	s.registration.mu.Lock()
	s.registration.prepare()
	pending, ok := s.registration.pending[browser]
	s.registration.mu.Unlock()
	if !ok {
		s.writeAPIError(w, r, 401, "auth.binding_expired", "快捷登录已过期，请重新授权", nil)
		return
	}
	provider, enabled := s.loginProvider(pending.Provider)
	if !enabled || !secretEqual(pending.Fingerprint, secretDigest(provider.ClientID+":"+provider.Secret)) {
		s.writeAPIError(w, r, 401, "auth.binding_expired", "登录配置已更新，请重新授权", nil)
		return
	}
	p := s.siteSettings.Snapshot().Settings.RegistrationPolicy()
	u := auth.User{}
	if pending.UserID > 0 {
		var err error
		u, err = s.auth.GetUser(r.Context(), pending.UserID)
		if err != nil {
			s.accessError(w, r, err)
			return
		}
		// The browser proof must still refer to an active, bound identity
		// before it can disclose the account's current contact details.
		if u.Status != auth.UserStatusActive || !secretEqual(u.OAuthBindings[pending.Provider], pending.ExternalKey) {
			s.writeAPIError(w, r, 401, "auth.binding_expired", "快捷登录已过期，请重新授权", nil)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"provider": site.LoginProviderNames[pending.Provider], "binding_id": pending.BindingID, "display_name": pending.Name, "existing": pending.UserID > 0, "required": missingOAuthBindings(p, u), "email": u.Email, "phone": u.Phone, "username": u.Username})
}

func (s *Server) oauthJSON(r *http.Request, method, endpoint string, form url.Values, bearer string) (map[string]any, error) {
	// Linux DO authenticates confidential clients with HTTP Basic at the token
	// endpoint. Send credentials in one place, never in a userinfo request.
	basicID, basicSecret := "", ""
	if method == http.MethodPost && endpoint == oauthEndpoints["linuxdo"].Token {
		basicID, basicSecret = form.Get("client_id"), form.Get("client_secret")
		form.Del("client_id")
		form.Del("client_secret")
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	} else if len(form) > 0 {
		endpoint += "?" + form.Encode()
	}
	req, err := http.NewRequestWithContext(r.Context(), method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NewPress OAuth")
	if basicID != "" {
		req.SetBasicAuth(basicID, basicSecret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := s.registrationClient().Do(req)
	if err != nil {
		return nil, errors.New("oauth transport unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 || response.StatusCode != 200 {
		return nil, errors.New("oauth response invalid")
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil {
		return nil, errors.New("oauth response invalid")
	}
	if result["error"] != nil || result["errcode"] != nil && oauthString(result["errcode"]) != "0" {
		return nil, errors.New("oauth rejected")
	}
	return result, nil
}
func oauthString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
func (s *Server) exchangeOAuth(r *http.Request, state oauthState, code, secret string) (string, string, error) {
	id := state.Provider
	endpoint := oauthEndpoints[id]
	form := url.Values{"client_id": {state.ClientID}, "client_secret": {secret}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {state.Redirect}}
	method := http.MethodPost
	if endpoint.PKCE {
		form.Set("code_verifier", state.Verifier)
	}
	if id == "qq" {
		method = http.MethodGet
		form.Set("fmt", "json")
	}
	if id == "wechat" {
		method = http.MethodGet
		form.Del("client_id")
		form.Del("client_secret")
		form.Del("redirect_uri")
		form.Set("appid", state.ClientID)
		form.Set("secret", secret)
	}
	tokens, err := s.oauthJSON(r, method, endpoint.Token, form, "")
	if err != nil {
		return "", "", err
	}
	access := oauthString(tokens["access_token"])
	if access == "" || len(access) > 8192 {
		return "", "", errors.New("missing access token")
	}
	var profile map[string]any
	subject := ""
	if id == "qq" {
		identity, e := s.oauthJSON(r, "GET", "https://graph.qq.com/oauth2.0/me", url.Values{"access_token": {access}, "fmt": {"json"}}, "")
		if e != nil || oauthString(identity["client_id"]) != state.ClientID {
			return "", "", errors.New("qq identity invalid")
		}
		subject = oauthString(identity["openid"])
		if subject == "" {
			return "", "", errors.New("missing openid")
		}
		profile, err = s.oauthJSON(r, "GET", endpoint.User, url.Values{"access_token": {access}, "oauth_consumer_key": {state.ClientID}, "openid": {subject}, "format": {"json"}}, "")
	} else if id == "wechat" {
		subject = oauthString(tokens["openid"])
		if subject == "" {
			return "", "", errors.New("missing openid")
		}
		profile, err = s.oauthJSON(r, "GET", endpoint.User, url.Values{"access_token": {access}, "openid": {subject}, "lang": {"zh_CN"}}, "")
		if err == nil && oauthString(profile["openid"]) != subject {
			return "", "", errors.New("wechat identity mismatch")
		}
	} else {
		profile, err = s.oauthJSON(r, "GET", endpoint.User, nil, access)
		key := "id"
		if id == "microsoft" || id == "google" {
			key = "sub"
		}
		subject = oauthString(profile[key])
	}
	if err != nil || subject == "" || len(subject) > 512 {
		return "", "", errors.New("oauth identity missing")
	}
	if id == "qq" && oauthString(profile["ret"]) != "0" {
		return "", "", errors.New("qq profile rejected")
	}
	name := oauthString(profile["name"])
	if name == "" {
		name = oauthString(profile["nickname"])
	}
	if name == "" {
		name = oauthString(profile["login"])
	}
	if name == "" {
		name = oauthString(profile["username"])
	}
	if name == "" {
		name = site.LoginProviderNames[id] + "用户"
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 160 {
		name = string([]rune(name)[:160])
	}
	return subject, name, nil
}
