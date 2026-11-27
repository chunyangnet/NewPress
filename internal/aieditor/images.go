package aieditor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
	"newpress.local/newpress/internal/safehttp"
)

const (
	ProtocolOpenAIImages       = "openai_images"
	ProtocolGemini             = "gemini_images"
	ProtocolGeminiInteractions = "gemini_interactions"
	ProtocolSeedream           = "seedream"
	ImageGenerationTimeout     = 8 * time.Minute
	MaxImageBytes              = 20 << 20
	maxImageResponseBytes      = 28 << 20
)

var geminiModelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type ImageGeneration struct {
	Prompt      string `json:"prompt"`
	AspectRatio string `json:"aspect_ratio"`
	Quality     string `json:"quality,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

type GeneratedImage struct {
	Data      []byte
	Extension string
}

// Image requests often wait for the entire image before returning headers.
// Downloads use a separate, credential-free public HTTPS client.
type ImageClient struct {
	HTTP     *http.Client
	Download *http.Client
}

func NewImageClient() *ImageClient {
	return &ImageClient{HTTP: safehttp.Client(ImageGenerationTimeout), Download: safehttp.Client(time.Minute)}
}

func (g ImageGeneration) Validate(protocol string) (ImageGeneration, error) {
	g.Prompt = strings.TrimSpace(g.Prompt)
	if g.Prompt == "" || utf8.RuneCountInString(g.Prompt) > 4000 || strings.ContainsRune(g.Prompt, 0) {
		return g, failure("invalid_image_prompt", "请填写 1–4000 字的画面描述", http.StatusBadRequest)
	}
	if !isImageProtocol(protocol) {
		return g, failure("wrong_protocol", "请选择生图接口", http.StatusBadRequest)
	}
	if g.AspectRatio == "" {
		g.AspectRatio = "1:1"
	}
	ratios := []string{"1:1", "3:2", "2:3", "16:9", "9:16"}
	if protocol == ProtocolOpenAIImages {
		ratios = ratios[:3]
	}
	if !slices.Contains(ratios, g.AspectRatio) {
		return g, failure("invalid_image_ratio", "当前接口不支持这个画幅比例，请重新选择", http.StatusBadRequest)
	}
	if !slices.Contains([]string{"", "auto", "low", "medium", "high"}, g.Quality) || (protocol != ProtocolOpenAIImages && g.Quality != "" && g.Quality != "auto") {
		return g, failure("invalid_image_quality", "当前接口不支持这个画质选项", http.StatusBadRequest)
	}
	resolutions := []string{""}
	if protocol == ProtocolGemini || protocol == ProtocolGeminiInteractions {
		resolutions = append(resolutions, "1K", "2K", "4K")
	}
	if protocol == ProtocolSeedream {
		resolutions = append(resolutions, "2K", "4K")
	}
	if !slices.Contains(resolutions, g.Resolution) {
		return g, failure("invalid_image_size", "当前接口不支持这个分辨率选项", http.StatusBadRequest)
	}
	return g, nil
}

func (g ImageGeneration) request(c Connection) (string, any) {
	model := strings.TrimPrefix(c.Model, "models/")
	switch c.Protocol {
	case ProtocolGemini:
		config := map[string]any{"aspectRatio": g.AspectRatio}
		if g.Resolution != "" {
			config["imageSize"] = g.Resolution
		}
		return "/models/" + model + ":generateContent", map[string]any{
			"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": g.Prompt}}}},
			"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}, "imageConfig": config},
		}
	case ProtocolGeminiInteractions:
		format := map[string]any{"type": "image", "aspect_ratio": g.AspectRatio}
		if g.Resolution != "" {
			format["image_size"] = g.Resolution
		}
		return "/interactions", map[string]any{"model": model, "input": g.Prompt, "store": false, "response_format": []any{format}}
	case ProtocolSeedream:
		sizes := map[string][2]int{"1:1": {2048, 2048}, "3:2": {2496, 1664}, "2:3": {1664, 2496}, "16:9": {2560, 1440}, "9:16": {1440, 2560}}
		size := sizes[g.AspectRatio]
		if g.Resolution == "4K" {
			size[0] *= 2
			size[1] *= 2
		}
		return "/images/generations", map[string]any{"model": c.Model, "prompt": g.Prompt, "size": imageSize(size[0], size[1]), "response_format": "b64_json", "watermark": true, "sequential_image_generation": "disabled", "stream": false}
	default:
		sizes := map[string]string{"1:1": "1024x1024", "3:2": "1536x1024", "2:3": "1024x1536"}
		payload := map[string]any{"model": c.Model, "prompt": g.Prompt, "n": 1, "size": sizes[g.AspectRatio], "output_format": "png"}
		if g.Quality != "" {
			payload["quality"] = g.Quality
		}
		// GPT Image returns b64_json by default; response_format is not supported.
		return "/images/generations", payload
	}
}

func imageSize(width, height int) string {
	return strconv.Itoa(width) + "x" + strconv.Itoa(height)
}

func (c *ImageClient) do(ctx context.Context, connection Connection, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, connection.BaseURL+path, reader)
	if err != nil {
		return nil, failure("invalid_endpoint", "API 地址无效", http.StatusBadRequest)
	}
	if connection.APIKey != "" {
		if connection.Protocol == ProtocolGemini || connection.Protocol == ProtocolGeminiInteractions {
			r.Header.Set("x-goog-api-key", connection.APIKey)
		} else {
			r.Header.Set("Authorization", "Bearer "+connection.APIKey)
		}
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	client, err := connection.requestClient(c.HTTP, ImageGenerationTimeout, ImageGenerationTimeout)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, connectionFailure("image_connection_failed", "生图连接失败，请检查地址与网络；本次不会自动重试", err, "upstream_connect", connection.Protocol)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		if response.StatusCode == 400 || response.StatusCode == 413 || response.StatusCode == 422 {
			return nil, responseFailure(failure("image_rejected", "服务商拒绝了生图请求，请检查模型、画幅和分辨率，或调整画面描述", http.StatusBadRequest), response, connection.APIKey)
		}
		return nil, responseFailure(upstreamError(response.StatusCode), response, connection.APIKey)
	}
	return response, nil
}

type imagePayload struct {
	Usage         json.RawMessage `json:"usage"`
	UsageMetadata json.RawMessage `json:"usageMetadata"`
	Data          []struct {
		B64 string `json:"b64_json"`
		URL string `json:"url"`
	} `json:"data"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Thought    bool `json:"thought"`
				InlineData struct {
					Data string `json:"data"`
					MIME string `json:"mimeType"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Outputs []struct {
		Type string `json:"type"`
		Data string `json:"data"`
		URI  string `json:"uri"`
		MIME string `json:"mime_type"`
	} `json:"outputs"`
}

func (c *ImageClient) Generate(ctx context.Context, connection Connection, generation ImageGeneration) ([]GeneratedImage, error) {
	return c.GenerateObserved(ctx, connection, generation, nil)
}

func (c *ImageClient) GenerateObserved(ctx context.Context, connection Connection, generation ImageGeneration, observe func(Usage, int)) ([]GeneratedImage, error) {
	connection, err := connection.Validate(true)
	if err != nil {
		return nil, err
	}
	generation, err = generation.Validate(connection.Protocol)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ImageGenerationTimeout)
	defer cancel()
	path, body := generation.request(connection)
	response, err := c.do(ctx, connection, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("image_interrupted", "图片传输中断，请稍后重试", http.StatusBadGateway)
	}
	if len(data) > maxImageResponseBytes {
		return nil, failure("image_too_large", "服务商返回内容过大，请降低分辨率", http.StatusBadGateway)
	}
	var payload imagePayload
	if json.Unmarshal(data, &payload) != nil {
		return nil, failure("invalid_image_response", "生图接口未返回有效的 JSON，请核对协议与地址", http.StatusBadGateway)
	}
	usage := usageTracker{}
	switch connection.Protocol {
	case ProtocolGemini:
		usage.update(payload.UsageMetadata, ProtocolGeminiText)
	case ProtocolOpenAIImages:
		usage.update(payload.Usage, ProtocolResponses)
	case ProtocolGeminiInteractions:
		usage.update(interactionImageUsage(payload.Usage), ProtocolResponses)
	default:
		usage.update(payload.Usage, "")
	}
	if observe != nil {
		observe(usage.value, 0)
	}
	type source struct{ data, url string }
	var sources []source
	switch connection.Protocol {
	case ProtocolGemini:
		for _, candidate := range payload.Candidates {
			for _, part := range candidate.Content.Parts {
				if !part.Thought && part.InlineData.Data != "" && strings.HasPrefix(part.InlineData.MIME, "image/") {
					sources = append(sources, source{data: part.InlineData.Data})
				}
			}
		}
	case ProtocolGeminiInteractions:
		for _, output := range payload.Outputs {
			if output.Type == "image" {
				sources = append(sources, source{data: output.Data, url: output.URI})
			}
		}
	default:
		for _, item := range payload.Data {
			if item.B64 != "" || item.URL != "" {
				sources = append(sources, source{item.B64, item.URL})
			}
		}
	}
	if len(sources) == 0 {
		return nil, failure("no_image", "服务商没有返回图片，可能触发内容限制或模型不支持生图；请调整描述或更换模型", http.StatusBadGateway)
	}
	if len(sources) > 4 {
		return nil, failure("too_many_images", "服务商返回图片过多，请选择单图生成模型", http.StatusBadGateway)
	}
	if observe != nil {
		observe(usage.value, len(sources))
	}
	var result []GeneratedImage
	total := 0
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var raw []byte
		if src.data != "" {
			if base64.StdEncoding.DecodedLen(len(src.data)) > MaxImageBytes+2 {
				return nil, failure("image_too_large", "图片超过 20 MiB，请降低分辨率", http.StatusBadGateway)
			}
			raw, err = base64.StdEncoding.DecodeString(src.data)
			if err != nil {
				return nil, failure("invalid_image", "图片编码无效，请检查接口适配", http.StatusBadGateway)
			}
		} else {
			raw, err = c.download(ctx, src.url)
			if err != nil {
				return nil, err
			}
		}
		total += len(raw)
		if total > MaxImageBytes {
			return nil, failure("image_too_large", "图片总大小超过 20 MiB，请降低分辨率", http.StatusBadGateway)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || !slices.Contains([]string{"png", "jpeg", "webp"}, format) || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 40_000_000 {
			return nil, failure("invalid_image", "图片格式或尺寸无效，仅接受不超过 8192 像素边长的 PNG、JPEG、WebP", http.StatusBadGateway)
		}
		if format == "jpeg" {
			format = "jpg"
		}
		result = append(result, GeneratedImage{Data: raw, Extension: format})
	}
	return result, nil
}

func (c *ImageClient) download(ctx context.Context, address string) ([]byte, error) {
	if len(address) > 8192 {
		return nil, failure("invalid_image_url", "图片下载地址无效", http.StatusBadGateway)
	}
	u, err := safehttp.ValidateURL(address)
	if err != nil {
		return nil, failure("invalid_image_url", "生图接口必须返回公网 HTTPS 图片或 Base64 图片", http.StatusBadGateway)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, failure("invalid_image_url", "图片下载地址无效", http.StatusBadGateway)
	}
	r.Header.Set("Accept", "image/png, image/jpeg, image/webp")
	client := c.Download
	if client == nil {
		client = safehttp.Client(time.Minute)
	}
	response, err := client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("image_download_failed", "生成完成，但图片下载失败，请检查服务商的下载地址", http.StatusBadGateway)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, failure("image_download_failed", "生成图片无法下载或地址已过期，请优先使用返回 Base64 的接口", http.StatusBadGateway)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxImageBytes+1))
	if err != nil {
		return nil, failure("image_download_failed", "图片下载中断", http.StatusBadGateway)
	}
	return data, nil
}

func (c *ImageClient) Models(ctx context.Context, connection Connection) ([]Model, error) {
	if connection.Protocol == ProtocolSeedream {
		return nil, failure("manual_image_models", "Seedream 请填写已开通的模型 ID 或推理接入点 ID，可在火山方舟控制台获取", http.StatusBadRequest)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	path := "/models"
	google := connection.Protocol == ProtocolGemini || connection.Protocol == ProtocolGeminiInteractions
	if google {
		path += "?pageSize=1000"
	}
	response, err := c.do(ctx, connection, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, failure("invalid_models", "模型列表过大或传输中断，可手动填写模型 ID", http.StatusBadGateway)
	}
	var payload struct {
		Data []struct {
			Model
			OutputModalities []string `json:"output_modalities"`
		} `json:"data"`
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return nil, failure("invalid_models", "无法读取模型列表，可手动填写模型 ID", http.StatusBadGateway)
	}
	models, seen := []Model{}, map[string]bool{}
	add := func(model Model) {
		if validValue(model.ID, 200) && !seen[model.ID] && len(models) < 1000 {
			seen[model.ID] = true
			model.Name = truncate(model.Name, 100)
			models = append(models, model)
		}
	}
	if google {
		for _, model := range payload.Models {
			id := strings.TrimPrefix(model.Name, "models/")
			if strings.Contains(id, "image") && !strings.HasPrefix(id, "imagen-") {
				add(Model{ID: id, Name: model.DisplayName})
			}
		}
	} else {
		for _, model := range payload.Data {
			if len(model.OutputModalities) == 0 || slices.Contains(model.OutputModalities, "image") {
				add(model.Model)
			}
		}
	}
	if len(models) == 0 {
		return nil, failure("no_image_models", "没有发现生图模型，请检查权限或手动填写服务商提供的模型 ID", http.StatusBadGateway)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// Interactions names total input/output counters separately from generateContent.
func interactionImageUsage(raw json.RawMessage) json.RawMessage {
	fields := usageFields(raw)
	converted := map[string]any{}
	for source, target := range map[string]string{"total_input_tokens": "input_tokens", "total_output_tokens": "output_tokens", "total_tokens": "total_tokens"} {
		if n := fields.count(source); n != nil {
			converted[target] = *n
		}
	}
	if n := fields.count("total_cached_tokens"); n != nil {
		converted["input_tokens_details"] = map[string]int{"cached_tokens": *n}
	}
	value, _ := json.Marshal(converted)
	return value
}
