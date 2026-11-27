// Package payment contains payment-provider protocols. It never credits balances:
// callers must compare the verified notification with their stored order and
// atomically apply its entitlement exactly once.
package payment

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrConfiguration = errors.New("payment provider is not configured")
	ErrUnsupported   = errors.New("payment operation is not supported")
	ErrSignature     = errors.New("payment signature verification failed")
	ErrResponse      = errors.New("payment provider returned an invalid response")
	ErrRequest       = errors.New("invalid payment request")
	ErrExpiry        = errors.New("payment channel cannot honor resource deadline")
)

const (
	Pending  = "pending"
	Paid     = "paid"
	Failed   = "failed"
	Refunded = "refunded"
)

// Config is stored privately by the application. Never include it in public API
// responses or logs. PublicKey is the provider's verification key, not our key.
type Config struct {
	Provider          string `json:"provider"`
	Endpoint          string `json:"endpoint"`
	MerchantID        string `json:"merchant_id"`
	AppID             string `json:"app_id"`
	APIKey            string `json:"api_key"`
	Secret            string `json:"secret"`
	PrivateKey        string `json:"private_key"`
	PublicKey         string `json:"public_key"`
	CertificateSerial string `json:"certificate_serial"`
	PlatformSerial    string `json:"platform_serial"`
	WebhookID         string `json:"webhook_id"`
	PaymentType       string `json:"payment_type"`
	Sandbox           bool   `json:"sandbox"`
}

// Amount is an integer in the currency's smallest unit. Currency is CNY, USD,
// EUR etc. LDC uses two fractional digits and is not a fiat exchange rate.
type Request struct {
	OrderID     string
	Amount      int64
	Currency    string
	Description string
	NotifyURL   string
	ReturnURL   string
	CancelURL   string
	ClientIP    string
	ExpiresAt   time.Time
}

type Checkout struct {
	ProviderOrderID string `json:"provider_order_id"`
	URL             string `json:"url,omitempty"`
	QRCode          string `json:"qr_code,omitempty"`
	// POST form fields contain only signed checkout data, never merchant secrets.
	Method string            `json:"method,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

type Notification struct {
	OrderID         string `json:"order_id"`
	ProviderOrderID string `json:"provider_order_id"`
	TransactionID   string `json:"transaction_id"`
	Amount          int64  `json:"amount"`
	Currency        string `json:"currency"`
	Status          string `json:"status"`
	EventID         string `json:"event_id"`
	// Closed is set only for a verified terminal provider state. A failed
	// attempt (for example PAYERROR) does not prove that the order is closed.
	Closed bool `json:"closed,omitempty"`
	// Acknowledgement is provider-specific and must only be sent after the
	// application's durable, idempotent order update has committed.
	Acknowledgement            string `json:"-"`
	AcknowledgementContentType string `json:"-"`
}

type Provider interface {
	Create(context.Context, Request) (Checkout, error)
	Query(context.Context, string, string) (Notification, error)
	// Form/GET callbacks pass their encoded values as body; JSON callbacks must
	// pass untouched raw request bytes. Cap incoming bodies before reading them.
	Verify(context.Context, http.Header, []byte) (Notification, error)
}

// Inspector separates a read-only reconciliation from providers whose normal
// Query also captures an approved payment (PayPal). Closed resource attempts
// must never authorize a new capture while checking their old result.
type Inspector interface {
	Inspect(context.Context, string, string) (Notification, error)
}

// NotificationInspector verifies a callback without capturing an approved
// payment. The application can then check delivery before authorizing capture.
type NotificationInspector interface {
	InspectNotification(context.Context, http.Header, []byte) (Notification, error)
}

type Definition struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Currencies []string `json:"currencies"`
	Fields     []string `json:"fields"`
	Supported  bool     `json:"supported"`
	Note       string   `json:"note"`
}

func Definitions() []Definition {
	return []Definition{
		{"alipay", "支付宝电脑网站支付", []string{"CNY"}, []string{"app_id", "merchant_id", "private_key", "public_key"}, true, "RSA2；支付宝公钥模式"},
		{"alipay_f2f", "支付宝当面付", []string{"CNY"}, []string{"app_id", "merchant_id", "private_key", "public_key"}, true, "RSA2 预创建二维码；需开通当面付"},
		{"wechat", "微信支付 Native", []string{"CNY"}, []string{"app_id", "merchant_id", "private_key", "public_key", "certificate_serial", "platform_serial", "secret"}, true, "API v3；平台公钥或平台证书验签；secret 为 APIv3 密钥"},
		{"epay", "易支付兼容 / 码支付网关", []string{"CNY"}, []string{"endpoint", "merchant_id", "secret", "payment_type"}, true, "仅兼容标准易支付 MD5 协议；非兼容码支付需单独适配"},
		{"linuxdo", "LINUX DO Credit", []string{"LDC"}, []string{"endpoint", "merchant_id", "secret"}, true, "官方易支付兼容协议；默认 pay.linux.do/epay，可改为官方 credit.linux.do/epay；LDC 是社区积分"},
		{"stripe", "Stripe Checkout", []string{"USD", "EUR", "GBP", "CNY", "HKD", "JPY"}, []string{"api_key", "secret"}, true, "托管收银台；secret 为 webhook signing secret"},
		{"paypal", "PayPal Checkout", []string{"USD", "EUR", "GBP", "HKD", "JPY"}, []string{"app_id", "secret", "webhook_id", "merchant_id"}, true, "Orders v2 CAPTURE；仅捕获完成到账；merchant_id 为收款商户 ID"},
		{"twocheckout", "2Checkout / Verifone", []string{"USD", "EUR", "GBP"}, []string{"merchant_id", "api_key", "secret"}, true, "托管 buy-link；api_key 为 buy-link secret word，secret 为 IPN secret key"},
	}
}
