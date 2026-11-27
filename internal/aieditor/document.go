package aieditor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/net/html"
	"newpress.local/newpress/internal/contenthtml"
)

const MaxOutputBytes = 512 << 10

type Document struct {
	Title         string   `json:"title"`
	Slug          string   `json:"slug"`
	Excerpt       string   `json:"excerpt"`
	Tags          []string `json:"tags"`
	CategoryIDs   []int64  `json:"category_ids"`
	CoverPrompt   string   `json:"cover_prompt"`
	Markdown      string   `json:"markdown"`
	ContentSource string   `json:"content_source"`
	Warnings      []string `json:"warnings"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM)) // Raw HTML remains disabled.

func truncate(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func metadataText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	return truncate(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value), limit)
}

func ParseDocument(raw string, categories []Category) (Document, error) {
	var doc Document
	if len(raw) > MaxOutputBytes || !utf8.ValidString(raw) {
		return doc, errors.New("生成内容过长或编码无效，请缩短篇幅后重试")
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	// Tolerate the common extra Markdown fence without stripping article code blocks.
	for _, fence := range []string{"```markdown\n", "```md\n"} {
		if strings.HasPrefix(raw, fence) && strings.HasSuffix(raw, "\n```") {
			raw = strings.TrimSuffix(strings.TrimPrefix(raw, fence), "\n```")
			break
		}
	}
	if strings.HasPrefix(raw, "<newpress-meta>") {
		end := strings.Index(raw, "</newpress-meta>")
		if end < 0 || end > 16<<10 {
			return doc, errors.New("文章信息未完整返回，已保留输出；请重新生成或在 Markdown 中修正后填入")
		}
		var meta struct {
			Title       string   `json:"title"`
			Slug        string   `json:"slug"`
			Excerpt     string   `json:"excerpt"`
			Tags        []string `json:"tags"`
			CategoryIDs []int64  `json:"category_ids"`
			CoverPrompt string   `json:"cover_prompt"`
		}
		if err := json.Unmarshal([]byte(raw[len("<newpress-meta>"):end]), &meta); err != nil {
			doc.Warnings = append(doc.Warnings, "文章信息格式不完整，已从正文提取标题与摘要，请检查后填入。")
		} else {
			doc.Title, doc.Slug, doc.Excerpt = meta.Title, meta.Slug, meta.Excerpt
			doc.Tags, doc.CategoryIDs, doc.CoverPrompt = meta.Tags, meta.CategoryIDs, meta.CoverPrompt
		}
		raw = strings.TrimSpace(raw[end+len("</newpress-meta>"):])
	}
	doc.Markdown = raw
	return PrepareDocument(doc, categories)
}

// PrepareDocument is also used after manual edits, so preview and insertion
// always share the server's renderer and sanitization policy.
func PrepareDocument(doc Document, categories []Category) (Document, error) {
	if len(doc.Markdown) > MaxOutputBytes || !utf8.ValidString(doc.Markdown) || len(doc.Tags) > 20 || len(doc.CategoryIDs) > 8 {
		return Document{}, errors.New("正文或文章信息超过限制")
	}
	doc.Markdown = strings.TrimSpace(doc.Markdown)
	first, rest, _ := strings.Cut(doc.Markdown, "\n")
	if strings.HasPrefix(first, "# ") {
		if strings.TrimSpace(doc.Title) == "" {
			doc.Title = strings.TrimSpace(strings.TrimPrefix(first, "# "))
		}
		if strings.TrimSpace(strings.TrimPrefix(first, "# ")) == strings.TrimSpace(doc.Title) {
			doc.Markdown = strings.TrimSpace(rest)
		}
	}
	var rendered bytes.Buffer
	if err := markdown.Convert([]byte(doc.Markdown), &rendered); err != nil {
		return Document{}, errors.New("无法转换 Markdown 正文，请检查格式")
	}
	// Text models cannot verify image URLs. Never mount remote or invented images
	// in the preview; the normal media picker is responsible for actual images.
	tokens := html.NewTokenizer(strings.NewReader(contenthtml.Sanitize(rendered.String())))
	var safe, plain strings.Builder
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			if tokens.Err() != io.EOF {
				return Document{}, errors.New("正文格式无效")
			}
			break
		}
		rawToken := string(tokens.Raw())
		if kind == html.TextToken {
			plain.Write(tokens.Text())
		}
		if kind == html.StartTagToken || kind == html.SelfClosingTagToken {
			name, _ := tokens.TagName()
			if string(name) == "img" {
				continue
			}
		}
		safe.WriteString(rawToken)
	}
	doc.ContentSource = safe.String()
	text := strings.Join(strings.Fields(plain.String()), " ")
	if strings.TrimSpace(text) == "" {
		return Document{}, errors.New("没有可填入的正文，已保留输出；请调整需求后重试")
	}
	if strings.TrimSpace(doc.Title) == "" {
		doc.Title = truncate(text, 36)
		doc.Warnings = append(doc.Warnings, "模型未提供标题，已根据正文生成，请检查。")
	}
	doc.Title = metadataText(doc.Title, 200)
	doc.Slug = strings.ToLower(strings.TrimSpace(doc.Slug))
	if len(doc.Slug) > 128 || !slugPattern.MatchString(doc.Slug) {
		doc.Slug = fmt.Sprintf("article-%x", sha256.Sum256([]byte(doc.Title)))[:24]
	}
	if strings.TrimSpace(doc.Excerpt) == "" {
		doc.Excerpt = truncate(text, 150)
	}
	doc.Excerpt, doc.CoverPrompt = metadataText(doc.Excerpt, 500), metadataText(doc.CoverPrompt, 1000)
	cleanTags, seenTags := []string{}, map[string]bool{}
	for _, tag := range doc.Tags {
		tag = metadataText(strings.ReplaceAll(strings.ReplaceAll(tag, ",", " "), "，", " "), 80)
		key := strings.ToLower(tag)
		if tag != "" && !seenTags[key] {
			cleanTags = append(cleanTags, tag)
			seenTags[key] = true
		}
	}
	doc.Tags = cleanTags
	allowed, seenIDs := map[int64]bool{}, map[int64]bool{}
	for _, category := range categories {
		allowed[category.ID] = true
	}
	ids := []int64{}
	for _, id := range doc.CategoryIDs {
		if allowed[id] && !seenIDs[id] {
			ids = append(ids, id)
			seenIDs[id] = true
		}
	}
	doc.CategoryIDs = ids
	if doc.Warnings == nil {
		doc.Warnings = []string{}
	}
	return doc, nil
}
