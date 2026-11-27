package httpserver

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
)

func (s *Server) accountAccessPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/account/access/" {
		s.renderError(w, 404, "页面没有找到")
		return
	}
	if _, ok := s.strictAPIQuery(w, r, map[string]bool{"flow": true}); !ok {
		return
	}
	data := s.baseViewFor(w, "加入与账号恢复", "从一封确认邮件开始，在这里留下你的阅读与创作。", r.URL.RequestURI())
	// Account pages need navigation state without publishing a canonical URL.
	data.Canonical = ""
	data.BodyClass = "is-account-access"
	data.Site.Robots = "noindex, nofollow, noarchive"
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", data.Site.Robots)
	s.render(w, 200, "account-access", data)
}

func (s *Server) accessDeliveryReady() bool {
	if s.mailSender == nil || !s.auth.Configured() {
		return false
	}
	u, err := url.Parse(s.config.PublicURL)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

func (s *Server) socialAccessConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, 200, s.registrationPublic(w, r))
}

func (s *Server) allowAccess(w http.ResponseWriter, r *http.Request, action string, limit int) bool {
	if r.Header.Get("Origin") == "" || !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "auth.origin_invalid", "请求来源未通过校验", nil)
		return false
	}
	key := action + ":" + s.commentGuard.sourceDigest(s.clientIPs.string(r))
	ok, retry := s.accessRate.allow(key, limit, time.Now())
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "auth.rate_limited", "尝试次数过多，请稍后再试", nil)
	}
	return ok
}

func (s *Server) socialAccessRequest(w http.ResponseWriter, r *http.Request) {
	if !s.allowAccess(w, r, "request", 8) {
		return
	}
	var input struct {
		Kind              string `json:"kind"`
		Email             string `json:"email"`
		VerificationToken string `json:"verification_token,omitempty"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 2048, "2 KiB"); !ok {
		return
	}
	if input.Kind != auth.AccessRegister && input.Kind != auth.AccessRecover {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	if input.Kind == auth.AccessRegister && s.siteSettings.Snapshot().Settings.AccountRegistration() != "open" {
		s.writeAPIError(w, r, 403, "auth.registration_closed", "本站暂未开放自由注册，请使用管理员邀请", nil)
		return
	}
	if !s.accessDeliveryReady() {
		s.writeAPIError(w, r, 503, "auth.email_unavailable", "邮箱服务暂不可用，请联系站点管理员", nil)
		return
	}
	if !s.requireVerification(w, r, input.Kind, input.VerificationToken) {
		return
	}
	raw, t, err := s.auth.IssueAccess(r.Context(), input.Kind, input.Email, "self-service")
	if errors.Is(err, auth.ErrRateLimited) {
		// Do not reveal whether this address has a pending request or an account.
		writeJSON(w, 202, map[string]bool{"ok": true})
		return
	}
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	if err = s.sendAccessMail(r, raw, t); err != nil {
		s.accessError(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) sendAccessMail(r *http.Request, raw string, t auth.AccessToken) error {
	subject, instruction := "确认你的邮箱", "请确认邮箱并设置你的用户名和密码。"
	if t.Kind == auth.AccessRecover {
		subject, instruction = "重设你的密码", "如果你在本站拥有启用的账号，可以通过以下链接设置新密码。"
	}
	if t.Kind == auth.AccessInvite {
		subject, instruction = "邀请你加入", "站点管理员邀请你加入。请通过下方链接设置你自己的用户名和密码。"
	}
	u, _ := url.Parse(s.config.PublicURL)
	u.Path = "/account/access/"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = url.Values{"token": {raw}, "kind": {t.Kind}}.Encode()
	// Mail readers see the link lifetime in the same zone as the rest of the
	// site, with the zone abbreviation so the instant stays unambiguous.
	expires := siteTime(s.siteLocation(r.Context()), t.ExpiresAt, "2006-01-02 15:04 MST")
	body := instruction + "\n\n" + u.String() + "\n\n链接有效至 " + expires + "，仅可使用一次。\n如果不是你本人发起或你不想加入，请忽略这封邮件。\n请勿转发此链接。"
	return s.mailSender.Send(r.Context(), t.Email, "NewPress · "+subject, body)
}

func (s *Server) socialAccessComplete(w http.ResponseWriter, r *http.Request) {
	if !s.allowAccess(w, r, "complete", 20) {
		return
	}
	var input struct {
		Token             string `json:"token"`
		Kind              string `json:"kind"`
		Password          string `json:"password"`
		ConfirmPassword   string `json:"confirm_password,omitempty"`
		VerificationToken string `json:"verification_token,omitempty"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 8<<10, "8 KiB"); !ok {
		return
	}
	if input.Kind != auth.AccessRecover {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	if !s.auth.Configured() {
		s.writeAPIError(w, r, 503, "auth.not_configured", "站点账号服务暂不可用", nil)
		return
	}
	if input.ConfirmPassword != "" && input.Password != input.ConfirmPassword {
		s.accessError(w, r, &auth.UserValidationError{Fields: map[string]string{"confirm_password": "两次输入的密码不一致"}})
		return
	}
	if !s.requireVerification(w, r, auth.AccessRecover, input.VerificationToken) {
		return
	}
	p, _ := s.currentAdmin(r)
	u, err := s.auth.CompleteAccess(r.Context(), input.Token, input.Kind, auth.UserInput{}, input.Password)
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	if p.UserID == u.ID {
		s.clearAdminCookie(w)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) invitationPrincipal(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	p, ok := s.socialPrincipal(w, r, write)
	if !ok {
		return p, false
	}
	if !p.Can(auth.CapabilityUsersManage) {
		s.writeAPIError(w, r, 403, "auth.capability_required", "需要用户管理权限", nil)
		return p, false
	}
	return p, true
}

func (s *Server) adminInvitations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.invitationPrincipal(w, r, false); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	items, err := s.auth.ListInvitations(r.Context())
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	rows := []map[string]any{}
	for _, t := range items {
		state := "pending"
		if t.ConsumedAt != nil {
			state = "closed"
		} else if !time.Now().Before(t.ExpiresAt) {
			state = "expired"
		}
		rows = append(rows, map[string]any{"id": t.Hash, "email": t.Email, "created_at": t.CreatedAt, "expires_at": t.ExpiresAt, "state": state})
	}
	writeJSON(w, 200, map[string]any{"items": rows, "available": s.accessDeliveryReady(), "registration": s.siteSettings.Snapshot().Settings.AccountRegistration()})
}

func (s *Server) adminIssueInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.invitationPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 2048, "2 KiB"); !ok {
		return
	}
	if s.siteSettings.Snapshot().Settings.AccountRegistration() == "closed" {
		s.writeAPIError(w, r, 409, "auth.registration_closed", "请先在站点设置中启用邀请注册或开放注册", nil)
		return
	}
	if !s.accessDeliveryReady() {
		s.writeAPIError(w, r, 503, "auth.email_unavailable", "请先配置站点邮箱服务", nil)
		return
	}
	raw, t, err := s.auth.IssueAccess(r.Context(), auth.AccessInvite, input.Email, p.Username)
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	if err = s.sendAccessMail(r, raw, t); err != nil {
		s.accessError(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

func (s *Server) adminRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.invitationPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	if len(input.ID) != 64 {
		s.accessError(w, r, auth.ErrAccessInvalid)
		return
	}
	if err := s.auth.RevokeInvitation(r.Context(), input.ID, p.Username); err != nil {
		s.accessError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) accessError(w http.ResponseWriter, r *http.Request, err error) {
	if fields := auth.UserFieldErrors(err); len(fields) > 0 {
		s.writeAPIError(w, r, 422, "auth.invalid_input", "请检查填写内容", map[string]any{"fields": fields})
		return
	}
	switch {
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(w, r)
	case errors.Is(err, auth.ErrRegistrationCodeInvalid):
		s.writeAPIError(w, r, 422, "auth.registration_code_invalid", err.Error(), map[string]any{"fields": map[string]string{"registration_code": err.Error()}})
	case errors.Is(err, auth.ErrAccessInvalid):
		s.writeAPIError(w, r, 400, "auth.link_invalid", "链接无效、已使用或已过期，请重新申请", nil)
	case errors.Is(err, auth.ErrUsernameConflict):
		s.writeAPIError(w, r, 409, "auth.username_unavailable", "此用户名已被使用，请换一个", nil)
	case errors.Is(err, auth.ErrPhoneConflict):
		s.writeAPIError(w, r, 409, "auth.phone_registered", "此手机号已有账号，请登录", nil)
	case errors.Is(err, auth.ErrRegistrationConflict):
		s.writeAPIError(w, r, 409, "auth.registration_conflict", "此请求或第三方身份已注册，请重新登录", nil)
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.writeAPIError(w, r, 401, "auth.login_failed", "账号暂不可用，请联系管理员", nil)
	case errors.Is(err, auth.ErrEmailConflict):
		s.writeAPIError(w, r, 409, "auth.email_registered", "此邮箱已有账号，请登录或重设密码", nil)
	case errors.Is(err, auth.ErrPasswordBusy):
		w.Header().Set("Retry-After", "1")
		s.writeAPIError(w, r, 429, "auth.busy", "密码操作较多，请稍后重试", nil)
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		s.writeAPIError(w, r, 429, "auth.rate_limited", "请求较频繁，请稍后再试", nil)
	default:
		// Transport/storage errors can contain private relay or account details.
		slog.Warn("account access operation unavailable")
		s.writeAPIError(w, r, 503, "auth.access_unavailable", "操作暂时无法完成，请一分钟后重试", nil)
	}
}
