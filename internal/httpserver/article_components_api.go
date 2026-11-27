package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

func componentDataPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "article-components")
}
func (s *Server) registerArticleComponentRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/v1/article-components/{post}/{widget}", s.articleComponentState)
	mux.HandleFunc("POST /api/v1/article-components/{post}/{widget}", s.articleComponentAction)
	mux.HandleFunc("GET /api/v1/article-component-profile/{post}/{widget}", s.articleComponentProfile)
	mux.HandleFunc("GET /api/v1/article-component-avatar/{post}/{widget}", s.articleComponentAvatar)
	mux.HandleFunc("POST /api/v1/admin/article-components/validate", s.validateArticleComponent)
}

func (s *Server) validateArticleComponent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	s.validateComponentConfig(w, r)
}

func (s *Server) validateComponentConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config string `json:"config"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 96<<10, "96 KiB"); !ok {
		return
	}
	c, err := articlewidgets.Decode(body.Config)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "config": c.Encode()})
}

type componentOwner struct {
	ID, AuthorID int64
	Scope        string
}

func (s *Server) componentTarget(w http.ResponseWriter, r *http.Request) (componentOwner, articlewidgets.Config, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	if _, ok := s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return componentOwner{}, articlewidgets.Config{}, false
	}
	if strings.HasPrefix(r.PathValue("post"), "shop:") {
		return s.shopComponentTarget(w, r)
	}
	id, err := strconv.ParseInt(r.PathValue("post"), 10, 64)
	if err != nil || id < 1 || !articlewidgets.ValidID(r.PathValue("widget")) {
		s.componentError(w, r, articlewidgets.ErrInvalid)
		return componentOwner{}, articlewidgets.Config{}, false
	}
	p, err := s.repository.GetByID(r.Context(), id)
	if err != nil || !content.PublicAt(p, time.Now()) || p.Type != content.TypePost && p.Type != content.TypePage {
		s.writeAPIError(w, r, 404, "widget.unavailable", "文章或组件暂不可用", nil)
		return componentOwner{}, articlewidgets.Config{}, false
	}
	c, found := articlewidgets.Find(p.ContentHTML, r.PathValue("widget"))
	if !found {
		s.writeAPIError(w, r, 404, "widget.unavailable", "文章或组件暂不可用", nil)
		return componentOwner{}, c, false
	}
	return componentOwner{ID: p.ID, AuthorID: p.AuthorID}, c, true
}

const componentCookie = "np_widget_visitor"

var visitorPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (s *Server) componentActor(w http.ResponseWriter, r *http.Request, c articlewidgets.Config, write bool) (string, string, int64, bool) {
	if p, ok := s.currentAdmin(r); ok && p.UserID > 0 && c.Mode != "guest" {
		if write && (len(r.Header.Values("X-CSRF-Token")) != 1 || !s.auth.CheckCSRF(p, r.Header.Get("X-CSRF-Token"))) {
			s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请刷新页面后重试", nil)
			return "", "", 0, false
		}
		return fmt.Sprintf("u:%d", p.UserID), p.CSRFToken, p.UserID, true
	}
	if c.Mode == "login" || c.Kind == "redpacket" {
		if write {
			s.writeAPIError(w, r, 401, "auth.required", "请先登录再参与", nil)
			return "", "", 0, false
		}
		return "", "", 0, true
	}
	var value string
	matches := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == componentCookie {
			value = cookie.Value
			matches++
		}
	}
	if matches != 1 || !visitorPattern.MatchString(value) {
		if write {
			s.writeAPIError(w, r, 403, "widget.visitor_expired", "参与凭据已过期，请刷新页面", nil)
			return "", "", 0, false
		}
		var err error
		value, err = newCurrencyRequestID()
		if err != nil {
			s.componentError(w, r, err)
			return "", "", 0, false
		}
		http.SetCookie(w, &http.Cookie{Name: componentCookie, Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil || s.config.PublicURL != "" && len(s.config.PublicURL) > 5 && s.config.PublicURL[:5] == "https", SameSite: http.SameSiteStrictMode, MaxAge: 365 * 86400})
	}
	if write && (len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(value), []byte(r.Header.Get("X-CSRF-Token"))) != 1) {
		s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请刷新页面后重试", nil)
		return "", "", 0, false
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("g:%x", digest), value, 0, true
}

func (s *Server) articleComponentState(w http.ResponseWriter, r *http.Request) {
	p, c, ok := s.componentTarget(w, r)
	if !ok {
		return
	}
	if c.Kind != "poll" && c.Kind != "rating" && c.Kind != "redpacket" {
		s.componentError(w, r, articlewidgets.ErrInvalid)
		return
	}
	actor, token, user, ok := s.componentActor(w, r, c, false)
	if !ok {
		return
	}
	st, err := s.articleComponents.State(articlewidgets.WithScope(r.Context(), p.Scope), p.ID, p.AuthorID, c, actor)
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	currencyName := ""
	if c.Kind == "redpacket" {
		var ledger virtualcurrency.Store
		if s.currencyCatalog != nil {
			ledger, _ = s.currencyCatalog.Store(r.Context(), c.Currency)
		} else if c.Currency == virtualcurrency.DefaultCurrencyKey {
			ledger = s.currency
		}
		if ledger != nil {
			if cfg, err := ledger.Settings(r.Context()); err == nil {
				currencyName = cfg.Name
			}
		}
	}
	writeJSON(w, 200, map[string]any{"state": st, "csrf_token": token, "authenticated": user > 0, "owner": user > 0 && user == p.AuthorID, "fingerprint": c.Fingerprint(), "config": c.Encode(), "currency_name": currencyName})
}

func (s *Server) articleComponentAction(w http.ResponseWriter, r *http.Request) {
	if !s.currencySameOrigin(r) {
		s.writeAPIError(w, r, 403, "auth.cross_site_request_rejected", "请从本站页面提交", nil)
		return
	}
	p, c, ok := s.componentTarget(w, r)
	if !ok {
		return
	}
	actor, _, user, ok := s.componentActor(w, r, c, true)
	if !ok {
		return
	}
	if allowed, _ := s.accessRate.allow("article-component:"+s.commentGuard.sourceDigest(s.clientIPs.string(r)), 60, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "widget.rate_limit", "操作较频繁，请稍后重试", nil)
		return
	}
	var body struct {
		Action      string `json:"action"`
		Value       int    `json:"value"`
		Fingerprint string `json:"fingerprint"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 2048, "2 KiB"); !ok {
		return
	}
	if body.Fingerprint != c.Fingerprint() {
		s.writeAPIError(w, r, 409, "widget.changed", "组件内容已更新，请刷新页面后参与", nil)
		return
	}
	var st articlewidgets.State
	var err error
	switch body.Action {
	case "vote":
		st, err = s.articleComponents.Vote(articlewidgets.WithScope(r.Context(), p.Scope), p.ID, p.AuthorID, c, actor, body.Value)
	case "claim", "activate", "pause":
		if c.Kind != "redpacket" || user < 1 {
			s.componentError(w, r, articlewidgets.ErrInvalid)
			return
		}
		owner, e := s.auth.GetUser(r.Context(), p.AuthorID)
		if e != nil || owner.Status != auth.UserStatusActive {
			s.writeAPIError(w, r, 404, "widget.owner_unavailable", "发放账户暂不可用", nil)
			return
		}
		var ledger virtualcurrency.Store
		if s.currencyCatalog != nil {
			ledger, err = s.currencyCatalog.Store(r.Context(), c.Currency)
		} else if c.Currency == virtualcurrency.DefaultCurrencyKey {
			ledger = s.currency
		}
		if err != nil || ledger == nil {
			s.writeAPIError(w, r, 503, "widget.currency_unavailable", "指定货币暂不可用", nil)
			return
		}
		if body.Action == "claim" {
			st, err = s.articleComponents.Claim(articlewidgets.WithScope(r.Context(), p.Scope), p.ID, p.AuthorID, user, c, ledger)
		} else {
			if user != p.AuthorID {
				s.writeAPIError(w, r, 403, "widget.owner_required", "只有发放账户可以启用或暂停红包", nil)
				return
			}
			err = s.articleComponents.Activate(articlewidgets.WithScope(r.Context(), p.Scope), p.ID, p.AuthorID, c, ledger, body.Action == "activate")
			if err == nil {
				st, err = s.articleComponents.State(articlewidgets.WithScope(r.Context(), p.Scope), p.ID, p.AuthorID, c, actor)
			}
		}
	default:
		err = articlewidgets.ErrInvalid
	}
	if err != nil {
		s.componentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"state": st})
}

func (s *Server) componentError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := 503, "组件服务暂不可用，请稍后重试"
	switch {
	case errors.Is(err, articlewidgets.ErrInvalid):
		status, message = 400, err.Error()
	case errors.Is(err, articlewidgets.ErrAlready), errors.Is(err, articlewidgets.ErrClosed):
		status, message = 409, err.Error()
	case errors.Is(err, articlewidgets.ErrCapacity):
		status, message = 429, err.Error()
	case errors.Is(err, virtualcurrency.ErrConflict):
		status, message = 409, "货币设置已变化，请作者重新启用红包"
	case errors.Is(err, virtualcurrency.ErrInsufficient):
		status, message = 409, "发放账户余额不足"
	case errors.Is(err, virtualcurrency.ErrDisabled), errors.Is(err, virtualcurrency.ErrTransferDisabled):
		status, message = 409, "此货币暂未开放转账"
	case errors.Is(err, virtualcurrency.ErrRateLimit):
		status, message = 429, "货币发放已达到今日转账限额"
	case errors.Is(err, virtualcurrency.ErrBalanceLimit):
		status, message = 409, "领取账户余额已达到上限"
	case errors.Is(err, virtualcurrency.ErrInvalid):
		status, message = 400, "红包金额或数量不符合货币设置"
	}
	s.writeAPIError(w, r, status, "widget.operation_failed", message, nil)
}
