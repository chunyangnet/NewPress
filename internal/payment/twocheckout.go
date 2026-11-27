package payment

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type twocheckout struct{ *client }

func (p *twocheckout) Create(ctx context.Context, r Request) (Checkout, error) {
	if e := p.valid(r); e != nil {
		return Checkout{}, e
	}
	amount, _ := FormatAmount(r.Amount, r.Currency)
	params := map[string]any{"merchant": p.config.MerchantID, "dynamic": 1, "prod": r.Description, "price": amount, "qty": 1, "type": "PRODUCT", "tangible": 0, "currency": strings.ToLower(r.Currency), "order-ext-ref": r.OrderID, "item-ext-ref": r.OrderID, "return-url": r.ReturnURL, "return-type": "redirect", "expiration": time.Now().Add(30 * time.Minute).Unix(), "language": "zh", "test": 0}
	if p.config.Sandbox {
		params["test"] = 1
	}
	header := base64.RawURLEncoding.EncodeToString(jsonBytes(map[string]string{"typ": "JWT", "alg": "HS512"}))
	claims := base64.RawURLEncoding.EncodeToString(jsonBytes(map[string]any{"sub": p.config.MerchantID, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}))
	mac := hmac.New(sha512.New, []byte(p.config.APIKey))
	mac.Write([]byte(header + "." + claims))
	token := header + "." + claims + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	b, _, e := p.do(ctx, http.MethodPost, p.endpoint+"/checkout/api/encrypt/generate/signature", "application/json", jsonBytes(params), http.Header{"Merchant-Token": {token}})
	if e != nil {
		return Checkout{}, e
	}
	var d struct {
		Signature string `json:"signature"`
	}
	if decode(b, &d) != nil || d.Signature == "" {
		return Checkout{}, ErrResponse
	}
	v := url.Values{}
	for k, value := range params {
		v.Set(k, fmt.Sprint(value))
	}
	v.Set("signature", d.Signature)
	return Checkout{ProviderOrderID: r.OrderID, URL: p.endpoint + "/checkout/buy/?" + v.Encode()}, nil
}

// 2Checkout's hosted buy-link does not return the numeric REFNO before payment.
// Completion is deliberately webhook-only; return-url is never payment proof.
func (p *twocheckout) Query(context.Context, string, string) (Notification, error) {
	return Notification{}, ErrUnsupported
}

type ipnFields struct {
	values url.Values
	signed string
}

func stripSlashes(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
			if value[i] == '0' {
				b.WriteByte(0)
				continue
			}
		}
		b.WriteByte(value[i])
	}
	return b.String()
}
func parseIPN(body []byte) (ipnFields, error) {
	if len(body) > maxBody {
		return ipnFields{}, ErrRequest
	}
	result := ipnFields{values: url.Values{}}
	groups := map[string][]string{}
	order := []string{}
	for _, part := range strings.Split(string(body), "&") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return result, ErrSignature
		}
		key, e := url.QueryUnescape(k)
		if e != nil {
			return result, ErrSignature
		}
		value, e := url.QueryUnescape(v)
		if e != nil {
			return result, ErrSignature
		}
		base := key
		if i := strings.IndexByte(key, '['); i >= 0 {
			base = key[:i]
			if !strings.HasSuffix(key, "]") {
				return result, ErrSignature
			}
		}
		if _, exists := groups[base]; !exists {
			order = append(order, base)
		} else if !strings.Contains(key, "[") {
			return result, ErrSignature
		}
		groups[base] = append(groups[base], stripSlashes(value))
		result.values.Add(base, value)
	}
	var signature strings.Builder
	for _, key := range order {
		if key == "HASH" || key == "SIGNATURE_SHA2_256" || key == "SIGNATURE_SHA3_256" {
			continue
		}
		for _, value := range groups[key] {
			signature.WriteString(strconv.Itoa(len([]byte(value))))
			signature.WriteString(value)
		}
	}
	result.signed = signature.String()
	return result, nil
}
func (p *twocheckout) Verify(_ context.Context, _ http.Header, b []byte) (Notification, error) {
	d, e := parseIPN(b)
	if e != nil {
		return Notification{}, e
	}
	v := d.values
	algo := "md5"
	key := "HASH"
	factory := func() hash.Hash { return md5.New() }
	if v.Get("SIGNATURE_SHA3_256") != "" {
		algo = "sha3-256"
		key = "SIGNATURE_SHA3_256"
		factory = func() hash.Hash { return sha3.New256() }
	} else if v.Get("SIGNATURE_SHA2_256") != "" {
		algo = "sha256"
		key = "SIGNATURE_SHA2_256"
		factory = sha256.New
	}
	signature, e := hex.DecodeString(v.Get(key))
	if e != nil {
		return Notification{}, ErrSignature
	}
	mac := hmac.New(factory, []byte(p.config.Secret))
	mac.Write([]byte(d.signed))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Notification{}, ErrSignature
	}
	for _, field := range []string{"REFNOEXT", "REFNO", "ORDERSTATUS", "MESSAGE_TYPE", "IPN_TOTALGENERAL", "CURRENCY", "IPN_DATE", "VENDOR_CODE", "TEST_ORDER"} {
		if len(v[field]) != 1 || v.Get(field) == "" {
			return Notification{}, ErrResponse
		}
	}
	if len(v["IPN_PID"]) < 1 || len(v["IPN_PNAME"]) < 1 {
		return Notification{}, ErrResponse
	}
	if v.Get("VENDOR_CODE") != p.config.MerchantID || v.Get("TEST_ORDER") != "0" && v.Get("TEST_ORDER") != "1" {
		return Notification{}, ErrResponse
	}
	test := v.Get("TEST_ORDER") == "1"
	if test != p.config.Sandbox {
		return Notification{}, ErrResponse
	}
	currency := strings.ToUpper(v.Get("CURRENCY"))
	amount, e := ParseAmount(v.Get("IPN_TOTALGENERAL"), currency)
	if e != nil || amount <= 0 {
		return Notification{}, ErrResponse
	}
	status := Pending
	switch v.Get("ORDERSTATUS") {
	case "COMPLETE":
		// Order status can remain COMPLETE on later refund/dispute messages.
		// Only the completion event itself authorizes fulfilment.
		if v.Get("MESSAGE_TYPE") == "COMPLETE" {
			status = Paid
		}
	case "REFUND":
		status = Refunded
	case "REJECTED", "CANCELED":
		status = Failed
	}
	if v.Get("REFUND_TYPE") != "" || strings.HasPrefix(v.Get("MESSAGE_TYPE"), "REFUND") || strings.HasPrefix(v.Get("MESSAGE_TYPE"), "CHARGEBACK") {
		status = Refunded // The commerce layer sends this to manual reconciliation.
	}
	date := time.Now().UTC().Format("20060102150405")
	var ackContent strings.Builder
	for _, value := range []string{v["IPN_PID"][0], v["IPN_PNAME"][0], v.Get("IPN_DATE"), date} {
		value = stripSlashes(value)
		ackContent.WriteString(strconv.Itoa(len([]byte(value))))
		ackContent.WriteString(value)
	}
	ackMac := hmac.New(factory, []byte(p.config.Secret))
	ackMac.Write([]byte(ackContent.String()))
	ack := fmt.Sprintf(`<sig algo="%s" date="%s">%s</sig>`, algo, date, hex.EncodeToString(ackMac.Sum(nil)))
	if algo == "md5" {
		ack = fmt.Sprintf("<EPAYMENT>%s|%s</EPAYMENT>", date, hex.EncodeToString(ackMac.Sum(nil)))
	}
	return Notification{OrderID: v.Get("REFNOEXT"), ProviderOrderID: v.Get("REFNOEXT"), TransactionID: v.Get("REFNO"), Amount: amount, Currency: currency, Status: status, EventID: v.Get("REFNO") + ":" + v.Get("ORDERSTATUS") + ":" + v.Get("IPN_DATE"), Acknowledgement: ack, AcknowledgementContentType: "application/xml; charset=utf-8"}, nil
}
