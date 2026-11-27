package httpserver

import (
	"context"
	"fmt"
	"html"
	"strings"

	htmlparser "golang.org/x/net/html"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/resources"
)

func resourceAsset(kind string) string { return coreui.AssetURL(kind + "/newpress-resources." + kind) }
func resourceShell(post int64, r resources.Resource, preview *resourcePreviewView) string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<aside class="npr-card" id="resource-%s" data-npr-resource="%s" data-npr-post="%d"`, e(r.ID), e(r.ID), post)
	if preview != nil {
		fmt.Fprintf(&b, ` data-npr-preview="%s"`, e(preview.mode))
	}
	b.WriteString(`><div class="npr-header"><span class="npr-lock" aria-hidden="true"></span><span>专属内容与资源</span>`)
	if r.Badge != "" {
		fmt.Fprintf(&b, `<span class="npr-badge">%s</span>`, e(r.Badge))
	}
	b.WriteString(`</div><div class="npr-main"><div class="npr-overview">`)
	if r.Cover != "" {
		fmt.Fprintf(&b, `<img class="npr-cover" src="%s" alt="" loading="lazy">`, e(r.Cover))
	} else {
		b.WriteString(`<span class="npr-file" aria-hidden="true"></span>`)
	}
	fmt.Fprintf(&b, `<div class="npr-description"><h3>%s</h3>`, e(r.Title))
	if r.Description != "" {
		fmt.Fprintf(&b, `<p>%s</p>`, e(r.Description))
	}
	metadata := []string{}
	if r.Size != "" {
		metadata = append(metadata, r.Size)
	}
	if r.Updated != "" {
		metadata = append(metadata, "更新于 "+r.Updated)
	}
	if len(metadata) > 0 {
		fmt.Fprintf(&b, `<small>%s</small>`, e(strings.Join(metadata, " · ")))
	}
	if len(r.Features) > 0 {
		b.WriteString(`<div class="npr-features">`)
		for _, feature := range r.Features {
			fmt.Fprintf(&b, `<span>%s</span>`, e(feature))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></div>`)
	if r.OriginalPrice != "" {
		fmt.Fprintf(&b, `<span class="npr-original">原价 <s>%s</s></span>`, e(r.OriginalPrice))
	}
	if preview != nil {
		b.WriteString(`<p class="npr-preview-notice" role="note">预览模式 · 不会下单、扣款、提交资料或消耗下载次数。</p>`)
		fmt.Fprintf(&b, `<div class="npr-content">%s</div>`, preview.fallback)
	} else {
		b.WriteString(`<div class="npr-content"><p role="status" aria-live="polite">正在核对解锁条件…</p></div>`)
	}
	if r.MemberCTA {
		if preview != nil {
			b.WriteString(`<div class="npr-membership" aria-disabled="true"><span>会员可享更多专属内容</span><span class="npr-member-link">查看权益（预览）</span></div>`)
		} else {
			b.WriteString(`<a class="npr-membership" href="/billing/?kind=membership"><span>会员可享更多专属内容</span><span class="npr-member-link">查看权益 →</span></a>`)
		}
	}
	if preview != nil {
		b.WriteString(`<div class="npr-note">正式访问时，按读者身份、解锁条件和访问限额核验权限。</div>`)
	} else {
		b.WriteString(`<div class="npr-note">解锁后可查看对应内容，下载记录保存在 <a href="/downloads/">我的下载</a>。</div>`)
	}
	b.WriteString(`</div></aside>`)
	return b.String()
}
func (s *Server) prepareResources(ctx context.Context, data *pageView) {
	data.resourcePreviews = nil
	if data.IsAdmin || data.Post.ID < 1 {
		return
	}
	cfg := resources.FromMeta(data.Post.Meta)
	if len(cfg.Items) == 0 {
		return
	}
	cards := map[string]string{}
	used := map[string]bool{}
	for _, item := range cfg.Items {
		var preview *resourcePreviewView
		if data.IsPreview {
			// Only the authenticated, owner-bound autosave preview may embed
			// private deliveries. Appearance previews get a static condition card.
			preview = s.resourcePreview(ctx, data, item)
			if preview.state != "" {
				if data.resourcePreviews == nil {
					data.resourcePreviews = make(map[string]string)
				}
				data.resourcePreviews[item.ID] = preview.state
			}
		}
		cards[item.ID] = resourceShell(data.Post.ID, item, preview)
	}
	// Replace text tokens only: an author cannot turn an attribute into a resource block.
	var out strings.Builder
	tokens := htmlparser.NewTokenizer(strings.NewReader(data.Post.ContentHTML))
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		raw := string(tokens.Raw())
		if kind == htmlparser.TextToken {
			for _, item := range cfg.Items {
				marker := "[resource:" + item.ID + "]"
				if strings.Contains(raw, marker) {
					if used[item.ID] {
						raw = strings.ReplaceAll(raw, marker, "")
					} else {
						raw = strings.Replace(raw, marker, cards[item.ID], 1)
						raw = strings.ReplaceAll(raw, marker, "")
						used[item.ID] = true
					}
				}
			}
		}
		out.WriteString(raw)
	}
	for _, item := range cfg.Items {
		if !used[item.ID] {
			out.WriteString(cards[item.ID])
		}
	}
	data.Post.ContentHTML = out.String()
}
func withResourceAssets(output []byte, bases ...string) []byte {
	result := withArticleWidgetStyles(output, bases...)
	marker := `<link rel="stylesheet" href="` + html.EscapeString(publicCoreAsset("css/newpress-article-widgets.css", bases...)) + `">`
	return []byte(strings.Replace(string(result), marker, marker+`<link rel="stylesheet" href="`+html.EscapeString(publicCoreAsset("css/newpress-resources.css", bases...))+`"><script defer src="`+html.EscapeString(publicCoreAsset("js/newpress-resources.js", bases...))+`"></script>`, 1))
}
