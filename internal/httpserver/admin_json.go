package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/packagefs"
)

type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

func (s *Server) adminAPIMethodNotAllowed(allow string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if _, _, ok := s.requireAdminJSONRead(writer, request); !ok {
			return
		}
		writer.Header().Set("Allow", allow)
		s.writeAPIError(writer, request, http.StatusMethodNotAllowed, "request.method_not_allowed", "此资源不支持当前 HTTP 方法", map[string]any{"allow": allow})
	}
}

func (s *Server) adminContentAPIMethodNotAllowed(allow string) http.HandlerFunc {
	methodNotAllowed := s.adminAPIMethodNotAllowed(allow)
	return func(writer http.ResponseWriter, request *http.Request) {
		if !s.requireAdminContentAPIEnabled(writer, request) {
			return
		}
		methodNotAllowed(writer, request)
	}
}

func (s *Server) requireAdminJSONRead(writer http.ResponseWriter, request *http.Request) (auth.Principal, bool, bool) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Add("Vary", "Cookie")
	if !s.auth.Configured() {
		if s.isLoopbackRequest(request) {
			return auth.Principal{}, true, true
		}
		s.writeAPIError(writer, request, http.StatusNotFound, "request.not_found", "资源不存在", nil)
		return auth.Principal{}, false, false
	}
	if principal, ok := s.currentAdmin(request); ok {
		if !principal.Can(auth.CapabilityAdminAccess) {
			s.writeAPIError(writer, request, http.StatusForbidden, "auth.capability_required", "当前账号不能访问管理 API", nil)
			return auth.Principal{}, false, false
		}
		return principal, false, true
	}
	writer.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
	s.writeAPIError(writer, request, http.StatusUnauthorized, "auth.authentication_required", "需要有效的管理员会话", nil)
	return auth.Principal{}, false, false
}

func (s *Server) requireAdminJSONReadCapability(writer http.ResponseWriter, request *http.Request, capabilities ...string) (auth.Principal, bool, bool) {
	principal, readOnly, ok := s.requireAdminJSONRead(writer, request)
	if !ok || readOnly || principalCanAny(principal, capabilities...) {
		return principal, readOnly, ok
	}
	s.writeAPIError(writer, request, http.StatusForbidden, "auth.capability_required", "当前账号没有访问此资源的权限", map[string]any{"required": capabilities})
	return auth.Principal{}, false, false
}

func (s *Server) requireAdminJSONWrite(writer http.ResponseWriter, request *http.Request, capabilities ...string) (auth.Principal, bool) {
	principal, readOnly, ok := s.requireAdminJSONRead(writer, request)
	if !ok {
		return auth.Principal{}, false
	}
	if readOnly || !s.auth.Configured() {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.write_disabled", "管理员凭据未配置，当前仅允许本机只读访问", nil)
		return auth.Principal{}, false
	}
	if len(capabilities) > 0 && !principalCanAny(principal, capabilities...) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.capability_required", "当前账号没有执行此操作的权限", map[string]any{"required": capabilities})
		return auth.Principal{}, false
	}
	if !s.sameOriginRequest(request) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.cross_site_request_rejected", "跨站写请求已拒绝", nil)
		return auth.Principal{}, false
	}
	tokens := request.Header.Values("X-CSRF-Token")
	if len(tokens) != 1 || !s.auth.CheckCSRF(principal, tokens[0]) {
		s.writeAPIError(writer, request, http.StatusForbidden, "auth.csrf_validation_failed", "CSRF 校验失败", nil)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) strictAPIQuery(writer http.ResponseWriter, request *http.Request, allowed map[string]bool) (url.Values, bool) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "查询字符串格式不正确", nil)
		return nil, false
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "查询参数未知或重复", map[string]any{"field": key})
			return nil, false
		}
	}
	return values, true
}

func (s *Server) decodeStrictAPIJSON(writer http.ResponseWriter, request *http.Request, target any) ([]byte, bool) {
	return s.decodeStrictAPIJSONLimit(writer, request, target, adminContentAPIRequestMaxBytes, "4 MiB")
}

func (s *Server) decodeStrictAPIJSONLimit(writer http.ResponseWriter, request *http.Request, target any, maxBytes int64, maxLabel string) ([]byte, bool) {
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		s.writeAPIError(writer, request, http.StatusUnsupportedMediaType, "request.json_content_type_required", "请求必须使用 application/json", nil)
		return nil, false
	}
	if charset, ok := parameters["charset"]; ok && !strings.EqualFold(charset, "utf-8") {
		s.writeAPIError(writer, request, http.StatusUnsupportedMediaType, "request.unsupported_charset", "JSON 请求必须使用 UTF-8", nil)
		return nil, false
	}
	if maxBytes <= 0 {
		maxBytes = adminContentAPIRequestMaxBytes
		maxLabel = "4 MiB"
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeAPIError(writer, request, http.StatusRequestEntityTooLarge, "request.body_too_large", "JSON 请求体不能超过 "+maxLabel, nil)
			return nil, false
		}
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_body", "无法读取请求体", nil)
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.empty_body", "JSON 请求体不能为空", nil)
		return nil, false
	}
	if err := validateUniqueJSONObject(body, target); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 对象格式不正确", map[string]any{"reason": boundedAPIErrorReason(err)})
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 字段未知或类型不正确", map[string]any{"reason": boundedAPIErrorReason(err)})
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_json", "JSON 请求只能包含一个对象", nil)
		return nil, false
	}
	return body, true
}

func validateUniqueJSONObject(body []byte, target any) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("top-level JSON value must be an object")
	}
	// Validate against the destination before custom fields can decode, while
	// keeping the original bytes for request fingerprints and replay checks.
	return packagefs.ValidateUniqueJSONFor(body, target)
}

func (s *Server) requireEmptyAPIBody(writer http.ResponseWriter, request *http.Request) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 1)
	body, err := io.ReadAll(request.Body)
	if err != nil || len(body) != 0 {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.body_not_allowed", "此操作不接受请求体", nil)
		return false
	}
	return true
}

func (s *Server) writeAPIError(writer http.ResponseWriter, request *http.Request, status int, code, message string, details any) {
	writeJSON(writer, status, apiErrorEnvelope{Error: apiErrorBody{
		Code: code, Message: message, RequestID: request.Header.Get("X-Request-ID"), Details: details,
	}})
}

func boundedAPIErrorReason(err error) string {
	if err == nil {
		return ""
	}
	runes := []rune(err.Error())
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return string(runes)
}
