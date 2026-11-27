package articlewidgets

import (
	"encoding/base64"
	"strings"
	"testing"

	"newpress.local/newpress/internal/contenthtml"
)

func example(kind string) Config {
	c := Config{Version: 1, ID: "component_test_123", Kind: kind, Title: "标题 <script>", Text: "说明 <img src=x onerror=alert(1)>", Mode: "both", Max: 100, Count: 10, Columns: 3, Items: []Item{{Title: "一", Value: 10, Image: "/media/a.webp"}, {Title: "二", Value: 20, Image: "/media/b.webp"}, {Title: "三", Value: 30, Image: "/media/c.webp"}}}
	switch kind {
	case "audio", "video":
		c.Provider = "local"
		c.URL = "/media/test.mp4"
	case "mention":
		c.Provider = "github"
		c.UID = "octocat"
	case "rating":
		c.Max = 5
	case "redpacket":
		c.Amount = "1.5"
		c.Currency = "main"
		c.Mode = "login"
	case "chart":
		c.Layout = "radar"
	case "compare":
		c.Items = c.Items[:2]
	case "table":
		c.Rows = [][]string{{"项目", "数值"}, {"甲", "1"}}
	}
	return c
}
func TestAllComponentsSurviveSanitizerAndEscape(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			c := example(kind)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			encoded := c.Encode()
			decoded, err := Decode(encoded)
			if err != nil || decoded.Fingerprint() != c.Fingerprint() {
				t.Fatal("round trip", err)
			}
			out := Render(c, 12, "chunnel")
			safe := contenthtml.Sanitize(out)
			if strings.Contains(safe, "<script>") || strings.Contains(safe, " onerror=") && !strings.Contains(safe, "&lt;img") {
				t.Fatal("unsafe HTML")
			}
			if !strings.Contains(safe, `class="npw"`) || !strings.Contains(safe, `data-np-config="`+encoded+`"`) {
				t.Fatal("sanitizer broke component", safe)
			}
			if kind == "poll" && !strings.Contains(safe, `data-npw-action="vote"`) {
				t.Fatal("vote control lost")
			}
			if kind == "compare" && !strings.Contains(safe, `type="range"`) {
				t.Fatal("slider lost")
			}
			if kind == "accordion" && !strings.Contains(safe, "<summary>") {
				t.Fatal("native fallback lost")
			}
		})
	}
}
func TestComponentVisualStructureSurvivesSanitizer(t *testing.T) {
	c := example("quote")
	c.Caption = "作者 <img src=x onerror=alert(1)>"
	safe := contenthtml.Sanitize(Render(c, 12, "chunnel"))
	if !strings.Contains(safe, `class="npw-quote-credit"`) || strings.Contains(safe, `<img`) {
		t.Fatal("quote attribution lost its layout or escaping", safe)
	}
	c = example("table")
	c.Rows = [][]string{{"状态"}, {"✓ 已完成 <script>"}, {"◷ 进行中"}, {"● 待开始"}}
	safe = contenthtml.Sanitize(Render(c, 12, "chunnel"))
	for _, tone := range []string{"green", "amber", "gray"} {
		if !strings.Contains(safe, `class="npw-table-status" data-npw-tone="`+tone+`"`) {
			t.Fatal("status styling stripped", safe)
		}
	}
	if strings.Contains(safe, "<script>") {
		t.Fatal("status text was not escaped")
	}
	for _, kind := range []string{"audio", "video"} {
		safe = contenthtml.Sanitize(Render(example(kind), 12, "chunnel"))
		for _, cls := range []string{"npw-media-stage", "npw-media-play", "npw-media-info", "npw-play-icon"} {
			if !strings.Contains(safe, `class="`+cls+`"`) {
				t.Fatal("media structure stripped", kind, cls)
			}
		}
	}
}

func TestComponentURLsAndConfigBounds(t *testing.T) {
	for _, u := range []string{"//evil.test/x", "/\\evil.test/x", "javascript:alert(1)", "data:text/html,hi", "https://u:p@evil.test/a", "/path\n/x", "/%2F/evil.test"} {
		if LocalURL(u) {
			t.Errorf("unsafe local %q", u)
		}
	}
	for _, u := range []string{"/media/a.png", "/media/a.png?v=1", "https://example.test/download"} {
		if !LinkURL(u) {
			t.Errorf("valid link %q", u)
		}
	}
	c := example("video")
	c.Provider = "youtube"
	c.UID = "https://evil.test"
	if c.Validate() == nil {
		t.Fatal("unvalidated frame")
	}
	c = example("mention")
	c.UID = "../../secrets"
	if c.Validate() == nil {
		t.Fatal("unvalidated profile")
	}
	c = example("chart")
	c.Items[0].Value = 101
	if c.Validate() == nil {
		t.Fatal("invalid chart")
	}
	if _, err := Decode(base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":"component_test_123","kind":"card","script":"hi"}`))); err == nil {
		t.Fatal("unknown field")
	}
	if _, err := Decode(strings.Repeat("a", 90000)); err == nil {
		t.Fatal("unbounded config")
	}
}
func TestComponentExpansionOpaqueAndDuplicateIDs(t *testing.T) {
	c := example("poll")
	raw := `<aside data-np-widget="poll" data-np-config="` + c.Encode() + `"><aside><p>private snapshot</p></aside></aside>`
	if got := References(raw); len(got) != 1 || !got[0].Valid {
		t.Fatal(got)
	}
	out := Expand("<p>before</p>"+raw+raw+"<p>after</p>", 12, "chunnel")
	if !strings.HasPrefix(out, "<p>before</p>") || !strings.HasSuffix(out, "<p>after</p>") || strings.Contains(out, "private snapshot") || strings.Count(out, `data-np-widget="poll"`) != 1 {
		t.Fatal(out)
	}
	if _, ok := Find(raw+raw, c.ID); ok {
		t.Fatal("ambiguous interaction")
	}
	if _, ok := Find(raw, c.ID); !ok {
		t.Fatal("missing interaction")
	}
}

func TestComponentLegacyCodePreservesRichPreformattedContent(t *testing.T) {
	for _, source := range []string{
		`<pre>keep me<code>print(1)</code></pre>`,
		`<pre><code>print(1)</code><code>print(2)</code></pre>`,
		`<pre><code><b>highlighted</b></code></pre>`,
		`<pre><code>unfinished`,
	} {
		if got := EnhanceCode(source, 12); got != source {
			t.Fatalf("changed rich source: %s", got)
		}
	}
	source := `<p>Before</p><pre><code class="language-python">print(&quot;hello&quot;)</code></pre><p>After</p>`
	out := EnhanceCode(source, 12)
	refs := References(out)
	if len(refs) != 1 || !refs[0].Valid || refs[0].Config.Text != `print("hello")` || refs[0].Config.Language != "python" || !strings.HasPrefix(out, "<p>Before</p>") || !strings.HasSuffix(out, "<p>After</p>") {
		t.Fatal(out)
	}
}

func TestCodeLanguageSurvivesSanitizationAndEnhancement(t *testing.T) {
	for _, language := range []string{"C#", "c++", "Objective-C", "fsharp", "tsx", "vb.net", "plaintext", "unknown_language"} {
		source := `<pre><code class="language-` + language + `">safe &lt;script&gt;</code></pre>`
		out := EnhanceCode(contenthtml.Sanitize(source), 12)
		refs := References(out)
		if len(refs) != 1 || refs[0].Config.Language != language || refs[0].Config.Text != "safe <script>" || !strings.Contains(out, `class="npw-code-language"`) {
			t.Fatalf("language %q lost during rendering: %s", language, out)
		}
		if EnhanceCode(out, 12) != out {
			t.Fatalf("code block enhanced twice: %s", out)
		}
	}
	out := EnhanceCode(`<pre><code>print(1)</code></pre>`, 12)
	if refs := References(out); len(refs) != 1 || refs[0].Config.Language != "auto" {
		t.Fatal("unlabeled code should request automatic detection", out)
	}
	c := example("code")
	c.Language, c.Title = "python", "example.py"
	out = Render(c, 12, "chunnel")
	if !strings.Contains(out, `aria-label="代码语言">python</span>`) || !strings.Contains(out, `title="example.py">example.py</span>`) {
		t.Fatal("language and filename must remain independently visible", out)
	}
}

func TestComponentPacketSenderAndConfigurationRemainSeparate(t *testing.T) {
	c := example("redpacket")
	c.Sender = "不可冒充发放人"
	c.Image = "/media/2/other.png"
	before := c.Encode()
	out := RenderWithSender(c, 42, "chunnel", "真实作者", "/media/1/author.png")
	if !strings.Contains(out, `class="npw-packet-sender-name">真实作者`) || !strings.Contains(out, `src="/media/1/author.png"`) || !strings.Contains(out, `data-np-config="`+before+`"`) || !strings.Contains(contenthtml.Sanitize(out), `data-npw-action="packet"`) {
		t.Fatal(out)
	}
	c.Kind = "gift"
	out = RenderWithSender(c, 42, "chunnel", "真实作者", "/media/1/author.png")
	if !strings.Contains(out, `class="npw-packet-sender-name">不可冒充发放人`) || !strings.Contains(out, `src="/media/2/other.png"`) {
		t.Fatal(out)
	}
	c = example("redpacket")
	for _, amount := range []string{"0", "0.00", "000.000"} {
		c.Amount = amount
		if c.Validate() == nil {
			t.Fatal("zero-value packet accepted", amount)
		}
	}
}
