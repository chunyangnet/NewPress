package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/messaging"
)

type socialContact struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Available bool   `json:"available"`
}
type socialMessage struct {
	ID          int64      `json:"id"`
	SenderID    int64      `json:"sender_id"`
	RecipientID int64      `json:"recipient_id"`
	Content     string     `json:"content"`
	Nonce       string     `json:"nonce,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at"`
}
type socialConversation struct {
	PeerID  int64         `json:"peer_id"`
	Last    socialMessage `json:"last"`
	Unread  int           `json:"unread"`
	Contact socialContact `json:"contact"`
}
type socialNotice struct {
	ID        int64     `json:"id"`
	PeerID    int64     `json:"peer_id"`
	MessageID int64     `json:"message_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
	Kind      string    `json:"kind"`
}

func socialMessageDTO(m messaging.Message, user int64) socialMessage {
	nonce := ""
	if m.SenderID == user {
		nonce = m.Nonce
	}
	return socialMessage{m.ID, m.SenderID, m.RecipientID, m.Content, nonce, m.CreatedAt, m.ReadAt}
}

func (s *Server) registerSocialRoutes(mux routeRegistrar) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/social/session":                                 s.socialSession,
		"POST /api/v1/social/login":                                  s.socialLogin,
		"POST /api/v1/social/logout":                                 s.socialLogout,
		"GET /api/v1/social/contacts":                                s.socialContacts,
		"GET /api/v1/social/inbox":                                   s.socialInbox,
		"GET /api/v1/social/messages/{peer}":                         s.socialMessages,
		"POST /api/v1/social/messages/{peer}":                        s.socialSend,
		"POST /api/v1/social/messages/{peer}/read":                   s.socialRead,
		"GET /api/v1/social/notifications":                           s.socialNotifications,
		"POST /api/v1/social/notifications":                          s.socialUpdateNotifications,
		"GET /api/v1/social/notice-center":                           s.noticeCenter,
		"POST /api/v1/social/notice-center":                          s.updateNoticeCenter,
		"GET /api/v1/social/notification-preferences":                s.notificationPreferences,
		"POST /api/v1/social/notification-preferences":               s.notificationPreferences,
		"POST /api/v1/social/notification-mail/retry":                s.retryNotificationMail,
		"GET /api/v1/social/engagement":                              s.socialEngagement,
		"POST /api/v1/social/engagement":                             s.socialSetEngagement,
		"GET /api/v1/social/library":                                 s.socialLibrary,
		"GET /api/v1/social/own-content":                             s.socialOwnContent,
		"POST /api/v1/social/import":                                 s.socialImportPreferences,
		"GET /api/v1/social/profile":                                 s.socialProfile,
		"GET /api/v1/social/oauth-bindings":                          s.socialOAuthBindings,
		"POST /api/v1/social/oauth-bindings/{provider}/start":        s.oauthStart,
		"POST /api/v1/social/oauth-bindings/{provider}/remove":       s.socialOAuthRemove,
		"POST /api/v1/social/profile":                                s.socialUpdateProfile,
		"POST /api/v1/social/profile/avatar":                         s.socialUploadAvatar,
		"POST /api/v1/social/access/register":                        s.socialRegister,
		"POST /api/v1/social/access/code":                            s.socialRegistrationCode,
		"POST /api/v1/social/access/code-login":                      s.socialCodeLogin,
		"GET /api/v1/social/access/pending":                          s.socialOAuthPending,
		"POST /api/v1/social/access/bind":                            s.socialOAuthBind,
		"GET /api/v1/social/access":                                  s.socialAccessConfig,
		"POST /api/v1/social/access/request":                         s.socialAccessRequest,
		"POST /api/v1/social/access/complete":                        s.socialAccessComplete,
		"GET /api/v1/admin/invitations":                              s.adminInvitations,
		"POST /api/v1/admin/invitations":                             s.adminIssueInvitation,
		"POST /api/v1/admin/invitations/revoke":                      s.adminRevokeInvitation,
		"GET /api/v1/admin/currencies":                               s.adminCurrencyList,
		"POST /api/v1/admin/currencies":                              s.adminCurrencyCreate,
		"GET /api/v1/admin/currencies/{currency}":                    s.adminCurrencyRead,
		"PUT /api/v1/admin/currencies/{currency}":                    s.adminCurrencyUpdate,
		"PATCH /api/v1/admin/currencies/{currency}/modules/{module}": s.adminCurrencyModuleUpdate,
		"GET /api/v1/admin/currencies/{currency}/wallet":             s.adminCurrencyWallet,
		"POST /api/v1/admin/currencies/{currency}/adjust":            s.adminCurrencyAdjust,
		"GET /api/v1/social/wallets":                                 s.socialWalletList,
		"GET /api/v1/social/wallets/{currency}":                      s.socialWallet,
		"GET /api/v1/social/wallets/{currency}/recipient":            s.socialWalletRecipient,
		"POST /api/v1/social/wallets/{currency}/transfer":            s.socialWalletTransfer,
		"POST /api/v1/social/wallets/{currency}/check-in":            s.socialWalletCheckIn,
		"POST /api/v1/social/wallets/{currency}/sync":                s.socialWalletSync,
	}
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Cookie")
			w.Header().Set("Cache-Control", "no-store")
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				if _, ok := s.strictAPIQuery(w, r, nil); !ok {
					return
				}
			}
			handler(w, r.WithContext(ctx))
		})
	}
	for _, pattern := range []string{
		"GET /api/v1/social/wallets/{path...}", "POST /api/v1/social/wallets/{path...}",
		"GET /api/v1/admin/currencies/{path...}", "POST /api/v1/admin/currencies/{path...}",
		"PUT /api/v1/admin/currencies/{path...}", "PATCH /api/v1/admin/currencies/{path...}",
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	}
}

func (s *Server) socialPrincipal(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	p, ok := s.currentAdmin(r)
	if !ok || p.UserID <= 0 {
		s.writeAPIError(w, r, 401, "auth.authentication_required", "请先登录后继续", nil)
		return p, false
	}
	if write && (!s.sameOriginRequest(r) || !s.auth.CheckCSRF(p, r.Header.Get("X-CSRF-Token"))) {
		s.writeAPIError(w, r, 403, "auth.csrf_invalid", "登录状态已变化，请刷新后重试", nil)
		return p, false
	}
	return p, true
}

func (s *Server) socialSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	p, ok := s.currentAdmin(r)
	if !ok || p.UserID <= 0 {
		writeJSON(w, 200, map[string]any{"authenticated": false, "configured": s.auth.Configured()})
		return
	}
	writeJSON(w, 200, map[string]any{"authenticated": true, "configured": true, "id": p.UserID, "name": p.Label(), "csrf_token": p.CSRFToken, "can_admin": p.Can(auth.CapabilityAdminAccess), "can_publish": canCreateContent(p) && s.siteSettings.Snapshot().Settings.API.AdminContentEnabled})
}
func (s *Server) socialLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "auth.origin_invalid", "登录来源未通过校验", nil)
		return
	}
	if !s.auth.Configured() {
		s.writeAPIError(w, r, 503, "auth.not_configured", "站点暂未开放账号登录，请联系管理员", nil)
		return
	}
	var input struct {
		Username          string `json:"username"`
		Password          string `json:"password"`
		VerificationToken string `json:"verification_token,omitempty"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 8<<10, "8 KiB"); !ok {
		return
	}
	if len(input.Username) > 320 || len(input.Password) > 1024 || strings.TrimSpace(input.Username) == "" || input.Password == "" {
		s.writeAPIError(w, r, 422, "auth.invalid_input", "请填写有效的用户名和密码", nil)
		return
	}
	if !s.requireVerification(w, r, "login", input.VerificationToken) {
		return
	}
	key := s.clientIPs.string(r)
	token, _, err := s.auth.AuthenticateWithMetadataContext(r.Context(), key, input.Username, input.Password, auth.SessionMetadata{IPAddress: key, UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, auth.ErrSessionUnavailable) {
			s.sessionStorageError(w, r)
			return
		}
		status := 401
		message := "用户名或密码不正确"
		if errors.Is(err, auth.ErrPasswordBusy) {
			status = http.StatusTooManyRequests
			message = "登录请求较多，请稍后重试"
			w.Header().Set("Retry-After", "1")
		} else if errors.Is(err, auth.ErrRateLimited) {
			status = 429
			message = "尝试次数过多，请稍后重试"
			w.Header().Set("Retry-After", "900")
		}
		s.writeAPIError(w, r, status, "auth.login_failed", message, nil)
		return
	}
	// A successful new login replaces the current browser session.
	if !s.replaceBrowserSession(w, r, token) {
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) socialLogout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.socialPrincipal(w, r, true); !ok {
		return
	}
	var input struct{}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	_, token, _ := s.currentAdminSession(r)
	if err := s.auth.RevokeSessionContext(r.Context(), token); err != nil {
		s.sessionStorageError(w, r)
		return
	}
	s.clearAdminCookie(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) socialContact(r *http.Request, id int64) socialContact {
	u, err := s.auth.GetUser(r.Context(), id)
	if err != nil || u.Status != auth.UserStatusActive {
		return socialContact{ID: id, Name: "已停用账号"}
	}
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	return socialContact{ID: u.ID, Name: name, Username: u.Username, Available: true}
}
func (s *Server) socialContacts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "page": true})
	if !ok {
		return
	}
	q := strings.TrimSpace(values.Get("q"))
	page, err := positiveQueryInt(values.Get("page"), 1, 10000)
	if err != nil || !utf8.ValidString(q) || utf8.RuneCountInString(q) > 80 {
		s.writeAPIError(w, r, 400, "contacts.query_invalid", "搜索词最多 80 字符，页码需为正整数", nil)
		return
	}
	query, err := auth.NormalizeUserQuery(auth.UserQuery{Status: auth.UserStatusActive, Search: q, Limit: 25, Offset: (page - 1) * 25, PublicOnly: true, ExcludeUserID: p.UserID})
	if err != nil {
		s.writeAPIError(w, r, 400, "contacts.query_invalid", "请输入有效的用户名或昵称", nil)
		return
	}
	users, err := s.auth.ListUsers(r.Context(), query)
	if err != nil {
		s.socialError(w, r, err)
		return
	}
	items := []socialContact{}
	for _, u := range users.Items {
		name := u.DisplayName
		if name == "" {
			name = u.Username
		}
		items = append(items, socialContact{ID: u.ID, Name: name, Username: u.Username, Available: true})
	}
	writeJSON(w, 200, map[string]any{"items": items, "page": page, "has_more": page*25 < users.Total})
}
func (s *Server) socialCursor(w http.ResponseWriter, r *http.Request, key, raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	id, err := positivePathID(raw)
	if err != nil {
		s.writeAPIError(w, r, 400, "messages.cursor_invalid", key+" 分页位置无效", nil)
		return 0, false
	}
	return id, true
}
func (s *Server) socialInbox(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"before": true})
	if !ok {
		return
	}
	before, ok := s.socialCursor(w, r, "before", values.Get("before"))
	if !ok {
		return
	}
	result, err := s.messages.Conversations(r.Context(), p.UserID, before)
	if err != nil {
		s.socialError(w, r, err)
		return
	}
	items := []socialConversation{}
	var cursor int64
	for _, c := range result.Items {
		items = append(items, socialConversation{PeerID: c.PeerID, Last: socialMessageDTO(c.Last, p.UserID), Unread: c.Unread, Contact: s.socialContact(r, c.PeerID)})
		cursor = c.Last.ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "unread": result.Unread, "has_more": result.HasMore, "next_cursor": cursor})
}
func (s *Server) socialPeer(w http.ResponseWriter, r *http.Request, user int64) (int64, bool) {
	id, err := positivePathID(r.PathValue("peer"))
	if err != nil || id == user {
		s.writeAPIError(w, r, 404, "contacts.not_found", "联系人不存在", nil)
		return 0, false
	}
	return id, true
}
func (s *Server) socialMessages(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	peer, ok := s.socialPeer(w, r, p.UserID)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"before": true, "after": true})
	if !ok {
		return
	}
	before, ok := s.socialCursor(w, r, "before", values.Get("before"))
	if !ok {
		return
	}
	after, ok := s.socialCursor(w, r, "after", values.Get("after"))
	if !ok {
		return
	}
	if before > 0 && after > 0 {
		s.writeAPIError(w, r, 400, "messages.cursor_invalid", "before 和 after 不能同时使用", nil)
		return
	}
	result, err := s.messages.Messages(r.Context(), p.UserID, peer, messaging.MessageQuery{Before: before, After: after})
	if err != nil {
		s.socialError(w, r, err)
		return
	}
	contact := s.socialContact(r, peer)
	if !contact.Available && len(result.Items) == 0 && before == 0 && after == 0 {
		s.writeAPIError(w, r, 404, "contacts.not_found", "这个联系人目前不可用", nil)
		return
	}
	items := []socialMessage{}
	var cursor int64
	for _, m := range result.Items {
		items = append(items, socialMessageDTO(m, p.UserID))
		cursor = m.ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "contact": contact, "has_more": result.HasMore, "next_cursor": cursor, "read_through": result.ReadThrough})
}
func (s *Server) socialSend(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	peer, ok := s.socialPeer(w, r, p.UserID)
	if !ok {
		return
	}
	contact := s.socialContact(r, peer)
	if !contact.Available {
		s.writeAPIError(w, r, 422, "messages.recipient_invalid", "这个联系人目前无法接收私信", nil)
		return
	}
	var input struct {
		Content string `json:"content"`
		Nonce   string `json:"nonce"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 16<<10, "16 KiB"); !ok {
		return
	}
	m, err := s.messages.Send(r.Context(), messaging.Input{SenderID: p.UserID, RecipientID: peer, Content: input.Content, Nonce: input.Nonce})
	if err != nil {
		s.socialError(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"message": socialMessageDTO(m, p.UserID)})
}
func (s *Server) socialRead(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	peer, ok := s.socialPeer(w, r, p.UserID)
	if !ok {
		return
	}
	var input struct {
		Through int64 `json:"through"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	if err := s.messages.Read(r.Context(), p.UserID, peer, input.Through); err != nil {
		s.socialError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) socialNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"before": true, "filter": true})
	if !ok {
		return
	}
	before, ok := s.socialCursor(w, r, "before", values.Get("before"))
	if !ok {
		return
	}
	filter := values.Get("filter")
	if filter != "" && filter != "all" && filter != "interaction" && filter != "unread" {
		s.writeAPIError(w, r, 400, "notifications.filter_invalid", "请选择有效的通知类型", nil)
		return
	}
	result, err := s.messages.Notices(r.Context(), p.UserID, messaging.NoticeQuery{Before: before, UnreadOnly: filter == "unread"})
	if err != nil {
		s.socialError(w, r, err)
		return
	}
	items := []socialNotice{}
	contacts := map[int64]socialContact{}
	var cursor int64
	for _, n := range result.Items {
		c, exists := contacts[n.PeerID]
		if !exists {
			c = s.socialContact(r, n.PeerID)
			contacts[n.PeerID] = c
		}
		items = append(items, socialNotice{ID: n.ID, PeerID: n.PeerID, MessageID: n.MessageID, Title: c.Name + " 发来私信", CreatedAt: n.CreatedAt, Read: n.ReadAt != nil, Kind: "interaction"})
		cursor = n.ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "unread": result.Unread, "has_more": result.HasMore, "next_cursor": cursor, "through": result.Through})
}
func (s *Server) socialUpdateNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Through int64  `json:"through"`
		Action  string `json:"action"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	if input.Through <= 0 || (input.Action != "read" && input.Action != "read_all" && input.Action != "clear") {
		s.writeAPIError(w, r, 422, "notifications.action_invalid", "通知操作或截止位置无效", nil)
		return
	}
	if err := s.messages.UpdateNotices(r.Context(), p.UserID, input.Through, input.Action); err != nil {
		s.socialError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) socialError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, messaging.ErrInvalid):
		s.writeAPIError(w, r, 422, "messages.invalid", "消息需为 1 至 2000 字符，阅读位置需属于当前会话", nil)
	case errors.Is(err, messaging.ErrRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(600))
		s.writeAPIError(w, r, 429, "messages.rate_limited", "发送过于频繁，请稍后重试", nil)
	case errors.Is(err, messaging.ErrConflict):
		s.writeAPIError(w, r, 409, "messages.conflict", "发送标识已用于其他消息，请重新编辑后发送", nil)
	default:
		slog.Error("messaging operation failed", "error", err)
		s.writeAPIError(w, r, 503, "messages.unavailable", "消息暂时不可用，请稍后重试", nil)
	}
}
