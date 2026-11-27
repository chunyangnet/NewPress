package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func paypalOrderData(status string) map[string]any {
	unit := map[string]any{"custom_id": request("USD").OrderID, "payee": map[string]string{"merchant_id": "seller"}, "amount": map[string]string{"currency_code": "USD", "value": "12.34"}}
	if status == "COMPLETED" {
		unit["payments"] = map[string]any{"captures": []any{map[string]any{"id": "CAP123", "status": "COMPLETED", "final_capture": true, "amount": map[string]string{"currency_code": "USD", "value": "12.34"}}}}
	}
	return map[string]any{"id": "ORDER123", "status": status, "intent": "CAPTURE", "purchase_units": []any{unit}}
}
func TestPayPalApprovalCaptureAndVerifiedWebhook(t *testing.T) {
	status := "CREATED"
	captures := 0
	verifySuccess := true
	var lastVerifiedBody []byte
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/oauth2/token" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "app" || pass != "secret" {
				t.Error("oauth credentials")
			}
			jsonReply(w, map[string]string{"access_token": "token", "token_type": "Bearer"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing access token")
		}
		switch r.URL.Path {
		case "/v2/checkout/orders":
			var d struct {
				Units []struct {
					ID string `json:"custom_id"`
				} `json:"purchase_units"`
			}
			_ = json.NewDecoder(r.Body).Decode(&d)
			if len(d.Units) != 1 || d.Units[0].ID != request("USD").OrderID || r.Header.Get("PayPal-Request-Id") != request("USD").OrderID {
				t.Error("create reference or idempotency")
			}
			jsonReply(w, map[string]any{"id": "ORDER123", "links": []any{map[string]string{"rel": "payer-action", "href": "https://www.sandbox.paypal.com/checkoutnow?token=ORDER123"}}})
		case "/v2/checkout/orders/ORDER123":
			jsonReply(w, paypalOrderData(status))
		case "/v2/checkout/orders/ORDER123/capture":
			if r.Method != "POST" || r.Header.Get("PayPal-Request-Id") != "cap_ORDER123" {
				t.Error("capture contract")
			}
			captures++
			status = "COMPLETED"
			jsonReply(w, map[string]string{"id": "ORDER123", "status": "COMPLETED"})
		case "/v1/notifications/verify-webhook-signature":
			var d map[string]any
			lastVerifiedBody, _ = io.ReadAll(r.Body)
			_ = json.Unmarshal(lastVerifiedBody, &d)
			if d["webhook_id"] != "hook" || d["transmission_id"] != "transmission" || d["webhook_event"] == nil {
				t.Error("webhook verification contract")
			}
			value := "FAILURE"
			if verifySuccess {
				value = "SUCCESS"
			}
			jsonReply(w, map[string]string{"verification_status": value})
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	})
	p, e := New(Config{Provider: "paypal", AppID: "app", Secret: "secret", WebhookID: "hook", MerchantID: "seller", Sandbox: true}, client)
	if e != nil {
		t.Fatal(e)
	}
	c, e := p.Create(context.Background(), request("USD"))
	if e != nil || c.ProviderOrderID != "ORDER123" {
		t.Fatal(c, e)
	}
	n, e := p.Query(context.Background(), c.ProviderOrderID, request("USD").OrderID)
	if e != nil || n.Status != Pending || captures != 0 {
		t.Fatal(n, e, captures)
	}
	status = "APPROVED"
	n, e = p.Query(context.Background(), c.ProviderOrderID, request("USD").OrderID)
	if e != nil || n.Status != Paid || n.TransactionID != "CAP123" || captures != 1 {
		t.Fatal(n, e, captures)
	}
	n, e = p.Query(context.Background(), c.ProviderOrderID, request("USD").OrderID)
	if e != nil || n.Status != Paid || captures != 1 {
		t.Fatal("duplicate capture", n, e, captures)
	}
	body := jsonBytes(map[string]any{"id": "EV123", "summary": "A & B", "event_type": "PAYMENT.CAPTURE.COMPLETED", "resource": map[string]any{"id": "CAP123", "status": "COMPLETED", "custom_id": request("USD").OrderID, "payee": map[string]string{"merchant_id": "seller"}, "supplementary_data": map[string]any{"related_ids": map[string]string{"order_id": "ORDER123"}}}})
	body = append([]byte("\n  "), bytes.ReplaceAll(body, []byte(`\u0026`), []byte("&"))...)
	body = append(body, '\n')
	h := http.Header{"Paypal-Auth-Algo": {"SHA256withRSA"}, "Paypal-Cert-Url": {"https://api-m.sandbox.paypal.com/cert"}, "Paypal-Transmission-Id": {"transmission"}, "Paypal-Transmission-Sig": {"signature"}, "Paypal-Transmission-Time": {"2026-11-15T12:00:00Z"}}
	n, e = p.Verify(context.Background(), h, body)
	if e != nil || n.Status != Paid || n.EventID != "EV123" {
		t.Fatal(n, e)
	}
	if !bytes.Contains(lastVerifiedBody, body) {
		t.Fatal("PayPal verification must preserve original event whitespace and HTML characters")
	}
	id, e := ExtractOrderID("paypal", h, body)
	if e != nil || id != request("USD").OrderID {
		t.Fatal(id, e)
	}
	verifySuccess = false
	if _, e = p.Verify(context.Background(), h, body); !errors.Is(e, ErrSignature) {
		t.Fatal("unverified webhook")
	}
	verifySuccess = true
	if _, e = p.Verify(context.Background(), h, []byte(strings.Replace(string(body), `"merchant_id":"seller"`, `"merchant_id":"another"`, 1))); e == nil {
		t.Fatal("wrong receiving merchant")
	}
}

func twoCheckoutBody(t *testing.T, status string) []byte {
	t.Helper()
	pairs := [][2]string{{"REFNO", "123"}, {"REFNOEXT", request("USD").OrderID}, {"ORDERSTATUS", status}, {"MESSAGE_TYPE", status}, {"IPN_PID[]", "product1"}, {"IPN_PID[]", "product2"}, {"IPN_PNAME[]", "会员 & 测试"}, {"IPN_PNAME[]", "second"}, {"IPN_DATE", "20261115120000"}, {"IPN_TOTALGENERAL", "12.34"}, {"CURRENCY", "USD"}, {"VENDOR_CODE", "merchant"}, {"TEST_ORDER", "1"}}
	var parts []string
	for _, pair := range pairs {
		parts = append(parts, url.QueryEscape(pair[0])+"="+url.QueryEscape(pair[1]))
	}
	body := strings.Join(parts, "&")
	parsed, e := parseIPN([]byte(body))
	if e != nil {
		t.Fatal(e)
	}
	mac := hmac.New(sha256.New, []byte("ipn_secret"))
	mac.Write([]byte(parsed.signed))
	return []byte(body + "&SIGNATURE_SHA2_256=" + hex.EncodeToString(mac.Sum(nil)))
}

func resignTwoCheckout(t *testing.T, body []byte) []byte {
	t.Helper()
	unsigned := strings.Split(string(body), "&SIGNATURE_SHA2_256=")[0]
	parsed, e := parseIPN([]byte(unsigned))
	if e != nil {
		t.Fatal(e)
	}
	mac := hmac.New(sha256.New, []byte("ipn_secret"))
	mac.Write([]byte(parsed.signed))
	return []byte(unsigned + "&SIGNATURE_SHA2_256=" + hex.EncodeToString(mac.Sum(nil)))
}

func TestTwoCheckoutSignedBuyLinkIPNAndACK(t *testing.T) {
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkout/api/encrypt/generate/signature" {
			t.Error(r.URL.Path)
		}
		parts := strings.Split(r.Header.Get("Merchant-Token"), ".")
		if len(parts) != 3 {
			t.Error("JWT token")
			return
		}
		mac := hmac.New(sha512.New, []byte("buy_secret"))
		mac.Write([]byte(parts[0] + "." + parts[1]))
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if !hmac.Equal(signature, mac.Sum(nil)) {
			t.Error("JWT signature")
		}
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		if params["order-ext-ref"] != request("USD").OrderID || params["merchant"] != "merchant" || params["price"] != "12.34" {
			t.Error("buy link contract")
		}
		jsonReply(w, map[string]string{"signature": "provider_signature"})
	})
	p, e := New(Config{Provider: "twocheckout", MerchantID: "merchant", APIKey: "buy_secret", Secret: "ipn_secret", Sandbox: true}, client)
	if e != nil {
		t.Fatal(e)
	}
	c, e := p.Create(context.Background(), request("USD"))
	if e != nil || c.ProviderOrderID != request("USD").OrderID {
		t.Fatal(c, e)
	}
	u, _ := url.Parse(c.URL)
	if u.Query().Get("signature") != "provider_signature" {
		t.Fatal(c.URL)
	}
	body := twoCheckoutBody(t, "COMPLETE")
	n, e := p.Verify(context.Background(), nil, body)
	if e != nil || n.Status != Paid || n.Amount != 1234 || n.TransactionID != "123" || !strings.HasPrefix(n.Acknowledgement, `<sig algo="sha256"`) {
		t.Fatal(n, e)
	}
	id, e := ExtractOrderID("twocheckout", nil, body)
	if e != nil || id != request("USD").OrderID {
		t.Fatal(id, e)
	}
	n, e = p.Verify(context.Background(), nil, twoCheckoutBody(t, "AUTHRECEIVED"))
	if e != nil || n.Status == Paid {
		t.Fatal("authorization is not capture", n, e)
	}
	if _, e = p.Verify(context.Background(), nil, []byte(strings.Replace(string(body), "12.34", "99.99", 1))); !errors.Is(e, ErrSignature) {
		t.Fatal("tampered price")
	}
	wrongMerchant := resignTwoCheckout(t, []byte(strings.Replace(string(body), "VENDOR_CODE=merchant", "VENDOR_CODE=other", 1)))
	if _, e = p.Verify(context.Background(), nil, wrongMerchant); e == nil {
		t.Fatal("signed notification for another merchant accepted")
	}
	wrongMode := resignTwoCheckout(t, []byte(strings.Replace(string(body), "TEST_ORDER=1", "TEST_ORDER=0", 1)))
	if _, e = p.Verify(context.Background(), nil, wrongMode); e == nil {
		t.Fatal("live transaction accepted by sandbox")
	}
	refund := resignTwoCheckout(t, []byte(strings.Replace(string(body), "MESSAGE_TYPE=COMPLETE", "MESSAGE_TYPE=REFUND", 1)))
	n, e = p.Verify(context.Background(), nil, refund)
	if e != nil || n.Status != Refunded {
		t.Fatal("refund must not be reclassified by original COMPLETE order status", n, e)
	}
	unknown := resignTwoCheckout(t, []byte(strings.Replace(string(body), "MESSAGE_TYPE=COMPLETE", "MESSAGE_TYPE=OTHER_EVENT", 1)))
	n, e = p.Verify(context.Background(), nil, unknown)
	if e != nil || n.Status == Paid {
		t.Fatal("unknown event must not authorize fulfilment", n, e)
	}
	if _, e = p.Query(context.Background(), c.ProviderOrderID, request("USD").OrderID); !errors.Is(e, ErrUnsupported) {
		t.Fatal("buy link query must not fake success")
	}
}

func TestExtractOrderIDIsUntrustedAndBounded(t *testing.T) {
	for _, provider := range []string{"epay", "linuxdo", "alipay", "alipay_f2f"} {
		id, e := ExtractOrderID(provider, nil, []byte("out_trade_no=np_test&sign=invalid"))
		if e != nil || id != "np_test" {
			t.Fatal(provider, id, e)
		}
	}
	for _, raw := range []string{"out_trade_no=../secret", "out_trade_no=a&out_trade_no=b", fmt.Sprintf("out_trade_no=%s", strings.Repeat("x", 65))} {
		if _, e := ExtractOrderID("epay", nil, []byte(raw)); e == nil {
			t.Fatal(raw)
		}
	}
	if _, e := ExtractOrderID("wechat", nil, []byte(`{}`)); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
}
