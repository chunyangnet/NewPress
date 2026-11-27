package accessconfig

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func fixtureSMS(provider string) SMS {
	p := DefaultSMS()
	p.Enabled = true
	p.Provider = provider
	p.AccessKey = "fixture-key"
	p.Secret = "fixture-secret"
	p.SignName = "测试签名"
	p.TemplateID = "template-123"
	p.AppID = "1400006666"
	p.Sender = "csms100000001"
	if provider == "tencent" || provider == "huawei" {
		p.TemplateParams = `["{code}","{minutes}"]`
	}
	if provider == "huawei" || provider == "custom" {
		p.Endpoint = "https://sms.example.com/send"
	}
	if provider == "twilio" {
		p.AccessKey = "AC" + strings.Repeat("0", 32)
		p.Sender = "+12025550123"
	}
	return p
}

func TestSMSProviderProtocolsAndBusinessFailures(t *testing.T) {
	good := map[string]string{"aliyun": `{"Code":"OK"}`, "tencent": `{"Response":{"SendStatusSet":[{"Code":"Ok"}]}}`, "huawei": `{"code":"000000","result":[{"status":"000000"}]}`, "yunpian": `{"code":0}`, "twilio": `{"sid":"SM123","status":"queued","error_code":null}`, "custom": `{"data":{"accepted":true}}`}
	for _, info := range SMSCatalog() {
		t.Run(info.ID, func(t *testing.T) {
			p := fixtureSMS(info.ID)
			if info.ID == "custom" {
				p.SuccessPath = "data.accepted"
				p.SuccessValue = "true"
			}
			calls := 0
			client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Scheme != "https" || strings.Contains(r.URL.RawQuery, p.Secret) {
					t.Fatal("unsafe request")
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing request timeout")
				}
				r.Body = io.NopCloser(strings.NewReader(string(body)))
				_ = r.ParseForm()
				switch p.Provider {
				case "aliyun":
					if r.Form.Get("PhoneNumbers") != "13800138000" || r.Form.Get("TemplateParam") != `{"code":"012345"}` || !strings.HasPrefix(r.Header.Get("Authorization"), "ACS3-HMAC-SHA256 Credential=fixture-key,") {
						t.Fatal("invalid aliyun request", string(body))
					}
				case "tencent":
					var payload map[string]any
					_ = json.Unmarshal(body, &payload)
					if payload["SmsSdkAppId"] != "1400006666" || r.Header.Get("X-TC-Version") != "2021-01-11" || !strings.Contains(r.Header.Get("Authorization"), "/sms/tc3_request") {
						t.Fatal("invalid tencent request")
					}
				case "huawei":
					if r.Form.Get("templateParas") != `["012345","10"]` || r.Form.Get("from") != "csms100000001" || !strings.Contains(r.Header.Get("X-WSSE"), "PasswordDigest=") {
						t.Fatal("invalid huawei request")
					}
				case "yunpian":
					if r.Form.Get("apikey") != p.Secret || r.Form.Get("mobile") != "13800138000" || !strings.Contains(r.Form.Get("text"), "012345") {
						t.Fatal("invalid yunpian request")
					}
				case "twilio":
					id, secret, ok := r.BasicAuth()
					if !ok || id != p.AccessKey || secret != p.Secret || r.Form.Get("To") != "+8613800138000" || r.Form.Get("From") != p.Sender {
						t.Fatal("invalid twilio request")
					}
				case "custom":
					if r.Header.Get("Authorization") != "Bearer "+p.Secret || !strings.Contains(string(body), `"code":"012345"`) {
						t.Fatal("invalid gateway request")
					}
				}
				if calls == 1 {
					return response(200, good[p.Provider]), nil
				}
				return response(200, `{"code":500,"message":"fixture-secret 012345 +8613800138000"}`), nil
			})}
			if err := SendSMS(t.Context(), p, "+8613800138000", "012345", "register", client); err != nil {
				t.Fatal(err)
			}
			err := SendSMS(t.Context(), p, "+8613800138000", "012345", "register", client)
			if err == nil || strings.Contains(err.Error(), p.Secret) || strings.Contains(err.Error(), "012345") || calls != 2 {
				t.Fatal("failure accepted, retried or leaked", err, calls)
			}
		})
	}
}

func TestSMSCustomJSONEscapingFormAuthAndSuccessPresence(t *testing.T) {
	for _, format := range []string{"json", "form"} {
		for _, auth := range []string{"none", "bearer", "basic", "header"} {
			t.Run(format+auth, func(t *testing.T) {
				p := fixtureSMS("custom")
				p.Secret = `quote"slash\{code}`
				p.Auth = auth
				p.Encoding = format
				p.SuccessPath = "result.0.code"
				p.SuccessValue = ""
				p.Payload = `{"recipient":"{phone}","otp":"{code}","key":"{secret}"}`
				client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
					if format == "json" {
						var data map[string]string
						if json.NewDecoder(r.Body).Decode(&data) != nil || data["key"] != p.Secret || data["otp"] != "123456" {
							t.Fatal("JSON escaping or recursive expansion")
						}
					} else {
						r.ParseForm()
						if r.PostForm.Get("key") != p.Secret {
							t.Fatal("form escaping")
						}
					}
					if auth == "header" && r.Header.Get("X-API-Key") != p.Secret {
						t.Fatal("missing API key")
					}
					if auth == "basic" {
						u, s, ok := r.BasicAuth()
						if !ok || u != p.AccessKey || s != p.Secret {
							t.Fatal("missing basic auth")
						}
					}
					return response(200, `{"result":[{"code":""}]}`), nil
				})}
				if err := SendSMS(t.Context(), p, "+8613800138000", "123456", "test", client); err != nil {
					t.Fatal(err)
				}
				client.Transport = testTransport(func(*http.Request) (*http.Response, error) { return response(200, `{"result":[{}]}`), nil })
				if SendSMS(t.Context(), p, "+8613800138000", "123456", "test", client) == nil {
					t.Fatal("missing success field matched empty string")
				}
			})
		}
	}
}

func TestSMSTransportRejectsRedirectsAndOversize(t *testing.T) {
	p := fixtureSMS("custom")
	for _, status := range []int{301, 302, 307, 400, 500} {
		calls := 0
		c := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls++
			r := response(status, "")
			r.Header.Set("Location", "https://elsewhere.example.com")
			return r, nil
		})}
		if SendSMS(t.Context(), p, "+8613800138000", "123456", "test", c) == nil || calls != 1 {
			t.Fatal("redirect or failed HTTP accepted", status, calls)
		}
	}
	c := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) { return response(200, strings.Repeat("x", 65537)), nil })}
	if SendSMS(t.Context(), p, "+8613800138000", "123456", "test", c) == nil {
		t.Fatal("oversize response accepted")
	}
	hits := 0
	local := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer local.Close()
	p.Endpoint = local.URL
	if SendSMS(t.Context(), p, "+8613800138000", "123456", "test", nil) == nil || hits != 0 {
		t.Fatal("SSRF accepted")
	}
}

func TestHuaweiPublishedDigestFormat(t *testing.T) {
	p := fixtureSMS("huawei")
	r, err := smsRequest(t.Context(), p, "+8613800138000", "123456", "test", time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), "fixed-nonce")
	if err != nil {
		t.Fatal(err)
	}
	// Huawei's WSSE digest base64-encodes the lowercase SHA-256 hexadecimal text.
	if !strings.Contains(r.Header.Get("X-WSSE"), `PasswordDigest="NWNhY2RjMzhhZjZhNWI1MDk2ZmRhZThjMjJlYzljODM2ZTI4ODU3Yzc1ODU1ZTFjNTUxNTBjYjdkNjlmNmVkNw=="`) {
		t.Fatal("incorrect Huawei WSSE digest")
	}
}

func TestCloudSMSSignaturesMatchIndependentVectors(t *testing.T) {
	for id, expected := range map[string]string{
		"aliyun":  "48f889de64b61902947a92a1f1021673285224d309568cba8c205d78dfad22a5",
		"tencent": "43d4d2143b94fa82f8e59574f4ce79647014c95547cde036449258f92686d211",
	} {
		r, err := smsRequest(t.Context(), fixtureSMS(id), "+8613800138000", "123456", "test", time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), "fixed-nonce")
		if err != nil || !strings.HasSuffix(r.Header.Get("Authorization"), expected) {
			t.Fatal("signature mismatch", id, err)
		}
	}
}
