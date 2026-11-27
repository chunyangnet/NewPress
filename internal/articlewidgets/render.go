package articlewidgets

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

func escape(value string) string    { return html.EscapeString(value) }
func paragraph(value string) string { return strings.ReplaceAll(escape(value), "\n", "<br>") }
func number(value float64) string   { return strconv.FormatFloat(value, 'f', -1, 64) }

func tableCell(value string) string {
	tone := ""
	switch {
	case strings.HasPrefix(value, "✓ "), strings.HasPrefix(value, "✔ "):
		tone = "green"
	case strings.HasPrefix(value, "◷ "):
		tone = "amber"
	case strings.HasPrefix(value, "● "):
		tone = "gray"
	}
	if tone != "" {
		return `<span class="npw-table-status" data-npw-tone="` + tone + `">` + paragraph(value) + `</span>`
	}
	return paragraph(value)
}

// Render returns semantic HTML with no scripts, inline styles or remote media.
// Rich interactions progressively enhance this readable, keyboard-safe fallback.
func Render(c Config, postID int64, skin string) string {
	return RenderWithSender(c, postID, skin, "", "")
}

// Sender values are presentation-only account data; they never change the
// saved configuration, component identity, or transfer authorization.
func RenderWithSender(c Config, postID int64, skin, sender, avatar string) string {
	return RenderForTarget(c, strconv.FormatInt(postID, 10), skin, sender, avatar)
}

// RenderForTarget supports independent content owners such as shop products.
func RenderForTarget(c Config, target, skin, sender, avatar string) string {
	if c.Validate() != nil {
		return `<aside class="npw-invalid"><p>组件配置无效，请在编辑器中重新设置。</p></aside>`
	}
	if skin != "chunnel" {
		skin = "inherit"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<aside class="npw" data-np-widget="%s" data-np-config="%s" data-npw-post="%s" data-npw-skin="%s" data-npw-tone="%s" data-npw-style="%s" data-npw-width="%s" data-npw-density="%s">`, c.Kind, c.Encode(), escape(target), skin, escape(c.Tone), escape(c.Style), escape(c.Width), escape(c.Density))
	title := func() {
		if c.Title != "" {
			fmt.Fprintf(&b, `<h3 class="npw-title">%s</h3>`, escape(c.Title))
		}
	}
	text := func() {
		if c.Text != "" {
			fmt.Fprintf(&b, `<p class="npw-text">%s</p>`, paragraph(c.Text))
		}
	}
	button := func(action, label string) {
		fmt.Fprintf(&b, `<button type="button" class="npw-button" data-npw-action="%s">%s</button>`, action, escape(label))
	}
	link := func(url, label string) {
		if LinkURL(url) {
			fmt.Fprintf(&b, `<a class="npw-link" href="%s" rel="nofollow noopener noreferrer">%s</a>`, escape(url), escape(label))
		}
	}
	image := func(src, alt string) {
		if LocalURL(src) {
			fmt.Fprintf(&b, `<img class="npw-image" src="%s" alt="%s" loading="lazy" decoding="async">`, escape(src), escape(alt))
		}
	}
	status := func() { b.WriteString(`<p class="npw-status" role="status" aria-live="polite"></p>`) }
	switch c.Kind {
	case "callout":
		icons := map[string]string{"amber": "✦", "green": "✓", "red": "!", "purple": "✧"}
		icon := icons[c.Tone]
		if icon == "" {
			icon = "i"
		}
		fmt.Fprintf(&b, `<span class="npw-callout-icon" aria-hidden="true">%s</span><div class="npw-callout-body">`, icon)
		title()
		text()
		b.WriteString(`</div>`)
	case "quote":
		b.WriteString(`<span class="npw-quote-mark" aria-hidden="true">“</span><blockquote>`)
		text()
		if c.Caption != "" {
			fmt.Fprintf(&b, `<p class="npw-quote-credit">—— %s</p>`, escape(c.Caption))
		}
		b.WriteString(`</blockquote>`)
	case "code":
		language := strings.ToLower(c.Language)
		label := language
		switch language {
		case "", "auto":
			label = "自动识别"
		case "plaintext", "text", "txt":
			label = "纯文本"
		}
		fmt.Fprintf(&b, `<div class="npw-code-bar"><span class="npw-window-dots" aria-hidden="true"><i></i><i></i><i></i></span><span class="npw-code-language" aria-label="代码语言">%s</span><span class="npw-code-label" title="%s">%s</span><button type="button" class="npw-copy" data-npw-action="copy" aria-label="复制代码">⧉ Copy</button></div><pre class="npw-code" tabindex="0" aria-label="代码内容，可横向滚动"><code class="language-%s">%s</code></pre>`, escape(label), escape(c.Title), escape(c.Title), escape(language), escape(c.Text))
	case "download":
		b.WriteString(`<div class="npw-download-heading"><span class="npw-symbol" aria-hidden="true">↓</span><div class="npw-download-body">`)
		title()
		text()
		b.WriteString(`</div></div>`)
		if c.Caption != "" {
			b.WriteString(`<div class="npw-download-code">`)
			fmt.Fprintf(&b, `<p class="npw-meta"><span>提取码</span><code tabindex="0" title="%s">%s</code></p>`, escape(c.Caption), escape(c.Caption))
			b.WriteString(`<button type="button" class="npw-button" data-npw-action="password" aria-label="复制提取码" title="复制提取码">复制</button>`)
			b.WriteString(`</div>`)
		}
		link(c.URL, "前往下载")
	case "gift":
		if c.Sender != "" {
			sender = c.Sender
		}
		if c.Image != "" {
			avatar = c.Image
		}
		renderPacketInvitation(&b, c, sender, avatar)
		b.WriteString(`<details class="npw-gift-reveal"><summary>拆开礼物</summary>`)
		fmt.Fprintf(&b, `<p class="npw-gift-code">%s</p>`, paragraph(c.Caption))
		button("password", "复制礼物内容")
		link(c.URL, "使用礼物")
		b.WriteString(`</details>`)
	case "redpacket":
		renderPacketInvitation(&b, c, sender, avatar)
		b.WriteString(`<div class="npw-packet-owner"></div>`)
		status()
	case "gallery", "carousel", "image-card":
		if c.Kind != "image-card" {
			title()
		}
		items := c.Items
		if c.Kind == "image-card" {
			items = []Item{{Title: c.Title, Text: c.Text, Image: c.Image}}
		}
		fmt.Fprintf(&b, `<div class="npw-gallery" data-npw-columns="%d" data-npw-layout="%s">`, c.Columns, escape(c.Layout))
		for i, item := range items {
			fmt.Fprintf(&b, `<figure class="npw-slide" data-npw-index="%d">`, i)
			if LocalURL(item.Image) {
				fmt.Fprintf(&b, `<a class="npw-lightbox-link" href="%s" data-npw-action="lightbox">`, escape(item.Image))
				image(item.Image, item.Title)
				b.WriteString(`</a>`)
			}
			if item.Title != "" || item.Text != "" {
				fmt.Fprintf(&b, `<figcaption><strong>%s</strong><span>%s</span></figcaption>`, escape(item.Title), escape(item.Text))
			}
			b.WriteString(`</figure>`)
		}
		b.WriteString(`</div>`)
		if c.Kind == "carousel" {
			b.WriteString(`<nav class="npw-carousel-nav" aria-label="轮播图片">`)
			button("previous", "← 上一张")
			b.WriteString(`<span class="npw-carousel-count"></span>`)
			button("next", "下一张 →")
			b.WriteString(`</nav>`)
		}
	case "audio", "video":
		platform := func() {
			b.WriteString(`<div class="npw-media-heading">`)
			fmt.Fprintf(&b, `<span class="npw-platform" data-npw-platform="%s">%s</span>`, escape(c.Provider), escape(map[string]string{"local": "本地媒体", "": "本地媒体", "netease": "网易云音乐", "youtube": "YouTube", "bilibili": "哔哩哔哩"}[c.Provider]))
			b.WriteString(`</div>`)
		}
		if c.Kind == "video" {
			platform()
		}
		b.WriteString(`<div class="npw-media-stage">`)
		if c.Kind == "audio" {
			b.WriteString(`<div class="npw-audio-cover">`)
			if c.Image != "" {
				image(c.Image, "")
			} else {
				b.WriteString(`<span class="npw-audio-record" aria-hidden="true">♪</span>`)
			}
			b.WriteString(`</div><div class="npw-media-info">`)
			title()
			text()
			platform()
			b.WriteString(`</div>`)
		} else {
			if c.Image != "" {
				image(c.Image, c.Title)
			}
		}
		b.WriteString(`<button type="button" class="npw-media-play" data-npw-action="play" aria-label="播放"><span class="npw-play-icon" aria-hidden="true"></span><span class="npw-play-label">播放</span></button></div><div class="npw-player"></div>`)
		if c.Kind == "video" {
			b.WriteString(`<div class="npw-media-info">`)
			title()
			text()
			b.WriteString(`</div>`)
		}
		b.WriteString(`<div class="npw-media-links">`)
		if c.Provider == "local" || c.Provider == "" {
			link(c.URL, "下载原文件")
			for _, item := range c.Items {
				link(item.URL, item.Title)
			}
		} else {
			link(c.ExternalURL(), "在原平台打开")
		}
		b.WriteString(`</div>`)
		status()
	case "table":
		title()
		b.WriteString(`<div class="npw-table-scroll" tabindex="0" role="region" aria-label="表格，可横向滚动"><table>`)
		for i, row := range c.Rows {
			if i == 0 {
				b.WriteString(`<thead>`)
			} else if i == 1 {
				b.WriteString(`<tbody>`)
			}
			b.WriteString(`<tr>`)
			for _, cell := range row {
				if i == 0 {
					fmt.Fprintf(&b, `<th scope="col">%s</th>`, paragraph(cell))
				} else {
					fmt.Fprintf(&b, `<td>%s</td>`, tableCell(cell))
				}
			}
			b.WriteString(`</tr>`)
			if i == 0 {
				b.WriteString(`</thead>`)
			}
		}
		if len(c.Rows) > 1 {
			b.WriteString(`</tbody>`)
		}
		b.WriteString(`</table></div>`)
	case "accordion", "tabs":
		title()
		b.WriteString(`<div class="npw-panels">`)
		for i, item := range c.Items {
			fmt.Fprintf(&b, `<details class="npw-panel" data-npw-index="%d"`, i)
			if c.Kind == "tabs" || c.Open {
				b.WriteString(` open`)
			}
			fmt.Fprintf(&b, `><summary>%s</summary><div class="npw-panel-body"><p>%s</p></div></details>`, escape(item.Title), paragraph(item.Text))
		}
		b.WriteString(`</div>`)
	case "card", "ai":
		if c.Kind == "card" && c.Image != "" {
			image(c.Image, c.Caption)
		}
		b.WriteString(`<div class="npw-card-content">`)
		if c.Kind == "ai" {
			b.WriteString(`<span class="npw-ai-label">✧ AI 辅助创作</span>`)
		}
		title()
		text()
		if c.Kind == "ai" && c.Image != "" {
			image(c.Image, c.Caption)
		}
		link(c.URL, "了解更多")
		b.WriteString(`</div>`)
	case "badges":
		b.WriteString(`<div class="npw-badges">`)
		for _, item := range c.Items {
			if item.URL != "" {
				fmt.Fprintf(&b, `<a class="npw-badge" href="%s" rel="nofollow noopener noreferrer">%s</a>`, escape(item.URL), escape(item.Title))
			} else {
				fmt.Fprintf(&b, `<span class="npw-badge">%s</span>`, escape(item.Title))
			}
		}
		b.WriteString(`</div>`)
	case "progress":
		b.WriteString(`<div class="npw-progress-heading">`)
		title()
		fmt.Fprintf(&b, `<span>%s%%</span></div><progress max="%s" value="%s" aria-label="%s">%s / %s</progress>`, number(mathRound(c.Value/c.Max*100)), number(c.Max), number(c.Value), escape(c.Title), number(c.Value), number(c.Max))
		text()
	case "counter":
		title()
		fmt.Fprintf(&b, `<p class="npw-counter"><strong>%s</strong><span>%s</span></p>`, number(c.Value), escape(c.Unit))
		text()
	case "poll", "rating":
		title()
		text()
		b.WriteString(`<div class="npw-vote-options">`)
		if c.Kind == "poll" {
			for i, item := range c.Items {
				fmt.Fprintf(&b, `<button type="button" class="npw-vote" data-npw-action="vote" data-npw-index="%d"><span>%s</span><span class="npw-vote-count">—</span></button>`, i, escape(item.Title))
			}
		} else {
			for i := 1; i <= int(c.Max); i++ {
				fmt.Fprintf(&b, `<button type="button" class="npw-star" data-npw-action="vote" data-npw-index="%d" aria-label="%d 分" aria-pressed="false"><span class="npw-star-shape" aria-hidden="true"></span><small aria-hidden="true">%d</small></button>`, i, i, i)
			}
		}
		b.WriteString(`</div><p class="npw-results">尚未加载结果</p>`)
		mode := map[string]string{"login": "登录用户每人一次", "guest": "游客参与，同一浏览器一次", "both": "登录或游客参与，同一身份一次"}[c.Mode]
		fmt.Fprintf(&b, `<small class="npw-meta">%s</small>`, mode)
		status()
	case "compare":
		title()
		b.WriteString(`<div class="npw-compare">`)
		for i, item := range c.Items {
			fmt.Fprintf(&b, `<figure class="npw-compare-image" data-npw-index="%d">`, i)
			image(item.Image, item.Title)
			fmt.Fprintf(&b, `<figcaption>%s</figcaption></figure>`, escape(item.Title))
		}
		b.WriteString(`</div><label class="npw-compare-control">拖动比较<input type="range" min="0" max="100" value="50" aria-label="图片对比位置"></label>`)
	case "annotation", "glossary":
		fmt.Fprintf(&b, `<details class="npw-definition"><summary><mark>%s</mark><span aria-hidden="true"> ⓘ</span></summary><p>%s</p></details>`, escape(c.Title), paragraph(c.Text))
	case "mention":
		label := c.Title
		if label == "" {
			label = c.UID
		}
		fmt.Fprintf(&b, `<div class="npw-mention"><a class="npw-mention-link" href="%s" rel="nofollow noopener noreferrer">@%s <span class="npw-platform" data-npw-platform="%s">%s</span></a><div class="npw-user-card"><div class="npw-user-cover"></div>`, escape(c.ExternalURL()), escape(label), escape(c.Provider), escape(map[string]string{"github": "GitHub", "site": "本站", "bilibili": "哔哩哔哩"}[c.Provider]))
		image(c.Image, c.Title)
		fmt.Fprintf(&b, `<h3 class="npw-title">%s</h3>`, escape(label))
		text()
		b.WriteString(`<p class="npw-user-meta"></p>`)
		link(c.ExternalURL(), "查看主页")
		b.WriteString(`</div></div>`)
	case "chart":
		chartLabel := map[string]string{"radar": "雷达多边形", "bar": "柱状图", "line": "折线图"}[c.Layout]
		fmt.Fprintf(&b, `<div class="npw-chart-meta"><span>%s</span><span>刻度上限 %s%s</span></div>`, escape(chartLabel), number(c.Max), escape(c.Unit))
		title()
		text()
		b.WriteString(`<div class="npw-chart" role="img" aria-label="数据图表"></div><details class="npw-chart-data" open><summary>查看图表数据</summary><table><thead><tr><th scope="col">项目</th><th scope="col">数值</th></tr></thead><tbody>`)
		for _, item := range c.Items {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s %s</td></tr>`, escape(item.Title), number(item.Value), escape(c.Unit))
		}
		b.WriteString(`</tbody></table></details>`)
	}
	if c.Caption != "" && c.Kind != "quote" && c.Kind != "download" && c.Kind != "gift" {
		fmt.Fprintf(&b, `<p class="npw-caption">%s</p>`, escape(c.Caption))
	}
	b.WriteString(`</aside>`)
	return b.String()
}

func renderPacketInvitation(b *strings.Builder, c Config, sender, avatar string) {
	if sender == "" {
		sender = "文章作者"
	}
	label := "积分红包"
	if c.Kind == "gift" {
		label = "礼物红包"
	}
	b.WriteString(`<span class="npw-packet-from" aria-hidden="true">`)
	if LocalURL(avatar) {
		fmt.Fprintf(b, `<img class="npw-packet-avatar" src="%s" alt="" width="30" height="30" loading="lazy">`, escape(avatar))
	}
	fmt.Fprintf(b, `<span class="npw-packet-sender-name">%s</span></span><button type="button" class="npw-packet-launch" data-npw-action="packet"><span class="npw-packet-card-main"><span class="npw-packet-mini" aria-hidden="true"></span><span class="npw-packet-card-title">%s</span></span><span class="npw-packet-card-label">%s</span></button>`, escape(sender), escape(c.Title), label)
}

func mathRound(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
