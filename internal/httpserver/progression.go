package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) SetProgressionService(service *progression.Service) { s.progression = service }
func (s *Server) registerProgressionRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/progression/{$}", s.adminProgressionPage)
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/admin/progression":                    s.adminProgressionRead,
		"PUT /api/v1/admin/progression":                    s.adminProgressionUpdate,
		"POST /api/v1/admin/progression/preview":           s.adminProgressionPreview,
		"PATCH /api/v1/admin/progression/modules/{module}": s.adminProgressionModule,
		"GET /api/v1/admin/progression/account":            s.adminProgressionAccount,
		"POST /api/v1/admin/progression/experience":        s.adminProgressionExperience,
		"POST /api/v1/admin/progression/membership":        s.adminProgressionMembership,
		"POST /api/v1/admin/progression/orders/{action}":   s.adminProgressionOrder,
		"GET /api/v1/social/progression":                   s.socialProgression,
		"POST /api/v1/social/progression/check-in":         s.socialProgressionCheckIn,
		"POST /api/v1/social/progression/sync":             s.socialProgressionSync,
		"POST /api/v1/social/progression/purchase":         s.socialProgressionPurchase,
		"POST /api/v1/social/progression/purchase/cancel":  s.socialProgressionCancel,
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
	for _, prefix := range []string{"/api/v1/admin/progression/", "/api/v1/social/progression/", "/admin/progression/"} {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH"} {
			mux.HandleFunc(method+" "+prefix+"{path...}", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		}
	}
}
func (s *Server) progressionAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.progression == nil || s.progression.Store == nil {
		s.progressionError(w, r, progression.ErrUnavailable)
		return false
	}
	return true
}
func (s *Server) progressionAdmin(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	if write {
		p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
		if !ok {
			return p, false
		}
		if len(r.Header.Values("X-CSRF-Token")) != 1 || !s.currencySameOrigin(r) {
			s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请从本站页面提交操作", nil)
			return p, false
		}
		if !s.currencyRate(w, r, "progression-admin:"+strconv.FormatInt(p.UserID, 10), 100) {
			return p, false
		}
		return p, s.progressionAvailable(w, r)
	}
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return p, false
	}
	return p, s.progressionAvailable(w, r)
}
func (s *Server) progressionUser(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	p, ok := s.currencyPrincipal(w, r, write)
	if !ok {
		return p, false
	}
	if write && !s.currencyRate(w, r, "progression-user:"+strconv.FormatInt(p.UserID, 10), 40) {
		return p, false
	}
	return p, s.progressionAvailable(w, r)
}
func (s *Server) progressionError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "progression.unavailable", "成长服务暂不可用；如已提交购买，请稍后同步状态"
	switch {
	case errors.Is(err, progression.ErrInvalid):
		status, code, message = 422, "progression.invalid", "请检查经验、等级门槛、会员方案和操作参数"
	case errors.Is(err, progression.ErrConflict):
		status, code, message = 409, "progression.settings_changed", "规则已更新，请重新载入后核对"
	case errors.Is(err, progression.ErrDisabled):
		status, code, message = 409, "progression.disabled", "此模块或会员方案当前未开启"
	case errors.Is(err, progression.ErrIneligible):
		status, code, message = 403, "progression.ineligible", "账号或内容暂不符合奖励条件，请检查邮箱验证状态"
	case errors.Is(err, progression.ErrLimit):
		status, code, message = 409, "progression.limit", "已达到经验或会员期限上限"
	case errors.Is(err, progression.ErrReused):
		status, code, message = 409, "progression.request_reused", "操作编号已绑定其他请求，请先核对原操作"
	case errors.Is(err, progression.ErrMemberConflict):
		status, code, message = 409, "progression.membership_changed", "会员状态已变化，请重新载入后核对开通或续期方式"
	case errors.Is(err, progression.ErrPending):
		status, code, message = 409, "progression.pending", "该账号有待确认的购买订单，请先恢复或取消订单"
	case errors.Is(err, progression.ErrUsedPlan):
		status, code, message = 409, "progression.plan_used", "已使用的方案不能删除，可关闭方案以停止新开通"
	case errors.Is(err, auth.ErrUserNotFound):
		status, code, message = 404, "progression.user_not_found", "账号不存在"
	default:
		c, cc, m := currencyErrorInfo(err)
		if c != 503 {
			status, code, message = c, cc, m
		}
	}
	s.writeAPIError(w, r, status, code, message, nil)
}

// Every editable field is required, including false switches and zero-valued
// versions. Reject aliases and noncanonical string integers before mutation.
func progressionShape(raw any, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return raw != nil
	}
	if t == reflect.TypeFor[time.Time]() {
		_, ok := raw.(string)
		return ok
	}
	switch t.Kind() {
	case reflect.Struct:
		fields, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name == "" || name == "-" {
				continue
			}
			v, exists := fields[name]
			if !exists && (name == "since" || strings.Contains(tag, ",omitempty") || (t == reflect.TypeFor[progression.CheckInRules]() && (name == "mode" || name == "timezone"))) {
				continue
			}
			if !exists {
				return false
			}
			if strings.Contains(tag, ",string") {
				str, ok := v.(string)
				if !ok {
					return false
				}
				n, err := strconv.ParseInt(str, 10, 64)
				if err != nil || strconv.FormatInt(n, 10) != str {
					return false
				}
			} else if !progressionShape(v, f.Type) {
				return false
			}
		}
		return true
	case reflect.Slice:
		values, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if !progressionShape(v, t.Elem()) {
				return false
			}
		}
		return true
	default:
		return raw != nil
	}
}
func (s *Server) decodeProgression(w http.ResponseWriter, r *http.Request, target any) bool {
	if len(r.Header.Values("Content-Type")) != 1 {
		s.writeAPIError(w, r, 415, "request.json_content_type_required", "请使用唯一的 application/json 内容类型", nil)
		return false
	}
	body, ok := s.decodeStrictAPIJSONLimit(w, r, target, 128<<10, "128 KiB")
	if !ok {
		return false
	}
	var raw any
	if !utf8.Valid(body) || json.Unmarshal(body, &raw) != nil || !canonicalCurrencyJSON(raw) || !progressionShape(raw, reflect.TypeOf(target)) {
		s.writeAPIError(w, r, 400, "request.invalid_json", "请提交完整且格式准确的字段", nil)
		return false
	}
	return true
}
func decodeProgressionModule(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return progression.ErrInvalid
	}
	var data any
	if json.Unmarshal(raw, &data) != nil || !progressionShape(data, reflect.TypeOf(target)) {
		return progression.ErrInvalid
	}
	return nil
}

type progressionCurrency struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Precision int    `json:"precision"`
	Enabled   bool   `json:"enabled"`
}

func (s *Server) progressionCurrencies(ctx context.Context) ([]progressionCurrency, error) {
	out := []progressionCurrency{}
	if s.progression.Currencies == nil {
		return out, nil
	}
	items, err := s.progression.Currencies.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range items {
		out = append(out, progressionCurrency{Key: c.Key, Name: c.Settings.Name, Symbol: c.Settings.Symbol, Precision: c.Settings.Precision, Enabled: c.Settings.Enabled})
	}
	return out, nil
}
func (s *Server) writeProgressionSettings(w http.ResponseWriter, r *http.Request, cfg progression.Settings) {
	coins, err := s.progressionCurrencies(r.Context())
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": cfg, "currencies": coins})
}
func (s *Server) adminProgressionRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.progressionAdmin(w, r, false); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	cfg, err := s.progression.Store.Settings(r.Context())
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	s.writeProgressionSettings(w, r, cfg)
}
func (s *Server) adminProgressionPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.progressionAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Settings progression.Settings `json:"settings"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	preview, err := s.progression.PreviewSettings(r.Context(), in.Settings)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, preview)
}
func (s *Server) adminProgressionUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Settings progression.Settings `json:"settings"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	cfg, err := s.progression.UpdateSettings(r.Context(), in.Settings.Version, in.Settings)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	slog.Info("progression settings updated", "actor_id", p.UserID, "version", cfg.Version)
	s.writeProgressionSettings(w, r, cfg)
}
func (s *Server) adminProgressionModule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version  int64           `json:"version,string"`
		Settings json.RawMessage `json:"settings"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	cfg, err := s.progression.Store.Settings(r.Context())
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	var target any
	switch r.PathValue("module") {
	case "experience":
		target = &cfg.Experience
	case "levels":
		target = &cfg.Levels
	case "check-in":
		target = &cfg.CheckIn
	case "registration":
		target = &cfg.Registration
	case "publication":
		target = &cfg.Publication
	case "membership":
		target = &cfg.Membership
	default:
		http.NotFound(w, r)
		return
	}
	if err = decodeProgressionModule(in.Settings, target); err == nil {
		cfg, err = s.progression.UpdateSettings(r.Context(), in.Version, cfg)
	}
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	slog.Info("progression module updated", "actor_id", p.UserID, "module", r.PathValue("module"), "version", cfg.Version)
	s.writeProgressionSettings(w, r, cfg)
}

type progressionOrderView struct {
	UserID        int64     `json:"user_id,string"`
	RequestID     string    `json:"request_id"`
	Status        string    `json:"status"`
	Mode          string    `json:"mode"`
	Plan          string    `json:"plan"`
	PlanName      string    `json:"plan_name"`
	Days          int       `json:"days"`
	Version       int64     `json:"version,string"`
	MemberVersion int64     `json:"member_version,string"`
	Currency      string    `json:"currency"`
	Amount        string    `json:"amount"`
	Symbol        string    `json:"symbol"`
	CreatedAt     time.Time `json:"created_at"`
	CompletedAt   time.Time `json:"completed_at"`
}

func progressionOrder(o progression.Order) progressionOrderView {
	return progressionOrderView{UserID: o.UserID, RequestID: o.RequestID, Status: o.Status, Mode: o.Mode, Plan: o.Plan, PlanName: o.PlanName, Days: o.Days, Version: o.ConfigVersion, MemberVersion: o.MemberVersion, Currency: o.Currency, Amount: virtualcurrency.FormatAmount(o.Amount, o.CurrencyPrecision), Symbol: o.CurrencySymbol, CreatedAt: o.CreatedAt, CompletedAt: o.CompletedAt}
}

type progressionAccountView struct {
	progression.Snapshot
	Username      string                `json:"username"`
	DisplayName   string                `json:"display_name"`
	Status        string                `json:"status"`
	VerifiedEmail bool                  `json:"verified_email"`
	PendingOrder  *progressionOrderView `json:"pending_order"`
	Currencies    []progressionCurrency `json:"currencies"`
}

func (s *Server) progressionAccount(ctx context.Context, user auth.User, before int64) (progressionAccountView, error) {
	snap, err := s.progression.Store.Snapshot(ctx, user.ID, before)
	if err != nil {
		return progressionAccountView{}, err
	}
	v := progressionAccountView{Snapshot: snap, Username: user.Username, DisplayName: user.DisplayName, Status: user.Status, VerifiedEmail: user.EmailVerifiedAt != nil}
	if snap.PendingOrder != nil {
		o := progressionOrder(*snap.PendingOrder)
		v.PendingOrder = &o
	}
	v.Snapshot.PendingOrder = nil
	v.Currencies, err = s.progressionCurrencies(ctx)
	return v, err
}
func (s *Server) progressionReadAccount(w http.ResponseWriter, r *http.Request, user auth.User, before string) {
	var cursor int64
	var err error
	if before != "" {
		cursor, err = currencyID(before)
	}
	if err != nil {
		s.progressionError(w, r, progression.ErrInvalid)
		return
	}
	v, err := s.progressionAccount(r.Context(), user, cursor)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) adminProgressionAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, false)
	if !ok {
		return
	}
	if p.UserID < 1 {
		s.writeAPIError(w, r, 403, "auth.authentication_required", "请登录后查看账号记录", nil)
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"username": true, "before": true})
	if !ok {
		return
	}
	u, err := s.auth.FindUserByUsername(r.Context(), strings.TrimSpace(q.Get("username")))
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	s.progressionReadAccount(w, r, u, q.Get("before"))
}
func (s *Server) socialProgression(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionUser(w, r, false)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"before": true})
	if !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	s.progressionReadAccount(w, r, u, q.Get("before"))
}
func (s *Server) adminProgressionExperience(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, true)
	if !ok {
		return
	}
	var in struct {
		UserID    int64  `json:"user_id,string"`
		Delta     int64  `json:"delta,string"`
		Version   int64  `json:"version,string"`
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	if _, err := s.auth.GetUser(r.Context(), in.UserID); err != nil {
		s.progressionError(w, r, err)
		return
	}
	result, err := s.progression.Store.Adjust(r.Context(), progression.Adjustment{ActorID: p.UserID, UserID: in.UserID, Delta: in.Delta, ExpectedVersion: in.Version, RequestID: in.RequestID, Reason: in.Reason})
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) adminProgressionMembership(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, true)
	if !ok {
		return
	}
	var in struct {
		UserID        int64     `json:"user_id,string"`
		Version       int64     `json:"version,string"`
		MemberVersion int64     `json:"member_version,string"`
		Action        string    `json:"action"`
		Plan          string    `json:"plan"`
		Days          int       `json:"days"`
		Since         time.Time `json:"since,omitempty"`
		Until         time.Time `json:"until,omitempty"`
		RequestID     string    `json:"request_id"`
		Reason        string    `json:"reason"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	if _, err := s.auth.GetUser(r.Context(), in.UserID); err != nil {
		s.progressionError(w, r, err)
		return
	}
	result, err := s.progression.Store.ManageMembership(r.Context(), progression.MembershipInput{ActorID: p.UserID, UserID: in.UserID, ExpectedVersion: in.Version, MemberVersion: in.MemberVersion, Action: in.Action, Plan: in.Plan, Days: in.Days, Since: in.Since, Until: in.Until, RequestID: in.RequestID, Reason: in.Reason})
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) socialProgressionCheckIn(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version   int64  `json:"version,string"`
		RequestID string `json:"request_id"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	result, err := s.progression.CheckIn(r.Context(), p.UserID, in.Version, in.RequestID)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) socialProgressionSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionUser(w, r, true)
	if !ok {
		return
	}
	var in struct{}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	if err := s.progression.Sync(r.Context(), p.UserID); err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) socialProgressionPurchase(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version       int64  `json:"version,string"`
		MemberVersion int64  `json:"member_version,string"`
		Plan          string `json:"plan"`
		Mode          string `json:"mode"`
		RequestID     string `json:"request_id"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	order, err := s.progression.Purchase(r.Context(), progression.PurchaseInput{UserID: p.UserID, ExpectedVersion: in.Version, MemberVersion: in.MemberVersion, Plan: in.Plan, Mode: in.Mode, RequestID: in.RequestID})
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, progressionOrder(order))
}
func (s *Server) socialProgressionCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		RequestID string `json:"request_id"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	o, err := s.progression.Cancel(r.Context(), p.UserID, in.RequestID)
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	writeJSON(w, 200, progressionOrder(o))
}
func (s *Server) adminProgressionOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := s.progressionAdmin(w, r, true)
	if !ok {
		return
	}
	var in struct {
		UserID    int64  `json:"user_id,string"`
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}
	if !s.decodeProgression(w, r, &in) {
		return
	}
	if !virtualcurrency.ValidReason(in.Reason) {
		s.progressionError(w, r, progression.ErrInvalid)
		return
	}
	var o progression.Order
	var err error
	switch r.PathValue("action") {
	case "recover":
		o, _, err = s.progression.RecoverRequest(r.Context(), in.UserID, in.RequestID)
	case "cancel":
		o, err = s.progression.Cancel(r.Context(), in.UserID, in.RequestID)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.progressionError(w, r, err)
		return
	}
	slog.Info("membership order reviewed", "actor_id", p.UserID, "user_id", in.UserID, "request_id", in.RequestID, "action", r.PathValue("action"), "reason", in.Reason)
	writeJSON(w, 200, progressionOrder(o))
}
func (s *Server) adminProgressionPage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if s.progression == nil || s.progression.Store == nil {
		s.renderFeatureRecovery(w, r, "progression", "", s.adminView("progression", p, readOnly), 503, "")
		return
	}
	data := s.baseViewFor(w, "成长与会员", "配置经验奖励、等级门槛与会员方案", "/admin/progression/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-progression"
	data.Admin = s.adminView("progression", p, readOnly)
	s.render(w, 200, "admin-progression", data)
}

type progressionBadge struct {
	Name, Color, Kind string
	Level             int
}

func (s *Server) progressionBadges(ctx context.Context, user int64, public bool) []progressionBadge {
	badges := []progressionBadge{}
	if s.progression == nil {
		return badges
	}
	v, err := s.progression.Store.Snapshot(ctx, user, 0)
	if err != nil {
		return badges
	}
	if v.Level != nil && (!public || v.Settings.Levels.PublicBadges) {
		for i, level := range v.Settings.Levels.Items {
			if level.Key == v.Level.Key {
				badges = append(badges, progressionBadge{Name: v.Level.Name, Color: v.Level.Color, Kind: "level", Level: i + 1})
				break
			}
		}
	}
	if v.MembershipActive && (!public || v.Settings.Membership.PublicBadges) {
		if p, ok := v.Settings.Plan(v.Account.Membership.Plan); ok {
			badges = append(badges, progressionBadge{Name: p.Name, Color: p.Color, Kind: "membership"})
		}
	}
	return badges
}
