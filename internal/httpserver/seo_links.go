package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

const maxSEOLinks = 200

type contentLinkResult struct {
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Target  string `json:"target,omitempty"`
}
type contentLinksReport struct {
	PostID    int64               `json:"post_id"`
	Version   int64               `json:"version"`
	Items     []contentLinkResult `json:"items"`
	Counts    map[string]int      `json:"counts"`
	Truncated bool                `json:"truncated"`
	CheckedAt time.Time           `json:"checked_at"`
}

func extractContentLinks(post content.Post) ([]contentLinkResult, bool) {
	items := []contentLinkResult{}
	seen := map[string]bool{}
	truncated := false
	add := func(value, kind string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		if len(items) == maxSEOLinks {
			truncated = true
			return
		}
		seen[value] = true
		items = append(items, contentLinkResult{URL: value, Kind: kind})
	}
	add(post.CoverURL, "cover")
	tokens := html.NewTokenizer(strings.NewReader(post.ContentHTML))
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		for _, attr := range token.Attr {
			if token.Data == "a" && attr.Key == "href" {
				add(attr.Val, "link")
			}
			if token.Data == "img" && attr.Key == "src" {
				add(attr.Val, "image")
			}
		}
		if truncated {
			break
		}
	}
	return items, truncated
}

func (s *Server) adminContentLinksAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminContentAPIEnabled(w, r) {
		return
	}
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentRead)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	post, ok := s.loadAdminContentAPI(w, r)
	if !ok || !s.requireContentAPIObjectRead(w, r, p, post) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, truncated := extractContentLinks(post)
	report := contentLinksReport{PostID: post.ID, Version: post.Version, Items: items, Truncated: truncated, CheckedAt: time.Now().UTC(), Counts: map[string]int{"ok": 0, "redirect": 0, "broken": 0, "unchecked": 0}}
	base, err := url.Parse(s.absolutePublicURL(s.contentPath(post)))
	if err != nil {
		s.writeAPIError(w, r, 503, "seo.check_unavailable", "站点地址无法解析", nil)
		return
	}
	for i, item := range report.Items {
		if err = ctx.Err(); err != nil {
			break
		}
		report.Items[i], err = s.checkContentLink(ctx, base, item)
		if err != nil {
			break
		}
		report.Counts[report.Items[i].Status]++
	}
	if err != nil {
		s.writeAPIError(w, r, 503, "seo.check_unavailable", "检查未完成，请稍后重试", nil)
		return
	}
	writeJSON(w, 200, report)
}

func (s *Server) checkContentLink(ctx context.Context, base *url.URL, item contentLinkResult) (contentLinkResult, error) {
	result := func(status, message string) (contentLinkResult, error) {
		item.Status, item.Message = status, message
		return item, nil
	}
	if len(item.URL) > 2048 || !utf8.ValidString(item.URL) {
		return result("unchecked", "链接过长或编码不正确")
	}
	reference, err := url.Parse(item.URL)
	if err != nil {
		return result("broken", "链接格式不正确")
	}
	u := base.ResolveReference(reference)
	if (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, base.Host) || u.Scheme != base.Scheme || u.User != nil {
		return result("unchecked", "外部或非网页链接，未发起网络请求")
	}
	if u.Fragment != "" {
		return result("unchecked", "包含页内锚点，需在页面中核对")
	}
	if strings.HasPrefix(u.Path, "/media/") {
		link, ok := assetref.New(s.config.PublicURL).URL(u.String(), base.Path)
		if !ok || s.media == nil {
			return result("broken", "媒体路径不正确")
		}
		stored, e := s.media.Get(link.ID)
		if e != nil || stored.Status() != media.StatusActive || stored.PublicURL != link.Path {
			return result("broken", "媒体不存在或不再公开")
		}
		name, format, e := mediaImageQuery(&http.Request{URL: u})
		if e != nil {
			return result("broken", "图片尺寸或格式参数不正确")
		}
		var file media.ReadSeekCloser
		if name == "" {
			_, file, e = s.media.Open(link.ID)
		} else {
			_, file, e = s.media.OpenVariant(link.ID, name, format, false)
		}
		if e != nil {
			return result("broken", "图片或派生文件不可读取")
		}
		_ = file.Close()
		return result("ok", "媒体文件可读取")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return result("unchecked", "带查询参数的页面需人工核对")
	}
	if u.Path == "/" || u.Path == "/explore/" || u.Path == "/community/" || u.Path == "/series/" {
		return result("ok", "站内公开入口")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && (parts[0] == "posts" || parts[0] == "pages") && !strings.HasSuffix(u.Path, "/") && content.ValidSlug(parts[1]) {
		if parts[0] == "posts" {
			_, err = s.repository.GetPublishedBySlug(ctx, parts[1])
		} else {
			_, err = s.repository.GetPublishedPageBySlug(ctx, parts[1])
		}
		if err == nil {
			return result("ok", "目标内容公开可用")
		}
		if !errors.Is(err, content.ErrNotFound) {
			return item, err
		}
	} else if len(parts) == 2 && (parts[0] == "categories" || parts[0] == "tags") && strings.HasSuffix(u.Path, "/") {
		taxonomy := content.TaxonomyCategory
		if parts[0] == "tags" {
			taxonomy = content.TaxonomyTag
		}
		_, _, _, err = s.repository.ListPublishedByTerm(ctx, taxonomy, parts[1], 1, 0)
		if errors.Is(err, content.ErrNotFound) {
			return result("broken", "分类或标签归档不存在")
		}
		if err != nil {
			return item, err
		}
		return result("ok", "归档可用")
	} else if len(parts) == 2 && parts[0] == "series" && strings.HasSuffix(u.Path, "/") {
		_, _, err = s.repository.PublishedSeries(ctx, parts[1])
		if errors.Is(err, content.ErrNotFound) {
			return result("broken", "选集不存在或未公开")
		}
		if err != nil {
			return item, err
		}
		return result("ok", "选集公开可用")
	} else if strings.HasPrefix(u.Path, "/theme-assets/") {
		relative := strings.TrimPrefix(u.Path, "/theme-assets/")
		if relative == "" || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") || strings.Contains(relative, ":") {
			return result("broken", "主题资源路径不正确")
		}
		parts := strings.SplitN(relative, "/", 3)
		if len(parts) == 3 && len(parts[1]) == 24 {
			if _, _, err := s.themes.ReadVersionedAsset(parts[0], parts[1], parts[2]); err != nil {
				return result("broken", "主题资源文件不可读取")
			}
		} else {
			file, _, _, err := s.themes.OpenAsset(relative)
			if err != nil {
				return result("broken", "主题资源文件不可读取")
			}
			file.Close()
		}
		return result("ok", "主题资源可读取")
	}
	if content.ValidRedirectPath(u.Path) {
		target, err := s.repository.ResolveRedirect(ctx, u.Path)
		if err == nil {
			item.Target = target
			return result("redirect", "旧链接可用，建议更新为当前地址")
		}
		if !errors.Is(err, content.ErrNotFound) {
			return item, err
		}
		return result("broken", "目标不存在或未公开")
	}
	return result("unchecked", "此类路径暂不支持检查")
}
