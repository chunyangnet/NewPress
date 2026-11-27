package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type wechat struct{ *client }

func (p *wechat) headers(method, path string, body []byte) (http.Header, error) {
	n := nonce()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	s, e := rsaSign(p.config.PrivateKey, method+"\n"+path+"\n"+timestamp+"\n"+n+"\n"+string(body)+"\n")
	if e != nil {
		return nil, e
	}
	return http.Header{"Authorization": {fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, p.config.MerchantID, n, timestamp, p.config.CertificateSerial, s)}, "Wechatpay-Serial": {p.config.PlatformSerial}}, nil
}
func (p *wechat) verifySignature(h http.Header, b []byte) error {
	timestamp := h.Get("Wechatpay-Timestamp")
	t, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute || h.Get("Wechatpay-Serial") != p.config.PlatformSerial || h.Get("Wechatpay-Nonce") == "" {
		return ErrSignature
	}
	return rsaVerify(p.config.PublicKey, []byte(timestamp+"\n"+h.Get("Wechatpay-Nonce")+"\n"+string(b)+"\n"), h.Get("Wechatpay-Signature"))
}
func (p *wechat) call(ctx context.Context, method, path string, data any) ([]byte, error) {
	var body []byte
	if data != nil {
		body = jsonBytes(data)
	}
	headers, e := p.headers(method, path, body)
	if e != nil {
		return nil, e
	}
	b, h, e := p.do(ctx, method, p.endpoint+path, "application/json", body, headers)
	if e != nil {
		return nil, e
	}
	if e = p.verifySignature(h, b); e != nil {
		return nil, e
	}
	return b, nil
}
func (p *wechat) Create(ctx context.Context, r Request) (Checkout, error) {
	if e := p.valid(r); e != nil {
		return Checkout{}, e
	}
	if len(r.OrderID) > 32 || len([]byte(r.Description)) > 127 {
		return Checkout{}, ErrRequest
	}
	expires := r.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(30 * time.Minute)
	}
	if !expires.After(time.Now()) {
		return Checkout{}, ErrExpiry
	}
	b, e := p.call(ctx, http.MethodPost, "/v3/pay/transactions/native", map[string]any{"appid": p.config.AppID, "mchid": p.config.MerchantID, "description": r.Description, "out_trade_no": r.OrderID, "notify_url": r.NotifyURL, "time_expire": expires.Format(time.RFC3339), "amount": map[string]any{"total": r.Amount, "currency": "CNY"}})
	if e != nil {
		return Checkout{}, e
	}
	var d struct {
		CodeURL string `json:"code_url"`
	}
	if decode(b, &d) != nil || !strings.HasPrefix(d.CodeURL, "weixin://wxpay/") {
		return Checkout{}, ErrResponse
	}
	return Checkout{ProviderOrderID: r.OrderID, QRCode: d.CodeURL}, nil
}
func (p *wechat) notification(b []byte) (Notification, error) {
	var d struct {
		App      string `json:"appid"`
		Merchant string `json:"mchid"`
		Order    string `json:"out_trade_no"`
		Trade    string `json:"transaction_id"`
		State    string `json:"trade_state"`
		Amount   struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if decode(b, &d) != nil || d.App != p.config.AppID || d.Merchant != p.config.MerchantID || d.Order == "" || d.Amount.Total <= 0 || d.Amount.Currency != "CNY" {
		return Notification{}, ErrResponse
	}
	status := Pending
	switch d.State {
	case "SUCCESS":
		status = Paid
		if d.Trade == "" {
			return Notification{}, ErrResponse
		}
	case "CLOSED", "REVOKED", "PAYERROR":
		status = Failed
	case "REFUND":
		status = Refunded
	}
	return Notification{OrderID: d.Order, ProviderOrderID: d.Order, TransactionID: d.Trade, Amount: d.Amount.Total, Currency: d.Amount.Currency, Status: status, Closed: d.State == "CLOSED" || d.State == "REVOKED", EventID: d.Trade + ":" + d.State, Acknowledgement: `{"code":"SUCCESS","message":"成功"}`, AcknowledgementContentType: "application/json; charset=utf-8"}, nil
}
func (p *wechat) Verify(_ context.Context, h http.Header, b []byte) (Notification, error) {
	if len(b) > maxBody {
		return Notification{}, ErrRequest
	}
	if e := p.verifySignature(h, b); e != nil {
		return Notification{}, e
	}
	var d struct {
		ID       string `json:"id"`
		Event    string `json:"event_type"`
		Resource struct {
			Algorithm  string `json:"algorithm"`
			Ciphertext string `json:"ciphertext"`
			Nonce      string `json:"nonce"`
			AAD        string `json:"associated_data"`
		} `json:"resource"`
	}
	if decode(b, &d) != nil || d.ID == "" || d.Event != "TRANSACTION.SUCCESS" || d.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return Notification{}, ErrResponse
	}
	block, e := aes.NewCipher([]byte(p.config.Secret))
	if e != nil {
		return Notification{}, ErrConfiguration
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return Notification{}, ErrConfiguration
	}
	data, e := base64.StdEncoding.DecodeString(d.Resource.Ciphertext)
	if e != nil || len(d.Resource.Nonce) != gcm.NonceSize() {
		return Notification{}, ErrSignature
	}
	plain, e := gcm.Open(nil, []byte(d.Resource.Nonce), data, []byte(d.Resource.AAD))
	if e != nil {
		return Notification{}, ErrSignature
	}
	n, e := p.notification(plain)
	n.EventID = d.ID
	return n, e
}
func (p *wechat) Query(ctx context.Context, _ string, orderID string) (Notification, error) {
	b, e := p.call(ctx, http.MethodGet, "/v3/pay/transactions/out-trade-no/"+url.PathEscape(orderID)+"?mchid="+url.QueryEscape(p.config.MerchantID), nil)
	if e != nil {
		return Notification{}, e
	}
	n, e := p.notification(b)
	if e == nil && n.OrderID != orderID {
		return Notification{}, ErrResponse
	}
	return n, e
}
