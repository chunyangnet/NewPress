package accessconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"newpress.local/newpress/internal/safehttp"
)

type SMS struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	AccessKey      string `json:"access_key"`
	Secret         string `json:"secret,omitempty"`
	HasSecret      bool   `json:"has_secret,omitempty"`
	ClearSecret    bool   `json:"clear_secret,omitempty"`
	Region         string `json:"region"`
	AppID          string `json:"app_id"`
	SignName       string `json:"sign_name"`
	TemplateID     string `json:"template_id"`
	TemplateParams string `json:"template_params"`
	Sender         string `json:"sender"`
	Message        string `json:"message"`
	Endpoint       string `json:"endpoint"`
	Encoding       string `json:"encoding"`
	Auth           string `json:"auth"`
	AuthHeader     string `json:"auth_header"`
	Payload        string `json:"payload"`
	SuccessPath    string `json:"success_path"`
	SuccessValue   string `json:"success_value"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type SMSProvider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Docs        string `json:"docs"`
}

func SMSCatalog() []SMSProvider {
	return []SMSProvider{
		{"aliyun", "阿里云短信", "国内验证码模板；AccessKey 与 ACS3 签名。", "https://help.aliyun.com/zh/sms/developer-reference/api-dysmsapi-2017-05-25-sendsms"},
		{"tencent", "腾讯云短信", "国内及国际号码；SecretId、应用 SDKAppID 与模板。", "https://cloud.tencent.com/document/api/382/55981"},
		{"huawei", "华为云短信", "应用接入地址、AppKey / AppSecret 与签名通道号。", "https://support.huaweicloud.com/api-msgsms/sms_05_0001.html"},
		{"yunpian", "云片", "国内单条短信，使用 API Key 与已审核的短信内容。", "https://www.yunpian.com/doc/zh_CN/domestic/single_send.html"},
		{"twilio", "Twilio", "国际短信；Account SID、Auth Token 与发送号码或 Messaging Service SID。", "https://www.twilio.com/docs/messaging/api/message-resource"},
		{"custom", "自定义 HTTP 网关", "POST JSON / 表单，支持 Bearer、Basic 或自定义认证头与业务成功条件。", ""},
	}
}

func DefaultSMS() SMS {
	return SMS{Provider: "aliyun", Region: "ap-guangzhou", TemplateParams: `{"code":"{code}"}`, Message: "【你的签名】验证码：{code}，{minutes}分钟内有效。", Encoding: "json", Auth: "bearer", AuthHeader: "X-API-Key", Payload: `{"phone":"{phone}","code":"{code}","purpose":"{purpose}"}`, TimeoutSeconds: 8}
}

var placeholderPattern = regexp.MustCompile(`\{([a-z_]+)\}`)
var jsonPathPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)
var headerPattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
var twilioAccountPattern = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)

func validateTemplate(raw string) error {
	if len(raw) > 16384 {
		return errors.New("请求模板不能超过 16 KiB")
	}
	for _, m := range placeholderPattern.FindAllStringSubmatch(raw, -1) {
		if !slices.Contains([]string{"code", "phone", "phone_national", "purpose", "minutes", "secret"}, m[1]) {
			return fmt.Errorf("不支持的模板变量：%s", m[0])
		}
	}
	return nil
}

func hasTemplateValue(value any, variable string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, variable)
	case []any:
		for _, item := range v {
			if hasTemplateValue(item, variable) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if hasTemplateValue(item, variable) {
				return true
			}
		}
	}
	return false
}

func normalizeSMS(in, old SMS) (SMS, error) {
	in.AccessKey, in.Secret = strings.TrimSpace(in.AccessKey), strings.TrimSpace(in.Secret)
	in.Endpoint, in.Sender = strings.TrimSpace(in.Endpoint), strings.TrimSpace(in.Sender)
	in.SignName, in.TemplateID, in.AppID, in.Region = strings.TrimSpace(in.SignName), strings.TrimSpace(in.TemplateID), strings.TrimSpace(in.AppID), strings.TrimSpace(in.Region)
	in.SuccessPath, in.AuthHeader = strings.TrimSpace(in.SuccessPath), strings.TrimSpace(in.AuthHeader)
	if in.ClearSecret && in.Secret != "" {
		return SMS{}, errors.New("清除短信密钥与填写新密钥不能同时选择")
	}
	if in.Secret == "" && !in.ClearSecret && in.Provider == old.Provider && in.AccessKey == old.AccessKey && in.Endpoint == old.Endpoint && in.Auth == old.Auth && in.AuthHeader == old.AuthHeader {
		in.Secret = old.Secret
	}
	in.HasSecret, in.ClearSecret = false, false
	if !slices.ContainsFunc(SMSCatalog(), func(p SMSProvider) bool { return p.ID == in.Provider }) {
		return SMS{}, errors.New("请选择支持的短信服务")
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 8
	}
	if in.TimeoutSeconds < 3 || in.TimeoutSeconds > 20 {
		return SMS{}, errors.New("短信超时应为 3–20 秒")
	}
	for _, v := range []string{in.AccessKey, in.Secret, in.AppID, in.TemplateID, in.Region} {
		if !validCredential(v, 4096) {
			return SMS{}, errors.New("短信凭据或标识不能含空白且不能超过 4096 字节")
		}
	}
	if len(in.SignName) > 200 || len(in.Sender) > 200 || strings.ContainsAny(in.SignName+in.Sender, "\r\n") {
		return SMS{}, errors.New("短信签名或发送方格式不正确")
	}
	if len(in.Endpoint) > 2048 || len(in.SuccessPath) > 256 || len(in.SuccessValue) > 512 {
		return SMS{}, errors.New("短信地址或响应条件过长")
	}
	if in.Endpoint != "" {
		if _, err := safehttp.ValidateURL(in.Endpoint); err != nil {
			return SMS{}, err
		}
	}
	for _, raw := range []string{in.TemplateParams, in.Message, in.Payload} {
		if err := validateTemplate(raw); err != nil {
			return SMS{}, err
		}
	}
	if in.Provider != "custom" && strings.Contains(in.TemplateParams+in.Message, "{secret}") {
		return SMS{}, errors.New("密钥变量仅可用于自定义网关请求参数，不能写入短信正文或模板")
	}
	if !in.Enabled {
		return in, nil
	}
	if in.Secret == "" && !(in.Provider == "custom" && in.Auth == "none") {
		return SMS{}, errors.New("启用短信前请填写密钥；更换服务、认证方式、账号或地址后须重新填写")
	}
	if slices.Contains([]string{"aliyun", "tencent", "huawei", "twilio"}, in.Provider) && in.AccessKey == "" {
		return SMS{}, errors.New("请填写短信服务的账号标识 / AccessKey / SecretId")
	}
	switch in.Provider {
	case "aliyun", "tencent", "huawei":
		if in.TemplateID == "" || (in.Provider != "huawei" && in.SignName == "") {
			return SMS{}, errors.New("请填写已审核的短信签名及模板 ID")
		}
		if in.Provider == "tencent" && (in.AppID == "" || in.Region == "") {
			return SMS{}, errors.New("腾讯云短信需要 SDKAppID 和地域")
		}
		if in.Provider == "huawei" && (in.Endpoint == "" || in.Sender == "") {
			return SMS{}, errors.New("华为云短信需要完整发送接口地址和签名通道号")
		}
		if !strings.Contains(in.TemplateParams, "{code}") {
			return SMS{}, errors.New("模板参数中必须包含 {code}")
		}
		if in.Provider == "aliyun" {
			var params map[string]string
			if json.Unmarshal([]byte(in.TemplateParams), &params) != nil || len(params) == 0 {
				return SMS{}, errors.New("阿里云模板参数须为 JSON 字符串对象，例如 {\"code\":\"{code}\"}")
			}
			found := false
			for _, value := range params {
				found = found || strings.Contains(value, "{code}")
			}
			if !found {
				return SMS{}, errors.New("阿里云模板参数值中必须包含 {code}")
			}
		} else {
			var params []string
			if json.Unmarshal([]byte(in.TemplateParams), &params) != nil || len(params) == 0 || len(params) > 20 {
				return SMS{}, errors.New("模板参数须为 JSON 字符串数组，例如 [\"{code}\",\"{minutes}\"]")
			}
		}
	case "twilio", "yunpian":
		if in.Message == "" || !strings.Contains(in.Message, "{code}") {
			return SMS{}, errors.New("短信内容必须包含 {code}")
		}
		if in.Provider == "twilio" && (!twilioAccountPattern.MatchString(in.AccessKey) || in.Sender == "") {
			return SMS{}, errors.New("Twilio 需要有效的 Account SID（AC 开头）及 From 或 Messaging Service SID")
		}
	case "custom":
		if in.Endpoint == "" || !slices.Contains([]string{"json", "form"}, in.Encoding) || !slices.Contains([]string{"none", "bearer", "basic", "header"}, in.Auth) {
			return SMS{}, errors.New("请选择网关地址、请求格式和认证方式")
		}
		if in.Auth == "basic" && (in.AccessKey == "" || strings.Contains(in.AccessKey, ":")) {
			return SMS{}, errors.New("Basic 认证需要不含冒号的用户名")
		}
		if in.Auth == "header" && (!headerPattern.MatchString(in.AuthHeader) || slices.Contains([]string{"Host", "Content-Length", "Transfer-Encoding", "Content-Type", "Connection", "Cookie", "Proxy-Authorization"}, http.CanonicalHeaderKey(in.AuthHeader))) {
			return SMS{}, errors.New("自定义认证头名称无效或属于保留字段")
		}
		var payload map[string]any
		if json.Unmarshal([]byte(in.Payload), &payload) != nil || len(payload) == 0 || !hasTemplateValue(payload, "{code}") || !(hasTemplateValue(payload, "{phone}") || hasTemplateValue(payload, "{phone_national}")) {
			return SMS{}, errors.New("请求参数须为 JSON 对象，并包含 {code} 和 {phone}（或 {phone_national}）")
		}
		if in.Encoding == "form" {
			for _, v := range payload {
				switch v.(type) {
				case string, float64, bool:
				default:
					return SMS{}, errors.New("表单参数只接受字符串、数字或布尔值；嵌套内容请写为 JSON 字符串")
				}
			}
		}
		if in.SuccessPath != "" && !jsonPathPattern.MatchString(in.SuccessPath) {
			return SMS{}, errors.New("业务成功字段路径无效，例如 code、data.success 或 result.0.code")
		}
	}
	return in, nil
}

func SMSReady(p *SMS) bool {
	if p == nil || !p.Enabled {
		return false
	}
	_, err := normalizeSMS(*p, SMS{})
	return err == nil
}
