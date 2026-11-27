package httpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/safehttp"
	"newpress.local/newpress/internal/site"
)

const accessBrowserCookie = "np_access_browser"

type registrationRuntime struct {
	mu      sync.Mutex
	codes   map[string]*contactCode
	states  map[string]oauthState
	pending map[string]oauthPending
	client  *http.Client // injectable transport for protocol tests
}
type contactCode struct {
	Browser, Channel, Target, Purpose, Digest string
	Expires, Sent                             time.Time
	Attempts                                  int
	Ready                                     bool
	Failed                                    bool
}

func secretDigest(value string) string {
	d := sha256.Sum256([]byte(value))
	return hex.EncodeToString(d[:])
}
func secretEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (rt *registrationRuntime) prepare() {
	if rt.codes == nil {
		rt.codes = map[string]*contactCode{}
		rt.states = map[string]oauthState{}
		rt.pending = map[string]oauthPending{}
	}
	now := time.Now()
	for key, item := range rt.codes {
		if !now.Before(item.Expires) {
			delete(rt.codes, key)
		}
	}
	for key, item := range rt.states {
		if !now.Before(item.Expires) {
			delete(rt.states, key)
		}
	}
	for key, item := range rt.pending {
		if !now.Before(item.Expires) {
			delete(rt.pending, key)
		}
	}
}
func (s *Server) accessBrowser(w http.ResponseWriter, r *http.Request, create bool) string {
	if c, err := r.Cookie(accessBrowserCookie); err == nil {
		if _, err := auth.AccessHash(c.Value); err == nil {
			return secretDigest(c.Value)
		}
	}
	if !create {
		return ""
	}
	raw, err := auth.RandomCredential()
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: accessBrowserCookie, Value: raw, Path: "/", MaxAge: 1800, HttpOnly: true, Secure: strings.HasPrefix(s.config.PublicURL, "https://"), SameSite: http.SameSiteLaxMode})
	return secretDigest(raw)
}
func (s *Server) registrationClient() *http.Client {
	if s.registration.client != nil {
		return s.registration.client
	}
	return safehttp.Client(6 * time.Second)
}
func (s *Server) smsReady() bool {
	return s.auth.Configured() && accessconfig.SMSReady(s.accessConfig().SMS)
}

func (s *Server) registrationPublic(w http.ResponseWriter, r *http.Request) map[string]any {
	settings := s.siteSettings.Snapshot().Settings
	p := settings.RegistrationPolicy()
	if settings.AccountRegistration() == "invite" && p.Referrals().Enabled {
		p.Fields.InvitationCode = "required"
	}
	s.accessBrowser(w, r, true)
	providers := []map[string]string{}
	for _, id := range site.LoginProviderOrder {
		if _, ok := s.loginProvider(id); ok && s.oauthRedirect(id) != "" {
			providers = append(providers, map[string]string{"id": id, "name": site.LoginProviderNames[id], "url": "/auth/" + id + "/start"})
		}
	}
	return map[string]any{"registration": settings.AccountRegistration(), "email_available": s.accessDeliveryReady(), "sms_available": s.smsReady(), "recovery_available": s.accessDeliveryReady(), "minimum_password_length": 12, "enhanced": true, "fields": p.Fields, "email_verification": p.EmailVerification, "phone_verification": p.PhoneVerification, "phone_region": p.PhoneRegions(), "code_login_enabled": p.AllowsCodeLogin(), "password_confirmation": p.PasswordConfirmation, "registration_code_url": p.RegistrationCodeURL, "oauth_bindings": p.OAuthBindings, "providers": providers, "configured": s.auth.Configured()}
}

func (s *Server) socialRegistrationCode(w http.ResponseWriter, r *http.Request) {
	if !s.allowAccess(w, r, "code", 12) {
		return
	}
	var input struct {
		Channel           string `json:"channel"`
		Target            string `json:"target"`
		Purpose           string `json:"purpose"`
		VerificationToken string `json:"verification_token"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	browser := s.accessBrowser(w, r, false)
	if browser == "" {
		s.writeAPIError(w, r, 409, "auth.reload_required", "请刷新页面后重试", nil)
		return
	}
	if input.Purpose != "register" && input.Purpose != "login" && input.Purpose != "bind" {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	settings := s.siteSettings.Snapshot().Settings
	if input.Purpose == "login" && !settings.RegistrationPolicy().AllowsCodeLogin() {
		s.writeAPIError(w, r, 403, "auth.code_login_disabled", "本站已关闭验证码登录，请使用密码或快捷登录", nil)
		return
	}
	if input.Purpose == "register" && settings.AccountRegistration() == "closed" {
		s.writeAPIError(w, r, 403, "auth.registration_closed", "本站暂未开放注册", nil)
		return
	}
	if input.Purpose == "bind" {
		s.registration.mu.Lock()
		s.registration.prepare()
		pending, ok := s.registration.pending[browser]
		s.registration.mu.Unlock()
		provider, enabled := s.loginProvider(pending.Provider)
		ok = ok && enabled && secretEqual(pending.Fingerprint, secretDigest(provider.ClientID+":"+provider.Secret))
		if !ok {
			s.writeAPIError(w, r, 401, "auth.binding_expired", "快捷登录已过期，请重新授权", nil)
			return
		}
	}
	input.Target = strings.TrimSpace(input.Target)
	available := false
	if input.Channel == "email" {
		var err error
		input.Target, err = auth.NormalizeAccessEmail(input.Target)
		if err != nil {
			s.accessError(w, r, err)
			return
		}
		available = s.accessDeliveryReady()
	} else if input.Channel == "phone" && auth.ValidPhone(input.Target) {
		if input.Purpose != "login" && !s.siteSettings.Snapshot().Settings.RegistrationPolicy().AllowsPhone(input.Target) {
			s.accessError(w, r, &auth.UserValidationError{Fields: map[string]string{"phone": "请选择本站允许的手机区号"}})
			return
		}
		available = s.smsReady()
	} else {
		s.writeAPIError(w, r, 422, "auth.invalid_input", "请输入有效的邮箱或带国家区号的手机号", nil)
		return
	}
	if !available {
		s.writeAPIError(w, r, 503, "auth.delivery_unavailable", "此验证通道暂不可用，请联系管理员", nil)
		return
	}
	scene := "register"
	if input.Purpose == "login" {
		scene = "login"
	}
	if !s.requireVerification(w, r, scene, input.VerificationToken) {
		return
	}
	s.registration.mu.Lock()
	s.registration.prepare()
	for _, old := range s.registration.codes {
		if old.Channel == input.Channel && old.Target == input.Target && time.Since(old.Sent) < time.Minute {
			s.registration.mu.Unlock()
			w.Header().Set("Retry-After", "60")
			s.writeAPIError(w, r, 429, "auth.code_cooldown", "请一分钟后重新获取验证码", nil)
			return
		}
	}
	if len(s.registration.codes) >= 1000 {
		s.registration.mu.Unlock()
		s.writeAPIError(w, r, 429, "auth.rate_limited", "请稍后再试", nil)
		return
	}
	raw, err := auth.RandomCredential()
	if err != nil {
		s.registration.mu.Unlock()
		s.accessError(w, r, err)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		s.registration.mu.Unlock()
		s.accessError(w, r, err)
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	key := secretDigest(raw)
	entry := &contactCode{Browser: browser, Channel: input.Channel, Target: input.Target, Purpose: input.Purpose, Digest: secretDigest(raw + ":" + code), Expires: time.Now().Add(10 * time.Minute), Sent: time.Now()}
	s.registration.codes[key] = entry
	s.registration.mu.Unlock()
	if input.Channel == "email" {
		err = s.mailSender.Send(r.Context(), input.Target, "NewPress · 验证码", "你的验证码是："+code+"\n10 分钟内有效，请勿转发。如果不是本人操作，请忽略。")
	} else {
		sms := s.accessConfig().SMS
		if sms == nil {
			err = errors.New("sms unavailable")
		} else {
			err = accessconfig.SendSMS(r.Context(), *sms, input.Target, code, input.Purpose, s.registration.client)
		}
	}
	s.registration.mu.Lock()
	entry.Ready = err == nil
	if err != nil {
		entry.Failed = true
		entry.Expires = entry.Sent.Add(time.Minute)
		entry.Digest = ""
	}
	s.registration.mu.Unlock()
	if err != nil {
		s.writeAPIError(w, r, 503, "auth.delivery_unavailable", "验证码未能发送，请稍后重试", nil)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "challenge": raw, "retry_after": 60, "expires_in": 600})
}

// Caller holds registration.mu until the resulting account mutation commits.
func (s *Server) checkContact(browser, channel, target, purpose, challenge, code string) error {
	entry := s.registration.codes[secretDigest(challenge)]
	if entry == nil || entry.Failed || !entry.Ready || entry.Browser != browser || entry.Channel != channel || entry.Target != target || entry.Purpose != purpose || !time.Now().Before(entry.Expires) || entry.Attempts >= 5 {
		return &auth.UserValidationError{Fields: map[string]string{channel + "_code": "验证码无效或已过期，请重新获取"}}
	}
	if !secretEqual(entry.Digest, secretDigest(challenge+":"+code)) {
		entry.Attempts++
		return &auth.UserValidationError{Fields: map[string]string{channel + "_code": "验证码不正确"}}
	}
	return nil
}

type registrationInput struct {
	RequestID         string `json:"request_id"`
	BindingID         string `json:"binding_id,omitempty"`
	Username          string `json:"username"`
	DisplayName       string `json:"display_name"`
	Email             string `json:"email"`
	Phone             string `json:"phone"`
	Password          string `json:"password"`
	ConfirmPassword   string `json:"confirm_password"`
	InvitationCode    string `json:"invitation_code"`
	RegistrationCode  string `json:"registration_code"`
	EmailChallenge    string `json:"email_challenge"`
	EmailCode         string `json:"email_code"`
	PhoneChallenge    string `json:"phone_challenge"`
	PhoneCode         string `json:"phone_code"`
	Token             string `json:"token"`
	Kind              string `json:"kind"`
	VerificationToken string `json:"verification_token"`
}

func validateRegistrationInput(p site.RegistrationPolicy, input *registrationInput, binding bool) map[string]string {
	fields := map[string]string{}
	values := map[string]*string{"username": &input.Username, "display_name": &input.DisplayName, "email": &input.Email, "phone": &input.Phone, "password": &input.Password, "invitation_code": &input.InvitationCode, "registration_code": &input.RegistrationCode}
	for key, mode := range p.Fields.Modes() {
		v := values[key]
		if key != "password" {
			*v = strings.TrimSpace(*v)
		}
		if mode == "hidden" {
			*v = ""
		}
		required := mode == "required"
		if binding {
			required = false
			for _, k := range p.OAuthBindings {
				required = required || k == key
			}
		}
		if required && *v == "" {
			fields[key] = "请填写此项"
		}
	}
	input.Email = strings.ToLower(input.Email)
	if input.Phone != "" && (!auth.ValidPhone(input.Phone) || !p.AllowsPhone(input.Phone)) {
		fields["phone"] = "请输入有效的手机号，并使用本站允许的区号"
	}
	if input.Password != "" && p.PasswordConfirmation && input.Password != input.ConfirmPassword {
		fields["confirm_password"] = "两次输入的密码不一致"
	}
	if input.InvitationCode != "" && !p.Referrals().Enabled {
		fields["invitation_code"] = "用户邀请分享已关闭"
	}
	return fields
}

func (s *Server) socialRegister(w http.ResponseWriter, r *http.Request) {
	s.registerOrBind(w, r, false)
}
func (s *Server) socialOAuthBind(w http.ResponseWriter, r *http.Request) {
	s.registerOrBind(w, r, true)
}
func (s *Server) registerOrBind(w http.ResponseWriter, r *http.Request, binding bool) {
	if !s.allowAccess(w, r, "register", 20) {
		return
	}
	var input registrationInput
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 12<<10, "12 KiB"); !ok {
		return
	}
	if !s.auth.Configured() {
		s.writeAPIError(w, r, 503, "auth.not_configured", "站点账号服务暂不可用", nil)
		return
	}
	browser := s.accessBrowser(w, r, false)
	if browser == "" {
		s.writeAPIError(w, r, 409, "auth.reload_required", "请刷新页面后重试", nil)
		return
	}
	if _, err := auth.AccessHash(input.RequestID); err != nil {
		s.writeAPIError(w, r, 422, "auth.invalid_input", "请求标识无效，请刷新后重试", nil)
		return
	}
	settings := s.siteSettings.Snapshot().Settings
	p := settings.RegistrationPolicy()
	if settings.AccountRegistration() == "invite" && p.Referrals().Enabled {
		p.Fields.InvitationCode = "required"
	}
	digestInput := input
	digestInput.VerificationToken = ""
	encoded, _ := json.Marshal(digestInput)
	digest := secretDigest(string(encoded))
	requestKey := secretDigest(browser + ":" + input.RequestID)
	// Reconcile a committed registration before consuming another verification
	// proof. The browser secret and complete request digest must both match.
	if prior, err := s.auth.FindRegistration(r.Context(), "request", requestKey); err == nil {
		if prior.RegistrationDigest != digest || prior.RegistrationVersion != prior.Version || time.Since(prior.UpdatedAt) > 15*time.Minute {
			s.accessError(w, r, auth.ErrRegistrationConflict)
			return
		}
		if len(prior.OAuthBindings) > 0 && len(missingOAuthBindings(p, prior)) > 0 {
			s.writeAPIError(w, r, 409, "auth.registration_updated", "绑定要求已更新，请重新快捷登录", nil)
			return
		}
		s.finishRegistration(w, r, prior)
		return
	} else if !errors.Is(err, auth.ErrUserNotFound) {
		s.accessError(w, r, err)
		return
	}
	if !s.requireVerification(w, r, "register", input.VerificationToken) {
		return
	}
	s.registration.mu.Lock()
	defer s.registration.mu.Unlock()
	s.registration.prepare()
	var pending oauthPending
	var existing auth.User
	if binding {
		var ok bool
		pending, ok = s.registration.pending[browser]
		if !ok || !secretEqual(pending.BindingID, input.BindingID) {
			s.writeAPIError(w, r, 401, "auth.binding_expired", "快捷登录已过期，请重新授权", nil)
			return
		}
		provider, enabled := s.loginProvider(pending.Provider)
		if !enabled {
			s.writeAPIError(w, r, 403, "auth.provider_disabled", "此登录方式已停用", nil)
			return
		}
		if !secretEqual(pending.Fingerprint, secretDigest(provider.ClientID+":"+provider.Secret)) {
			s.writeAPIError(w, r, 401, "auth.binding_expired", "登录配置已更新，请重新授权", nil)
			return
		}
		if pending.UserID > 0 {
			var err error
			existing, err = s.auth.GetUser(r.Context(), pending.UserID)
			if err != nil || existing.Status != auth.UserStatusActive {
				s.accessError(w, r, auth.ErrInvalidCredentials)
				return
			}
		}
		if existing.ID == 0 {
			if linked, err := s.auth.FindRegistration(r.Context(), "external", pending.ExternalKey); err == nil {
				existing = linked
			} else if !errors.Is(err, auth.ErrUserNotFound) {
				s.accessError(w, r, err)
				return
			}
		}
	}
	if existing.ID == 0 && settings.AccountRegistration() == "closed" {
		s.writeAPIError(w, r, 403, "auth.registration_closed", "本站暂未开放注册", nil)
		return
	}
	// Existing accounts only supply missing binding fields. Their saved values
	// are carried forward, never replaced by data returned from an OAuth profile.
	if existing.ID > 0 {
		if input.Username == "" {
			input.Username = existing.Username
		}
		if input.DisplayName == "" {
			input.DisplayName = existing.DisplayName
		}
		if input.Email == "" {
			input.Email = existing.Email
		}
		if input.Phone == "" {
			input.Phone = existing.Phone
		}
	}
	if input.Kind == auth.AccessRegister && input.Token != "" && settings.AccountRegistration() != "open" {
		s.writeAPIError(w, r, 403, "auth.registration_closed", "注册方式已调整，请联系管理员", nil)
		return
	}
	if binding && input.Token != "" {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	var linkEmail string
	if input.Token != "" {
		record, err := s.auth.RegistrationLink(r.Context(), input.Token, input.Kind)
		if err != nil {
			s.accessError(w, r, err)
			return
		}
		linkEmail = record.Email
		input.Email = record.Email
	}
	fields := validateRegistrationInput(p, &input, binding)
	if existing.ID > 0 {
		// Completing an existing identity must not reapply new-account admission.
		input.InvitationCode, input.RegistrationCode = "", ""
		delete(fields, "invitation_code")
		delete(fields, "registration_code")
		if p.Fields.Username == "hidden" {
			input.Username = existing.Username
		}
		if p.Fields.DisplayName == "hidden" {
			input.DisplayName = existing.DisplayName
		}
		if p.Fields.Email == "hidden" {
			input.Email = existing.Email
		}
		if p.Fields.Phone == "hidden" {
			input.Phone = existing.Phone
		}
		// This is a completion step, not an account-editing endpoint. Preserve
		// established values even if a caller forges extra visible fields.
		missing := map[string]bool{}
		for _, key := range missingOAuthBindings(p, existing) {
			missing[key] = true
		}
		if !missing["username"] {
			input.Username = existing.Username
		}
		if !missing["display_name"] {
			input.DisplayName = existing.DisplayName
		}
		if !missing["email"] {
			input.Email = existing.Email
		}
		if !missing["phone"] {
			input.Phone = existing.Phone
			delete(fields, "phone")
		}
		if !missing["password"] {
			input.Password = ""
			delete(fields, "confirm_password")
		}
	}
	if input.Kind == auth.AccessInvite && linkEmail != "" {
		delete(fields, "invitation_code")
	}
	if linkEmail != "" {
		input.Email = linkEmail
		delete(fields, "email")
	}
	if binding {
		if input.DisplayName == "" {
			input.DisplayName = pending.Name
		}
		if existing.ID > 0 && existing.PasswordLogin {
			delete(fields, "password")
		}
	}
	if existing.ID == 0 {
		if settings.AccountRegistration() == "invite" && !(input.Kind == auth.AccessInvite && linkEmail != "") {
			if !p.Referrals().Enabled || input.InvitationCode == "" {
				fields["invitation_code"] = "请填写有效的邀请码或打开邀请邮件"
			}
		}
		if p.Fields.RegistrationCode == "required" && input.RegistrationCode == "" {
			fields["registration_code"] = "请填写注册码"
		}
		if p.Fields.InvitationCode == "required" && input.InvitationCode == "" && !(input.Kind == auth.AccessInvite && linkEmail != "") {
			fields["invitation_code"] = "请填写邀请码"
		}
	}
	if len(fields) > 0 {
		s.accessError(w, r, &auth.UserValidationError{Fields: fields})
		return
	}
	verifiedEmail := linkEmail != "" || existing.EmailVerifiedAt != nil && existing.Email == input.Email
	verifiedPhone := existing.PhoneVerifiedAt != nil && existing.Phone == input.Phone
	purpose := "register"
	if binding {
		purpose = "bind"
	}
	if input.Email != "" && p.EmailVerification && !verifiedEmail {
		if err := s.checkContact(browser, "email", input.Email, purpose, input.EmailChallenge, input.EmailCode); err != nil {
			s.accessError(w, r, err)
			return
		}
		verifiedEmail = true
	}
	if input.Phone != "" && p.PhoneVerification && !verifiedPhone {
		if err := s.checkContact(browser, "phone", input.Phone, purpose, input.PhoneChallenge, input.PhoneCode); err != nil {
			s.accessError(w, r, err)
			return
		}
		verifiedPhone = true
	}
	if input.Username == "" {
		raw, err := auth.RandomCredential()
		if err != nil {
			s.accessError(w, r, err)
			return
		}
		input.Username = "member_" + strings.ToLower(raw[:16])
	}
	noPassword := input.Password == ""
	password := input.Password
	if noPassword && existing.ID == 0 {
		var err error
		password, err = auth.RandomCredential()
		if err != nil {
			s.accessError(w, r, err)
			return
		}
	}
	uinput := auth.UserInput{Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, Phone: input.Phone, Locale: "zh-CN", EmailVerified: verifiedEmail, PhoneVerified: verifiedPhone, ExternalKey: pending.ExternalKey, RegistrationKey: requestKey, RegistrationDigest: digest, DisablePassword: noPassword}
	uinput.OAuthProvider = pending.Provider
	if input.InvitationCode != "" && p.Referrals().Enabled {
		uinput.ReferralHash = auth.RegistrationCodeHash(input.InvitationCode)
		uinput.ReferralPolicy = p.Referrals()
	}
	if existing.ID == 0 && input.RegistrationCode != "" {
		uinput.RegistrationCodeHash = auth.RegistrationCodeHash(input.RegistrationCode)
	}
	var u auth.User
	var err error
	if existing.ID > 0 {
		u, err = s.auth.BindRegistration(r.Context(), existing, uinput, password)
	} else if input.Token != "" {
		u, err = s.auth.CompleteAccess(r.Context(), input.Token, input.Kind, uinput, password)
	} else {
		u, err = s.auth.RegisterAccount(r.Context(), uinput, password)
	}
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	delete(s.registration.codes, secretDigest(input.EmailChallenge))
	delete(s.registration.codes, secretDigest(input.PhoneChallenge))
	delete(s.registration.pending, browser)
	s.finishRegistration(w, r, u)
}

func (s *Server) finishRegistration(w http.ResponseWriter, r *http.Request, u auth.User) {
	token, err := s.auth.LoginVerifiedUser(r.Context(), u.ID, auth.SessionMetadata{IPAddress: s.clientIPs.string(r), UserAgent: r.UserAgent()})
	if err != nil {
		s.accessError(w, r, err)
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
	writeJSON(w, 200, map[string]any{"ok": true, "username": u.Username, "authenticated": true})
}

func (s *Server) socialCodeLogin(w http.ResponseWriter, r *http.Request) {
	if !s.allowAccess(w, r, "code-login", 20) {
		return
	}
	if !s.siteSettings.Snapshot().Settings.RegistrationPolicy().AllowsCodeLogin() {
		s.writeAPIError(w, r, 403, "auth.code_login_disabled", "本站已关闭验证码登录，请使用密码或快捷登录", nil)
		return
	}
	var input struct {
		Channel           string `json:"channel"`
		Target            string `json:"target"`
		Challenge         string `json:"challenge"`
		Code              string `json:"code"`
		VerificationToken string `json:"verification_token"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	if input.Channel != "email" && input.Channel != "phone" {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	if !s.requireVerification(w, r, "login", input.VerificationToken) {
		return
	}
	browser := s.accessBrowser(w, r, false)
	input.Target = strings.TrimSpace(input.Target)
	if input.Channel == "email" {
		input.Target = strings.ToLower(input.Target)
	}
	s.registration.mu.Lock()
	defer s.registration.mu.Unlock()
	s.registration.prepare()
	if err := s.checkContact(browser, input.Channel, input.Target, "login", input.Challenge, input.Code); err != nil {
		s.accessError(w, r, err)
		return
	}
	u, err := s.auth.FindRegistration(r.Context(), input.Channel, input.Target)
	if err != nil || u.Status != auth.UserStatusActive || input.Channel == "email" && u.EmailVerifiedAt == nil || input.Channel == "phone" && u.PhoneVerifiedAt == nil {
		s.writeAPIError(w, r, 401, "auth.login_failed", "未找到可使用此验证方式登录的账号", nil)
		return
	}
	delete(s.registration.codes, secretDigest(input.Challenge))
	s.finishRegistration(w, r, u)
}
