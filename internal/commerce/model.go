// Package commerce coordinates durable payment orders, wallet credits and
// memberships. Provider return URLs never constitute payment evidence.
package commerce

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

var (
	ErrInvalid          = errors.New("invalid commerce request")
	ErrConflict         = errors.New("commerce settings changed")
	ErrDisabled         = errors.New("commerce is disabled")
	ErrNotFound         = errors.New("order not found")
	ErrReused           = errors.New("commerce request reused")
	ErrUnverified       = errors.New("payment does not match order")
	ErrUnavailable      = errors.New("commerce unavailable")
	ErrPending          = errors.New("order is pending")
	ErrResourceDelivery = errors.New("resource delivery requires reconciliation")
)

const Disclaimer = "支付由所选第三方渠道处理，本站不保存银行卡信息。购买前请核对商品、金额、币种与退款规则。交易费用和汇率以支付渠道结算为准。LDC 为社区积分，站内余额、经验与会员权益不构成法币存款、投资收益或兑付承诺。运营方应自行确认业务资质、税务及所在地适用规则；本软件提供技术工具，不替代法律意见或支付机构服务协议。"

// SealedConfig contains AES-GCM ciphertext, never a cleartext merchant key.
type Channel struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Currency     string `json:"currency"`
	Enabled      bool   `json:"enabled"`
	SealedConfig string `json:"sealed_config"`
}
type Product struct {
	Key            string `json:"key"`
	Title          string `json:"title"`
	Enabled        bool   `json:"enabled"`
	Kind           string `json:"kind"`
	Currency       string `json:"currency"`
	Price          int64  `json:"price,string"`
	TargetCurrency string `json:"target_currency"`
	Credit         int64  `json:"credit,string"`
	Plan           string `json:"plan"`
}
type Exchange struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Enabled   bool   `json:"enabled"`
	From      string `json:"from"`
	To        string `json:"to"`
	FromUnits int64  `json:"from_units,string"`
	ToUnits   int64  `json:"to_units,string"`
	FeeBPS    int    `json:"fee_bps"`
	Minimum   int64  `json:"minimum,string"`
	Maximum   int64  `json:"maximum,string"`
}
type Settings struct {
	Version              int64      `json:"version,string"`
	Enabled              bool       `json:"enabled"`
	RequireVerifiedEmail bool       `json:"require_verified_email"`
	OrderMinutes         int        `json:"order_minutes"`
	Disclaimer           string     `json:"disclaimer"`
	RefundPolicy         string     `json:"refund_policy"`
	Channels             []Channel  `json:"channels"`
	Products             []Product  `json:"products"`
	Exchanges            []Exchange `json:"exchanges"`
}

func Defaults() Settings {
	return Settings{Version: 1, RequireVerifiedEmail: true, OrderMinutes: 30, Disclaimer: Disclaimer, RefundPolicy: "退款需联系站点管理员，由管理员核对订单、权益使用情况与支付渠道规则后处理。", Channels: []Channel{}, Products: []Product{}, Exchanges: []Exchange{}}
}

type Order struct {
	ID               string                   `json:"id"`
	UserID           int64                    `json:"user_id,string"`
	RequestID        string                   `json:"request_id"`
	Fingerprint      string                   `json:"fingerprint"`
	Kind             string                   `json:"kind"`
	Title            string                   `json:"title"`
	Product          string                   `json:"product"`
	Channel          string                   `json:"channel"`
	Provider         string                   `json:"provider"`
	PaymentScope     string                   `json:"payment_scope"`
	SealedConfig     string                   `json:"sealed_config"`
	Status           string                   `json:"status"`
	Amount           int64                    `json:"amount,string"`
	Currency         string                   `json:"currency"`
	Precision        int                      `json:"precision"`
	TargetCurrency   string                   `json:"target_currency"`
	TargetPrecision  int                      `json:"target_precision"`
	Credit           int64                    `json:"credit,string"`
	SourceCurrency   string                   `json:"source_currency"`
	SourceVersion    int64                    `json:"source_version,string"`
	SourcePrecision  int                      `json:"source_precision"`
	Plan             string                   `json:"plan"`
	Days             int                      `json:"days"`
	Checkout         payment.Checkout         `json:"checkout"`
	TransactionID    string                   `json:"transaction_id"`
	CreditEntry      int64                    `json:"credit_entry,string"`
	MemberEntry      int64                    `json:"member_entry,string"`
	SourceEntry      int64                    `json:"source_entry,string"`
	ErrorCode        string                   `json:"error_code"`
	Disclaimer       string                   `json:"disclaimer"`
	RefundPolicy     string                   `json:"refund_policy"`
	SettingsVersion  int64                    `json:"settings_version,string"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
	ExpiresAt        time.Time                `json:"expires_at"`
	PaidAt           time.Time                `json:"paid_at"`
	CompletedAt      time.Time                `json:"completed_at"`
	LeaseUntil       time.Time                `json:"lease_until"`
	CheckoutAttempts int                      `json:"checkout_attempts"`
	ResourceKey      string                   `json:"resource_key,omitempty"`
	ResourceURL      string                   `json:"resource_url,omitempty"`
	ResourceTerms    *resources.PurchaseTerms `json:"resource_terms,omitempty"`
	ResourceDeadline time.Time                `json:"resource_deadline,omitempty"`
	ResourceAttempt  int                      `json:"resource_attempt,omitempty"`
	PaymentClosedAt  time.Time                `json:"payment_closed_at,omitempty"`
	ShopOrder        string                   `json:"shop_order,omitempty"`
	ShopInstallment  int                      `json:"shop_installment,omitempty"`
	ShopSettled      bool                     `json:"shop_settled,omitempty"`
}

// PublicOrder deliberately omits encrypted snapshots, request fingerprints and leases.
func PublicOrder(o Order) map[string]any {
	if o.Status != "pending" || !o.ExpiresAt.After(time.Now()) {
		o.Checkout = payment.Checkout{}
	}
	qrURL := ""
	if o.Checkout.QRCode != "" {
		qrURL = "/api/v1/social/billing/orders/" + o.ID + "/qr"
	}
	return map[string]any{"resource_url": o.ResourceURL, "resource_ends_at": o.ResourceDeadline, "id": o.ID, "user_id": stringID(o.UserID), "kind": o.Kind, "title": o.Title, "status": o.Status, "amount": virtualcurrency.FormatAmount(o.Amount, o.Precision), "currency": o.Currency, "checkout": o.Checkout, "qr_url": qrURL, "target_currency": o.TargetCurrency, "credit": virtualcurrency.FormatAmount(o.Credit, o.TargetPrecision), "plan": o.Plan, "days": o.Days, "created_at": o.CreatedAt, "expires_at": o.ExpiresAt, "paid_at": o.PaidAt, "completed_at": o.CompletedAt, "error_code": o.ErrorCode, "disclaimer": o.Disclaimer, "refund_policy": o.RefundPolicy}
}

type State struct {
	Schema   int              `json:"schema"`
	Settings Settings         `json:"settings"`
	Orders   map[string]Order `json:"orders"`
}

func InitialState() State { return State{Schema: 1, Settings: Defaults(), Orders: map[string]Order{}} }

type Backend interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}

func NewID() string {
	// 31 characters fit WeChat's 32-character merchant order limit.
	var b [14]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "np_" + hex.EncodeToString(b[:])
}
func validID(v string) bool {
	if len(v) != 31 || !strings.HasPrefix(v, "np_") || v != strings.ToLower(v) {
		return false
	}
	_, err := hex.DecodeString(v[3:])
	return err == nil
}
func ValidOrderID(v string) bool { return validID(v) }
func validText(v string, max int) bool {
	return v != "" && len([]rune(v)) <= max && !strings.ContainsAny(v, "\x00\r\n<>")
}
func ValidateSettings(c Settings) error {
	if c.Version < 1 || c.OrderMinutes < 5 || c.OrderMinutes > 1440 || len([]rune(c.Disclaimer)) < 20 || len(c.Disclaimer) > 12000 || len(c.RefundPolicy) > 8000 || c.RefundPolicy == "" || len(c.Channels) > 40 || len(c.Products) > 100 || len(c.Exchanges) > 100 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, ch := range c.Channels {
		if !virtualcurrency.ValidCurrencyKey(ch.Key) || seen["c"+ch.Key] || !validText(ch.Name, 60) || ch.SealedConfig == "" || len(ch.SealedConfig) > 65536 {
			return ErrInvalid
		}
		seen["c"+ch.Key] = true
		found := false
		for _, d := range payment.Definitions() {
			if d.Key == ch.Provider {
				for _, v := range d.Currencies {
					if v == ch.Currency {
						found = true
					}
				}
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	for _, p := range c.Products {
		if !virtualcurrency.ValidCurrencyKey(p.Key) || seen["p"+p.Key] || !validText(p.Title, 80) || p.Price < 1 || p.Price > 100_000_000_000 || p.Kind != "recharge" && p.Kind != "membership" {
			return ErrInvalid
		}
		seen["p"+p.Key] = true
		if p.Kind == "recharge" && (!virtualcurrency.ValidCurrencyKey(p.TargetCurrency) || p.Credit < 1 || p.Credit > 10_000_000_000_000) || p.Kind == "membership" && !virtualcurrency.ValidCurrencyKey(p.Plan) {
			return ErrInvalid
		}
		if _, err := Exponent(p.Currency); err != nil {
			return err
		}
	}
	for _, x := range c.Exchanges {
		if !virtualcurrency.ValidCurrencyKey(x.Key) || seen["x"+x.Key] || !validText(x.Title, 80) || !virtualcurrency.ValidCurrencyKey(x.From) || !virtualcurrency.ValidCurrencyKey(x.To) || x.From == x.To || x.FromUnits < 1 || x.ToUnits < 1 || x.Minimum < 1 || x.Maximum < x.Minimum || x.Maximum > 10_000_000_000_000 || x.FeeBPS < 0 || x.FeeBPS > 5000 {
			return ErrInvalid
		}
		seen["x"+x.Key] = true
		if x.FromUnits > 10_000_000_000_000 || x.ToUnits > 10_000_000_000_000 {
			return ErrInvalid
		}
	}
	return nil
}
func Exponent(c string) (int, error) {
	switch c {
	case "JPY":
		return 0, nil
	case "CNY", "USD", "EUR", "GBP", "HKD", "LDC":
		return 2, nil
	}
	return 0, ErrInvalid
}
func ValidateState(s State) error {
	if s.Schema != 1 || ValidateSettings(s.Settings) != nil || s.Orders == nil || len(s.Orders) > 100000 {
		return ErrInvalid
	}
	requests, transactions := map[string]bool{}, map[string]bool{}
	for id, o := range s.Orders {
		if id != o.ID || !validID(id) || o.UserID < 1 || !virtualcurrency.ValidRequestID(o.RequestID) || o.Amount < 1 || o.Fingerprint == "" || o.CreatedAt.IsZero() || o.ExpiresAt.Before(o.CreatedAt) {
			return ErrInvalid
		}
		if o.CheckoutAttempts < 0 || o.CheckoutAttempts > 100 {
			return ErrInvalid
		}
		if !validText(o.Title, 80) || !virtualcurrency.ValidCurrencyKey(o.Product) || o.SettingsVersion < 1 || o.UpdatedAt.Before(o.CreatedAt) || o.Precision < 0 || o.Precision > 8 || o.TargetPrecision < 0 || o.TargetPrecision > 8 || o.CreditEntry < 0 || o.MemberEntry < 0 || o.SourceEntry < 0 {
			return ErrInvalid
		}
		switch o.Kind {
		case "exchange":
			if o.SourceCurrency != o.Currency || o.SourceCurrency == o.TargetCurrency || !virtualcurrency.ValidCurrencyKey(o.SourceCurrency) || o.SourceVersion < 1 || o.SourcePrecision != o.Precision || o.SealedConfig != "" {
				return ErrInvalid
			}
		case "recharge", "membership", "shop", "resource":
			exp, err := Exponent(o.Currency)
			if err != nil || exp != o.Precision || !virtualcurrency.ValidCurrencyKey(o.Channel) || o.SealedConfig == "" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if o.Kind == "resource" {
			if len(o.ResourceKey) != 64 || o.ResourceURL != "" && (!strings.HasPrefix(o.ResourceURL, "/posts/") && !strings.HasPrefix(o.ResourceURL, "/pages/") || strings.ContainsAny(o.ResourceURL, "\\\r\n")) {
				return ErrInvalid
			}
			if o.ResourceAttempt < 0 || o.ResourceAttempt > 1_000_000 || !o.ResourceDeadline.IsZero() && (o.ResourceDeadline.Before(o.CreatedAt) || o.ExpiresAt.After(o.ResourceDeadline)) {
				return ErrInvalid
			}
			if o.ResourceTerms != nil && (!o.ResourceTerms.Valid() || o.ResourceTerms.Kind != "payment" || o.ResourceTerms.Amount != o.Amount || o.ResourceTerms.Currency != o.Currency || o.ResourceTerms.LegacyKey() != o.ResourceKey || o.ResourceTerms.EndsAt != nil && !o.ResourceTerms.EndsAt.Equal(o.ResourceDeadline)) {
				return ErrInvalid
			}
		} else if o.Kind == "shop" {
			if !validText(o.ShopOrder, 80) || o.ShopInstallment < 1 || o.ShopInstallment > 60 {
				return ErrInvalid
			}
		} else if o.Kind == "membership" {
			if !virtualcurrency.ValidCurrencyKey(o.Plan) || o.Days < 1 || o.Days > 3650 {
				return ErrInvalid
			}
		} else if !virtualcurrency.ValidCurrencyKey(o.TargetCurrency) || o.Credit < 1 {
			return ErrInvalid
		}
		switch o.Status {
		case "pending", "paid", "completed", "cancelled", "refunded", "review":
		default:
			return ErrInvalid
		}
		if (o.Status == "paid" || o.Status == "completed") && (o.PaidAt.IsZero() || o.Kind != "exchange" && o.TransactionID == "" || o.Kind == "exchange" && o.SourceEntry < 1) {
			return ErrInvalid
		}
		if o.Status == "completed" && (o.CompletedAt.IsZero() || o.Kind == "shop" && !o.ShopSettled || o.Kind == "membership" && o.MemberEntry < 1 || o.Kind != "membership" && o.Kind != "shop" && o.Kind != "resource" && o.CreditEntry < 1) {
			return ErrInvalid
		}
		k := virtualcurrency.FingerprintForStore(stringID(o.UserID), o.RequestID)
		if requests[k] {
			return ErrInvalid
		}
		requests[k] = true
		if o.TransactionID != "" {
			k = o.PaymentScope + ":" + o.TransactionID
			if transactions[k] {
				return ErrInvalid
			}
			transactions[k] = true
		}
		if o.Kind != "exchange" && o.SealedConfig == "" {
			return ErrInvalid
		}
		if o.Checkout.URL != "" && !SafeCheckoutURL(o.Checkout.URL) {
			return ErrInvalid
		}
		if len(o.Checkout.URL) > 16384 || len(o.Checkout.QRCode) > 2048 || len(o.Checkout.Fields) > 32 || o.Checkout.Method != "" && o.Checkout.Method != "GET" && o.Checkout.Method != "POST" {
			return ErrInvalid
		}
		for key, value := range o.Checkout.Fields {
			if len(key) > 128 || len(value) > 16384 {
				return ErrInvalid
			}
		}
	}
	return nil
}
func SafeCheckoutURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}
