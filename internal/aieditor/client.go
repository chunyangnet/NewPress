package aieditor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/safehttp"
)

const GenerationTimeout = 6 * time.Minute

type Connection struct {
	ProviderID  string `json:"provider_id,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	APIVersion  string `json:"api_version,omitempty"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model,omitempty"`
	StreamUsage *bool  `json:"stream_usage,omitempty"`
	// Only settings resolution or an authorized administrator probe can grant
	// this trust. It is intentionally unavailable to JSON clients.
	approvedEndpoint string
	requestAuthority *modelRequestAuthority
}

type modelRequestAuthority struct{ check func() error }

type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type Result struct {
	Text         string
	FinishReason string
	Usage        Usage
}

// Thinking is kept separate from Result.Text so it can never enter a document.
// Only explicit, displayable provider fields are forwarded; encrypted payloads
// and signatures are not decoded or exposed.
const MaxThinkingBytes = 256 << 10

type ThinkingDelta struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type StreamCallbacks struct {
	Delta        func(string) error
	Thinking     func(ThinkingDelta) error
	ThinkingDone func() error
	Usage        func(Usage) error
}

// Error messages deliberately never interpolate upstream responses, request
// URLs, or credentials. Providers sometimes echo the Authorization header.
type Error struct {
	Code    string
	Message string
	Status  int
	Details Diagnostic
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

func failure(code, message string, status int) error {
	return &Error{Code: code, Message: message, Status: status}
}

func NormalizeBaseURL(raw string) (string, error) {
	return normalizeEndpoint(raw, "")
}

func normalizeEndpoint(raw, protocol string) (string, error) {
	return normalizeEndpointPolicy(raw, protocol, false)
}

func normalizeEndpointPolicy(raw, protocol string, approved bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2048 {
		return "", failure("invalid_endpoint", "API 地址过长", http.StatusBadRequest)
	}
	u, err := safehttp.ValidateURL(raw)
	if approved {
		u, err = safehttp.ValidateEndpoint(raw)
	}
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" {
		if approved {
			return "", failure("invalid_endpoint", "请输入有效的 HTTP 或 HTTPS 地址，不含账号、查询参数或保留地址", http.StatusBadRequest)
		}
		return "", failure("invalid_endpoint", "默认仅支持公网 HTTPS（443 端口）；HTTP、本地或其他端口须由管理员在接口设置中明确允许", http.StatusBadRequest)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/images/generations", "/interactions", "/models"} {
		u.Path = strings.TrimSuffix(u.Path, suffix)
	}
	if u.Path == "" {
		u.Path = "/v1"
		if protocol == ProtocolGemini || protocol == ProtocolGeminiInteractions || protocol == ProtocolGeminiText {
			u.Path = "/v1beta"
		} else if protocol == ProtocolSeedream {
			u.Path = "/api/v3"
		} else if protocol == ProtocolAzure || protocol == ProtocolAzureResponses {
			u.Path = "/openai/v1"
		} else if protocol == ProtocolAzureLegacy {
			u.Path = "/openai"
		}
	}
	if (protocol == ProtocolAzure || protocol == ProtocolAzureResponses) && u.Path == "/openai" {
		u.Path += "/v1"
	}
	if protocol == ProtocolAzureLegacy && strings.Contains(u.Path, "/deployments/") {
		return "", failure("invalid_endpoint", "Azure 部署式接口请填写资源根地址，并在模型中填写部署名称", http.StatusBadRequest)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func validValue(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func (c Connection) Validate(model bool) (Connection, error) {
	if !validProtocol(c.Protocol) {
		return Connection{}, failure("invalid_protocol", "请选择受支持的接口协议", http.StatusBadRequest)
	}
	base, err := normalizeEndpointPolicy(c.BaseURL, c.Protocol, c.approvedEndpoint != "")
	if err != nil {
		return Connection{}, err
	}
	if c.approvedEndpoint != "" && base != c.approvedEndpoint {
		return Connection{}, failure("invalid_endpoint", "API 地址已变化，请重新授权此地址", http.StatusBadRequest)
	}
	c.BaseURL, c.APIKey, c.Model = base, strings.TrimSpace(c.APIKey), strings.TrimSpace(c.Model)
	c.APIVersion, err = normalizeAPIVersion(c.APIVersion, c.Protocol)
	if err != nil {
		return Connection{}, err
	}
	if !(c.APIKey == "" && c.approvedEndpoint != "") && !validValue(c.APIKey, 4096) {
		return Connection{}, failure("invalid_key", "请填写有效的 API Key，不包含空格或换行", http.StatusBadRequest)
	}
	if model && !validValue(c.Model, 200) {
		return Connection{}, failure("invalid_model", "请选择探测到的模型，或填写服务商提供的模型 ID", http.StatusBadRequest)
	}
	if model {
		if err := validateModel(c.Protocol, c.Model); err != nil {
			return Connection{}, err
		}
	}
	return c, nil
}

// ApproveProbeConnection must only be called after checking settings:manage.
// Saved connections receive their trust from SettingsStore instead.
func ApproveProbeConnection(c Connection) (Connection, error) {
	if c.ProviderID != "" {
		return Connection{}, failure("ambiguous_connection", "已保存接口请使用保存的地址授权设置", http.StatusBadRequest)
	}
	base, err := normalizeEndpointPolicy(c.BaseURL, c.Protocol, true)
	if err != nil {
		return Connection{}, err
	}
	c.approvedEndpoint = base
	return c.Validate(false)
}

func (c Connection) requestClient(fallback *http.Client, timeout, headerTimeout time.Duration) (*http.Client, error) {
	if c.requestAuthority != nil {
		if err := c.requestAuthority.check(); err != nil {
			return nil, err
		}
	}
	if c.approvedEndpoint == "" {
		return fallback, nil
	}
	if c.BaseURL != c.approvedEndpoint {
		return nil, failure("invalid_endpoint", "API 地址已变化，请重新授权此地址", http.StatusBadRequest)
	}
	client, err := safehttp.EndpointClient(c.approvedEndpoint, timeout, headerTimeout)
	if err != nil {
		return nil, failure("invalid_endpoint", "API 地址无效", http.StatusBadRequest)
	}
	return client, nil
}

type Client struct{ HTTP *http.Client }

func NewClient() *Client {
	return &Client{HTTP: safehttp.ClientWithHeaderTimeout(GenerationTimeout, 45*time.Second)}
}

func upstreamError(status int) error {
	switch status {
	case 401:
		return failure("invalid_key", "API Key 未通过验证，请检查后重新探测", http.StatusBadRequest)
	case 403:
		return failure("provider_forbidden", "此 Key 没有访问该接口或模型的权限", http.StatusBadRequest)
	case 404, 405:
		return failure("endpoint_not_found", "接口或模型不存在，请核对 API 地址；不支持模型列表的服务可手动填写模型 ID", http.StatusBadRequest)
	case 429:
		return failure("provider_rate_limit", "服务商限流或额度不足，请稍后重试并检查账户额度", http.StatusTooManyRequests)
	case 400, 413, 422:
		return failure("provider_rejected", "模型拒绝了请求，请检查模型 ID、参考正文长度和 Token 上限；上限可留空使用服务商默认值", http.StatusBadRequest)
	default:
		return failure("provider_unavailable", "服务商暂时不可用，请检查接口配置后重试", http.StatusBadGateway)
	}
}

func (c *Client) do(ctx context.Context, connection Connection, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, failure("invalid_request", "无法准备写作请求", http.StatusBadRequest)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, connection.BaseURL+path, reader)
	if err != nil {
		return nil, failure("invalid_endpoint", "API 地址无效", http.StatusBadRequest)
	}
	if connection.Protocol == ProtocolAnthropic {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	if connection.APIKey != "" {
		switch connection.Protocol {
		case ProtocolAnthropic:
			request.Header.Set("x-api-key", connection.APIKey)
		case ProtocolGeminiText:
			request.Header.Set("x-goog-api-key", connection.APIKey)
		case ProtocolAzure, ProtocolAzureResponses, ProtocolAzureLegacy:
			request.Header.Set("api-key", connection.APIKey)
		default:
			request.Header.Set("Authorization", "Bearer "+connection.APIKey)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	client, err := connection.requestClient(c.HTTP, GenerationTimeout, 45*time.Second)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, connectionFailure("connection_failed", "无法连接 API，请检查地址、端口、证书与服务端网络", err, "upstream_connect", connection.Protocol)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, responseFailure(upstreamError(response.StatusCode), response, connection.APIKey)
	}
	return response, nil
}

// ModelsChecked checks every model-list request, including native pagination
// and image protocols. The hook belongs to this connection copy; it never
// changes the shared client or a saved connection's endpoint policy.
func (c *Client) ModelsChecked(ctx context.Context, connection Connection, check func() error) ([]Model, error) {
	if check != nil {
		connection.requestAuthority = &modelRequestAuthority{check: check}
	}
	return c.Models(ctx, connection)
}

func (c *Client) Models(ctx context.Context, connection Connection) (modelsResult []Model, resultErr error) {
	defer func() { resultErr = WithDiagnostic(resultErr, "models", connection.Protocol) }()
	connection, err := connection.Validate(false)
	if err != nil {
		return nil, err
	}
	if isImageProtocol(connection.Protocol) {
		return (&ImageClient{HTTP: c.HTTP}).Models(ctx, connection)
	}
	if connection.Protocol == ProtocolAnthropic || connection.Protocol == ProtocolGeminiText {
		return c.nativeModels(ctx, connection)
	}
	if connection.Protocol == ProtocolAzureLegacy {
		return nil, failure("manual_models", "Azure 部署式接口请手动填写控制台中的部署名称", http.StatusBadRequest)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := c.do(ctx, connection, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, failure("invalid_models", "模型列表过大或传输中断，请重试或手动填写模型", http.StatusBadGateway)
	}
	var payload struct {
		Data []struct {
			Model
			OutputModalities []string `json:"output_modalities"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.Data == nil {
		return nil, failure("invalid_models", "接口未返回兼容的模型列表，可手动填写模型 ID", http.StatusBadGateway)
	}
	models, seen := []Model{}, map[string]bool{}
	for _, model := range payload.Data {
		textOutput := len(model.OutputModalities) == 0
		for _, kind := range model.OutputModalities {
			textOutput = textOutput || kind == "text"
		}
		if validValue(model.ID, 200) && !seen[model.ID] && textOutput {
			models = append(models, Model{ID: model.ID, Name: truncate(model.Name, 100), OwnedBy: truncate(model.OwnedBy, 100)})
			seen[model.ID] = true
		}
		if len(models) == 1000 {
			break
		}
	}
	if len(models) == 0 {
		return nil, failure("no_models", "该 Key 暂无可用文本模型，可核对权限或手动填写模型 ID", http.StatusBadGateway)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

type completionMessage struct {
	Content          json.RawMessage `json:"content"`
	Refusal          string          `json:"refusal"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	Thinking         json.RawMessage `json:"thinking"`
}

// Compatible services use different names for the same visible text. Pick one
// instead of concatenating aliases, and ignore unknown structured extensions.
func (m completionMessage) thinkingText() string {
	for _, raw := range []json.RawMessage{m.ReasoningContent, m.Reasoning, m.Thinking} {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			return text
		}
	}
	return ""
}

type completion struct {
	Choices []struct {
		Index        int               `json:"index"`
		Delta        completionMessage `json:"delta"`
		Message      completionMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", errors.New("unexpected content format")
	}
	for _, part := range parts {
		if part.Type == "text" {
			value += part.Text
		}
	}
	return value, nil
}

func (c *Client) Stream(ctx context.Context, connection Connection, messages []Message, maxTokens int, delta func(string) error) (streamResult Result, resultErr error) {
	return c.StreamWithCallbacks(ctx, connection, messages, maxTokens, StreamCallbacks{Delta: delta})
}

func (c *Client) StreamWithCallbacks(ctx context.Context, connection Connection, messages []Message, maxTokens int, callbacks StreamCallbacks) (streamResult Result, resultErr error) {
	return c.StreamIllustrated(ctx, connection, messages, maxTokens, callbacks, nil)
}

func (c *Client) StreamIllustrated(ctx context.Context, connection Connection, messages []Message, maxTokens int, callbacks StreamCallbacks, image *VisionImage) (streamResult Result, resultErr error) {
	defer func() { resultErr = WithDiagnostic(resultErr, "text_stream", connection.Protocol) }()
	var result Result
	connection, err := connection.Validate(true)
	if err != nil {
		return result, err
	}
	if !isTextProtocol(connection.Protocol) {
		return result, failure("wrong_protocol", "该接口用于生图，请选择写作接口", http.StatusBadRequest)
	}
	path, payload := textRequest(connection, messages, maxTokens)
	if image != nil {
		if err := attachVision(connection.Protocol, payload, messages, *image); err != nil {
			return result, err
		}
	}
	response, err := c.do(ctx, connection, http.MethodPost, path, payload)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	defer func() {
		var known *Error
		if errors.As(resultErr, &known) && known.Details.UpstreamStatus == 0 {
			copy := *known
			copy.Details = responseDiagnostic(response, connection.APIKey)
			copy.Details.Stage, copy.Details.Hint = "text_stream", "接口已响应，但正文或流式事件不符合预期。检查协议及中转服务适配。"
			resultErr = &copy
		}
	}()
	var output strings.Builder
	thinkingBytes, thinkingActive, thinkingTruncated := 0, false, false
	finishThinking := func() error {
		if !thinkingActive {
			return nil
		}
		thinkingActive = false
		if callbacks.ThinkingDone != nil {
			return callbacks.ThinkingDone()
		}
		return nil
	}
	emitThinking := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if text == "" || thinkingTruncated || callbacks.Thinking == nil {
			return nil
		}
		// A long thought must not consume the article's output budget. Retain a
		// bounded UTF-8 prefix and continue reading the rest of the response.
		remaining := MaxThinkingBytes - thinkingBytes
		if len(text) > remaining {
			for remaining > 0 && !utf8.RuneStart(text[remaining]) {
				remaining--
			}
			text, thinkingTruncated = text[:remaining], true
		}
		thinkingBytes += len(text)
		thinkingActive = true
		return callbacks.Thinking(ThinkingDelta{Text: text, Truncated: thinkingTruncated})
	}
	emit := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if output.Len()+len(text) > MaxOutputBytes {
			return failure("output_limit", "生成正文已达到 512 KiB 上限，已保留收到的内容", http.StatusBadGateway)
		}
		if text == "" {
			return nil
		}
		if err := finishThinking(); err != nil {
			return err
		}
		output.WriteString(text)
		if callbacks.Delta != nil {
			return callbacks.Delta(text)
		}
		return nil
	}
	usage := usageTracker{}
	updateUsage := func(raw json.RawMessage) error {
		if !usage.update(raw, connection.Protocol) {
			return nil
		}
		result.Usage = usage.value
		if callbacks.Usage != nil {
			return callbacks.Usage(result.Usage)
		}
		return nil
	}
	native := nativeTextStream{protocol: connection.Protocol, result: &result, emit: emit, emitThinking: emitThinking, finishThinking: finishThinking, updateUsage: updateUsage}
	consume := func(data []byte) error {
		if native.supported() {
			return native.consume(data)
		}
		var part completion
		if json.Unmarshal(data, &part) != nil {
			return failure("invalid_stream", "服务商返回了无法解析的数据，已保留收到的正文", http.StatusBadGateway)
		}
		if err := updateUsage(part.Usage); err != nil {
			return err
		}
		if len(part.Error) > 0 && string(part.Error) != "null" {
			return failure("provider_stream_error", "生成被服务商中断，已保留收到的正文，请检查额度或稍后重试", http.StatusBadGateway)
		}
		if result.FinishReason != "" {
			return nil // A trailing usage-only chunk is still consumed above.
		}
		for _, choice := range part.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Delta.Refusal != "" || choice.Message.Refusal != "" || choice.FinishReason == "content_filter" {
				return failure("provider_refusal", "模型未能完成这次写作，请调整需求后重试；已收到的内容已保留", http.StatusBadGateway)
			}
			thinking := choice.Delta.thinkingText()
			if thinking == "" {
				thinking = choice.Message.thinkingText()
			}
			if err := emitThinking(thinking); err != nil {
				return err
			}
			raw := choice.Delta.Content
			if len(raw) == 0 {
				raw = choice.Message.Content
			}
			text, err := contentText(raw)
			if err != nil {
				return failure("invalid_stream", "服务商正文格式不兼容，已保留收到的内容", http.StatusBadGateway)
			}
			if err := emit(text); err != nil {
				return err
			}
			if choice.FinishReason != "" {
				result.FinishReason = choice.FinishReason
			}
		}
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "application/json" {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if readErr != nil || len(data) > 2<<20 {
			return result, failure("invalid_stream", "生成响应过大或传输中断", http.StatusBadGateway)
		}
		err = consume(data)
		if err == nil && (result.FinishReason == "" || native.supported() && !native.done) {
			err = io.ErrUnexpectedEOF
		}
	} else if mediaType == "text/event-stream" {
		scanner := bufio.NewScanner(io.LimitReader(response.Body, (8<<20)+1))
		scanner.Buffer(make([]byte, 8192), 1<<20)
		var event strings.Builder
		done, total := false, 0
		dispatch := func() error {
			data := strings.TrimSpace(event.String())
			event.Reset()
			if data == "[DONE]" {
				done = !native.supported() || native.done
				return nil
			}
			if data == "" {
				return nil
			}
			err := consume([]byte(data))
			// Gemini gateways may send usageMetadata after finishReason. Read
			// through EOF while ignoring text after that terminal candidate.
			done = native.done && native.protocol != ProtocolGeminiText
			return err
		}
		for scanner.Scan() {
			line := scanner.Text()
			total += len(line) + 1
			if total > 8<<20 || event.Len()+len(line) > 1<<20 {
				err = failure("output_limit", "服务商的流式响应超过限制，已保留收到的内容", http.StatusBadGateway)
				break
			}
			if line == "" {
				err = dispatch()
				if err != nil || done {
					break
				}
			} else if strings.HasPrefix(line, "data:") {
				event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
				event.WriteByte('\n')
			}
		}
		if err == nil && !done && event.Len() > 0 {
			err = dispatch()
		}
		if err == nil {
			err = scanner.Err()
		}
		if err == nil && !done && (result.FinishReason == "" || native.supported() && !native.done) {
			err = io.ErrUnexpectedEOF
		}
	} else {
		err = failure("invalid_stream", "API 未返回兼容的流式或 JSON 响应，请核对接口地址", http.StatusBadGateway)
	}
	if err == nil {
		err = finishThinking()
	}
	result.Text = output.String()
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var known *Error
		if errors.As(err, &known) {
			return result, err
		}
		return result, failure("stream_interrupted", "连接在生成完成前中断，已保留收到的正文，可编辑后填入或重新生成", http.StatusBadGateway)
	}
	if strings.TrimSpace(result.Text) == "" {
		return result, failure("empty_response", "模型没有返回正文，请调整需求或更换文本模型", http.StatusBadGateway)
	}
	return result, nil
}
