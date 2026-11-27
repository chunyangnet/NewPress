package contenthtml

import (
	"strings"
	"testing"
)

func TestResponsiveImagesSanitizeAndPreserveSourceMeaning(t *testing.T) {
	view := ResponsiveImage{Src: "/media/1/a.png?variant=large", SrcSet: "/media/1/a.png?variant=small 640w, /media/1/a.png?variant=large 1920w", Width: 1920, Height: 1080}
	result := WithResponsiveImages(`<p>说明 &amp; 示例</p><img src="/media/1/a.png" alt="一句 &quot;引用&quot;" onerror="alert(1)"><script>alert(1)</script><img src="https://example.test/external.png"><code>&lt;img src="/media/1/a.png"&gt;</code>`, map[string]ResponsiveImage{"/media/1/a.png": view})
	if strings.Contains(result, "onerror") || strings.Contains(result, "<script") || strings.Count(result, "srcset=") != 1 || !strings.Contains(result, "alt=") || !strings.Contains(result, "https://example.test/external.png") || !strings.Contains(result, "&lt;img") {
		t.Fatal(result)
	}
	view.Src = "javascript:alert(1)"
	if result := WithResponsiveImages(`<img src="/media/1/a.png">`, map[string]ResponsiveImage{"/media/1/a.png": view}); strings.Contains(result, "javascript") || strings.Contains(result, "srcset=") {
		t.Fatal(result)
	}
}
