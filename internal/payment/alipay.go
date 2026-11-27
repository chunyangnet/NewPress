package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type alipay struct{ *client }

func rsaSign(key, content string) (string, error) {
	k, e := privateKey(key)
	if e != nil {
		return "", e
	}
	digest := sha256.Sum256([]byte(content))
	signed, e := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
	if e != nil {
		return "", ErrConfiguration
	}
	return base64.StdEncoding.EncodeToString(signed), nil
}
func rsaVerify(key string, content []byte, signature string) error {
	k, e := publicKey(key)
	if e != nil {
		return ErrConfiguration
	}
	raw, e := base64.StdEncoding.DecodeString(signature)
	if e != nil {
		return ErrSignature
	}
	digest := sha256.Sum256(content)
	if rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], raw) != nil {
		return ErrSignature
	}
	return nil
}
func (p *alipay) params(method string, biz any) url.Values {
	return url.Values{"app_id": {p.config.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"}, "sign_type": {"RSA2"}, "timestamp": {time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")}, "version": {"1.0"}, "biz_content": {string(jsonBytes(biz))}}
}
func (p *alipay) sign(v url.Values) error {
	s, e := rsaSign(p.config.PrivateKey, canonicalForm(v, "sign"))
	if e == nil {
		v.Set("sign", s)
	}
	return e
}
func (p *alipay) call(ctx context.Context, v url.Values) (json.RawMessage, error) {
	if e := p.sign(v); e != nil {
		return nil, e
	}
	b, _, e := p.do(ctx, http.MethodPost, p.endpoint, "application/x-www-form-urlencoded", []byte(v.Encode()), nil)
	if e != nil {
		return nil, e
	}
	var envelope map[string]json.RawMessage
	if decode(b, &envelope) != nil {
		return nil, ErrResponse
	}
	key := strings.ReplaceAll(v.Get("method"), ".", "_") + "_response"
	payload := envelope[key]
	var sign string
	if json.Unmarshal(envelope["sign"], &sign) != nil || rsaVerify(p.config.PublicKey, payload, sign) != nil {
		return nil, ErrSignature
	}
	var status struct {
		Code string `json:"code"`
	}
	if decode(payload, &status) != nil || status.Code != "10000" {
		return nil, ErrResponse
	}
	return payload, nil
}
func (p *alipay) Create(ctx context.Context, r Request) (Checkout, error) {
	if e := p.valid(r); e != nil {
		return Checkout{}, e
	}
	amount, _ := FormatAmount(r.Amount, r.Currency)
	biz := map[string]string{"out_trade_no": r.OrderID, "total_amount": amount, "subject": r.Description, "timeout_express": "30m"}
	if !r.ExpiresAt.IsZero() {
		if !r.ExpiresAt.After(time.Now()) {
			return Checkout{}, ErrExpiry
		}
		delete(biz, "timeout_express")
		biz["time_expire"] = r.ExpiresAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
	}
	method := "alipay.trade.page.pay"
	if p.config.Provider == "alipay_f2f" {
		method = "alipay.trade.precreate"
	} else {
		biz["product_code"] = "FAST_INSTANT_TRADE_PAY"
	}
	v := p.params(method, biz)
	v.Set("notify_url", r.NotifyURL)
	v.Set("return_url", r.ReturnURL)
	if p.config.Provider == "alipay_f2f" {
		b, e := p.call(ctx, v)
		if e != nil {
			return Checkout{}, e
		}
		var d struct {
			Order string `json:"out_trade_no"`
			QR    string `json:"qr_code"`
		}
		if decode(b, &d) != nil || d.Order != r.OrderID || !checkoutURL(d.QR) {
			return Checkout{}, ErrResponse
		}
		return Checkout{ProviderOrderID: r.OrderID, QRCode: d.QR}, nil
	}
	if e := p.sign(v); e != nil {
		return Checkout{}, e
	}
	fields := map[string]string{}
	for k := range v {
		fields[k] = v.Get(k)
	}
	return Checkout{ProviderOrderID: r.OrderID, URL: p.endpoint, Method: "POST", Fields: fields}, nil
}
func alipayNotification(order, trade, amount, state string) (Notification, error) {
	v, e := ParseAmount(amount, "CNY")
	if e != nil || v <= 0 || order == "" || trade == "" {
		return Notification{}, ErrResponse
	}
	status := Pending
	switch state {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		status = Paid
	case "TRADE_CLOSED":
		status = Failed
	}
	return Notification{OrderID: order, ProviderOrderID: order, TransactionID: trade, Amount: v, Currency: "CNY", Status: status, Closed: state == "TRADE_CLOSED", EventID: trade + ":" + state, Acknowledgement: "success", AcknowledgementContentType: "text/plain; charset=utf-8"}, nil
}
func (p *alipay) Verify(_ context.Context, _ http.Header, b []byte) (Notification, error) {
	v, e := parseForm(b)
	if e != nil {
		return Notification{}, e
	}
	if v.Get("sign_type") != "RSA2" || v.Get("app_id") != p.config.AppID || v.Get("seller_id") != p.config.MerchantID {
		return Notification{}, ErrSignature
	}
	if e = rsaVerify(p.config.PublicKey, []byte(canonicalForm(v, "sign", "sign_type")), v.Get("sign")); e != nil {
		return Notification{}, e
	}
	n, e := alipayNotification(v.Get("out_trade_no"), v.Get("trade_no"), v.Get("total_amount"), v.Get("trade_status"))
	if v.Get("notify_id") != "" {
		n.EventID = v.Get("notify_id")
	}
	return n, e
}
func (p *alipay) Query(ctx context.Context, _ string, orderID string) (Notification, error) {
	b, e := p.call(ctx, p.params("alipay.trade.query", map[string]string{"out_trade_no": orderID}))
	if e != nil {
		return Notification{}, e
	}
	var d struct {
		Order  string `json:"out_trade_no"`
		Trade  string `json:"trade_no"`
		Amount string `json:"total_amount"`
		State  string `json:"trade_status"`
		Seller string `json:"seller_id"`
	}
	if decode(b, &d) != nil || d.Order != orderID || d.Seller != "" && d.Seller != p.config.MerchantID {
		return Notification{}, ErrResponse
	}
	return alipayNotification(d.Order, d.Trade, d.Amount, d.State)
}
