package accessconfig

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

func sha(raw string) string { d := sha256.Sum256([]byte(raw)); return hex.EncodeToString(d[:]) }
func mac(key []byte, raw string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(raw))
	return h.Sum(nil)
}
func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func expand(raw string, p SMS, phone, code, purpose string) string {
	// Replace once: values (including secrets) are never interpreted as templates.
	return strings.NewReplacer("{phone}", phone, "{phone_national}", strings.TrimPrefix(phone, "+86"), "{code}", code, "{purpose}", purpose, "{minutes}", "10", "{secret}", p.Secret).Replace(raw)
}

func expandJSON(raw string, p SMS, phone, code, purpose string) (any, error) {
	var value any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, errors.New("短信参数 JSON 无效")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("短信参数包含多余 JSON")
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			return expand(x, p, phone, code, purpose)
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
			return x
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
			return x
		default:
			return v
		}
	}
	return walk(value), nil
}

func smsRequest(ctx context.Context, p SMS, phone, code, purpose string, now time.Time, nonce string) (*http.Request, error) {
	endpoint, contentType := "", "application/x-www-form-urlencoded"
	form := url.Values{}
	var payload any
	var err error
	params := func() string {
		v, e := expandJSON(p.TemplateParams, p, phone, code, purpose)
		if e != nil {
			err = e
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	switch p.Provider {
	case "aliyun":
		endpoint = "https://dysmsapi.aliyuncs.com/"
		form = url.Values{"PhoneNumbers": {strings.TrimPrefix(phone, "+86")}, "SignName": {p.SignName}, "TemplateCode": {p.TemplateID}, "TemplateParam": {params()}}
	case "tencent":
		endpoint, contentType = "https://sms.tencentcloudapi.com/", "application/json; charset=utf-8"
		var values any
		values, err = expandJSON(p.TemplateParams, p, phone, code, purpose)
		payload = map[string]any{"PhoneNumberSet": []string{phone}, "SmsSdkAppId": p.AppID, "SignName": p.SignName, "TemplateId": p.TemplateID, "TemplateParamSet": values}
	case "huawei":
		endpoint = p.Endpoint
		form = url.Values{"from": {p.Sender}, "to": {phone}, "templateId": {p.TemplateID}, "templateParas": {params()}}
		if p.SignName != "" {
			form.Set("signature", p.SignName)
		}
	case "yunpian":
		if !strings.HasPrefix(phone, "+86") {
			return nil, errors.New("当前云片国内通道仅支持 +86 手机号")
		}
		endpoint = "https://sms.yunpian.com/v2/sms/single_send.json"
		form = url.Values{"apikey": {p.Secret}, "mobile": {strings.TrimPrefix(phone, "+86")}, "text": {expand(p.Message, p, phone, code, purpose)}}
	case "twilio":
		endpoint = "https://api.twilio.com/2010-04-01/Accounts/" + p.AccessKey + "/Messages.json"
		form = url.Values{"To": {phone}, "Body": {expand(p.Message, p, phone, code, purpose)}}
		if strings.HasPrefix(p.Sender, "MG") {
			form.Set("MessagingServiceSid", p.Sender)
		} else {
			form.Set("From", p.Sender)
		}
	case "custom":
		endpoint = p.Endpoint
		payload, err = expandJSON(p.Payload, p, phone, code, purpose)
		if err == nil && p.Encoding == "form" {
			for k, v := range payload.(map[string]any) {
				value, ok := scalar(v)
				if !ok {
					return nil, errors.New("网关表单参数必须为标量")
				}
				form.Set(k, value)
			}
			payload = nil
		} else {
			contentType = "application/json"
		}
	default:
		return nil, errors.New("短信服务未知")
	}
	if err != nil {
		return nil, err
	}
	if _, err := safehttp.ValidateURL(endpoint); err != nil {
		return nil, err
	}
	body := form.Encode()
	// SendSms declares its RPC parameters in the query, as in Alibaba's
	// official client. ACS3 uses RFC 3986 encoding (spaces are %20).
	if p.Provider == "aliyun" {
		endpoint += "?" + strings.ReplaceAll(body, "+", "%20")
		body = ""
	}
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = string(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, errors.New("短信请求创建失败")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NewPress SMS")
	switch p.Provider {
	case "aliyun":
		req.Header.Set("X-Acs-Action", "SendSms")
		req.Header.Set("X-Acs-Version", "2017-05-25")
		req.Header.Set("X-Acs-Date", now.UTC().Format("2006-01-02T15:04:05Z"))
		req.Header.Set("X-Acs-Signature-Nonce", nonce)
		req.Header.Set("X-Acs-Content-Sha256", sha(body))
		headerNames := []string{"host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-signature-nonce", "x-acs-version"}
		canonicalHeaders := "host:" + req.URL.Host + "\n"
		for _, name := range headerNames[1:] {
			canonicalHeaders += name + ":" + req.Header.Get(name) + "\n"
		}
		signed := strings.Join(headerNames, ";")
		canonical := "POST\n/\n" + req.URL.RawQuery + "\n" + canonicalHeaders + "\n" + signed + "\n" + sha(body)
		signature := hex.EncodeToString(mac([]byte(p.Secret), "ACS3-HMAC-SHA256\n"+sha(canonical)))
		req.Header.Set("Authorization", "ACS3-HMAC-SHA256 Credential="+p.AccessKey+",SignedHeaders="+signed+",Signature="+signature)
	case "tencent":
		timestamp := strconv.FormatInt(now.Unix(), 10)
		date := now.UTC().Format("2006-01-02")
		scope := date + "/sms/tc3_request"
		signed := "content-type;host;x-tc-action"
		canonical := "POST\n/\n\ncontent-type:" + contentType + "\nhost:" + req.URL.Host + "\nx-tc-action:sendsms\n\n" + signed + "\n" + sha(body)
		key := mac(mac(mac([]byte("TC3"+p.Secret), date), "sms"), "tc3_request")
		signature := hex.EncodeToString(mac(key, "TC3-HMAC-SHA256\n"+timestamp+"\n"+scope+"\n"+sha(canonical)))
		req.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential="+p.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
		req.Header.Set("X-TC-Action", "SendSms")
		req.Header.Set("X-TC-Version", "2021-01-11")
		req.Header.Set("X-TC-Timestamp", timestamp)
		req.Header.Set("X-TC-Region", p.Region)
	case "huawei":
		created := now.UTC().Format("2006-01-02T15:04:05Z")
		digest := base64.StdEncoding.EncodeToString([]byte(sha(nonce + created + p.Secret)))
		req.Header.Set("Authorization", `WSSE realm="SDP",profile="UsernameToken",type="Appkey"`)
		req.Header.Set("X-WSSE", fmt.Sprintf(`UsernameToken Username=%q,PasswordDigest=%q,Nonce=%q,Created=%q`, p.AccessKey, digest, nonce, created))
	case "twilio":
		req.SetBasicAuth(p.AccessKey, p.Secret)
	case "custom":
		switch p.Auth {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+p.Secret)
		case "basic":
			req.SetBasicAuth(p.AccessKey, p.Secret)
		case "header":
			req.Header.Set(p.AuthHeader, p.Secret)
		}
	}
	return req, nil
}

// SendSMS submits once. Automatic retries could duplicate paid messages.
// Errors never include response bodies, URLs, destination numbers or credentials.
func SendSMS(ctx context.Context, p SMS, phone, code, purpose string, client *http.Client) error {
	if !phonePattern.MatchString(phone) || !codePattern.MatchString(code) || !slices.Contains([]string{"register", "login", "bind", "test"}, purpose) {
		return errors.New("短信号码、验证码或用途无效")
	}
	normalized, configErr := normalizeSMS(p, SMS{})
	if !p.Enabled || configErr != nil {
		return errors.New("短信配置未完成或已关闭")
	}
	p = normalized
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	nonce, err := revision()
	if err != nil {
		return errors.New("无法生成短信请求标识")
	}
	req, err := smsRequest(ctx, p, phone, code, purpose, time.Now(), nonce)
	if err != nil {
		return err
	}
	if client == nil {
		client = safehttp.Client(time.Duration(p.TimeoutSeconds) * time.Second)
	}
	// Even injected clients cannot redirect a signed request to another origin.
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(req)
	if err != nil {
		return errors.New("短信接口连接失败或超时，请检查网络和接口地址")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("短信接口响应无效或过大")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("短信接口返回 HTTP %d，请检查凭据、权限及服务商日志", response.StatusCode)
	}
	if p.Provider == "custom" && p.SuccessPath == "" {
		return nil
	}
	var result map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result == nil {
		return errors.New("短信接口未返回有效 JSON")
	}
	get := func(path string) (string, bool) {
		var current any = result
		for _, key := range strings.Split(path, ".") {
			switch x := current.(type) {
			case map[string]any:
				current = x[key]
			case []any:
				i, e := strconv.Atoi(key)
				if e != nil || i < 0 || i >= len(x) {
					return "", false
				}
				current = x[i]
			default:
				return "", false
			}
		}
		return scalar(current)
	}
	match := func(path, expected string) bool { value, ok := get(path); return ok && value == expected }
	accepted := false
	switch p.Provider {
	case "aliyun":
		accepted = match("Code", "OK")
	case "tencent":
		accepted = match("Response.SendStatusSet.0.Code", "Ok") && result["Response"] != nil
		if v, ok := result["Response"].(map[string]any); ok && v["Error"] != nil {
			accepted = false
		}
	case "huawei":
		accepted = match("code", "000000") && match("result.0.status", "000000")
	case "yunpian":
		accepted = match("code", "0")
	case "twilio":
		sid, _ := get("sid")
		status, _ := get("status")
		accepted = sid != "" && slices.Contains([]string{"accepted", "queued", "sending", "sent", "delivered", "scheduled"}, status) && result["error_code"] == nil
	case "custom":
		accepted = match(p.SuccessPath, p.SuccessValue)
	}
	if !accepted {
		return errors.New("短信服务未接受发送，请检查签名、模板、余额和服务商日志")
	}
	return nil
}
