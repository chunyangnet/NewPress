package contenthtml

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	policy := bluemonday.UGCPolicy()
	// Alt text is plain text; the sanitizer escapes it when serializing HTML.
	policy.AllowAttrs("alt").OnElements("img")
	// Only the editor's semantic hint variants are allowed. This does not
	// enable arbitrary classes, inline styles or executable HTML attributes.
	policy.AllowElements("aside")
	policy.AllowAttrs("data-np-snippet", "data-np-snippet-version").Matching(regexp.MustCompile(`^[1-9][0-9]{0,18}$`)).OnElements("aside")
	policy.AllowAttrs("data-callout").Matching(regexp.MustCompile(`^(note|tip|warning)$`)).OnElements("aside")
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[a-zA-Z0-9_+.#-]{1,40}$`)).OnElements("code")
	// Shells contain public metadata only. Private preview snapshots are added
	// by the HTTP renderer after sanitization, never accepted from article HTML.
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^npr-[a-z-]+$`)).OnElements("aside", "div", "span", "h3", "p", "a", "img", "small", "strong", "li")
	policy.AllowAttrs("data-npr-resource").Matching(regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)).OnElements("aside")
	policy.AllowAttrs("data-npr-post").Matching(regexp.MustCompile(`^[1-9][0-9]{0,18}$`)).OnElements("aside")
	policy.AllowAttrs("data-npr-preview").Matching(regexp.MustCompile(`^(snapshot|appearance)$`)).OnElements("aside")
	// Article widgets keep a small semantic contract. Their public metadata is
	// resolved by the server; authors cannot supply arbitrary CSS or attributes.
	policy.AllowAttrs("data-np-widget").Matching(regexp.MustCompile(`^(post|callout|quote|code|download|gift|redpacket|gallery|image-card|audio|video|table|accordion|tabs|card|badges|progress|counter|poll|rating|compare|carousel|annotation|glossary|ai|mention|chart)$`)).OnElements("aside")
	policy.AllowAttrs("data-np-config").Matching(regexp.MustCompile(`^[A-Za-z0-9_-]+$`)).OnElements("aside")
	policy.AllowElements("details", "summary", "progress", "button", "input", "mark")
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^npw(?:-[a-z-]+)?$`)).OnElements("aside", "div", "span", "h3", "p", "button", "pre", "a", "img", "details", "small", "figure", "nav", "label", "strong")
	policy.AllowAttrs("data-npw-post").Matching(regexp.MustCompile(`^(?:[0-9]{1,19}|shop:[a-z0-9][a-z0-9_-]{0,63})$`)).OnElements("aside")
	policy.AllowAttrs("data-npw-index", "data-npw-columns").Matching(regexp.MustCompile(`^[0-9]{1,19}$`)).OnElements("aside", "div", "figure", "details", "button")
	policy.AllowAttrs("data-npw-skin", "data-npw-tone", "data-npw-style", "data-npw-width", "data-npw-density", "data-npw-layout", "data-npw-platform").Matching(regexp.MustCompile(`^[a-z-]{0,20}$`)).OnElements("aside", "div", "span")
	policy.AllowAttrs("data-npw-action").Matching(regexp.MustCompile(`^(copy|password|claim|packet|lightbox|previous|next|play|vote)$`)).OnElements("button", "a")
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^button$`)).OnElements("button")
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^range$`)).OnElements("input")
	policy.AllowAttrs("min", "max", "value").Matching(regexp.MustCompile(`^[0-9.]{1,20}$`)).OnElements("input", "progress")
	policy.AllowAttrs("open").OnElements("details")
	policy.AllowAttrs("role").Matching(regexp.MustCompile(`^(status|region|img)$`)).OnElements("p", "div")
	policy.AllowAttrs("aria-live").Matching(regexp.MustCompile(`^polite$`)).OnElements("p")
	policy.AllowAttrs("tabindex").Matching(regexp.MustCompile(`^0$`)).OnElements("div")
	policy.AllowAttrs("aria-label").OnElements("input", "button", "progress", "div", "nav")
	policy.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
	policy.AllowAttrs("data-np-post").Matching(regexp.MustCompile(`^[1-9][0-9]{0,18}$`)).OnElements("aside")
	policy.AllowAttrs("data-np-layout").Matching(regexp.MustCompile(`^(card|compact)$`)).OnElements("aside")
	policy.AllowAttrs("data-np-part").Matching(regexp.MustCompile(`^link$`)).OnElements("a")
	policy.AllowAttrs("data-np-part").Matching(regexp.MustCompile(`^(cover|body|eyebrow|excerpt|meta|arrow|date|likes)$`)).OnElements("span")
	policy.AllowAttrs("data-np-part").Matching(regexp.MustCompile(`^avatar$`)).OnElements("img")
	policy.AllowAttrs("data-np-part").Matching(regexp.MustCompile(`^title$`)).OnElements("strong")
	policy.AllowAttrs("aria-hidden").Matching(regexp.MustCompile(`^true$`)).OnElements("span")
	policy.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
	return policy
}

// Sanitize applies the same content policy during storage and theme rendering.
func Sanitize(source string) string {
	return NormalizeSnippetShells(policy.Sanitize(NormalizeSnippetShells(source)))
}
