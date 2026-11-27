package payment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type paypal struct{ *client }

func (p *paypal) token(ctx context.Context) (string, error) {
	h := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(p.config.AppID+":"+p.config.Secret))}}
	b, _, e := p.do(ctx, http.MethodPost, p.endpoint+"/v1/oauth2/token", "application/x-www-form-urlencoded", []byte("grant_type=client_credentials"), h)
	if e != nil {
		return "", e
	}
	var d struct {
		Token string `json:"access_token"`
		Type  string `json:"token_type"`
	}
	if decode(b, &d) != nil || d.Token == "" || !strings.EqualFold(d.Type, "Bearer") {
		return "", ErrResponse
	}
	return d.Token, nil
}
func (p *paypal) call(ctx context.Context, method, path string, data any, idempotency string) ([]byte, error) {
	var body []byte
	if data != nil {
		body = jsonBytes(data)
	}
	return p.callRaw(ctx, method, path, body, idempotency)
}
func (p *paypal) callRaw(ctx context.Context, method, path string, body []byte, idempotency string) ([]byte, error) {
	return p.callChecked(ctx, method, path, body, idempotency, nil)
}

func (p *paypal) callChecked(ctx context.Context, method, path string, body []byte, idempotency string, check func() error) ([]byte, error) {
	token, e := p.token(ctx)
	if e != nil {
		return nil, e
	}
	if check != nil {
		if e = check(); e != nil {
			return nil, e
		}
	}
	h := http.Header{"Authorization": {"Bearer " + token}, "Prefer": {"return=representation"}}
	if idempotency != "" {
		h.Set("PayPal-Request-Id", idempotency)
	}
	b, _, e := p.do(ctx, method, p.endpoint+path, "application/json", body, h)
	return b, e
}
func (p *paypal) Create(ctx context.Context, r Request) (Checkout, error) {
	if e := p.valid(r); e != nil {
		return Checkout{}, e
	}
	amount, _ := FormatAmount(r.Amount, r.Currency)
	cancel := r.CancelURL
	if cancel == "" {
		cancel = r.ReturnURL
	}
	b, e := p.call(ctx, http.MethodPost, "/v2/checkout/orders", map[string]any{"intent": "CAPTURE", "purchase_units": []any{map[string]any{"reference_id": r.OrderID, "custom_id": r.OrderID, "invoice_id": r.OrderID, "description": r.Description, "payee": map[string]string{"merchant_id": p.config.MerchantID}, "amount": map[string]string{"currency_code": r.Currency, "value": amount}}}, "payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]string{"return_url": r.ReturnURL, "cancel_url": cancel, "user_action": "PAY_NOW", "shipping_preference": "NO_SHIPPING"}}}}, r.OrderID)
	if e != nil {
		return Checkout{}, e
	}
	var d struct {
		ID    string `json:"id"`
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if decode(b, &d) != nil || d.ID == "" {
		return Checkout{}, ErrResponse
	}
	for _, link := range d.Links {
		if (link.Rel == "approve" || link.Rel == "payer-action") && checkoutURL(link.Href) {
			return Checkout{ProviderOrderID: d.ID, URL: link.Href}, nil
		}
	}
	return Checkout{}, ErrResponse
}

type paypalAmount struct {
	Currency string `json:"currency_code"`
	Value    string `json:"value"`
}
type paypalPayee struct {
	Merchant string `json:"merchant_id"`
}
type paypalCapture struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	CustomID      string       `json:"custom_id"`
	Amount        paypalAmount `json:"amount"`
	Payee         paypalPayee  `json:"payee"`
	Final         bool         `json:"final_capture"`
	Supplementary struct {
		IDs struct {
			Order string `json:"order_id"`
		} `json:"related_ids"`
	} `json:"supplementary_data"`
}
type paypalOrder struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Intent string `json:"intent"`
	Units  []struct {
		CustomID string       `json:"custom_id"`
		Amount   paypalAmount `json:"amount"`
		Payee    paypalPayee  `json:"payee"`
		Payments struct {
			Captures []paypalCapture `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
}

func (p *paypal) Query(ctx context.Context, id, orderID string) (Notification, error) {
	return p.query(ctx, id, orderID, true)
}

func (p *paypal) Inspect(ctx context.Context, id, orderID string) (Notification, error) {
	return p.query(ctx, id, orderID, false)
}

func (p *paypal) query(ctx context.Context, id, orderID string, capture bool) (Notification, error) {
	if err := ctx.Err(); err != nil {
		return Notification{}, err
	}
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Notification{}, ErrRequest
	}
	path := "/v2/checkout/orders/" + url.PathEscape(id)
	b, e := p.call(ctx, http.MethodGet, path, nil, "")
	if e != nil {
		return Notification{}, e
	}
	var d paypalOrder
	if decode(b, &d) != nil || d.ID != id || d.Intent != "CAPTURE" || len(d.Units) != 1 || d.Units[0].CustomID != orderID || d.Units[0].Payee.Merchant != p.config.MerchantID {
		return Notification{}, ErrResponse
	}
	if d.Status == "APPROVED" && capture {
		amount, err := ParseAmount(d.Units[0].Amount.Value, d.Units[0].Amount.Currency)
		if err != nil || amount <= 0 {
			return Notification{}, ErrResponse
		}
		original := Notification{OrderID: orderID, ProviderOrderID: id, Amount: amount, Currency: d.Units[0].Amount.Currency, Status: Pending}
		_, e = p.callChecked(ctx, http.MethodPost, path+"/capture", jsonBytes(map[string]any{}), "cap_"+id, func() error {
			return checkCapture(ctx, original)
		})
		if e != nil {
			return Notification{}, e
		}
		// Capture responses can omit purchase-unit metadata. Read the canonical
		// order instead of trusting an incomplete response for fulfilment.
		b, e = p.call(ctx, http.MethodGet, path, nil, "")
		if e != nil {
			return Notification{}, e
		}
		if decode(b, &d) != nil || d.ID != id || len(d.Units) != 1 || d.Units[0].CustomID != orderID || d.Units[0].Payee.Merchant != p.config.MerchantID {
			return Notification{}, ErrResponse
		}
	}
	unit := d.Units[0]
	amount, e := ParseAmount(unit.Amount.Value, unit.Amount.Currency)
	if e != nil || amount <= 0 {
		return Notification{}, ErrResponse
	}
	n := Notification{OrderID: orderID, ProviderOrderID: id, Amount: amount, Currency: unit.Amount.Currency, Status: Pending, EventID: id + ":" + d.Status}
	if d.Status == "COMPLETED" {
		if len(unit.Payments.Captures) != 1 {
			return Notification{}, ErrResponse
		}
		cap := unit.Payments.Captures[0]
		captureAmount, e := ParseAmount(cap.Amount.Value, cap.Amount.Currency)
		if e != nil || cap.Status != "COMPLETED" || cap.ID == "" || cap.Amount.Currency != unit.Amount.Currency || captureAmount != amount || !cap.Final {
			return Notification{}, ErrResponse
		}
		n.Status = Paid
		n.TransactionID = cap.ID
		n.EventID = cap.ID + ":COMPLETED"
	}
	if d.Status == "VOIDED" {
		n.Status = Failed
		n.Closed = true
	}
	return n, nil
}
func (p *paypal) Verify(ctx context.Context, h http.Header, b []byte) (Notification, error) {
	return p.verify(ctx, h, b, true)
}

func (p *paypal) InspectNotification(ctx context.Context, h http.Header, b []byte) (Notification, error) {
	return p.verify(ctx, h, b, false)
}

func (p *paypal) verify(ctx context.Context, h http.Header, b []byte, capture bool) (Notification, error) {
	var event struct {
		ID       string          `json:"id"`
		Type     string          `json:"event_type"`
		Resource json.RawMessage `json:"resource"`
	}
	if decode(b, &event) != nil || event.ID == "" {
		return Notification{}, ErrResponse
	}
	keys := map[string]string{"auth_algo": "Paypal-Auth-Algo", "cert_url": "Paypal-Cert-Url", "transmission_id": "Paypal-Transmission-Id", "transmission_sig": "Paypal-Transmission-Sig", "transmission_time": "Paypal-Transmission-Time"}
	payload := map[string]any{"webhook_id": p.config.WebhookID}
	for key, header := range keys {
		value := h.Get(header)
		if value == "" {
			return Notification{}, ErrSignature
		}
		payload[key] = value
	}
	// json.Marshal compacts even json.RawMessage and escapes HTML characters.
	// PayPal requires the event exactly as received; append its original bytes
	// into the verification envelope without reserializing the event.
	verificationBody := jsonBytes(payload)
	verificationBody = append(verificationBody[:len(verificationBody)-1], []byte(`,"webhook_event":`)...)
	verificationBody = append(verificationBody, b...)
	verificationBody = append(verificationBody, '}')
	verified, e := p.callRaw(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", verificationBody, "")
	if e != nil {
		return Notification{}, e
	}
	var response struct {
		Status string `json:"verification_status"`
	}
	if decode(verified, &response) != nil || response.Status != "SUCCESS" {
		return Notification{}, ErrSignature
	}
	if event.Type != "PAYMENT.CAPTURE.COMPLETED" && event.Type != "CHECKOUT.ORDER.APPROVED" {
		return Notification{}, ErrUnsupported
	}
	var n Notification
	if event.Type == "CHECKOUT.ORDER.APPROVED" {
		var order paypalOrder
		if decode(event.Resource, &order) != nil || len(order.Units) != 1 {
			return n, ErrResponse
		}
		n, e = p.query(ctx, order.ID, order.Units[0].CustomID, capture)
	} else {
		var cap paypalCapture
		if decode(event.Resource, &cap) != nil || cap.ID == "" || cap.Status != "COMPLETED" || cap.Payee.Merchant != p.config.MerchantID || cap.CustomID == "" || cap.Supplementary.IDs.Order == "" {
			return n, ErrResponse
		}
		n, e = p.Inspect(ctx, cap.Supplementary.IDs.Order, cap.CustomID)
		if e == nil && (n.TransactionID != cap.ID || n.Status != Paid) {
			return Notification{}, ErrResponse
		}
	}
	if e != nil {
		return n, e
	}
	n.EventID = event.ID
	n.Acknowledgement = "ok"
	n.AcknowledgementContentType = "text/plain; charset=utf-8"
	return n, nil
}
