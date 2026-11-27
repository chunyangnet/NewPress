package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) SetCommerceService(v *commerce.Service) {
	s.commerce = v
	if v != nil && v.ValidateResource == nil {
		v.ValidateResource = s.validateResourcePayment
	}
}
func (s *Server) registerCommerceRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/payments/{$}", s.adminPaymentsPage)
	mux.HandleFunc("GET /billing/{$}", s.billingPage)
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/admin/payments": s.adminPaymentsRead, "PUT /api/v1/admin/payments": s.adminPaymentsSave,
		"GET /api/v1/admin/payments/orders": s.adminPaymentOrders, "POST /api/v1/admin/payments/orders/{order}/sync": s.adminPaymentSync,
		"GET /api/v1/admin/payments/orders/{order}": s.adminPaymentRead,
		"GET /api/v1/social/billing":                s.socialBilling, "POST /api/v1/social/billing/orders": s.socialPaymentCreate,
		"GET /api/v1/social/billing/orders/{order}": s.socialPaymentRead, "POST /api/v1/social/billing/orders/{order}/sync": s.socialPaymentSync,
		"GET /api/v1/social/billing/orders/{order}/qr": s.socialPaymentQR,
		"POST /api/v1/social/billing/exchange":         s.socialExchange,
		"POST /api/v1/payments/notify/{order}":         s.paymentNotify, "GET /api/v1/payments/notify/{order}": s.paymentNotify,
		"POST /api/v1/payments/webhook/{channel}": s.paymentNotify, "GET /api/v1/payments/webhook/{channel}": s.paymentNotify,
	}
	for pattern, fn := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			if s.commerce == nil {
				s.writeAPIError(w, r, 503, "payment.unavailable", "支付服务尚未装配", nil)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			fn(w, r.WithContext(ctx))
		})
	}
}
func (s *Server) commerceError(w http.ResponseWriter, r *http.Request, e error) {
	code, msg, status := "payment.unavailable", "处理结果暂未确认，请在订单中同步状态。", 503
	switch {
	case errors.Is(e, commerce.ErrInvalid), errors.Is(e, payment.ErrRequest):
		code, msg, status = "payment.invalid", "请核对渠道、商品、金额、币种和协议勾选。", 422
	case errors.Is(e, commerce.ErrNotFound):
		code, msg, status = "payment.not_found", "订单不存在。", 404
	case errors.Is(e, commerce.ErrDisabled):
		code, msg, status = "payment.disabled", "此支付或兑换入口尚未启用。", 409
	case errors.Is(e, commerce.ErrConflict):
		code, msg, status = "payment.conflict", "配置已更新，请重新载入并核对价格。", 409
	case errors.Is(e, commerce.ErrReused):
		code, msg, status = "payment.request_reused", "该请求编号已用于另一笔订单。", 409
	case errors.Is(e, commerce.ErrPending):
		code, msg, status = "payment.pending", "请先同步已有订单，避免重复支付。", 409
	case errors.Is(e, commerce.ErrResourceDelivery):
		code, msg, status = "payment.resource_closed", "资源已不可交付，请返回文章核验访问条件。", 409
	case errors.Is(e, payment.ErrExpiry):
		code, msg, status = "payment.expiry", "此渠道无法满足资源剩余的访问时限，请选择其他方式。", 409
	case errors.Is(e, progression.ErrIneligible):
		code, msg, status = "payment.ineligible", "请确认账号有效，并按站点要求验证邮箱。", 403
	case errors.Is(e, payment.ErrConfiguration):
		code, msg, status = "payment.configuration", "渠道配置尚未完成，请联系管理员。", 422
	case errors.Is(e, payment.ErrUnsupported):
		code, msg, status = "payment.manual_sync", "此渠道等待支付通知；请稍后刷新订单或联系管理员核对。", 409
	case errors.Is(e, commerce.ErrUnverified), errors.Is(e, payment.ErrSignature):
		code, msg, status = "payment.unverified", "支付验证未通过。", 400
	case errors.Is(e, progression.ErrMemberConflict), errors.Is(e, progression.ErrPending):
		code, msg, status = "payment.member_conflict", "已有其他会员或待处理购买，请先处理原订单。", 409
	case errors.Is(e, virtualcurrency.ErrBalanceLimit):
		code, msg, status = "payment.balance_limit", "目标钱包余额将超过上限，请联系管理员。", 409
	}
	s.writeAPIError(w, r, status, code, msg, nil)
}
func (s *Server) paymentsAdmin(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return auth.Principal{}, false
	}
	if write {
		p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
		if !ok {
			return p, false
		}
		if len(r.Header.Values("X-CSRF-Token")) != 1 || !s.currencySameOrigin(r) {
			s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请从本站页面操作", nil)
			return p, false
		}
		return p, s.currencyRate(w, r, "payments-admin:"+strconv.FormatInt(p.UserID, 10), 60)
	}
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	return p, ok
}
func (s *Server) paymentsUser(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return auth.Principal{}, false
	}
	p, ok := s.currencyPrincipal(w, r, write)
	if !ok {
		return p, false
	}
	if write {
		return p, s.currencyRate(w, r, "payments-user:"+strconv.FormatInt(p.UserID, 10), 20)
	}
	return p, true
}

// Provider-specific optional settings can be omitted, while every surrounding
// editable setting must be explicit. Nulls, aliases and duplicate keys fail closed.
func commerceShape(raw any, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		m, ok := raw.(map[string]any)
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
			value, exists := m[name]
			if !exists && (strings.Contains(tag, ",omitempty") || t == reflect.TypeFor[payment.Config]()) {
				continue
			}
			if !exists {
				return false
			}
			if strings.Contains(tag, ",string") {
				v, ok := value.(string)
				if !ok {
					return false
				}
				n, e := strconv.ParseInt(v, 10, 64)
				if e != nil || strconv.FormatInt(n, 10) != v {
					return false
				}
			} else if !commerceShape(value, f.Type) {
				return false
			}
		}
		return true
	}
	if t.Kind() == reflect.Slice {
		list, ok := raw.([]any)
		if !ok {
			return false
		}
		for _, v := range list {
			if !commerceShape(v, t.Elem()) {
				return false
			}
		}
		return true
	}
	return raw != nil
}
func (s *Server) decodeCommerce(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if len(r.Header.Values("Content-Type")) != 1 {
		s.writeAPIError(w, r, 415, "request.json_content_type_required", "请使用唯一的 application/json 内容类型", nil)
		return false
	}
	body, ok := s.decodeStrictAPIJSONLimit(w, r, target, limit, strconv.FormatInt(limit/1024, 10)+" KiB")
	if !ok {
		return false
	}
	var raw any
	if !utf8.Valid(body) || json.Unmarshal(body, &raw) != nil || !canonicalCurrencyJSON(raw) || !commerceShape(raw, reflect.TypeOf(target)) {
		s.writeAPIError(w, r, 400, "request.invalid_json", "请提交完整且格式准确的字段", nil)
		return false
	}
	return true
}

type paymentChannelDTO struct {
	Key          string         `json:"key"`
	Name         string         `json:"name"`
	Currency     string         `json:"currency"`
	Enabled      bool           `json:"enabled"`
	Config       payment.Config `json:"config"`
	SavedSecrets []string       `json:"saved_secrets,omitempty"`
	ClearSecrets []string       `json:"clear_secrets,omitempty"`
}
type paymentSettingsDTO struct {
	Version              int64               `json:"version,string"`
	Enabled              bool                `json:"enabled"`
	RequireVerifiedEmail bool                `json:"require_verified_email"`
	OrderMinutes         int                 `json:"order_minutes"`
	Disclaimer           string              `json:"disclaimer"`
	RefundPolicy         string              `json:"refund_policy"`
	Channels             []paymentChannelDTO `json:"channels"`
	Products             []commerce.Product  `json:"products"`
	Exchanges            []commerce.Exchange `json:"exchanges"`
}

func (s *Server) paymentsDTO(c commerce.Settings) (paymentSettingsDTO, error) {
	d := paymentSettingsDTO{Version: c.Version, Enabled: c.Enabled, RequireVerifiedEmail: c.RequireVerifiedEmail, OrderMinutes: c.OrderMinutes, Disclaimer: c.Disclaimer, RefundPolicy: c.RefundPolicy, Channels: []paymentChannelDTO{}, Products: c.Products, Exchanges: c.Exchanges}
	for _, ch := range c.Channels {
		cfg, e := s.commerce.Vault.Open(ch.SealedConfig)
		if e != nil {
			return d, e
		}
		saved := []string{}
		for name, value := range map[string]*string{"api_key": &cfg.APIKey, "secret": &cfg.Secret, "private_key": &cfg.PrivateKey} {
			if *value != "" {
				saved = append(saved, name)
			}
			*value = ""
		}
		d.Channels = append(d.Channels, paymentChannelDTO{Key: ch.Key, Name: ch.Name, Currency: ch.Currency, Enabled: ch.Enabled, Config: cfg, SavedSecrets: saved})
	}
	return d, nil
}
func (s *Server) adminPaymentsRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, false); !ok {
		return
	}
	c, e := s.commerce.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	d, e := s.paymentsDTO(c)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	coins, e := s.progressionCurrencies(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	plans, e := s.progression.Store.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": d, "providers": payment.Definitions(), "currencies": coins, "plans": plans.Membership.Plans, "public_url": s.config.PublicURL})
}
func (s *Server) adminPaymentsSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var d paymentSettingsDTO
	if !s.decodeCommerce(w, r, &d, 256<<10) {
		return
	}
	current, e := s.commerce.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	if current.Version != d.Version {
		s.commerceError(w, r, commerce.ErrConflict)
		return
	}
	c := commerce.Settings{Version: d.Version, Enabled: d.Enabled, RequireVerifiedEmail: d.RequireVerifiedEmail, OrderMinutes: d.OrderMinutes, Disclaimer: d.Disclaimer, RefundPolicy: d.RefundPolicy, Channels: []commerce.Channel{}, Products: d.Products, Exchanges: d.Exchanges}
	for _, v := range d.Channels {
		cfg := v.Config
		for _, old := range current.Channels {
			if old.Key == v.Key && old.Provider == cfg.Provider {
				prior, err := s.commerce.Vault.Open(old.SealedConfig)
				if err != nil {
					s.commerceError(w, r, err)
					return
				}
				if cfg.APIKey == "" {
					cfg.APIKey = prior.APIKey
				}
				if cfg.Secret == "" {
					cfg.Secret = prior.Secret
				}
				if cfg.PrivateKey == "" {
					cfg.PrivateKey = prior.PrivateKey
				}
			}
		}
		for _, name := range v.ClearSecrets {
			switch name {
			case "api_key":
				cfg.APIKey = ""
			case "secret":
				cfg.Secret = ""
			case "private_key":
				cfg.PrivateKey = ""
			default:
				s.commerceError(w, r, commerce.ErrInvalid)
				return
			}
		}
		sealed, err := s.commerce.Vault.Seal(cfg)
		if err != nil {
			s.commerceError(w, r, err)
			return
		}
		c.Channels = append(c.Channels, commerce.Channel{Key: v.Key, Name: v.Name, Currency: v.Currency, Enabled: v.Enabled, Provider: cfg.Provider, SealedConfig: sealed})
	}
	c, e = s.commerce.UpdateSettings(r.Context(), c)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	out, e := s.paymentsDTO(c)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": out})
}
func (s *Server) adminPaymentOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, false); !ok {
		return
	}
	s.writePaymentOrders(w, r, 0)
}
func (s *Server) writePaymentOrders(w http.ResponseWriter, r *http.Request, user int64) {
	orders, e := s.commerce.Orders(r.Context(), user)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	out := []map[string]any{}
	for _, o := range orders {
		if user == 0 {
			out = append(out, adminPaymentOrder(o))
		} else {
			out = append(out, commerce.PublicOrder(o))
		}
	}
	writeJSON(w, 200, map[string]any{"orders": out})
}
func adminPaymentOrder(o commerce.Order) map[string]any {
	v := commerce.PublicOrder(o)
	v["channel"] = o.Channel
	v["provider"] = o.Provider
	v["transaction_id"] = o.TransactionID
	v["credit_entry"] = strconv.FormatInt(o.CreditEntry, 10)
	v["member_entry"] = strconv.FormatInt(o.MemberEntry, 10)
	v["source_entry"] = strconv.FormatInt(o.SourceEntry, 10)
	return v
}
func (s *Server) adminPaymentRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, false); !ok {
		return
	}
	o, e := s.commerce.Order(r.Context(), 0, r.PathValue("order"))
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"orders": []map[string]any{adminPaymentOrder(o)}})
}
func (s *Server) adminPaymentSync(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	o, e := s.commerce.Sync(r.Context(), 0, r.PathValue("order"))
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, commerce.PublicOrder(o))
}
func (s *Server) socialBilling(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	cfg, e := s.commerce.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	channels := []map[string]any{}
	products := []commerce.Product{}
	exchanges := []commerce.Exchange{}
	for _, c := range cfg.Channels {
		if c.Enabled {
			channels = append(channels, map[string]any{"key": c.Key, "name": c.Name, "currency": c.Currency, "provider": c.Provider})
		}
	}
	for _, v := range cfg.Products {
		if v.Enabled {
			products = append(products, v)
		}
	}
	for _, v := range cfg.Exchanges {
		if v.Enabled {
			exchanges = append(exchanges, v)
		}
	}
	orders, e := s.commerce.Orders(r.Context(), p.UserID)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	public := []map[string]any{}
	for _, o := range orders {
		if o.Kind == "shop" {
			continue
		}
		public = append(public, commerce.PublicOrder(o))
	}
	coins, e := s.progressionCurrencies(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, map[string]any{"version": stringIDCommerce(cfg.Version), "user_id": stringIDCommerce(p.UserID), "csrf_token": p.CSRFToken, "enabled": cfg.Enabled, "require_verified_email": cfg.RequireVerifiedEmail, "channels": channels, "products": products, "exchanges": exchanges, "currencies": coins, "orders": public, "disclaimer": cfg.Disclaimer, "refund_policy": cfg.RefundPolicy})
}
func stringIDCommerce(n int64) string { return strconv.FormatInt(n, 10) }
func (s *Server) socialPaymentCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version   int64  `json:"version,string"`
		Product   string `json:"product"`
		Channel   string `json:"channel"`
		RequestID string `json:"request_id"`
		Accepted  bool   `json:"accepted"`
	}
	if !s.decodeCommerce(w, r, &in, 4096) {
		return
	}
	o, e := s.commerce.Create(r.Context(), commerce.Purchase{UserID: p.UserID, Version: in.Version, Product: in.Product, Channel: in.Channel, RequestID: in.RequestID, Accepted: in.Accepted, ClientIP: s.clientIPs.string(r)})
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, commerce.PublicOrder(o))
}
func (s *Server) socialPaymentRead(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	o, e := s.commerce.Order(r.Context(), p.UserID, r.PathValue("order"))
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, commerce.PublicOrder(o))
}
func (s *Server) socialPaymentQR(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	o, err := s.commerce.Order(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.commerceError(w, r, err)
		return
	}
	if o.Status != "pending" || o.Checkout.QRCode == "" || o.ExpiresAt.Before(time.Now()) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(o.Checkout.QRCode, qrcode.Medium, 320)
	if err != nil {
		s.commerceError(w, r, commerce.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}
func (s *Server) socialPaymentSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	o, e := s.commerce.Sync(r.Context(), p.UserID, r.PathValue("order"))
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, commerce.PublicOrder(o))
}
func (s *Server) socialExchange(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version   int64  `json:"version,string"`
		Rule      string `json:"rule"`
		Amount    string `json:"amount"`
		RequestID string `json:"request_id"`
		Accepted  bool   `json:"accepted"`
	}
	if !s.decodeCommerce(w, r, &in, 4096) {
		return
	}
	cfg, e := s.commerce.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	from := ""
	for _, x := range cfg.Exchanges {
		if x.Key == in.Rule {
			from = x.From
		}
	}
	coin, e := s.currencyCatalog.Store(r.Context(), from)
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	c, e := coin.Settings(r.Context())
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	amount, e := virtualcurrency.ParseAmount(in.Amount, c.Precision)
	if e != nil {
		s.commerceError(w, r, commerce.ErrInvalid)
		return
	}
	o, e := s.commerce.Exchange(r.Context(), commerce.ExchangeInput{UserID: p.UserID, Version: in.Version, Rule: in.Rule, Amount: amount, RequestID: in.RequestID, Accepted: in.Accepted})
	if e != nil {
		s.commerceError(w, r, e)
		return
	}
	writeJSON(w, 200, commerce.PublicOrder(o))
}
func (s *Server) paymentNotify(w http.ResponseWriter, r *http.Request) {
	if !s.currencyRate(w, r, "payment-notify:"+s.clientIPs.string(r), 180) {
		return
	}
	var body []byte
	var e error
	if r.Method == http.MethodGet {
		body = []byte(r.URL.RawQuery)
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, e = io.ReadAll(r.Body)
	}
	if e != nil || len(body) > 1<<20 {
		http.Error(w, "invalid notification", 400)
		return
	}
	id := r.PathValue("order")
	if channel := r.PathValue("channel"); channel != "" {
		// Locate only; authentication always uses the order's frozen provider
		// configuration below, so rotating or removing a channel cannot orphan it.
		for _, definition := range payment.Definitions() {
			candidate, err := payment.ExtractOrderID(definition.Key, r.Header, body)
			if err != nil || !commerce.ValidOrderID(candidate) {
				continue
			}
			order, err := s.commerce.Order(r.Context(), 0, candidate)
			if err == nil && order.Channel == channel && order.Provider == definition.Key {
				if id != "" && id != candidate {
					http.Error(w, "ambiguous notification", 400)
					return
				}
				id = candidate
			}
		}
		if id == "" {
			http.Error(w, "invalid notification", 400)
			return
		}
	}
	if !commerce.ValidOrderID(id) {
		http.Error(w, "invalid notification", 400)
		return
	}
	n, e := s.commerce.Notify(r.Context(), id, r.Header, body)
	if e != nil {
		http.Error(w, "notification not committed", 503)
		return
	}
	contentType := n.AcknowledgementContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	ack := n.Acknowledgement
	if ack == "" {
		ack = "success"
	}
	_, _ = io.WriteString(w, ack)
}
func (s *Server) adminPaymentsPage(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	data := s.baseViewFor(w, "支付与订单", "管理支付渠道、充值商品、币种兑换与订单", "/admin/payments/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-payments"
	data.Admin = s.adminView("payments", p, ro)
	s.render(w, 200, "admin-payments", data)
}
func (s *Server) billingPage(w http.ResponseWriter, r *http.Request) {
	if s.commerce == nil && s.themes.HasTemplate("service-unavailable") {
		s.renderServiceStatus(w, 503, "支付与订单", "unavailable")
		return
	}
	data := s.baseViewFor(w, "充值与订单", "充值、开通会员与查看支付记录", "/billing/")
	data.BodyClass = "is-billing"
	data.ActiveNav = "account"
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	// Only this checkout page may submit provider-signed HTTPS payment forms.
	// All fields originate from a persisted, authenticated order.
	w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "form-action 'self'", "form-action 'self' https:", 1))
	s.render(w, 200, "billing", data)
}
