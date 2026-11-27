package wechat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

const MaxCollect = 10
const MaxCollectImages = 50

// The fetched page includes WeChat's scripts and styles, not just the article.
// Stored content is still checked against content.MaxContentBytes on import.
const maxArticlePageBytes = 8 << 20

var errArticlePageTooLarge = errors.New("文章页面超过 8 MiB，无法采集")

type Collected struct {
	URL      string   `json:"url"`
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Excerpt  string   `json:"excerpt"`
	Body     string   `json:"body"`
	Cover    string   `json:"cover"`
	Images   []string `json:"images"`
	Preview  string   `json:"preview"`
	Warnings []string `json:"warnings"`
}
type CollectFailure struct {
	URL     string `json:"url"`
	Message string `json:"message"`
}

func ArticleURL(raw string) (string, error) {
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	if len(raw) > 2048 {
		return "", errors.New("文章链接过长")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() != "mp.weixin.qq.com" || (u.Port() != "" && u.Port() != "443") || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("仅支持 mp.weixin.qq.com 的公开文章链接")
	}
	u.Scheme = "https"
	u.Host = "mp.weixin.qq.com"
	u.Fragment = ""
	if u.Path == "/s" || u.Path == "/s/" {
		q := u.Query()
		out := url.Values{}
		for _, key := range []string{"__biz", "mid", "idx", "sn"} {
			v := q.Get(key)
			if v == "" || len(q[key]) != 1 || len(v) > 160 {
				return "", errors.New("文章长链接缺少 __biz、mid、idx 或 sn")
			}
			out.Set(key, v)
		}
		u.Path = "/s"
		u.RawQuery = out.Encode()
	} else if regexp.MustCompile(`^/s/[A-Za-z0-9_-]{5,100}$`).MatchString(u.Path) {
		u.RawQuery = ""
	} else {
		return "", errors.New("请输入微信公众号文章分享链接")
	}
	return u.String(), nil
}
func (c *Client) FetchArticle(ctx context.Context, raw string) (Collected, error) {
	address, err := ArticleURL(raw)
	if err != nil {
		return Collected{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", address, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NewPressArticleImport/1.0)")
	req.Header.Set("Accept", "text/html")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Collected{}, errors.New("无法读取文章，请确认链接可公开访问后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Collected{}, errors.New("微信未提供文章内容，请检查链接或稍后重试")
	}
	address, err = ArticleURL(response.Request.URL.String())
	if err != nil {
		return Collected{}, errors.New("文章跳转到了登录或验证页面")
	}
	if response.ContentLength > maxArticlePageBytes {
		return Collected{}, errArticlePageTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxArticlePageBytes+1))
	if err != nil {
		return Collected{}, errors.New("文章页面读取中断，请重试")
	}
	if len(b) > maxArticlePageBytes {
		return Collected{}, errArticlePageTooLarge
	}
	return ParseArticle(address, string(b))
}
func ParseArticle(address, source string) (Collected, error) {
	a := Collected{Images: []string{}, Warnings: []string{}}
	if len(source) > maxArticlePageBytes {
		return a, errArticlePageTooLarge
	}
	if !utf8.ValidString(source) {
		return a, errors.New("文章页面不是 UTF-8 文本")
	}
	canonical, err := ArticleURL(address)
	if err != nil {
		return a, err
	}
	a.URL = canonical
	documentURL, _ := url.Parse(canonical)
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return a, errors.New("文章页面无法解析")
	}
	var body *html.Node
	meta := map[string]string{}
	var visit func(*html.Node, int) error
	visit = func(n *html.Node, depth int) error {
		if depth > 128 {
			return errors.New("文章结构嵌套过深")
		}
		if n.Type == html.ElementNode {
			attrs := map[string]string{}
			for _, v := range n.Attr {
				attrs[v.Key] = v.Val
			}
			if n.Data == "meta" {
				key := attrs["property"]
				if key == "" {
					key = attrs["name"]
				}
				meta[key] = attrs["content"]
			}
			switch attrs["id"] {
			case "js_content":
				body = n
			case "activity-name", "rich_media_title":
				a.Title = nodeText(n)
			case "js_name":
				a.Author = nodeText(n)
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if err := visit(ch, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err = visit(root, 0); err != nil {
		return a, err
	}
	if body == nil {
		return a, errors.New("未找到公开文章正文；文章可能已删除、需登录或触发微信验证，请在浏览器核对")
	}
	if a.Title == "" {
		a.Title = meta["og:title"]
	}
	a.Title = strings.TrimSpace(a.Title)
	if a.Author == "" {
		a.Author = meta["author"]
	}
	a.Excerpt = trimRunes(strings.TrimSpace(meta["description"]), 500)
	if u, e := ArticleURL(meta["og:url"]); e == nil {
		a.URL = u
	}
	a.Key = fmt.Sprintf("%x", sha256.Sum256([]byte(a.URL)))
	if a.Title == "" || utf8.RuneCountInString(a.Title) > content.MaxTitleRunes {
		return a, errors.New("文章标题缺失或过长")
	}
	seen := map[string]bool{}
	var clean func(*html.Node)
	clean = func(n *html.Node) {
		for child := n.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.ElementNode {
				switch child.Data {
				case "script", "style", "iframe", "object", "embed", "audio", "video", "mpvoice", "mp-video", "mp-common-videosnap", "mp-common-profile":
					n.RemoveChild(child)
					a.Warnings = appendUnique(a.Warnings, "音视频、小程序和动态内容未迁入")
					child = next
					continue
				}
				if child.Data == "img" {
					attrs := map[string]string{}
					for _, v := range child.Attr {
						attrs[v.Key] = v.Val
					}
					raw := attrs["data-src"]
					if raw == "" {
						raw = attrs["src"]
					}
					u, e := ImageURL(raw)
					if e != nil {
						n.RemoveChild(child)
						a.Warnings = appendUnique(a.Warnings, "部分非微信托管图片未迁入")
						child = next
						continue
					}
					alt, described := attrs["alt"]
					if !described {
						alt = "采集图片"
					}
					child.Attr = []html.Attribute{{Key: "src", Val: u}, {Key: "alt", Val: trimRunes(alt, 250)}}
					if !seen[u] {
						seen[u] = true
						a.Images = append(a.Images, u)
					}
				} else {
					attrs := []html.Attribute{}
					for _, attr := range child.Attr {
						if attr.Key == "href" || attr.Key == "colspan" || attr.Key == "rowspan" {
							if attr.Key == "href" {
								resolved, ok := collectionLink(documentURL, attr.Val)
								if !ok {
									a.Warnings = appendUnique(a.Warnings, "部分无效或不安全链接已转为普通文字")
									continue
								}
								attr.Val = resolved
							}
							attrs = append(attrs, attr)
						}
					}
					child.Attr = attrs
				}
			}
			clean(child)
			child = next
		}
	}
	clean(body)
	var text strings.Builder
	for n := body.FirstChild; n != nil; n = n.NextSibling {
		_ = html.Render(&text, n)
	}
	a.Body = contenthtml.Sanitize(text.String())
	if strings.TrimSpace(nodeText(body)) == "" && len(a.Images) == 0 {
		return a, errors.New("文章没有可导入的正文")
	}
	if len(a.Images) > 30 {
		return a, errors.New("单篇文章图片超过 30 张，请拆分后导入")
	}
	if u, e := ImageURL(meta["og:image"]); e == nil {
		a.Cover = u
		if !seen[u] {
			a.Images = append(a.Images, u)
		}
	}
	a.Author = trimRunes(strings.TrimSpace(a.Author), 160)
	a.Warnings = appendUnique(a.Warnings, "内容将保存为本站草稿，并保留公众号名称与原文链接")
	a.Preview = RewriteImages(a.Body, nil, true)
	return a, nil
}

// Resolve against the fetched article, never an untrusted <base> or og:url.
// Imported element IDs are removed, so fragments keep pointing to the source
// article instead of becoming broken anchors in the new document.
func collectionLink(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", false
	}
	reference, err := url.Parse(raw)
	if err != nil || reference.User != nil || reference.Opaque != "" {
		return "", false
	}
	resolved := base.ResolveReference(reference)
	if resolved.User != nil || resolved.Hostname() == "" || (resolved.Scheme != "https" && resolved.Scheme != "http") {
		return "", false
	}
	value := resolved.String()
	return value, len(value) <= 2048
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}
func appendUnique(list []string, item string) []string {
	for _, v := range list {
		if v == item {
			return list
		}
	}
	return append(list, item)
}
func RewriteImages(source string, mapping map[string]string, preview bool) string {
	root, _ := html.Parse(strings.NewReader(source))
	var body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
		}
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode && c.Data == "img" {
				var src string
				alt := "采集图片"
				for _, a := range c.Attr {
					if a.Key == "src" {
						src = a.Val
					} else if a.Key == "alt" {
						alt = trimRunes(a.Val, 250)
					}
				}
				if target := mapping[src]; target != "" && !preview {
					c.Attr = []html.Attribute{{Key: "src", Val: target}, {Key: "alt", Val: alt}}
				} else {
					c.Type = html.TextNode
					c.Data = "[图片将在确认导入时保存]"
					if alt != "" && alt != "采集图片" {
						c.Data = "[图片：" + alt + "；确认导入时保存]"
					}
					c.Attr = nil
				}
			}
			walk(c)
			c = next
		}
	}
	walk(root)
	var out strings.Builder
	if body != nil {
		for c := body.FirstChild; c != nil; c = c.NextSibling {
			_ = html.Render(&out, c)
		}
	}
	return out.String()
}
func Batch(articles []Collected, mapping map[string]string, author int64, name, actor string) content.ImportBatch {
	batch := content.ImportBatch{Source: "wechat-public", Format: "wechat"}
	for _, a := range articles {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(a.URL+"\x00"+a.Title+"\x00"+a.Author+"\x00"+a.Body)))
		// Fingerprint source metadata before rewriting image paths. Local media,
		// preview text and operator identity do not affect source comparisons.
		metadata, _ := json.Marshal([3]string{"wechat-metadata-v1", a.Excerpt, a.Cover})
		metadataDigest := fmt.Sprintf("%x", sha256.Sum256(metadata))
		body := RewriteImages(a.Body, mapping, false) + "<p>来源：" + stdhtml.EscapeString(a.Author) + " · <a href=\"" + stdhtml.EscapeString(a.URL) + "\" rel=\"nofollow noopener noreferrer\">阅读公众号原文</a></p>"
		batch.Items = append(batch.Items, content.ImportEntry{Key: a.Key, Digest: digest, MetadataDigest: metadataDigest, Input: content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "wechat-" + a.Key[:20], Title: a.Title, Excerpt: a.Excerpt, ContentSource: body, CoverURL: mapping[a.Cover], AuthorID: author, AuthorName: name, Actor: actor}})
	}
	return batch
}
func (c *Client) FetchImage(ctx context.Context, raw string) ([]byte, error) {
	address, err := ImageURL(raw)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", address, nil)
	req.Header.Set("Referer", "https://mp.weixin.qq.com/")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("微信图片下载失败")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("微信图片不可访问")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		return nil, errors.New("图片超过 5 MiB 或读取不完整")
	}
	return JPEG(data)
}
