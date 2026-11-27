package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type stripe struct{ *client }

func (p *stripe) headers(order string) http.Header {
	h := http.Header{"Authorization": {"Bearer " + p.config.APIKey}, "Stripe-Version": {"2024-06-20"}}
	if order != "" {
		h.Set("Idempotency-Key", order)
	}
	return h
}
func (p *stripe) Create(ctx context.Context, r Request) (Checkout, error) {
	if e := p.valid(r); e != nil {
		return Checkout{}, e
	}
	cancel := r.CancelURL
	if cancel == "" {
		cancel = r.ReturnURL
	}
	v := url.Values{"mode": {"payment"}, "client_reference_id": {r.OrderID}, "success_url": {r.ReturnURL}, "cancel_url": {cancel}, "line_items[0][price_data][currency]": {strings.ToLower(r.Currency)}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(r.Amount, 10)}, "line_items[0][price_data][product_data][name]": {r.Description}, "line_items[0][quantity]": {"1"}, "metadata[order_id]": {r.OrderID}, "payment_intent_data[metadata][order_id]": {r.OrderID}}
	if !r.ExpiresAt.IsZero() {
		if r.ExpiresAt.Before(time.Now().Add(30*time.Minute)) || r.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
			return Checkout{}, ErrExpiry
		}
		v.Set("expires_at", strconv.FormatInt(r.ExpiresAt.Unix(), 10))
	}
	b, _, e := p.do(ctx, http.MethodPost, p.endpoint+"/checkout/sessions", "application/x-www-form-urlencoded", []byte(v.Encode()), p.headers(r.OrderID))
	if e != nil {
		return Checkout{}, e
	}
	var d struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if decode(b, &d) != nil || d.ID == "" || !checkoutURL(d.URL) {
		return Checkout{}, ErrResponse
	}
	return Checkout{ProviderOrderID: d.ID, URL: d.URL}, nil
}

type stripeSession struct {
	ID            string          `json:"id"`
	Reference     string          `json:"client_reference_id"`
	Amount        int64           `json:"amount_total"`
	Currency      string          `json:"currency"`
	PaymentStatus string          `json:"payment_status"`
	Status        string          `json:"status"`
	Intent        json.RawMessage `json:"payment_intent"`
	Live          bool            `json:"livemode"`
}

func (p *stripe) notification(data []byte) (Notification, error) {
	var d stripeSession
	if decode(data, &d) != nil || d.ID == "" || d.Reference == "" || d.Amount <= 0 || d.Live == p.config.Sandbox {
		return Notification{}, ErrResponse
	}
	var intent string
	_ = json.Unmarshal(d.Intent, &intent)
	if intent == "" {
		var obj struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(d.Intent, &obj)
		intent = obj.ID
	}
	status := Pending
	if d.PaymentStatus == "paid" {
		status = Paid
		if intent == "" {
			return Notification{}, ErrResponse
		}
	} else if d.Status == "expired" {
		status = Failed
	}
	return Notification{OrderID: d.Reference, ProviderOrderID: d.ID, TransactionID: intent, Amount: d.Amount, Currency: strings.ToUpper(d.Currency), Status: status, Closed: status == Failed && d.Status == "expired", EventID: d.ID + ":" + d.PaymentStatus}, nil
}
func (p *stripe) Query(ctx context.Context, id, orderID string) (Notification, error) {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Notification{}, ErrRequest
	}
	b, _, e := p.do(ctx, http.MethodGet, p.endpoint+"/checkout/sessions/"+url.PathEscape(id), "", nil, p.headers(""))
	if e != nil {
		return Notification{}, e
	}
	n, e := p.notification(b)
	if e == nil && (n.OrderID != orderID || n.ProviderOrderID != id) {
		return Notification{}, ErrResponse
	}
	return n, e
}
func (p *stripe) Verify(_ context.Context, h http.Header, body []byte) (Notification, error) {
	if len(body) > maxBody {
		return Notification{}, ErrRequest
	}
	var timestamp string
	var signatures []string
	for _, item := range strings.Split(h.Get("Stripe-Signature"), ",") {
		a, b, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok {
			if a == "t" {
				if timestamp != "" {
					return Notification{}, ErrSignature
				}
				timestamp = b
			}
			if a == "v1" {
				signatures = append(signatures, b)
			}
		}
	}
	t, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return Notification{}, ErrSignature
	}
	mac := hmac.New(sha256.New, []byte(p.config.Secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	verified := false
	for _, s := range signatures {
		raw, e := hex.DecodeString(s)
		verified = verified || e == nil && hmac.Equal(raw, mac.Sum(nil))
	}
	if !verified {
		return Notification{}, ErrSignature
	}
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Live bool   `json:"livemode"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if decode(body, &event) != nil || event.ID == "" || event.Live == p.config.Sandbox {
		return Notification{}, ErrResponse
	}
	if event.Type != "checkout.session.completed" && event.Type != "checkout.session.async_payment_succeeded" && event.Type != "checkout.session.async_payment_failed" && event.Type != "checkout.session.expired" {
		return Notification{}, ErrUnsupported
	}
	n, e := p.notification(event.Data.Object)
	if e != nil {
		return n, e
	}
	if event.Type == "checkout.session.async_payment_failed" {
		n.Status = Failed
		n.Closed = false
	}
	n.EventID = event.ID
	n.Acknowledgement = "ok"
	n.AcknowledgementContentType = "text/plain; charset=utf-8"
	return n, nil
}
