// Package contentimport converts bounded, untrusted export documents into
// ordinary content inputs. It has no filesystem, network or persistence access.
package contentimport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/content"
)

const MaxBytes = 8 << 20

type File struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
type Request struct {
	Source  string            `json:"source"`
	Format  string            `json:"format"`
	State   string            `json:"state"`
	Files   []File            `json:"files"`
	Authors map[string]int64  `json:"authors,omitempty"`
	Media   map[string]string `json:"media,omitempty"`
}
type Item struct {
	Key            string              `json:"key"`
	Author         string              `json:"author"`
	SourceURL      string              `json:"source_url,omitempty"`
	OriginalStatus string              `json:"original_status"`
	Warnings       []string            `json:"warnings"`
	Images         []string            `json:"images"`
	Preview        string              `json:"preview"`
	Categories     []string            `json:"categories"`
	Tags           []string            `json:"tags"`
	Entry          content.ImportEntry `json:"-"`
	sourceHTML     string
}
type Skipped struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type Document struct {
	Batch    content.ImportBatch `json:"-"`
	Items    []Item              `json:"items"`
	Skipped  []Skipped           `json:"skipped"`
	Warnings []string            `json:"warnings"`
	Authors  []string            `json:"authors"`
	media    map[string]string
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func invalid(message string) error {
	return &content.ValidationError{Fields: map[string]string{"import": message}}
}
func Parse(request Request, now time.Time) (Document, error) {
	doc := Document{Batch: content.ImportBatch{Source: request.Source, Format: request.Format}, Items: []Item{}, Skipped: []Skipped{}, Warnings: []string{}, Authors: []string{}}
	if request.State != "draft" && request.State != "preserve" {
		return doc, invalid("请选择导为草稿或保留来源状态")
	}
	if len(request.Files) == 0 || len(request.Files) > content.MaxImportItems || len(request.Authors) > content.MaxImportItems || len(request.Media) > 200 {
		return doc, invalid("文件或映射数量超出限制")
	}
	if request.Format != "markdown" && request.Format != "wordpress" {
		return doc, invalid("仅支持 Markdown 或 WordPress WXR")
	}
	if request.Format == "wordpress" && len(request.Files) != 1 {
		return doc, invalid("每批只接受一个 WordPress XML 文件")
	}
	bytes := 0
	names := map[string]bool{}
	for _, file := range request.Files {
		bytes += len(file.Text)
		if bytes > MaxBytes || !utf8.ValidString(file.Text) || strings.ContainsRune(file.Text, 0) {
			return doc, invalid("文件须为 UTF-8 文本且总计不超过 8 MiB")
		}
		if file.Name == "" || len(file.Name) > 256 || !utf8.ValidString(file.Name) || strings.ContainsAny(file.Name, "/\\") || strings.IndexFunc(file.Name, unicode.IsControl) >= 0 || names[file.Name] {
			return doc, invalid("文件名无效或重复")
		}
		names[file.Name] = true
		var items []Item
		var err error
		if request.Format == "markdown" {
			var item Item
			item, err = parseMarkdown(file)
			items = []Item{item}
		} else {
			items, doc.Skipped, doc.Warnings, err = parseWXR(file)
		}
		if err != nil {
			return doc, err
		}
		doc.Items = append(doc.Items, items...)
	}
	if len(doc.Items) == 0 || len(doc.Items) > content.MaxImportItems {
		return doc, invalid("每批须包含 1–100 篇支持的文章或页面；请拆分导出")
	}
	mediaResolver := assetref.New("")
	for source, target := range request.Media {
		if source == "" || len(source) > 2048 || len(target) > 2048 {
			return doc, invalid("图片映射地址无效")
		}
		normalized, fields := content.NormalizePostInput(content.PostInput{Title: "media mapping", CoverURL: target}, now)
		if fields["cover_url"] != "" || !strings.HasPrefix(normalized.CoverURL, "/media/") {
			return doc, invalid("图片映射目标必须来自本站媒体库")
		}
		// Mappings must name an exact managed path; otherwise malformed URLs
		// can bypass the media reference guard's existence/status validation.
		targetURL, _ := url.Parse(normalized.CoverURL)
		link, ok := mediaResolver.URL(normalized.CoverURL, "/")
		if !ok || targetURL.EscapedPath() != link.Path {
			return doc, invalid("图片映射目标必须使用媒体库提供的 /media/{id}/{filename} 地址")
		}
	}
	doc.media = maps.Clone(request.Media)
	urls := map[string]string{}
	authors := map[string]bool{}
	keys := map[string]bool{}
	for i := range doc.Items {
		item := &doc.Items[i]
		input := &item.Entry.Input
		if keys[item.Key] {
			return doc, invalid("导出文件包含重复来源 ID")
		}
		keys[item.Key] = true
		item.Entry.Key = item.Key
		if input.Type == "" {
			input.Type = content.TypePost
		}
		if input.Status == "" {
			input.Status = content.StatusDraft
		}
		item.OriginalStatus = input.Status
		if request.State == "draft" && input.Status != content.StatusPrivate {
			input.Status = content.StatusDraft
		}
		if !content.ValidStatus(input.Status) {
			input.Status = content.StatusDraft
			item.Warnings = append(item.Warnings, "未知来源状态已改为草稿")
		}
		if input.Type == content.TypePage {
			item.Entry.Categories = nil
			input.TagNames = nil
		}
		if input.Title == "" {
			input.Title = "未命名内容 " + item.Key
			item.Warnings = append(item.Warnings, "源标题为空，已生成待编辑标题")
		}
		originalSlug := input.Slug
		input.Slug = content.NormalizeSlug(input.Slug)
		if input.Slug == "" {
			input.Slug = content.NormalizeSlug(input.Title)
		}
		if input.Slug == "" {
			input.Slug = "import-" + digest(request.Source + "/" + item.Key)[:16]
		}
		if originalSlug != "" && originalSlug != input.Slug {
			item.Warnings = append(item.Warnings, "别名已转换为本站支持的格式")
		}
		if (input.Status == content.StatusPublish || input.Status == content.StatusFuture) && input.PublishedAt.IsZero() {
			input.Status = content.StatusDraft
			item.Warnings = append(item.Warnings, "缺少有效发布时间，已改为草稿")
		}
		// Password-protected input is always private even without a source date.
		if input.Status == content.StatusPrivate && input.PublishedAt.IsZero() {
			input.PublishedAt = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
			item.Warnings = append(item.Warnings, "缺少发布时间，保留私密状态；日期需人工核对")
		}
		if item.SourceURL != "" {
			if urls[item.SourceURL] != "" {
				return doc, invalid("来源内容 URL 重复，无法确定内部链接映射")
			}
			urls[item.SourceURL] = content.PublicPath(content.Post{Type: input.Type, Slug: input.Slug})
		}
		if request.Format == "markdown" {
			target := content.PublicPath(content.Post{Type: input.Type, Slug: input.Slug})
			urls[item.Key] = target
			urls["./"+item.Key] = target
		}
		authors[item.Author] = true
	}
	for i := range doc.Items {
		item := &doc.Items[i]
		input := &item.Entry.Input
		// Retain source identities until the repository resolves existing targets.
		// This HTML and the media snapshot are never serialized into the preview.
		item.sourceHTML = input.ContentSource
		input.ContentSource, item.Images = transformHTML(item.sourceHTML, item.SourceURL, urls, doc.media)
		if len(item.Images) > 0 {
			item.Warnings = append(item.Warnings, fmt.Sprintf("%d 个图片来源需映射；未映射图片保留为链接", len(item.Images)))
		}
		if input.CoverURL != "" {
			if mapped := doc.media[input.CoverURL]; mapped != "" {
				input.CoverURL = mapped
			} else if !strings.HasPrefix(input.CoverURL, "/media/") {
				item.Images = appendUnique(item.Images, input.CoverURL)
				input.CoverURL = ""
				item.Warnings = append(item.Warnings, "来源封面尚未映射，未设置为本站封面")
			}
		}
		normalized, fields := content.NormalizePostInput(*input, now)
		if len(fields) > 0 {
			return doc, &content.ValidationError{Fields: fields}
		}
		*input = normalized
		item.Preview = normalized.ContentHTML
		item.Categories = append([]string{}, item.Entry.Categories...)
		item.Tags = append([]string{}, normalized.TagNames...)
		doc.Batch.Items = append(doc.Batch.Items, item.Entry)
	}
	for author := range authors {
		doc.Authors = append(doc.Authors, author)
	}
	sort.Strings(doc.Authors)
	_, err := content.NormalizeImportBatch(doc.Batch, now)
	return doc, err
}
func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	if len(values) < 200 {
		return append(values, value)
	}
	return values
}

// AlignTargets keeps links between this batch's entries pointed at existing
// targets even if an earlier imported draft was renamed without a public alias.
func AlignTargets(doc *Document, plan content.ImportPlan, now time.Time) {
	urls := map[string]string{}
	changed := false
	for i, row := range plan.Items {
		item := doc.Items[i]
		input := doc.Batch.Items[i].Input
		target := content.PublicPath(content.Post{Type: input.Type, Slug: input.Slug})
		if row.PostID > 0 {
			changed = changed || target != row.URL
			target = row.URL
		}
		if item.SourceURL != "" {
			urls[item.SourceURL] = target
		}
		if doc.Batch.Format == "markdown" {
			urls[item.Key] = target
			urls["./"+item.Key] = target
		}
	}
	if !changed {
		return
	}
	for i := range doc.Batch.Items {
		input := &doc.Batch.Items[i].Input
		item := &doc.Items[i]
		// A renamed target's old slug can be reused by another source. Resolve
		// each original filename/URL directly instead of remapping shared paths.
		input.ContentSource, _ = transformHTML(item.sourceHTML, item.SourceURL, urls, doc.media)
		normalized, _ := content.NormalizePostInput(*input, now)
		*input = normalized
		item.Preview = normalized.ContentHTML
	}
}
func safeSourceURL(raw string) string {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2048 {
		return ""
	}
	u.Fragment = ""
	return u.String()
}
func oldPath(raw string) (string, bool) {
	u, e := url.Parse(raw)
	if e != nil || u.RawQuery != "" || u.Fragment != "" || !content.ValidRedirectPath(u.Path) {
		return "", false
	}
	for _, bad := range []string{"%2f", "%5c", "%2e", "%25"} {
		if strings.Contains(strings.ToLower(u.EscapedPath()), bad) {
			return "", false
		}
	}
	return u.Path, true
}
func transformHTML(source, base string, urls, media map[string]string) (string, []string) {
	// Sanitize before parsing and again through NormalizePostInput afterwards.
	normalized, _ := content.NormalizePostInput(content.PostInput{Title: "Import", ContentSource: source}, time.Unix(0, 0))
	z := html.NewTokenizer(strings.NewReader(normalized.ContentHTML))
	var out strings.Builder
	images := []string{}
	resolve := func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return ""
		}
		if b, e := url.Parse(base); e == nil && b.Host != "" {
			return b.ResolveReference(u).String()
		}
		return raw
	}
	for {
		t := z.Next()
		if t == html.ErrorToken {
			if z.Err() != io.EOF {
				return "", images
			}
			break
		}
		if t != html.StartTagToken && t != html.SelfClosingTagToken {
			out.Write(z.Raw())
			continue
		}
		token := z.Token()
		if token.Data == "img" {
			src, alt := "", "图片"
			for _, a := range token.Attr {
				if a.Key == "src" {
					src = a.Val
				}
				if a.Key == "alt" && a.Val != "" {
					alt = a.Val
				}
			}
			resolved := resolve(src)
			mapped := media[src]
			if mapped == "" {
				mapped = media[resolved]
			}
			if mapped == "" && base == "" && strings.HasPrefix(src, "/media/") {
				mapped = src
			}
			if mapped != "" {
				token.Attr = []html.Attribute{{Key: "src", Val: mapped}, {Key: "alt", Val: alt}}
				out.WriteString(token.String())
			} else {
				images = appendUnique(images, resolved)
				link := safeSourceURL(resolved)
				if link != "" {
					out.WriteString(`<a href="` + html.EscapeString(link) + `">图片：` + html.EscapeString(alt) + `</a>`)
				} else {
					out.WriteString("[图片：" + html.EscapeString(alt) + "]")
				}
			}
			continue
		}
		if token.Data == "a" {
			for i, a := range token.Attr {
				if a.Key != "href" || strings.HasPrefix(a.Val, "#") {
					continue
				}
				resolved := resolve(a.Val)
				u, e := url.Parse(resolved)
				if e != nil {
					continue
				}
				fragment, rawFragment := u.Fragment, u.RawFragment
				u.Fragment, u.RawFragment = "", ""
				target := urls[u.String()]
				localFile := base == "" && !u.IsAbs() && u.Host == ""
				if localFile {
					// Markdown keys are filenames, while href paths are URL-encoded.
					// Path is decoded once by url.Parse; the encoded string can
					// select a different file whose name contains a literal percent.
					target = urls[u.Path]
				}
				if target != "" {
					mapped := url.URL{Path: target, Fragment: fragment, RawFragment: rawFragment}
					if localFile {
						mapped.RawQuery, mapped.ForceQuery = u.RawQuery, u.ForceQuery
					}
					token.Attr[i].Val = mapped.String()
				} else if safeSourceURL(resolved) != "" {
					token.Attr[i].Val = resolved
				}
			}
		}
		out.WriteString(token.String())
	}
	return out.String(), images
}
func sourceTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, e := time.Parse(layout, value); e == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid source date")
}
func fileTitle(name string) string { return strings.TrimSuffix(name, path.Ext(name)) }
