package payment

import (
	"context"
	"crypto/md5" // Required by the provider's documented compatibility protocol.
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type epay struct{ *client }

func canonicalForm(v url.Values, exclude ...string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		skip := false
		for _, s := range exclude {
			skip = skip || s == k
		}
		if !skip && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, k+"="+v.Get(k))
	}
	return strings.Join(pairs, "&")
}
func epaySign(v url.Values, secret string) string {
	sum := md5.Sum([]byte(canonicalForm(v, "sign", "sign_type") + secret))
	return hex.EncodeToString(sum[:])
}
func (p *epay) currency() string {
	if p.config.Provider == "linuxdo" {
		return "LDC"
	}
	return "CNY"
}
func (p *epay) Create(_ context.Context, r Request) (Checkout, error) {
	if err := p.valid(r); err != nil {
		return Checkout{}, err
	}
	amount, _ := FormatAmount(r.Amount, r.Currency)
	kind := p.config.PaymentType
	if kind == "" {
		kind = "alipay"
	}
	if p.config.Provider == "linuxdo" {
		kind = "epay"
	}
	v := url.Values{"pid": {p.config.MerchantID}, "type": {kind}, "out_trade_no": {r.OrderID}, "notify_url": {r.NotifyURL}, "return_url": {r.ReturnURL}, "name": {r.Description}, "money": {amount}, "sign_type": {"MD5"}}
	v.Set("sign", epaySign(v, p.config.Secret))
	endpoint := p.endpoint + "/submit.php"
	if p.config.Provider == "linuxdo" {
		endpoint = p.endpoint + "/pay/submit.php"
		// The official LDC router only accepts POST for this endpoint.
		fields := make(map[string]string, len(v))
		for key := range v {
			fields[key] = v.Get(key)
		}
		return Checkout{ProviderOrderID: r.OrderID, URL: endpoint, Method: "POST", Fields: fields}, nil
	}
	return Checkout{ProviderOrderID: r.OrderID, URL: endpoint + "?" + v.Encode(), Method: "GET"}, nil
}
func (p *epay) Verify(_ context.Context, _ http.Header, body []byte) (Notification, error) {
	v, e := parseForm(body)
	if e != nil {
		return Notification{}, e
	}
	if v.Get("sign_type") != "MD5" || v.Get("pid") != p.config.MerchantID || subtle.ConstantTimeCompare([]byte(strings.ToLower(v.Get("sign"))), []byte(epaySign(v, p.config.Secret))) != 1 {
		return Notification{}, ErrSignature
	}
	amount, e := ParseAmount(v.Get("money"), p.currency())
	if e != nil || amount <= 0 || v.Get("out_trade_no") == "" || v.Get("trade_no") == "" {
		return Notification{}, ErrResponse
	}
	status := Pending
	if v.Get("trade_status") == "TRADE_SUCCESS" {
		status = Paid
	}
	return Notification{OrderID: v.Get("out_trade_no"), ProviderOrderID: v.Get("out_trade_no"), TransactionID: v.Get("trade_no"), Amount: amount, Currency: p.currency(), Status: status, EventID: v.Get("trade_no") + ":" + v.Get("trade_status"), Acknowledgement: "success", AcknowledgementContentType: "text/plain; charset=utf-8"}, nil
}
func (p *epay) Query(ctx context.Context, _ string, orderID string) (Notification, error) {
	// Legacy EPay queries require the merchant key in HTTPS query parameters.
	// The client never follows redirects or returns the URL in errors/logs.
	v := url.Values{"act": {"order"}, "pid": {p.config.MerchantID}, "key": {p.config.Secret}, "out_trade_no": {orderID}}
	b, _, e := p.do(ctx, http.MethodGet, p.endpoint+"/api.php?"+v.Encode(), "", nil, nil)
	if e != nil {
		return Notification{}, e
	}
	var d struct {
		Code   int             `json:"code"`
		PID    json.RawMessage `json:"pid"`
		Order  string          `json:"out_trade_no"`
		Trade  string          `json:"trade_no"`
		Money  string          `json:"money"`
		Status json.RawMessage `json:"status"`
	}
	if decode(b, &d) != nil || d.Code != 1 || d.Order != orderID || strings.Trim(string(d.PID), "\"") != p.config.MerchantID || d.Trade == "" {
		return Notification{}, ErrResponse
	}
	amount, e := ParseAmount(d.Money, p.currency())
	if e != nil || amount <= 0 {
		return Notification{}, ErrResponse
	}
	status := Pending
	if strings.Trim(string(d.Status), "\"") == "1" {
		status = Paid
	}
	return Notification{OrderID: d.Order, ProviderOrderID: d.Order, TransactionID: d.Trade, Amount: amount, Currency: p.currency(), Status: status, EventID: d.Trade + ":" + status}, nil
}
