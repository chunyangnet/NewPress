package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) SetRecognitionService(service *recognition.Service) { s.recognition = service }
func (s *Server) registerRecognitionRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/recognition/{$}", s.adminRecognitionPage)
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/admin/recognition":          s.adminRecognitionRead,
		"PUT /api/v1/admin/recognition":          s.adminRecognitionSave,
		"GET /api/v1/admin/recognition/account":  s.adminRecognitionAccount,
		"POST /api/v1/admin/recognition/account": s.adminRecognitionManage,
		"GET /api/v1/social/recognition":         s.socialRecognition,
		"POST /api/v1/social/recognition":        s.socialRecognitionUpdate,
		"GET /api/v1/social/tips/{id}":           s.socialTipRead,
		"POST /api/v1/social/tips/{id}":          s.socialTipCreate,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			if s.recognition == nil {
				s.recognitionError(w, r, recognition.ErrUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/recognition/account" {
				if _, ok := s.strictAPIQuery(w, r, nil); !ok {
					return
				}
			}
			handler(w, r.WithContext(ctx))
		})
	}
}
func (s *Server) recognitionError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "recognition.unavailable", "服务暂不可用，已填写的内容会保留，请重试原操作"
	switch {
	case errors.Is(err, recognition.ErrInvalid):
		status, code, message = 422, "recognition.invalid", "请检查名称、样式、金额和表单内容"
	case errors.Is(err, recognition.ErrConflict):
		status, code, message = 409, "recognition.conflict", "配置或账号状态已更新，请重新载入后核对"
	case errors.Is(err, recognition.ErrForbidden):
		status, code, message = 403, "recognition.forbidden", "当前账号无法执行此操作，不能给自己的文章打赏"
	case errors.Is(err, recognition.ErrDisabled):
		status, code, message = 409, "recognition.disabled", "此功能当前未开启"
	case errors.Is(err, recognition.ErrPending):
		status, code, message = 409, "recognition.pending", "已有待处理的申请或打赏，请先查看原操作"
	case errors.Is(err, auth.ErrUserNotFound), errors.Is(err, content.ErrNotFound):
		status, code, message = 404, "recognition.not_found", "账号或文章不存在"
	default:
		if c, k, m := currencyErrorInfo(err); c != 503 {
			status, code, message = c, k, m
		}
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
func (s *Server) recognitionAdmin(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	if write {
		p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
		if !ok {
			return p, false
		}
		if len(r.Header.Values("X-CSRF-Token")) != 1 || !s.currencySameOrigin(r) {
			s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请从本站页面提交", nil)
			return p, false
		}
		return p, s.currencyRate(w, r, "recognition-admin:"+strconv.FormatInt(p.UserID, 10), 80)
	}
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	return p, ok
}
func (s *Server) adminRecognitionPage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	d := s.baseViewFor(w, "互动与身份", "管理文章打赏、头衔、勋章和创作者认证", "/admin/recognition/")
	d.IsAdmin, d.BodyClass, d.Admin = true, "is-admin is-admin-recognition", s.adminView("recognition", p, readOnly)
	s.render(w, 200, "admin-recognition", d)
}
func (s *Server) adminRecognitionRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.recognitionAdmin(w, r, false); !ok {
		return
	}
	st, err := s.recognition.Backend.Read(r.Context())
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	currencies := []map[string]any{}
	if s.currencyCatalog != nil {
		items, e := s.currencyCatalog.List(r.Context())
		if e != nil {
			s.recognitionError(w, r, e)
			return
		}
		for _, c := range items {
			currencies = append(currencies, map[string]any{"key": c.Key, "name": c.Settings.Name, "enabled": c.Settings.Enabled, "transfers": c.Settings.AllowTransfers})
		}
	}
	pending := []map[string]any{}
	keys := make([]string, 0)
	for key, a := range st.Identities {
		if a.Application != nil && a.Application.Status == "pending" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys[:min(len(keys), 100)] {
		id, _ := strconv.ParseInt(key, 10, 64)
		u, e := s.auth.GetUser(r.Context(), id)
		if e != nil {
			continue
		}
		a := st.Identities[key]
		pending = append(pending, map[string]any{"user_id": id, "username": u.Username, "name": u.DisplayName, "version": a.Version, "application": a.Application})
	}
	st.Settings.HideAmounts = s.recognitionHideAmounts(st)
	writeJSON(w, 200, map[string]any{"settings": st.Settings, "currencies": currencies, "pending": pending, "pending_total": len(keys)})
}
func (s *Server) adminRecognitionSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.recognitionAdmin(w, r, true); !ok {
		return
	}
	var cfg recognition.Settings
	body, ok := s.decodeStrictAPIJSONLimit(w, r, &cfg, 128<<10, "128 KiB")
	if !ok {
		return
	}
	var shape any
	if json.Unmarshal(body, &shape) != nil || !progressionShape(shape, reflect.TypeOf(cfg)) {
		s.writeAPIError(w, r, 400, "recognition.incomplete_settings", "请提交完整设置，字段名称和空值无效", nil)
		return
	}
	mediaValues := map[string]any{}
	for _, badge := range cfg.Badges {
		mediaValues[badge.Key] = badge.Image
	}
	release, ok := s.holdSettingMedia(w, r, mediaValues)
	if !ok {
		return
	}
	defer release()
	if err := s.recognition.SaveSettings(r.Context(), cfg); err != nil {
		s.recognitionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true})
}
func (s *Server) adminRecognitionAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.recognitionAdmin(w, r, false); !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"username": true})
	if !ok {
		return
	}
	u, err := s.auth.FindUserByUsername(r.Context(), strings.TrimSpace(q.Get("username")))
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	st, err := s.recognition.Backend.Read(r.Context())
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": u.ID, "name": u.DisplayName, "username": u.Username, "identity": st.Identity(u.ID), "badges": publicIdentityBadges(st, u.ID)})
}
func (s *Server) adminRecognitionManage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.recognitionAdmin(w, r, true)
	if !ok {
		return
	}
	s.recognitionIdentityWrite(w, r, p.UserID, true)
}
func (s *Server) socialRecognition(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok {
		return
	}
	st, err := s.recognition.Backend.Read(r.Context())
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": p.UserID, "identity": st.Identity(p.UserID), "badges": publicIdentityBadges(st, p.UserID), "catalog": st.Settings.Badges, "verification_enabled": st.Settings.VerificationEnabled, "verification_help": st.Settings.VerificationHelp, "max_medals": st.Settings.MaxMedals})
}
func (s *Server) socialRecognitionUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, true)
	if !ok {
		return
	}
	if !s.currencyRate(w, r, "recognition-user:"+strconv.FormatInt(p.UserID, 10), 20) {
		return
	}
	s.recognitionIdentityWrite(w, r, p.UserID, false)
}
func (s *Server) recognitionIdentityWrite(w http.ResponseWriter, r *http.Request, actor int64, admin bool) {
	var in recognition.IdentityInput
	if _, ok := s.decodeCurrencyJSON(w, r, &in); !ok {
		return
	}
	result, err := s.recognition.UpdateIdentity(r.Context(), actor, admin, in)
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"identity": result})
}
func (s *Server) socialTipRead(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok {
		return
	}
	id, err := currencyID(r.PathValue("id"))
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	post, err := s.repository.GetByID(r.Context(), id)
	if err != nil || !content.PublicAt(post, time.Now()) {
		s.recognitionError(w, r, content.ErrNotFound)
		return
	}
	st, err := s.recognition.Backend.Read(r.Context())
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	choices := []map[string]any{}
	if s.currencyCatalog != nil {
		for _, c := range st.Settings.Currencies {
			coin, e := s.currencyCatalog.Store(r.Context(), c.Key)
			if e != nil {
				continue
			}
			wallet, e := coin.Wallet(r.Context(), p.UserID, 0, 1)
			if e != nil || !wallet.Settings.Enabled || !wallet.Settings.AllowTransfers {
				continue
			}
			rules := wallet.Settings
			choices = append(choices, map[string]any{"key": c.Key, "name": rules.Name, "symbol": rules.Symbol, "precision": rules.Precision, "version": rules.Version, "presets": c.Presets, "balance": virtualcurrency.FormatAmount(wallet.Balance, rules.Precision), "min": virtualcurrency.FormatAmount(rules.MinTransfer, rules.Precision), "max": virtualcurrency.FormatAmount(rules.MaxTransfer, rules.Precision)})
		}
	}
	writeJSON(w, 200, map[string]any{"user_id": p.UserID, "enabled": st.Settings.TipsEnabled && p.UserID != post.AuthorID && post.AuthorID > 0 && post.Type == content.TypePost, "author": post.AuthorName, "settings_version": st.Settings.Version, "hide_amounts": s.recognitionHideAmounts(st), "allow_hide_amount": st.Settings.AllowHideAmount, "currencies": choices})
}
func (s *Server) socialTipCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, true)
	if !ok {
		return
	}
	if !s.currencyRate(w, r, "article-tip:"+strconv.FormatInt(p.UserID, 10), 20) {
		return
	}
	var in recognition.TipInput
	if _, ok = s.decodeCurrencyJSON(w, r, &in); !ok {
		return
	}
	id, err := currencyID(r.PathValue("id"))
	if err != nil || in.PostID != id {
		s.recognitionError(w, r, recognition.ErrInvalid)
		return
	}
	tip, err := s.recognition.Tip(r.Context(), p.UserID, in)
	if err != nil {
		s.recognitionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"completed": tip.EntryID > 0, "request_id": tip.RequestID, "message": "打赏成功，感谢你对创作的支持"})
}

// Keep legacy theme privacy until an administrator explicitly saves the unified settings.
func (s *Server) recognitionHideAmounts(st recognition.State) bool {
	return st.Settings.HideAmounts || !st.PrivacyUnified && s.recognitionThemeBool("tip_hide_amount", false)
}

func (s *Server) recognitionThemeBool(key string, fallback bool) bool {
	if s.themes == nil {
		return fallback
	}
	if b, ok := s.themes.SettingsSnapshot().Values[key].(bool); ok {
		return b
	}
	return fallback
}
func publicIdentityBadges(st recognition.State, user int64) []presentation.IdentityBadge {
	result := []presentation.IdentityBadge{}
	now := time.Now()
	grants := st.Identity(user).Grants
	for _, b := range st.PublicBadges(user, now) {
		badge := presentation.IdentityBadge{Key: b.Key, Kind: b.Kind, Name: b.Name, Description: b.Description, Icon: b.Icon, Color: b.Color, Background: b.Background, Style: b.Style, Image: b.Image}
		// Only medal display dates are public; audit actors/reasons stay private.
		if b.Kind == "medal" {
			for _, grant := range grants {
				if grant.Key == b.Key && grant.Active(now) {
					if !grant.GrantedAt.IsZero() {
						badge.GrantedAt = grant.GrantedAt.Format(time.RFC3339)
					}
					if !grant.ExpiresAt.IsZero() {
						badge.ExpiresAt = grant.ExpiresAt.Format(time.RFC3339)
					}
					break
				}
			}
		}
		result = append(result, badge)
	}
	return result
}
func (s *Server) identityProgressionBadges(ctx context.Context, user int64) []presentation.IdentityBadge {
	result := []presentation.IdentityBadge{}
	for _, b := range s.progressionBadges(ctx, user, true) {
		result = append(result, presentation.IdentityBadge{Kind: b.Kind, Name: b.Name, Color: b.Color, Level: b.Level, Style: "outline", Icon: "star"})
	}
	return result
}
func (s *Server) prepareRecognition(ctx context.Context, data *pageView) {
	if data.IsAdmin {
		return
	}
	copyright, exists := data.ThemeValues["article_copyright"].(bool)
	if data.Post.ID > 0 && data.Post.Type == content.TypePost && (!exists || copyright) {
		values := data.ThemeValues
		text, _ := values["article_copyright_text"].(string)
		if text == "" {
			text = "本文由 {author} 发布于 {site}。转载请注明作者与原文链接。"
		}
		title, _ := values["article_copyright_title"].(string)
		if title == "" {
			title = "版权说明"
		}
		replace := strings.NewReplacer("{author}", data.Post.AuthorName, "{site}", data.Site.Title, "{title}", data.Post.Title, "{url}", data.Canonical, "{year}", strconv.Itoa(data.Now.Year()))
		data.Recognition.CopyrightTitle, data.Recognition.CopyrightText = title, replace.Replace(text)
	}
	if data.IsPreview || s.recognition == nil && s.progression == nil || data.Post.ID == 0 && data.Author.ID == 0 && data.Account.ID == 0 {
		return
	}
	authorID := data.Post.AuthorID
	if data.Author.ID > 0 {
		authorID = data.Author.ID
	}
	authorActive := false
	if authorID > 0 {
		if user, e := s.auth.GetUser(ctx, authorID); e == nil && user.Status == auth.UserStatusActive {
			authorActive = true
			if data.Post.ID > 0 {
				data.Recognition.AuthorBadges = s.identityProgressionBadges(ctx, authorID)
			}
		}
	}
	if s.recognition == nil {
		return
	}
	st, err := s.recognition.Backend.Read(ctx)
	if err != nil {
		return
	}
	if authorActive {
		data.Recognition.AuthorBadges = append(data.Recognition.AuthorBadges, publicIdentityBadges(st, authorID)...)
	}
	if data.Account.ID > 0 {
		if user, e := s.auth.GetUser(ctx, data.Account.ID); e == nil && user.Status == auth.UserStatusActive {
			data.Recognition.AccountBadges = publicIdentityBadges(st, data.Account.ID)
		}
	}
	data.Recognition.TipsEnabled = st.Settings.TipsEnabled && authorActive && data.Post.ID > 0 && data.Post.AuthorID > 0 && data.Post.Type == content.TypePost
	if data.Recognition.TipsEnabled {
		data.Recognition.TipAPI = "/api/v1/social/tips/" + strconv.FormatInt(data.Post.ID, 10)
	}
}
func (s *Server) commentRecognition(ctx context.Context, post int64, items []comment.Comment) (map[int64][]presentation.IdentityBadge, map[int64]recognition.Support) {
	badges, supports := map[int64][]presentation.IdentityBadge{}, map[int64]recognition.Support{}
	if s.recognition == nil && s.progression == nil {
		return badges, supports
	}
	var st recognition.State
	var tips map[int64]recognition.Support
	if s.recognition != nil {
		if state, err := s.recognition.Backend.Read(ctx); err == nil {
			st = state
			tips = s.recognition.Supports(ctx, st, post, s.recognitionHideAmounts(st))
		}
	}
	byUser := map[int64][]presentation.IdentityBadge{}
	inactive := map[int64]bool{}
	for _, c := range items {
		if c.UserID < 1 || inactive[c.UserID] {
			continue
		}
		if cached, ok := byUser[c.UserID]; ok {
			badges[c.ID], supports[c.ID] = cached, tips[c.UserID]
			continue
		}
		if user, err := s.auth.GetUser(ctx, c.UserID); err != nil || user.Status != auth.UserStatusActive {
			inactive[c.UserID] = true
			continue
		}
		badges[c.ID] = []presentation.IdentityBadge{}
		badges[c.ID] = append(badges[c.ID], s.identityProgressionBadges(ctx, c.UserID)...)
		badges[c.ID] = append(badges[c.ID], publicIdentityBadges(st, c.UserID)...)
		supports[c.ID] = tips[c.UserID]
		byUser[c.UserID] = badges[c.ID]
	}
	return badges, supports
}
func (s *Server) decorateCommentRecognition(r *http.Request, nodes []*publicCommentNode, items []comment.Comment, post int64) {
	badges, tips := s.commentRecognition(r.Context(), post, items)
	var visit func([]*publicCommentNode)
	visit = func(nodes []*publicCommentNode) {
		for _, n := range nodes {
			n.Badges, n.Support = badges[n.ID], tips[n.ID]
			visit(n.Children)
		}
	}
	visit(nodes)
}
