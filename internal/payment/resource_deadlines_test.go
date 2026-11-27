package payment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestAlipayResourceDeadlineIsAbsoluteAndSigned(t *testing.T) {
	priv, pub := keys(t)
	provider, err := New(Config{Provider: "alipay", AppID: "app", MerchantID: "seller", PrivateKey: priv, PublicKey: pub}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := request("CNY")
	in.ExpiresAt = time.Now().Add(5 * time.Minute).UTC()
	checkout, err := provider.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	var biz map[string]string
	if err = json.Unmarshal([]byte(checkout.Fields["biz_content"]), &biz); err != nil {
		t.Fatal(err)
	}
	if biz["time_expire"] != in.ExpiresAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05") || biz["timeout_express"] != "" {
		t.Fatal("resource deadline reverted to a fresh relative timer", biz)
	}
	values := make(map[string][]string)
	for key, value := range checkout.Fields {
		values[key] = []string{value}
	}
	if err = rsaVerify(pub, []byte(canonicalForm(values, "sign")), checkout.Fields["sign"]); err != nil {
		t.Fatal("deadline not covered by signature", err)
	}
}

func TestStripeResourceDeadlineAndUnsupportedShortWindow(t *testing.T) {
	in := request("USD")
	in.ExpiresAt = time.Now().Add(31 * time.Minute).UTC()
	creates := 0
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		creates++
		_ = r.ParseForm()
		if r.PostForm.Get("expires_at") != strconv.FormatInt(in.ExpiresAt.Unix(), 10) {
			t.Error("missing absolute expiration")
		}
		jsonReply(w, map[string]string{"id": "cs_resource", "url": "https://checkout.stripe.com/c/pay/resource"})
	})
	p, err := New(Config{Provider: "stripe", APIKey: "key", Secret: "secret"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Create(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for _, expiry := range []time.Time{time.Now().Add(time.Minute), time.Now().Add(25 * time.Hour)} {
		in.ExpiresAt = expiry
		if _, err = p.Create(t.Context(), in); !errors.Is(err, ErrExpiry) {
			t.Fatal("unsupported expiration sent to channel", err)
		}
	}
	if creates != 1 {
		t.Fatal("invalid resource windows reached the payment provider", creates)
	}
}

func TestWechatResourceDeadlineOverridesDefaultThirtyMinutes(t *testing.T) {
	priv, pub := keys(t)
	in := request("CNY")
	in.ExpiresAt = time.Now().Add(3 * time.Minute).UTC()
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Expires string `json:"time_expire"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Expires != in.ExpiresAt.Format(time.RFC3339) {
			t.Error("resource deadline not passed to Native order", body.Expires)
		}
		response := jsonBytes(map[string]string{"code_url": "weixin://wxpay/bizpayurl?pr=resource"})
		for key, values := range wechatSign(priv, response) {
			w.Header()[key] = values
		}
		_, _ = w.Write(response)
	})
	p, err := New(Config{Provider: "wechat", AppID: "app", MerchantID: "merchant", PrivateKey: priv, PublicKey: pub, CertificateSerial: "merchant-cert", PlatformSerial: "platform", Secret: "12345678901234567890123456789012"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Create(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}

func TestProviderClosureIsDistinctFromPaymentAttemptFailure(t *testing.T) {
	for _, state := range []string{"TRADE_CLOSED", "WAIT_BUYER_PAY", "TRADE_SUCCESS"} {
		n, err := alipayNotification("order", "trade", "1.00", state)
		if err != nil || n.Closed != (state == "TRADE_CLOSED") {
			t.Fatal(state, n, err)
		}
	}
	wx := &wechat{client: &client{config: Config{AppID: "app", MerchantID: "merchant"}}}
	for _, state := range []string{"CLOSED", "REVOKED", "PAYERROR", "NOTPAY", "SUCCESS"} {
		n, err := wx.notification(jsonBytes(map[string]any{"appid": "app", "mchid": "merchant", "out_trade_no": "order", "transaction_id": "trade", "trade_state": state, "amount": map[string]any{"total": 100, "currency": "CNY"}}))
		if err != nil || n.Closed != (state == "CLOSED" || state == "REVOKED") {
			t.Fatal(state, n, err)
		}
	}
	stripeProvider := &stripe{client: &client{config: Config{}}}
	n, err := stripeProvider.notification(jsonBytes(map[string]any{"id": "cs_resource", "client_reference_id": "order", "amount_total": 100, "currency": "usd", "livemode": true, "status": "expired", "payment_status": "unpaid"}))
	if err != nil || !n.Closed || n.Status != Failed {
		t.Fatal("expired session did not prove closure", n, err)
	}
}

func TestPayPalReadOnlyInspectionCannotCaptureAnApprovedOrder(t *testing.T) {
	state := "APPROVED"
	captures := 0
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			jsonReply(w, map[string]string{"access_token": "token", "token_type": "Bearer"})
		case "/v2/checkout/orders/ORDER123":
			jsonReply(w, paypalOrderData(state))
		default:
			captures++
			t.Error("read-only reconciliation called a capture", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	p, err := New(Config{Provider: "paypal", AppID: "app", Secret: "secret", WebhookID: "hook", MerchantID: "seller", Sandbox: true}, client)
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := p.(Inspector)
	if !ok {
		t.Fatal("missing read-only inspection")
	}
	n, err := reader.Inspect(context.Background(), "ORDER123", request("USD").OrderID)
	if err != nil || n.Status != Pending || captures != 0 {
		t.Fatal("approved order was captured", n, err, captures)
	}
	state = "VOIDED"
	n, err = reader.Inspect(t.Context(), "ORDER123", request("USD").OrderID)
	if err != nil || !n.Closed || n.Status != Failed || captures != 0 {
		t.Fatal("voided order not safely reconciled", n, err, captures)
	}
}

func TestPayPalResourceApprovalVerificationAndCaptureCheck(t *testing.T) {
	state := "APPROVED"
	captures := 0
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			jsonReply(w, map[string]string{"access_token": "token", "token_type": "Bearer"})
		case "/v1/notifications/verify-webhook-signature":
			jsonReply(w, map[string]string{"verification_status": "SUCCESS"})
		case "/v2/checkout/orders/ORDER123":
			jsonReply(w, paypalOrderData(state))
		case "/v2/checkout/orders/ORDER123/capture":
			captures++
			state = "COMPLETED"
			jsonReply(w, paypalOrderData(state))
		default:
			t.Error("unexpected payment request", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	p, err := New(Config{Provider: "paypal", AppID: "app", Secret: "secret", WebhookID: "hook", MerchantID: "seller", Sandbox: true}, client)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"Paypal-Auth-Algo": {"SHA256withRSA"}, "Paypal-Cert-Url": {"https://api-m.sandbox.paypal.com/cert"}, "Paypal-Transmission-Id": {"transmission"}, "Paypal-Transmission-Sig": {"signature"}, "Paypal-Transmission-Time": {"2026-11-25T00:00:00Z"}}
	body := jsonBytes(map[string]any{"id": "EV_APPROVED", "event_type": "CHECKOUT.ORDER.APPROVED", "resource": paypalOrderData(state)})
	reader, ok := p.(NotificationInspector)
	if !ok {
		t.Fatal("missing callback inspection")
	}
	n, err := reader.InspectNotification(t.Context(), h, body)
	if err != nil || n.Status != Pending || n.Acknowledgement == "" || captures != 0 {
		t.Fatal("resource approval captured before delivery validation", n, err, captures)
	}
	denied := errors.New("resource no longer deliverable")
	checks := 0
	ctx := WithCaptureCheck(t.Context(), func(_ context.Context, n Notification) error {
		checks++
		if n.OrderID != request("USD").OrderID || n.ProviderOrderID != "ORDER123" || n.Amount != 1234 || n.Currency != "USD" {
			t.Error("capture check did not receive original terms", n)
		}
		return denied
	})
	if _, err = p.Query(ctx, "ORDER123", request("USD").OrderID); !errors.Is(err, denied) || captures != 0 || checks != 1 {
		t.Fatal("capture ignored delivery check", err, captures, checks)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = p.Query(ctx, "ORDER123", request("USD").OrderID); !errors.Is(err, context.Canceled) || captures != 0 {
		t.Fatal("cancelled query initiated capture", err, captures)
	}
	if n, err = p.Query(t.Context(), "ORDER123", request("USD").OrderID); err != nil || n.Status != Paid || captures != 1 {
		t.Fatal("valid resource could not be captured", n, err, captures)
	}
}
