package payment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

const maxBody = 1 << 20

type client struct {
	config   Config
	http     *http.Client
	endpoint string
}

func CurrencyExponent(currency string) (int, error) {
	switch strings.ToUpper(currency) {
	case "JPY":
		return 0, nil
	case "CNY", "USD", "EUR", "GBP", "HKD", "LDC", "AUD", "CAD", "SGD", "CHF":
		return 2, nil
	default:
		return 0, ErrRequest
	}
}

func FormatAmount(amount int64, currency string) (string, error) {
	exp, err := CurrencyExponent(currency)
	if err != nil || amount < 0 {
		return "", ErrRequest
	}
	if exp == 0 {
		return strconv.FormatInt(amount, 10), nil
	}
	return fmt.Sprintf("%d.%02d", amount/100, amount%100), nil
}

func ParseAmount(value, currency string) (int64, error) {
	exp, err := CurrencyExponent(currency)
	if err != nil {
		return 0, err
	}
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "+-eE") {
		return 0, ErrResponse
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrResponse
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > 90_000_000_000_000 {
		return 0, ErrResponse
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" {
			return 0, ErrResponse
		}
	}
	if len(fraction) > exp {
		if strings.Trim(fraction[exp:], "0") != "" {
			return 0, ErrResponse
		}
		fraction = fraction[:exp]
	}
	for _, c := range fraction {
		if c < '0' || c > '9' {
			return 0, ErrResponse
		}
	}
	if exp == 0 {
		return whole, nil
	}
	fraction += strings.Repeat("0", exp-len(fraction))
	minor, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, ErrResponse
	}
	return whole*100 + minor, nil
}

func New(config Config, supplied *http.Client) (Provider, error) {
	config.Provider = strings.TrimSpace(config.Provider)
	if config.Sandbox && (config.Provider == "wechat" || config.Provider == "linuxdo" || config.Provider == "epay") {
		// These protocols do not implement an isolated sandbox. A test flag must
		// never silently submit an order to the live merchant account.
		return nil, ErrConfiguration
	}
	endpoints := map[string]string{"alipay": "https://openapi.alipay.com/gateway.do", "alipay_f2f": "https://openapi.alipay.com/gateway.do", "wechat": "https://api.mch.weixin.qq.com", "linuxdo": "https://pay.linux.do/epay", "stripe": "https://api.stripe.com/v1", "paypal": "https://api-m.paypal.com", "twocheckout": "https://secure.2checkout.com"}
	endpoint := endpoints[config.Provider]
	if config.Provider == "linuxdo" && strings.TrimRight(config.Endpoint, "/") == "https://credit.linux.do/epay" {
		endpoint = "https://credit.linux.do/epay"
	}
	if config.Sandbox {
		switch config.Provider {
		case "alipay", "alipay_f2f":
			endpoint = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
		case "paypal":
			endpoint = "https://api-m.sandbox.paypal.com"
		}
	}
	if config.Provider == "epay" {
		endpoint = strings.TrimRight(config.Endpoint, "/")
		if !safeURL(endpoint) {
			return nil, ErrConfiguration
		}
	} else if endpoint == "" || config.Endpoint != "" && strings.TrimRight(config.Endpoint, "/") != endpoint {
		return nil, ErrConfiguration
	}
	required := map[string][]string{"alipay": {config.AppID, config.MerchantID, config.PrivateKey, config.PublicKey}, "alipay_f2f": {config.AppID, config.MerchantID, config.PrivateKey, config.PublicKey}, "wechat": {config.AppID, config.MerchantID, config.PrivateKey, config.PublicKey, config.CertificateSerial, config.PlatformSerial, config.Secret}, "epay": {config.MerchantID, config.Secret}, "linuxdo": {config.MerchantID, config.Secret}, "stripe": {config.APIKey, config.Secret}, "paypal": {config.AppID, config.Secret, config.WebhookID, config.MerchantID}, "twocheckout": {config.MerchantID, config.APIKey, config.Secret}}
	for _, v := range required[config.Provider] {
		if strings.TrimSpace(v) == "" {
			return nil, ErrConfiguration
		}
	}
	if config.Provider == "wechat" && len(config.Secret) != 32 {
		return nil, ErrConfiguration
	}
	if config.Provider == "alipay" || config.Provider == "alipay_f2f" || config.Provider == "wechat" {
		if _, err := privateKey(config.PrivateKey); err != nil {
			return nil, ErrConfiguration
		}
		if _, err := publicKey(config.PublicKey); err != nil {
			return nil, ErrConfiguration
		}
	}
	if config.Provider == "epay" && config.PaymentType != "" && config.PaymentType != "alipay" && config.PaymentType != "wxpay" && config.PaymentType != "qqpay" {
		return nil, ErrConfiguration
	}
	if supplied == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = publicDial
		supplied = &http.Client{Transport: transport, Timeout: 20 * time.Second}
	} else {
		copy := *supplied
		supplied = &copy
		if supplied.Timeout <= 0 || supplied.Timeout > 30*time.Second {
			supplied.Timeout = 20 * time.Second
		}
	}
	supplied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &client{config: config, http: supplied, endpoint: endpoint}
	switch config.Provider {
	case "epay", "linuxdo":
		return &epay{c}, nil
	case "stripe":
		return &stripe{c}, nil
	case "paypal":
		return &paypal{c}, nil
	case "alipay", "alipay_f2f":
		return &alipay{c}, nil
	case "wechat":
		return &wechat{c}, nil
	case "twocheckout":
		return &twocheckout{c}, nil
	}
	return nil, ErrConfiguration
}

func safeURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return false
	}
	return true
}
func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	return ok && safehttp.PublicIP(address)
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrConfiguration
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, ErrResponse
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, ErrConfiguration
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	addresses := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, ip.IP)
	}
	return dialResolvedAddresses(ctx, network, port, addresses, d.DialContext)
}

func dialResolvedAddresses(ctx context.Context, network, port string, addresses []net.IP, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	var lastErr error
	for _, address := range addresses {
		conn, err := dial(ctx, network, net.JoinHostPort(address.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrResponse
}
func checkoutURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil {
		return false
	}
	u.RawQuery = ""
	return safeURL(u.String())
}
func (c *client) valid(r Request) error {
	if len(r.OrderID) < 1 || len(r.OrderID) > 64 || r.Amount <= 0 || r.Amount > 90_000_000_000_000 || len(r.Description) > 512 {
		return ErrRequest
	}
	for _, v := range r.OrderID {
		if !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == '_' || v == '-') {
			return ErrRequest
		}
	}
	ok := false
	for _, d := range Definitions() {
		if d.Key == c.config.Provider {
			for _, currency := range d.Currencies {
				ok = ok || currency == r.Currency
			}
		}
	}
	if !ok {
		return ErrRequest
	}
	urls := []string{r.NotifyURL, r.ReturnURL}
	if r.CancelURL != "" {
		urls = append(urls, r.CancelURL)
	}
	for _, raw := range urls {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(c.config.Sandbox && u.Scheme == "http")) {
			return ErrRequest
		}
	}
	return nil
}
func (c *client) do(ctx context.Context, method, target, contentType string, body []byte, headers http.Header) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, nil, ErrRequest
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, ErrResponse
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(data) > maxBody || res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, ErrResponse
	}
	return data, res.Header, nil
}
func decode(data []byte, target any) error {
	if len(data) > maxBody {
		return ErrResponse
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return ErrResponse
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrResponse
	}
	return nil
}
func jsonBytes(value any) []byte { b, _ := json.Marshal(value); return b }
func nonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func privateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, ErrConfiguration
	}
	if k, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
		if r, ok := k.(*rsa.PrivateKey); ok && r.N.BitLen() >= 2048 {
			return r, nil
		}
	}
	if k, e := x509.ParsePKCS1PrivateKey(block.Bytes); e == nil && k.N.BitLen() >= 2048 {
		return k, nil
	}
	return nil, ErrConfiguration
}
func publicKey(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, ErrConfiguration
	}
	if cert, e := x509.ParseCertificate(block.Bytes); e == nil {
		if k, ok := cert.PublicKey.(*rsa.PublicKey); ok && k.N.BitLen() >= 2048 {
			return k, nil
		}
	}
	if k, e := x509.ParsePKIXPublicKey(block.Bytes); e == nil {
		if r, ok := k.(*rsa.PublicKey); ok && r.N.BitLen() >= 2048 {
			return r, nil
		}
	}
	if k, e := x509.ParsePKCS1PublicKey(block.Bytes); e == nil && k.N.BitLen() >= 2048 {
		return k, nil
	}
	return nil, ErrConfiguration
}
func one(values url.Values, key string) (string, error) {
	if len(values[key]) != 1 {
		return "", ErrSignature
	}
	return values.Get(key), nil
}
func parseForm(body []byte) (url.Values, error) {
	if len(body) > maxBody {
		return nil, ErrRequest
	}
	v, e := url.ParseQuery(string(body))
	if e != nil {
		return nil, ErrSignature
	}
	for _, items := range v {
		if len(items) != 1 {
			return nil, ErrSignature
		}
	}
	return v, nil
}
