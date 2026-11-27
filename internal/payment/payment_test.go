package payment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		copy.URL = &u
		return http.DefaultTransport.RoundTrip(copy)
	}), Timeout: 5 * time.Second}
}
func keys(t *testing.T) (string, string) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	public, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}))
}
func request(currency string) Request {
	return Request{OrderID: "np_1234567890abcdef1234567890ab", Amount: 1234, Currency: currency, Description: "会员 & 测试", NotifyURL: "https://site.example/api/notify/np_1234567890abcdef1234567890ab", ReturnURL: "https://site.example/account/payments", CancelURL: "https://site.example/account/payments"}
}
func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func TestAmountsAndNetworkBoundaries(t *testing.T) {
	for _, c := range []struct {
		raw, currency string
		want          int64
	}{{"12.34", "CNY", 1234}, {"12.34000", "USD", 1234}, {"0.01", "LDC", 1}, {"1234", "JPY", 1234}} {
		n, e := ParseAmount(c.raw, c.currency)
		if e != nil || n != c.want {
			t.Fatalf("%+v got %d %v", c, n, e)
		}
		formatted, e := FormatAmount(n, c.currency)
		if e != nil {
			t.Fatal(e)
		}
		again, e := ParseAmount(formatted, c.currency)
		if e != nil || again != n {
			t.Fatal(formatted)
		}
	}
	for _, s := range []string{"-1", "+1", "1e2", "1.001", " 1.00", "1.", "NaN", "9223372036854775808"} {
		if _, e := ParseAmount(s, "USD"); e == nil {
			t.Errorf("accepted %s", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "169.254.169.254", "100.100.100.200", "100.64.0.1", "0.0.0.1", "198.18.0.1"} {
		if publicIP(net.ParseIP(s)) {
			t.Errorf("public %s", s)
		}
	}
	if publicIP(net.IP{127, 0, 0, 1}) {
		t.Fatal("four-byte loopback")
	}
	for _, endpoint := range []string{"http://gateway.example", "https://127.0.0.1", "https://localhost", "https://gateway.example/?secret=x", "https://user:pass@gateway.example"} {
		if _, e := New(Config{Provider: "epay", Endpoint: endpoint, MerchantID: "1", Secret: "secret"}, nil); e == nil {
			t.Fatal(endpoint)
		}
	}
	if _, e := New(Config{Provider: "stripe", APIKey: "sk", Secret: "secret", Endpoint: "https://attacker.example"}, nil); e == nil {
		t.Fatal("official host override")
	}
	for _, provider := range []string{"epay", "linuxdo", "wechat"} {
		if _, e := New(Config{Provider: provider, Endpoint: "https://gateway.example", MerchantID: "merchant", Secret: "secret", Sandbox: true}, nil); !errors.Is(e, ErrConfiguration) {
			t.Fatalf("%s must not silently use a live endpoint in sandbox: %v", provider, e)
		}
	}
}

func TestEPayAndLDCFlow(t *testing.T) {
	for _, kind := range []string{"epay", "linuxdo"} {
		t.Run(kind, func(t *testing.T) {
			cfg := Config{Provider: kind, Endpoint: "https://gateway.example", MerchantID: "merchant", Secret: "secret"}
			currency := "CNY"
			if kind == "linuxdo" {
				cfg.Endpoint = ""
				currency = "LDC"
			}
			client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/api.php") || r.URL.Query().Get("key") != "secret" {
					t.Error("query contract")
				}
				jsonReply(w, map[string]any{"code": 1, "pid": "merchant", "out_trade_no": request(currency).OrderID, "trade_no": "gateway123", "money": "12.34", "status": 1})
			})
			p, e := New(cfg, client)
			if e != nil {
				t.Fatal(e)
			}
			checkout, e := p.Create(context.Background(), request(currency))
			if e != nil {
				t.Fatal(e)
			}
			u, _ := url.Parse(checkout.URL)
			params := u.Query()
			if kind == "linuxdo" {
				if checkout.Method != "POST" || u.RawQuery != "" || checkout.Fields["type"] != "epay" {
					t.Fatal("official LDC checkout requires a POST form", checkout.Method)
				}
				for key, value := range checkout.Fields {
					params.Set(key, value)
				}
			}
			if params.Get("money") != "12.34" || params.Get("sign") != epaySign(params, "secret") {
				t.Fatal("create signature")
			}
			if kind == "linuxdo" && u.Path != "/epay/pay/submit.php" {
				t.Fatal(u.Path)
			}
			v := url.Values{"pid": {"merchant"}, "out_trade_no": {request(currency).OrderID}, "trade_no": {"gateway123"}, "money": {"12.34"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
			v.Set("sign", epaySign(v, "secret"))
			n, e := p.Verify(context.Background(), nil, []byte(v.Encode()))
			if e != nil || n.Status != Paid || n.Amount != 1234 || n.Currency != currency || n.ProviderOrderID != checkout.ProviderOrderID {
				t.Fatalf("%+v %v", n, e)
			}
			n, e = p.Query(context.Background(), checkout.ProviderOrderID, request(currency).OrderID)
			if e != nil || n.Status != Paid || n.TransactionID != "gateway123" {
				t.Fatalf("%+v %v", n, e)
			}
			v.Set("money", "99.99")
			if _, e = p.Verify(context.Background(), nil, []byte(v.Encode())); !errors.Is(e, ErrSignature) {
				t.Fatal("tampered money accepted")
			}
			v.Set("pid", "other")
			v.Set("sign", epaySign(v, "secret"))
			if _, e = p.Verify(context.Background(), nil, []byte(v.Encode())); !errors.Is(e, ErrSignature) {
				t.Fatal("wrong merchant accepted")
			}
			if _, e = p.Verify(context.Background(), nil, []byte(v.Encode()+"&pid=merchant")); e == nil {
				t.Fatal("duplicate field accepted")
			}
		})
	}
}

func stripeSigned(t *testing.T, body []byte, secret string, at time.Time) http.Header {
	t.Helper()
	ts := fmt.Sprint(at.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return http.Header{"Stripe-Signature": {"t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))}}
}
func TestStripeCreateQueryWebhook(t *testing.T) {
	id := request("USD").OrderID
	session := map[string]any{"id": "cs_123", "client_reference_id": id, "amount_total": 1234, "currency": "usd", "payment_status": "paid", "status": "complete", "payment_intent": "pi_123", "livemode": false}
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test" {
			t.Error("auth")
		}
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("line_items[0][price_data][unit_amount]") != "1234" || r.Form.Get("client_reference_id") != id || r.Header.Get("Idempotency-Key") != id {
				t.Error("checkout contract")
			}
			jsonReply(w, map[string]string{"id": "cs_123", "url": "https://checkout.stripe.com/c/pay/cs_123"})
		} else {
			jsonReply(w, session)
		}
	})
	p, e := New(Config{Provider: "stripe", APIKey: "sk_test", Secret: "whsec_test", Sandbox: true}, client)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		c, e := p.Create(context.Background(), request("USD"))
		if e != nil || c.ProviderOrderID != "cs_123" {
			t.Fatal(c, e)
		}
	}
	n, e := p.Query(context.Background(), "cs_123", id)
	if e != nil || n.Status != Paid || n.Amount != 1234 {
		t.Fatal(n, e)
	}
	session["payment_status"] = "unpaid"
	n, e = p.Query(context.Background(), "cs_123", id)
	if e != nil || n.Status != Pending {
		t.Fatal(n, e)
	}
	session["payment_status"] = "paid"
	body := jsonBytes(map[string]any{"id": "evt_123", "type": "checkout.session.completed", "livemode": false, "data": map[string]any{"object": session}})
	h := stripeSigned(t, body, "whsec_test", time.Now())
	n, e = p.Verify(context.Background(), h, body)
	if e != nil || n.EventID != "evt_123" || n.TransactionID != "pi_123" {
		t.Fatal(n, e)
	}
	order, e := ExtractOrderID("stripe", h, body)
	if e != nil || order != id {
		t.Fatal(order, e)
	}
	if _, e = p.Verify(context.Background(), h, append(body, ' ')); !errors.Is(e, ErrSignature) {
		t.Fatal("changed payload")
	}
	if _, e = p.Verify(context.Background(), stripeSigned(t, body, "whsec_test", time.Now().Add(-6*time.Minute)), body); !errors.Is(e, ErrSignature) {
		t.Fatal("replay window")
	}
	if _, e = p.Query(context.Background(), "cs_123", "wrong"); e == nil {
		t.Fatal("different order accepted")
	}
}

func TestAlipayRSA2MerchantAndResponseVerification(t *testing.T) {
	priv, pub := keys(t)
	id := request("CNY").OrderID
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if rsaVerify(pub, []byte(canonicalForm(r.PostForm, "sign")), r.PostForm.Get("sign")) != nil {
			t.Error("request signature")
		}
		method := r.PostForm.Get("method")
		response := map[string]any{"code": "10000", "out_trade_no": id, "trade_no": "ali_123", "total_amount": "12.34", "trade_status": "TRADE_SUCCESS", "seller_id": "seller"}
		if method == "alipay.trade.precreate" {
			response["qr_code"] = "https://qr.alipay.com/pay123"
		}
		raw := jsonBytes(response)
		sign, _ := rsaSign(priv, string(raw))
		jsonReply(w, map[string]any{strings.ReplaceAll(method, ".", "_") + "_response": json.RawMessage(raw), "sign": sign})
	})
	for _, kind := range []string{"alipay", "alipay_f2f"} {
		p, e := New(Config{Provider: kind, AppID: "app", MerchantID: "seller", PrivateKey: priv, PublicKey: pub}, client)
		if e != nil {
			t.Fatal(e)
		}
		c, e := p.Create(context.Background(), request("CNY"))
		if e != nil || c.ProviderOrderID != id || kind == "alipay" && (c.Method != "POST" || c.Fields["sign"] == "") || kind == "alipay_f2f" && c.QRCode == "" {
			t.Fatal(c, e)
		}
		n, e := p.Query(context.Background(), id, id)
		if e != nil || n.Status != Paid || n.Amount != 1234 {
			t.Fatal(n, e)
		}
		v := url.Values{"app_id": {"app"}, "seller_id": {"seller"}, "out_trade_no": {id}, "trade_no": {"ali_123"}, "total_amount": {"12.34"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"RSA2"}}
		s, _ := rsaSign(priv, canonicalForm(v, "sign", "sign_type"))
		v.Set("sign", s)
		n, e = p.Verify(context.Background(), nil, []byte(v.Encode()))
		if e != nil || n.Status != Paid {
			t.Fatal(n, e)
		}
		v.Set("total_amount", "99.99")
		if _, e = p.Verify(context.Background(), nil, []byte(v.Encode())); !errors.Is(e, ErrSignature) {
			t.Fatal("changed amount")
		}
		v.Set("seller_id", "another")
		s, _ = rsaSign(priv, canonicalForm(v, "sign", "sign_type"))
		v.Set("sign", s)
		if _, e = p.Verify(context.Background(), nil, []byte(v.Encode())); !errors.Is(e, ErrSignature) {
			t.Fatal("wrong seller")
		}
	}
}

func wechatSign(priv string, b []byte) http.Header {
	timestamp := fmt.Sprint(time.Now().Unix())
	s, _ := rsaSign(priv, timestamp+"\nnonce\n"+string(b)+"\n")
	return http.Header{"Wechatpay-Timestamp": {timestamp}, "Wechatpay-Nonce": {"nonce"}, "Wechatpay-Serial": {"platform"}, "Wechatpay-Signature": {s}}
}
func TestWechatSignedEncryptedCallbackAndQuery(t *testing.T) {
	priv, pub := keys(t)
	secret := "12345678901234567890123456789012"
	id := request("CNY").OrderID
	transaction := map[string]any{"appid": "app", "mchid": "merchant", "out_trade_no": id, "transaction_id": "wx123", "trade_state": "SUCCESS", "amount": map[string]any{"total": 1234, "currency": "CNY"}}
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "WECHATPAY2-SHA256-RSA2048") {
			t.Error("auth")
		}
		var b []byte
		if r.Method == http.MethodPost {
			b = jsonBytes(map[string]string{"code_url": "weixin://wxpay/bizpayurl?pr=test"})
		} else {
			b = jsonBytes(transaction)
		}
		for key, values := range wechatSign(priv, b) {
			w.Header()[key] = values
		}
		_, _ = w.Write(b)
	})
	p, e := New(Config{Provider: "wechat", MerchantID: "merchant", AppID: "app", PrivateKey: priv, PublicKey: pub, CertificateSerial: "merchant_cert", PlatformSerial: "platform", Secret: secret}, client)
	if e != nil {
		t.Fatal(e)
	}
	c, e := p.Create(context.Background(), request("CNY"))
	if e != nil || !strings.HasPrefix(c.QRCode, "weixin://") {
		t.Fatal(c, e)
	}
	tooLong := request("CNY")
	tooLong.OrderID = "np_" + strings.Repeat("a", 32)
	if _, e = p.Create(context.Background(), tooLong); !errors.Is(e, ErrRequest) {
		t.Fatal("WeChat out_trade_no exceeds its 32-character API limit", e)
	}
	n, e := p.Query(context.Background(), id, id)
	if e != nil || n.Status != Paid {
		t.Fatal(n, e)
	}
	block, _ := aes.NewCipher([]byte(secret))
	gcm, _ := cipher.NewGCM(block)
	sealed := gcm.Seal(nil, []byte("123456789012"), jsonBytes(transaction), []byte("transaction"))
	body := jsonBytes(map[string]any{"id": "wxevent", "event_type": "TRANSACTION.SUCCESS", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(sealed), "nonce": "123456789012", "associated_data": "transaction"}})
	h := wechatSign(priv, body)
	n, e = p.Verify(context.Background(), h, body)
	if e != nil || n.Amount != 1234 || n.EventID != "wxevent" {
		t.Fatal(n, e)
	}
	h.Set("Wechatpay-Serial", "other")
	if _, e = p.Verify(context.Background(), h, body); !errors.Is(e, ErrSignature) {
		t.Fatal("wrong platform")
	}
	h = wechatSign(priv, body)
	if _, e = p.Verify(context.Background(), h, append(body, ' ')); !errors.Is(e, ErrSignature) {
		t.Fatal("tampered encrypted notification")
	}
	for _, field := range []string{"appid", "mchid"} {
		original := transaction[field]
		transaction[field] = "other"
		if _, e = p.Query(context.Background(), id, id); e == nil {
			t.Fatalf("valid signature accepted another %s", field)
		}
		transaction[field] = original
	}
	transaction["amount"] = map[string]any{"total": 1234, "currency": "USD"}
	if _, e = p.Query(context.Background(), id, id); e == nil {
		t.Fatal("valid signature accepted unsupported settlement currency")
	}
}

func TestProviderClientRejectsRedirectAndLargeBody(t *testing.T) {
	for _, mode := range []string{"redirect", "large"} {
		t.Run(mode, func(t *testing.T) {
			client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "https://other.example/collect", 302)
				} else {
					_, _ = io.WriteString(w, strings.Repeat("x", maxBody+1))
				}
			})
			p, e := New(Config{Provider: "stripe", APIKey: "key", Secret: "secret"}, client)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.Query(context.Background(), "cs_1", "order"); !errors.Is(e, ErrResponse) {
				t.Fatal(e)
			}
		})
	}
}
