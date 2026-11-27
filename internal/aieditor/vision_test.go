package aieditor

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestVisionUsesProtocolSpecificImageParts(t *testing.T) {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	messages := []Message{{Role: "system", Content: "only describe"}, {Role: "user", Content: "source"}}
	for _, tc := range []struct{ protocol, marker string }{{ProtocolAnthropic, "media_type"}, {ProtocolGeminiText, "inlineData"}, {ProtocolResponses, "input_image"}, {ProtocolAzureLegacy, "image_url"}} {
		_, body := textRequest(Connection{Protocol: tc.protocol, Model: "test"}, messages, 512)
		if err := attachVision(tc.protocol, body, messages, VisionImage{MIME: "image/png", Data: buf.Bytes()}); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), tc.marker) || !strings.Contains(string(raw), "iVBORw0KGgo") || !strings.Contains(string(raw), "source") {
			t.Fatal(tc.protocol, string(raw))
		}
	}
}
