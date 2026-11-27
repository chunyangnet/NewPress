package aieditor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
)

// Diagnostic contains only bounded metadata and application-owned text. Never
// put raw URLs, headers, upstream messages, prompts or wrapped errors here.
type Diagnostic struct {
	Stage             string `json:"stage,omitempty"`
	Protocol          string `json:"protocol,omitempty"`
	UpstreamStatus    int    `json:"upstream_status,omitempty"`
	ResponseType      string `json:"response_type,omitempty"`
	UpstreamRequestID string `json:"upstream_request_id,omitempty"`
	ProviderCode      string `json:"provider_code,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Hint              string `json:"hint,omitempty"`
}

func networkReason(err error) (string, string) {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "请求超时。检查服务端到接口的网络，或稍后重试。"
	case errors.Is(err, context.Canceled):
		return "cancelled", "请求已取消。"
	case errors.As(err, &unknown), errors.As(err, &hostname), errors.As(err, &certificate):
		return "tls_certificate", "HTTPS 证书校验失败。检查证书有效期、域名和证书链。"
	case errors.As(err, &dns):
		return "dns", "服务器无法解析接口域名。检查域名与服务器 DNS。"
	case errors.As(err, &network) && network.Timeout():
		return "timeout", "连接或等待响应超时。检查接口是否可达及服务商处理时间。"
	}
	// Match known internal transport errors without returning their text or URL.
	for e := err; e != nil; e = errors.Unwrap(e) {
		switch e.Error() {
		case "remote DNS unavailable":
			return "dns", "服务器无法解析接口域名。检查域名与服务器 DNS。"
		case "nonpublic remote address rejected", "reserved remote address rejected", "request outside approved endpoint", "unapproved remote address":
			return "address_policy", "地址未通过网络访问检查。HTTP／本地接口须在设置中授权；保留地址仍不可访问。"
		case "remote connection unavailable":
			return "connect", "服务器无法连接接口。检查服务是否启动、端口、防火墙及服务器出口网络。"
		}
	}
	return "network", "检查服务器到接口的网络、端口和 TLS 配置。"
}

// WithDiagnostic adds context without exposing the underlying error string.
// Existing, more specific stages (e.g. image download) take precedence.
func WithDiagnostic(err error, stage, protocol string) error {
	if err == nil {
		return nil
	}
	var known *Error
	if errors.As(err, &known) {
		copy := *known
		if copy.Details.Stage == "" {
			copy.Details.Stage = stage
		}
		if copy.Details.Protocol == "" {
			copy.Details.Protocol = protocol
		}
		return &copy
	}
	reason, hint := networkReason(err)
	code, message, status := "connection_failed", "AI 请求未能完成，请查看错误详细信息", http.StatusBadGateway
	if reason == "timeout" {
		code, message, status = "timeout", "AI 请求超时，请稍后重试", http.StatusGatewayTimeout
	}
	return &Error{Code: code, Message: message, Status: status, Details: Diagnostic{Stage: stage, Protocol: protocol, Reason: reason, Hint: hint}, cause: err}
}

func connectionFailure(code, message string, err error, stage, protocol string) error {
	reason, hint := networkReason(err)
	return &Error{Code: code, Message: message, Status: http.StatusBadGateway, Details: Diagnostic{Stage: stage, Protocol: protocol, Reason: reason, Hint: hint}, cause: err}
}

func responseDiagnostic(response *http.Response, key string) Diagnostic {
	d := Diagnostic{Stage: "upstream_response", UpstreamStatus: response.StatusCode}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json", "text/event-stream", "text/html", "text/plain":
		d.ResponseType = mediaType
	case "":
		d.ResponseType = "missing"
	default:
		d.ResponseType = "other"
	}
	for _, name := range []string{"x-request-id", "request-id", "apim-request-id", "x-ms-request-id"} {
		id := response.Header.Get(name)
		if len(id) < 8 || len(id) > 128 || key != "" && strings.Contains(id, key) {
			continue
		}
		valid := true
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
				valid = false
				break
			}
		}
		if valid {
			d.UpstreamRequestID = id
			break
		}
	}
	switch {
	case response.StatusCode >= 300 && response.StatusCode < 400:
		d.Reason, d.Hint = "redirect_blocked", "接口返回了重定向。请填写最终 API 地址；系统不会跟随跳转。"
	case response.StatusCode == 401:
		d.Hint = "检查 Key 是否有效、是否过期，以及所选协议的鉴权方式。"
	case response.StatusCode == 403:
		d.Hint = "检查 Key 的模型权限、账户状态和服务商访问限制。"
	case response.StatusCode == 404 || response.StatusCode == 405:
		d.Hint = "核对 API 根地址、协议和模型或部署名称。"
	case response.StatusCode == 429:
		d.Hint = "检查余额、配额和请求频率；稍后重试。"
	case response.StatusCode >= 500:
		d.Hint = "服务商或中转服务返回服务器错误，可在其控制台凭请求 ID 排查。"
	default:
		d.Hint = "核对协议、模型及参数是否符合服务商要求。"
	}
	return d
}

// Read only a small error envelope and expose recognized codes with fixed
// explanations. Providers can echo keys and entire prompts in error.message.
func responseFailure(err error, response *http.Response, key string) error {
	var known *Error
	if !errors.As(err, &known) {
		return err
	}
	copy := *known
	copy.Details = responseDiagnostic(response, key)
	data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	var envelope struct {
		Error struct {
			Code   string `json:"code"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		for _, code := range []string{envelope.Error.Code, envelope.Error.Type, envelope.Error.Status} {
			if hint := providerHints[code]; hint != "" && (key == "" || !strings.Contains(code, key)) {
				copy.Details.ProviderCode, copy.Details.Hint = code, hint
				break
			}
		}
	}
	return &copy
}

var providerHints = map[string]string{
	"insufficient_quota":       "服务商报告额度不足。检查余额、计费状态和项目配额。",
	"rate_limit_exceeded":      "服务商报告频率限制。降低并发或稍后重试。",
	"rate_limit_error":         "服务商报告频率限制。降低并发或稍后重试。",
	"invalid_api_key":          "服务商报告 Key 无效。重新填写有效 Key 后重试。",
	"authentication_error":     "服务商报告鉴权失败。检查 Key 与接口协议。",
	"permission_error":         "服务商报告权限不足。检查账户和模型权限。",
	"model_not_found":          "服务商找不到该模型。核对模型 ID 或 Azure 部署名称。",
	"DeploymentNotFound":       "Azure 找不到该部署。核对资源地址和部署名称。",
	"context_length_exceeded":  "输入超出模型上下文长度。缩短参考正文或提示词。",
	"content_policy_violation": "服务商拒绝了这次内容请求。调整主题或画面描述。",
	"content_filter":           "服务商的内容过滤器阻止了本次请求。",
	"unsupported_parameter":    "接口不支持提交的参数。核对协议、Token 上限或生图选项。",
	"invalid_request_error":    "服务商认为请求参数无效。核对协议、模型及生成选项。",
	"overloaded_error":         "服务商当前负载过高，请稍后重试。",
	"RESOURCE_EXHAUSTED":       "服务商资源或配额耗尽。检查配额与请求频率。",
	"INVALID_ARGUMENT":         "服务商认为参数无效。检查协议、模型及生成选项。",
	"PERMISSION_DENIED":        "服务商拒绝访问。检查 Key、模型权限和账户状态。",
	"UNAUTHENTICATED":          "服务商未通过鉴权。检查 Key 与鉴权方式。",
	"NOT_FOUND":                "服务商未找到模型或接口。核对 API 地址和模型 ID。",
}
