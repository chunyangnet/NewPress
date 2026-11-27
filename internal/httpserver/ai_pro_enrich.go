package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	xhtml "golang.org/x/net/html"
	"html"
	"io"
	"net/url"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type aiRelatedTopic struct{ ID, Title, Body, Image string }
type aiEnrichment struct {
	Excerpt, Slug, ImagePrompt string
	Tags                       []string
	CategoryID                 int64
	Topics                     []aiRelatedTopic
}

var autoSlugPattern = regexp.MustCompile("^(post|page)-[0-9]{8}-[0-9]{6}(-[a-f0-9]{16})?$")

func needsCategory(p content.Post) bool {
	return p.Type == content.TypePost && (len(p.Categories) == 0 || (len(p.Categories) == 1 && (p.Category == "未分类" || p.Categories[0].Slug == "uncategorized")))
}
func bodyImages(body string) []string {
	z := xhtml.NewTokenizer(strings.NewReader(body))
	var links []string
	for len(links) < 16 {
		tok := z.Next()
		if tok == xhtml.ErrorToken {
			break
		}
		if tok != xhtml.StartTagToken && tok != xhtml.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "img" {
			continue
		}
		for _, a := range t.Attr {
			if a.Key == "src" && a.Val != "" {
				links = append(links, a.Val)
				break
			}
		}
	}
	return links
}
func enrichmentFields(p content.Post, r aijobs.Rules) []string {
	canChange := !r.SafeUpdate || !content.PublicAt(p, time.Now())
	fields := []string{}
	if r.Summary && content.DefaultExcerpt(p) {
		fields = append(fields, "excerpt")
	}
	if r.Tags && p.Type == content.TypePost && len(p.Tags) == 0 {
		fields = append(fields, "tags")
	}
	if r.Slug && canChange && (p.Slug == "" || autoSlugPattern.MatchString(p.Slug) || p.Slug == content.NormalizeSlug(p.Title)) {
		fields = append(fields, "slug")
	}
	if r.Category && canChange && needsCategory(p) {
		fields = append(fields, "categoryID")
	}
	if r.Topics && p.Meta["ai_pro_max_topics"] == nil {
		fields = append(fields, "topics")
	}
	if r.Cover && p.CoverURL == "" || r.Inline && len(bodyImages(p.ContentHTML)) == 0 {
		fields = append(fields, "imagePrompt")
	}
	return fields
}
func wantsEnrichment(p content.Post, r aijobs.Rules) bool { return len(enrichmentFields(p, r)) > 0 }
func (s *Server) localVision(p content.Post) (*aieditor.VisionImage, error) {
	for _, raw := range append([]string{p.CoverURL}, bodyImages(p.ContentHTML)...) {
		u, err := url.Parse(raw)
		if err != nil || u.Host != "" || u.Scheme != "" {
			continue
		}
		bits := strings.Split(strings.TrimPrefix(u.Path, "/media/"), "/")
		if !strings.HasPrefix(u.Path, "/media/") || len(bits) != 2 {
			continue
		}
		id, err := strconv.ParseInt(bits[0], 10, 64)
		if err != nil || id < 1 {
			continue
		}
		item, err := s.media.Get(id)
		if err != nil || item.CanProcessImage() != nil || item.SizeBytes > 4<<20 {
			continue
		}
		if u.EscapedPath() != item.PublicURL {
			continue
		}
		_, reader, err := s.media.Open(id)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, (4<<20)+1))
		reader.Close()
		if err != nil {
			return nil, err
		}
		if len(data) <= 4<<20 {
			return &aieditor.VisionImage{Data: data, MIME: item.MIMEType}, nil
		}
	}
	return nil, nil
}
func (s *Server) enrichment(ctx context.Context, j aijobs.Job, p content.Post) (string, error) {
	if p.Version != j.Work.PostVersion || p.Status == content.StatusTrash {
		return "", aijobs.ErrStale
	}
	r := j.Work.Rules
	fields := enrichmentFields(p, r)
	if !categoryMatches(p, r) && !slices.Contains(fields, "categoryID") {
		return "不在指定分类范围", nil
	}
	if len(fields) == 0 {
		return "已有完整字段", nil
	}
	categories, err := s.aiEditorCategories(ctx)
	if err != nil {
		return "", err
	}
	var image *aieditor.VisionImage
	if r.Vision {
		image, err = s.localVision(p)
		if err != nil {
			return "", err
		}
	}
	topicLimit := r.TopicLimit
	if topicLimit == 0 {
		topicLimit = 3
	}
	facts := map[string]any{"话题数量上限": topicLimit, "标题": p.Title, "正文": aiCommentContext(p.ContentSource, 14000), "需要的字段": fields, "已有分类": categories}
	messages := quotedMessages("只返回 JSON 对象。按需要的字段给出 excerpt 简短摘要、tags 最多 8 个标签、slug 英文短横线别名、categoryID 一个已有分类 ID、topics 按话题数量上限给出相关问题及有内容的解答 每项含 title 与 body、imagePrompt 一句画面描述。不要改写原文", facts, j.Language)
	text, err := s.autoText(ctx, j, messages, image, 4096)
	if err != nil {
		return "", err
	}
	var out aiEnrichment
	if err = decodeAIObject(text, &out); err != nil {
		return "", err
	}
	in := inputFromPost(p)
	matched := categoryMatches(p, r)
	count := 0
	if slices.Contains(fields, "categoryID") && slices.ContainsFunc(categories, func(c aieditor.Category) bool { return c.ID == out.CategoryID }) {
		in.CategoryIDs = []int64{out.CategoryID}
		in.Category = ""
		count++
		if r.CategoryID != 0 {
			matched = (out.CategoryID == r.CategoryID) != r.ExcludeCategory
		}
	}
	if matched {
		if slices.Contains(fields, "excerpt") && strings.TrimSpace(out.Excerpt) != "" && utf8.RuneCountInString(out.Excerpt) <= content.MaxExcerptRunes {
			in.Excerpt = out.Excerpt
			count++
		}
		if slices.Contains(fields, "slug") && content.ValidSlug(out.Slug) {
			in.Slug = out.Slug
			count++
		}
		if slices.Contains(fields, "tags") && len(out.Tags) > 0 {
			in.TagNames = out.Tags[:min(8, len(out.Tags))]
			count++
		}
		if slices.Contains(fields, "imagePrompt") {
			if !s.jobCapability(ctx, j, auth.CapabilityMediaWrite) {
				return "", aijobs.ErrActor
			}
			prompt := strings.TrimSpace(out.ImagePrompt)
			if prompt == "" {
				prompt = p.Title
			}
			item, err := s.autoImage(ctx, j, p.Title, prompt)
			if err != nil {
				return "", err
			}
			if r.Cover && p.CoverURL == "" {
				in.CoverURL = item.PublicURL
			}
			if r.Inline && len(bodyImages(p.ContentHTML)) == 0 {
				in.ContentSource += "\n<figure><img src=\"" + html.EscapeString(item.PublicURL) + "\" alt=\"" + html.EscapeString(p.Title) + "\"></figure>"
			}
			count++
		}
		if slices.Contains(fields, "topics") {
			var topics []aiRelatedTopic
			for _, v := range out.Topics[:min(len(out.Topics), topicLimit)] {
				v.Title = strings.TrimSpace(v.Title)
				v.Body = strings.TrimSpace(v.Body)
				if v.Title == "" || v.Body == "" || utf8.RuneCountInString(v.Title) > 200 || utf8.RuneCountInString(v.Body) > 5000 {
					continue
				}
				v.ID = aiDigest(v.Title)[:16]
				v.Image = in.CoverURL
				if !slices.ContainsFunc(topics, func(t aiRelatedTopic) bool { return t.ID == v.ID }) {
					if r.TopicImages {
						if !s.jobCapability(ctx, j, auth.CapabilityMediaWrite) {
							return "", aijobs.ErrActor
						}
						img, e := s.autoImage(ctx, j, v.Title, v.Title+"。"+workbenchText(v.Body, 450))
						if e != nil {
							return "", e
						}
						v.Image = img.PublicURL
					}
					topics = append(topics, v)
				}
			}
			if len(topics) > 0 {
				raw, _ := json.Marshal(topics)
				in.AITopics = string(raw)
				count++
			}
		}
	}
	if count == 0 {
		if !matched {
			return "不在指定分类范围", nil
		}
		return "", aijobs.ErrOutput
	}
	in.Actor = "AI Pro Max · " + j.Author
	err = s.aiJobs.Commit(ctx, j, func() error {
		if !s.jobCapability(ctx, j, auth.CapabilityContentWrite) {
			return aijobs.ErrActor
		}
		_, err := s.aiBaseContent().Update(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version, Actor: in.Actor}, in)
		return err
	})
	if errors.Is(err, content.ErrVersionConflict) {
		err = aijobs.ErrStale
	}
	return fmt.Sprintf("已处理 %d 项 · 文章 #%d", count, p.ID), err
}
func (s *Server) autoImage(ctx context.Context, j aijobs.Job, title, prompt string) (media.Item, error) {
	provider := s.aiSettings.Snapshot().DefaultImageProvider
	c, err := s.aiSettings.ResolveImage(aieditor.Connection{ProviderID: provider})
	if err != nil {
		return media.Item{}, aijobs.ErrModel
	}
	o := s.observeAI(provider, c, "image", j.UserID)
	o.record.Purpose = "pro_image"
	defer o.finish(ctx)
	imgs, err := s.aiImages.GenerateObserved(ctx, c, aieditor.ImageGeneration{Prompt: prompt, AspectRatio: "3:2"}, func(u aieditor.Usage, count int) { o.first(); o.record.Usage = u; o.record.Images = count })
	if err != nil {
		o.fail(err)
		return media.Item{}, err
	}
	if len(imgs) == 0 {
		return media.Item{}, aijobs.ErrOutput
	}
	if err := ctx.Err(); err != nil {
		return media.Item{}, err
	}
	img := imgs[0]
	item, err := s.media.Upload(ctx, bytes.NewReader(img.Data), media.UploadInput{UploadSource: "ai-pro-image", Title: title, Filename: "ai-" + j.ID + "." + img.Extension, UploadedBy: j.Author}, min(s.effectiveMaxUploadBytes(), aieditor.MaxImageBytes))
	if err != nil && !errors.Is(err, media.ErrDuplicate) {
		o.fail(err)
		return media.Item{}, err
	}
	if item.CanProcessImage() == nil && item.Image.Recipe == 0 {
		item, err = s.media.ProcessImage(ctx, item.ID, item.Version, media.ImageInput{FocalX: 50, FocalY: 50})
		if err != nil {
			return media.Item{}, err
		}
	}
	o.record.Status = "success"
	o.record.ErrorCode = ""
	return item, nil
}
