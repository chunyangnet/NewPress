package contenthtml

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestResourcePreviewCannotCarryEncodedAuthoredHTML(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"preview":true,"post":1,"id":"bundle","variants":[{"body":"<img src=x onerror=alert(1)>"}]}`))
	source := `<aside class="npr-card" data-npr-resource="bundle" data-npr-post="1" data-npr-preview="snapshot" data-npr-preview-state="` + payload + `" onclick="alert(1)"><script>alert(1)</script><p>预览占位</p></aside>`
	got := Sanitize(source)
	for _, forbidden := range []string{"data-npr-preview-state", payload, "onclick", "<script"} {
		if strings.Contains(got, forbidden) {
			t.Fatal("authored preview bypassed sanitization", forbidden, got)
		}
	}
	if !strings.Contains(got, `data-npr-preview="snapshot"`) || !strings.Contains(got, "预览占位") || Sanitize(got) != got {
		t.Fatal("inert preview shell did not survive rendering", got)
	}
}
