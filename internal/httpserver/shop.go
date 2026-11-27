package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/shop"
)

func (s *Server) SetShopService(v *shop.Service) { s.shop = v }

func (s *Server) registerShopRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /shop/{$}", s.shopPage)
	mux.HandleFunc("GET /shop/orders/{$}", s.shopPage)
	mux.HandleFunc("GET /shop/product/{product}/{$}", s.shopPage)
	mux.HandleFunc("GET /admin/shop/{$}", s.adminShopPage)
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/shop/catalog":                                        s.shopCatalog,
		"GET /api/v1/shop/products/{product}/reviews":                     s.shopProductReviews,
		"GET /api/v1/shop/orders/{order}/review":                          s.shopOrderReview,
		"POST /api/v1/shop/orders/{order}/review":                         s.shopCreateReview,
		"GET /api/v1/admin/shop/reviews":                                  s.adminShopReviews,
		"PUT /api/v1/admin/shop/reviews/{review}":                         s.adminShopReview,
		"GET /api/v1/shop/coupons":                                        s.shopCoupons,
		"GET /api/v1/shop/session":                                        s.shopSession,
		"GET /api/v1/shop/my-coupons":                                     s.shopMyCoupons,
		"POST /api/v1/shop/coupons/claim":                                 s.shopClaimCoupon,
		"POST /api/v1/shop/quote":                                         s.shopQuote,
		"GET /api/v1/shop/orders":                                         s.shopOrders,
		"POST /api/v1/shop/orders":                                        s.shopCreate,
		"GET /api/v1/shop/orders/{order}":                                 s.shopOrder,
		"POST /api/v1/shop/orders/{order}/pay":                            s.shopPay,
		"POST /api/v1/shop/orders/{order}/sync":                           s.shopSync,
		"POST /api/v1/shop/orders/{order}/cancel":                         s.shopCancel,
		"POST /api/v1/shop/orders/{order}/received":                       s.shopReceived,
		"POST /api/v1/shop/orders/{order}/refund":                         s.shopRefund,
		"GET /api/v1/admin/shop":                                          s.adminShopCatalog,
		"PUT /api/v1/admin/shop":                                          s.adminShopSave,
		"POST /api/v1/admin/shop/components/validate":                     s.validateShopComponent,
		"GET /api/v1/admin/shop/article-widgets":                          s.shopArticleWidgets,
		"GET /api/v1/admin/shop/coupons":                                  s.adminShopCoupons,
		"PUT /api/v1/admin/shop/coupons":                                  s.adminShopSaveCoupons,
		"GET /api/v1/admin/shop/orders":                                   s.adminShopOrders,
		"POST /api/v1/admin/shop/orders/{order}/ship":                     s.adminShopShip,
		"POST /api/v1/admin/shop/orders/{order}/tickets/{ticket}/deliver": s.adminShopDeliverPrize,
		"POST /api/v1/admin/shop/orders/{order}/refund":                   s.adminShopRefund,
		"POST /api/v1/admin/shop/orders/{order}/refund-review":            s.adminShopRefundReview,
		"POST /api/v1/admin/shop/advance":                                 s.adminShopAdvance,
	}
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			if s.shop == nil {
				s.writeAPIError(w, r, 503, "shop.unavailable", "商城服务尚未装配。", nil)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			handler(w, r.WithContext(ctx))
		})
	}
}

func (s *Server) shopError(w http.ResponseWriter, r *http.Request, err error) {
	code, message, status := "shop.unavailable", "商城暂时不可用，请稍后重试。", 503
	switch {
	case errors.Is(err, shop.ErrServiceModules):
		code, message, status = "shop.service_modules_invalid", "请检查服务说明：最多 60 项，标题最多 24 字，说明最多 100 字，链接使用站内路径或 HTTPS 地址。", 422
	case errors.Is(err, shop.ErrReviewInvalid):
		code, message, status = "shop.review_invalid", "请选择 1 至 5 星并填写评价，评价和回复最多 2000 字；隐藏评价需要填写原因。", 422
	case errors.Is(err, shop.ErrReviewEligibility):
		code, message, status = "shop.review_ineligible", "订单付清并完成交付后才可评价，实物商品请先确认收货。", 409
	case errors.Is(err, shop.ErrReviewExists):
		code, message, status = "shop.review_exists", "这笔订单已提交评价，每笔订单只能评价一次。", 409
	case errors.Is(err, shop.ErrReviewConflict):
		code, message, status = "shop.review_conflict", "此评价已被更新，请刷新评价列表后重试。", 409
	case errors.Is(err, shop.ErrInvalid):
		code, message, status = "shop.invalid", "请检查商品、数量、购买计划及收货信息。", 422
	case errors.Is(err, shop.ErrConflict):
		code, message, status = "shop.conflict", "商品配置已更新，请刷新并重新确认价格。", 409
	case errors.Is(err, shop.ErrDisabled):
		code, message, status = "shop.disabled", "商城尚未开放。", 409
	case errors.Is(err, shop.ErrNotFound):
		code, message, status = "shop.not_found", "商品或订单不存在。", 404
	case errors.Is(err, shop.ErrRaffleTotal):
		code, message, status = "shop.raffle_total", "剩余抽奖码不足，请刷新商品后调整数量。", 409
	case errors.Is(err, shop.ErrRaffleUserLimit):
		code, message, status = "shop.raffle_user_limit", "已超出账号在本活动或商品的剩余额度，请调整数量或查看待付款订单。", 409
	case errors.Is(err, shop.ErrRaffleOrderLimit):
		code, message, status = "shop.raffle_order_limit", "超过此商品单笔订单的抽奖码上限，请减少数量。", 422
	case errors.Is(err, shop.ErrRaffleClosed):
		code, message, status = "shop.raffle_closed", "本轮抽奖已截止，可在商品页查看开奖进度。", 409
	case errors.Is(err, shop.ErrStock):
		code, message, status = "shop.stock", "商品库存不足或当前不可购买。", 409
	case errors.Is(err, shop.ErrReused):
		code, message, status = "shop.request_reused", "此请求编号已用于其他订单。", 409
	case errors.Is(err, shop.ErrState):
		code, message, status = "shop.state", "订单状态已变化，或尚未到付款时间，请刷新订单。", 409
	case errors.Is(err, shop.ErrIneligible):
		code, message, status = "shop.ineligible", "此优惠券不适用于当前商品、订单或账号。", 422
	}
	s.writeAPIError(w, r, status, code, message, nil)
}

func validShopJSON(v any, key string) bool {
	if v == nil {
		return key == "installment" || key == "group" || key == "raffle" || key == "variants" || key == "bundle" || key == "codes" || key == "services"
	}
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if k != strings.ToLower(k) || !validShopJSON(item, k) {
				return false
			}
		}
	case []any:
		for _, item := range x {
			if !validShopJSON(item, "") {
				return false
			}
		}
	}
	return true
}
func (s *Server) decodeShop(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if len(r.Header.Values("Content-Type")) != 1 {
		s.writeAPIError(w, r, 415, "request.json_content_type_required", "请使用 application/json。", nil)
		return false
	}
	body, ok := s.decodeStrictAPIJSONLimit(w, r, target, limit, "商城请求大小上限")
	if !ok {
		return false
	}
	var raw any
	if !utf8.Valid(body) || json.Unmarshal(body, &raw) != nil || !validShopJSON(raw, "") {
		s.writeAPIError(w, r, 400, "request.invalid_json", "字段名、空值或文本编码无效。", nil)
		return false
	}
	return true
}

func (s *Server) shopPaymentConfig(ctx context.Context) ([]map[string]any, bool, error) {
	channels := []map[string]any{}
	if s.commerce == nil {
		return channels, false, nil
	}
	settings, err := s.commerce.Settings(ctx)
	if err != nil {
		return nil, false, err
	}
	if settings.Enabled {
		for _, ch := range settings.Channels {
			if ch.Enabled {
				channels = append(channels, map[string]any{"key": ch.Key, "name": ch.Name, "currency": ch.Currency, "provider": ch.Provider})
			}
		}
	}
	return channels, settings.Enabled, nil
}
func (s *Server) shopCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	overview, err := s.shop.StorefrontOverview(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	catalog := overview.Catalog
	groups, err := s.shop.Groups(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	publicGroups := []map[string]any{}
	if catalog.Enabled {
		for _, g := range groups {
			if g.Status == "forming" || g.Status == "open" {
				publicGroups = append(publicGroups, map[string]any{"id": g.ID, "product": g.Product, "variant": g.Variant, "size": g.Size, "paid": g.Paid, "status": g.Status, "expires_at": g.ExpiresAt})
			}
		}
	}
	channels, enabled, err := s.shopPaymentConfig(r.Context())
	if err != nil {
		s.commerceError(w, r, err)
		return
	}
	coupons, err := s.shop.CouponList(r.Context(), 0, false)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	currencies, err := s.shop.CurrencyList(r.Context(), 0)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalog": catalog, "groups": publicGroups, "channels": channels, "payment_enabled": enabled, "currencies": currencies, "coupons": coupons, "product_stats": overview.ProductStats, "raffle_rounds": overview.RaffleRounds, "raffle_quotas": overview.RaffleQuotas, "server_time": overview.ServerTime})
}
func (s *Server) shopCoupons(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	coupons, err := s.shop.CouponList(r.Context(), 0, true)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"coupons": coupons})
}
func (s *Server) shopSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	currencies, err := s.shop.CurrencyList(r.Context(), p.UserID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	coupons := []shop.CouponOption{}
	if p.UserID > 0 {
		coupons, err = s.shop.CouponList(r.Context(), p.UserID, false)
		if err != nil {
			s.shopError(w, r, err)
			return
		}
	}
	quotas, err := s.shop.PersonalRaffleQuotas(r.Context(), p.UserID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": stringIDCommerce(p.UserID), "csrf_token": p.CSRFToken, "currencies": currencies, "coupons": coupons, "raffle_quotas": quotas})
}
func (s *Server) shopMyCoupons(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	coupons, err := s.shop.CouponList(r.Context(), p.UserID, false)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"coupons": coupons})
}
func (s *Server) shopClaimCoupon(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	claim, err := s.shop.ClaimCoupon(r.Context(), p.UserID, in.Code)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"claim": claim})
}
func (s *Server) shopQuote(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if !s.currencySameOrigin(r) {
		s.writeAPIError(w, r, 403, "auth.origin_invalid", "请从本站商城查询价格。", nil)
		return
	}
	if !s.currencyRate(w, r, "shop-quote:"+s.clientIPs.string(r), 90) {
		return
	}
	var in shop.QuoteInput
	if !s.decodeShop(w, r, &in, 8192) {
		return
	}
	user := int64(0)
	if in.Coupon != "" {
		p, ok := s.paymentsUser(w, r, false)
		if !ok {
			return
		}
		user = p.UserID
	} else if p, ok := s.currentAdmin(r); ok {
		user = p.UserID
	}
	quote, err := s.shop.QuoteFor(r.Context(), user, in)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"quote": quote})
}

type shopPurchaseDTO struct {
	shop.QuoteInput
	Version   int64        `json:"version,string"`
	RequestID string       `json:"request_id"`
	Accepted  bool         `json:"accepted"`
	Address   shop.Address `json:"address"`
}

func (s *Server) shopCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in shopPurchaseDTO
	if !s.decodeShop(w, r, &in, 16384) {
		return
	}
	order, err := s.shop.Create(r.Context(), shop.Purchase{QuoteInput: in.QuoteInput, UserID: p.UserID, Version: in.Version, RequestID: in.RequestID, Accepted: in.Accepted, Address: in.Address})
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func publicShopOrders(orders []shop.Order) []shop.Order {
	out := make([]shop.Order, 0, len(orders))
	for _, o := range orders {
		out = append(out, shop.PublicOrder(o))
	}
	return out
}
func (s *Server) shopOrders(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	orders, err := s.shop.Orders(r.Context(), p.UserID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"orders": publicShopOrders(orders), "csrf_token": p.CSRFToken})
}
func (s *Server) shopOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	order, err := s.shop.Order(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	payments := []map[string]any{}
	if s.commerce != nil {
		list, e := s.commerce.ShopPayments(r.Context(), p.UserID, order.ID)
		if e != nil {
			s.commerceError(w, r, e)
			return
		}
		for _, payment := range list {
			v := commerce.PublicOrder(payment)
			v["index"] = payment.ShopInstallment - 1
			payments = append(payments, v)
		}
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order), "payments": payments})
}
func (s *Server) shopPay(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Index    int    `json:"index"`
		Channel  string `json:"channel"`
		Accepted bool   `json:"accepted"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	orderID := r.PathValue("order")
	order, err := s.shop.Order(r.Context(), p.UserID, orderID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	if strings.HasPrefix(order.Currency, "wallet:") {
		if in.Channel != "wallet" {
			s.writeAPIError(w, r, 422, "shop.wallet_channel_required", "请选择订单对应的站内货币支付。", nil)
			return
		}
		if !in.Accepted {
			s.writeAPIError(w, r, 422, "shop.consent_required", "请确认订单后再使用站内货币支付。", nil)
			return
		}
		paid, err := s.shop.PayWallet(r.Context(), p.UserID, orderID, in.Index)
		if err != nil {
			s.shopError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(paid)})
		return
	}
	if in.Channel == "wallet" {
		s.writeAPIError(w, r, 422, "shop.wallet_currency_mismatch", "此订单不能使用站内货币支付。", nil)
		return
	}
	if s.commerce == nil {
		s.commerceError(w, r, commerce.ErrUnavailable)
		return
	}
	request, err := s.shop.Payment(r.Context(), p.UserID, r.PathValue("order"), in.Index)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	payment, err := s.commerce.CreateShopPayment(r.Context(), commerce.ShopPayment{UserID: p.UserID, OrderID: request.OrderID, Title: request.Title, Currency: request.Currency, Amount: request.Amount, Installment: request.Index + 1, ExpiresAt: request.ExpiresAt, Channel: in.Channel, Accepted: in.Accepted, ClientIP: s.clientIPs.string(r)})
	if err != nil {
		s.commerceError(w, r, err)
		return
	}
	order, err = s.shop.Order(r.Context(), p.UserID, request.OrderID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order), "payment": commerce.PublicOrder(payment)})
}
func (s *Server) shopSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var empty struct{}
	if !s.decodeShop(w, r, &empty, 1024) {
		return
	}
	order, err := s.shop.Order(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	pending := false
	if s.commerce != nil {
		payments, e := s.commerce.ShopPayments(r.Context(), p.UserID, order.ID)
		if e != nil {
			s.commerceError(w, r, e)
			return
		}
		for _, payment := range payments {
			if _, e = s.commerce.Sync(r.Context(), p.UserID, payment.ID); e != nil {
				pending = true
			}
		}
	}
	if err = s.shop.Advance(r.Context()); err != nil {
		s.shopError(w, r, err)
		return
	}
	order, err = s.shop.Order(r.Context(), p.UserID, order.ID)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order), "sync_pending": pending})
}
func (s *Server) shopCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var empty struct{}
	if !s.decodeShop(w, r, &empty, 1024) {
		return
	}
	order, err := s.shop.Cancel(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}

func (s *Server) shopReceived(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct{}
	if !s.decodeShop(w, r, &in, 1024) {
		return
	}
	order, err := s.shop.ConfirmReceipt(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) shopRefund(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	order, err := s.shop.RequestRefund(r.Context(), p.UserID, r.PathValue("order"), in.Note)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) adminShopPage(w http.ResponseWriter, r *http.Request) {
	p, readonly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	data := s.baseViewFor(w, "商城管理", "管理商品、库存、订单与交付。", "/admin/shop/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-shop"
	data.Admin = s.adminView("shop", p, readonly)
	s.render(w, 200, "admin-shop", data)
}
func (s *Server) adminShopCatalog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsAdmin(w, r, false)
	if !ok {
		return
	}
	catalog, err := s.shop.Catalog(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	channels, enabled, err := s.shopPaymentConfig(r.Context())
	if err != nil {
		s.commerceError(w, r, err)
		return
	}
	currencies, err := s.shop.CurrencyList(r.Context(), 0)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalog": catalog, "channels": channels, "payment_enabled": enabled, "currencies": currencies, "csrf_token": p.CSRFToken, "service_library": shop.ServiceModuleLibrary()})
}
func (s *Server) adminShopSave(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.paymentsAdmin(w, r, true)
	if !ok {
		return
	}
	var in shop.Catalog
	if !s.decodeShop(w, r, &in, 4<<20) {
		return
	}
	previous, err := s.shop.Catalog(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	owners := make(map[string]int64, len(previous.Products))
	for _, p := range previous.Products {
		owners[p.Key] = p.DescriptionAuthorID
	}
	for i := range in.Products {
		p := &in.Products[i]
		p.DescriptionAuthorID = owners[p.Key]
		if p.DescriptionAuthorID == 0 && strings.TrimSpace(p.DescriptionSource) != "" {
			p.DescriptionAuthorID = principal.UserID
		}
	}
	mediaValues := map[string]any{}
	for i, product := range in.Products {
		for key, value := range productMediaValues(product) {
			mediaValues[fmt.Sprintf("product_%d_%s", i, key)] = value
		}
	}
	release, ok := s.holdSettingMedia(w, r, mediaValues)
	if !ok {
		return
	}
	defer release()
	catalog, err := s.shop.SaveCatalog(r.Context(), in)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"catalog": catalog})
}
func (s *Server) adminShopCoupons(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, false); !ok {
		return
	}
	version, coupons, err := s.shop.CouponConfig(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"version": version, "coupons": coupons})
}
func (s *Server) adminShopSaveCoupons(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Version int64         `json:"version"`
		Coupons []shop.Coupon `json:"coupons"`
	}
	if !s.decodeShop(w, r, &in, 1<<20) {
		return
	}
	version, err := s.shop.SaveCoupons(r.Context(), in.Version, in.Coupons)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	_, coupons, err := s.shop.CouponConfig(r.Context())
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"version": version, "coupons": coupons})
}
func (s *Server) adminShopOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, false); !ok {
		return
	}
	orders, err := s.shop.Orders(r.Context(), 0)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"orders": publicShopOrders(orders)})
}
func (s *Server) adminShopShip(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Carrier  string `json:"carrier"`
		Tracking string `json:"tracking"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	order, err := s.shop.Ship(r.Context(), shop.Shipment{OrderID: r.PathValue("order"), Carrier: in.Carrier, Tracking: in.Tracking})
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) adminShopRefund(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	order, err := s.shop.ReconcileRefund(r.Context(), shop.Refund{OrderID: r.PathValue("order"), Reference: in.Reference, Note: in.Note})
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) adminShopRefundReview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Approved bool   `json:"approved"`
		Note     string `json:"note"`
	}
	if !s.decodeShop(w, r, &in, 4096) {
		return
	}
	order, err := s.shop.ReviewRefund(r.Context(), r.PathValue("order"), in.Approved, in.Note)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) adminShopDeliverPrize(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if !s.decodeShop(w, r, &in, 20<<10) {
		return
	}
	order, err := s.shop.DeliverPrize(r.Context(), r.PathValue("order"), r.PathValue("ticket"), in.Content)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
}
func (s *Server) adminShopAdvance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	var empty struct{}
	if !s.decodeShop(w, r, &empty, 1024) {
		return
	}
	if err := s.shop.Advance(r.Context()); err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
