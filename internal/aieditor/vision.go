package aieditor

import (
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
)

type VisionImage struct {
	Data []byte
	MIME string
}

func attachVision(protocol string, payload map[string]any, messages []Message, image VisionImage) error {
	if len(image.Data) == 0 || len(image.Data) > 4<<20 || !slices.Contains([]string{"image/jpeg", "image/png", "image/webp"}, image.MIME) || http.DetectContentType(image.Data) != image.MIME || len(messages) == 0 || messages[len(messages)-1].Role != "user" {
		return errors.New("无效的识图输入")
	}
	encoded := base64.StdEncoding.EncodeToString(image.Data)
	text := messages[len(messages)-1].Content
	if protocol == ProtocolGeminiText {
		rows := payload["contents"].([]map[string]any)
		rows[len(rows)-1]["parts"] = []any{map[string]string{"text": text}, map[string]any{"inlineData": map[string]string{"mimeType": image.MIME, "data": encoded}}}
		return nil
	}
	rows := []map[string]any{}
	for _, m := range messages {
		if protocol == ProtocolAnthropic && m.Role == "system" {
			continue
		}
		rows = append(rows, map[string]any{"role": m.Role, "content": m.Content})
	}
	key := "messages"
	kind := "text"
	var part any
	uri := "data:" + image.MIME + ";base64," + encoded
	switch protocol {
	case ProtocolAnthropic:
		part = map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": image.MIME, "data": encoded}}
	case ProtocolResponses, ProtocolAzureResponses:
		key = "input"
		kind = "input_text"
		part = map[string]string{"type": "input_image", "image_url": uri}
	default:
		part = map[string]any{"type": "image_url", "image_url": map[string]string{"url": uri}}
	}
	rows[len(rows)-1]["content"] = []any{map[string]string{"type": kind, "text": strings.TrimSpace(text)}, part}
	payload[key] = rows
	return nil
}
