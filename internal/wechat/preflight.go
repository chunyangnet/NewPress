package wechat

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

var ErrCheckBusy = errors.New("正在准备公众号图片，请稍后再检查或重试")

type CheckIssue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type SyncCheck struct {
	PostID        int64        `json:"post_id"`
	Version       int64        `json:"version"`
	ConfigVersion int64        `json:"config_version"`
	Title         string       `json:"title"`
	AutoPublish   bool         `json:"auto_publish"`
	Ready         bool         `json:"ready"`
	TitleChars    int          `json:"title_chars"`
	ContentChars  int          `json:"content_chars"`
	ContentBytes  int          `json:"content_bytes"`
	Images        int          `json:"images"`
	CheckedImages int          `json:"checked_images"`
	UploadBytes   int64        `json:"upload_bytes"`
	Issues        []CheckIssue `json:"issues"`
	Warnings      []string     `json:"warnings"`
	CheckedAt     time.Time    `json:"checked_at"`
}

type syncImage struct {
	key, source string
	cover       bool
	nodes       []*html.Node
}

type syncPlan struct {
	article Article
	body    *html.Node
	images  []syncImage
	issues  []CheckIssue
}

func syncablePost(post content.Post) bool {
	return post.Type == content.TypePost && (post.Status == content.StatusDraft || post.Status == content.StatusPublish || post.Status == content.StatusFuture)
}

// CheckSync uses the same local preparation as the worker, but never requests
// a token, uploads media, creates a job, or changes stored content or settings.
// Temporary JPEGs are removed before the check returns.
func (s *Service) CheckSync(ctx context.Context, id, version, configVersion int64) (SyncCheck, error) {
	var result SyncCheck
	cfg, err := ReadConfig(ctx, s.Store)
	if err != nil {
		return result, err
	}
	if cfg.Version != configVersion || configVersion < 1 {
		return result, ErrConflict
	}
	if !cfg.Enabled {
		return result, ErrDisabled
	}
	post, err := s.Content.GetByID(ctx, id)
	if err != nil {
		return result, err
	}
	if post.Version != version {
		return result, ErrConflict
	}
	if !syncablePost(post) {
		return result, errors.New("仅支持检查文章草稿、定时文章或已发布文章")
	}
	plan, err := buildSyncPlan(cfg, post, s.PublicURL)
	if err != nil {
		return result, err
	}
	result = SyncCheck{PostID: id, Version: version, ConfigVersion: cfg.Version, Title: post.Title, AutoPublish: cfg.AutoPublish, TitleChars: utf8.RuneCountInString(post.Title), ContentChars: utf8.RuneCountInString(plan.article.Content), ContentBytes: len(plan.article.Content), Images: len(plan.images), Issues: append([]CheckIssue{}, plan.issues...), Warnings: []string{}, CheckedAt: time.Now().UTC()}
	if len(plan.images) > 0 {
		result.Warnings = append(result.Warnings, "正文长度暂不含上传后的微信图片地址，发送时会再次核对。")
	}
	if utf8.RuneCountInString(post.Excerpt) > 120 {
		result.Warnings = append(result.Warnings, "摘要将使用前 120 字，正文不会自动截断。")
	}
	if cfg.Author == "" && utf8.RuneCountInString(post.AuthorName) > 16 {
		result.Warnings = append(result.Warnings, "作者名称将使用前 16 字。")
	}
	if cfg.IncludeSource && !content.PublicAt(post, time.Now()) {
		result.Warnings = append(result.Warnings, "文章尚未公开，本次不附加“阅读原文”入口。")
	}
	if len(plan.issues) == 0 {
		images, issues, err := s.stageSyncImages(ctx, plan.images, nil)
		if images != nil {
			defer images.close()
			result.CheckedImages = images.checked
			result.UploadBytes = images.bytes
		}
		if err != nil {
			return SyncCheck{}, err
		}
		result.Issues = append(result.Issues, issues...)
	}
	// Long image preparation must not approve a version that changed meanwhile.
	current, err := s.Content.GetByID(ctx, id)
	if err != nil {
		return SyncCheck{}, err
	}
	latest, err := ReadConfig(ctx, s.Store)
	if err != nil {
		return SyncCheck{}, err
	}
	if current.Version != version || !syncablePost(current) || latest.Version != cfg.Version {
		return SyncCheck{}, ErrConflict
	}
	result.Ready = len(result.Issues) == 0
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func buildSyncPlan(cfg Config, post content.Post, publicURL string) (*syncPlan, error) {
	p := &syncPlan{article: Article{Type: "news", Title: post.Title, Author: cfg.Author, Digest: trimRunes(post.Excerpt, 120), Thumb: cfg.ThumbMediaID}, issues: []CheckIssue{}}
	issue := func(field, message string) { p.issues = append(p.issues, CheckIssue{field, message}) }
	// The remote copy cannot follow local withdrawals. Until an explicit export
	// snapshot is part of the send contract, never silently send empty shells or
	// expand revocable content into a permanent external copy.
	if len(contenthtml.SnippetReferences(post.ContentHTML)) > 0 || len(contenthtml.SnippetReferences(post.ContentSource)) > 0 {
		issue("content", "文章含共享片段引用，暂不能同步到公众号；请先在独立稿件中整理并确认要发送的完整正文")
	}
	if p.article.Author == "" {
		p.article.Author = trimRunes(post.AuthorName, 16)
	}
	if utf8.RuneCountInString(post.Title) > 32 {
		issue("title", "公众号标题最多 32 字，请先缩短本站标题")
	}
	if cfg.OpenComment {
		p.article.OpenComment = 1
		if cfg.FansComment {
			p.article.FansComment = 1
		}
	}
	if cfg.IncludeSource && content.PublicAt(post, time.Now()) {
		p.article.SourceURL = strings.TrimRight(publicURL, "/") + content.PublicPath(post)
	}
	if len(p.article.SourceURL) > 1024 {
		issue("source", "阅读原文地址超过微信限制")
	}
	// Relative links belong to the original article, not the site homepage.
	// Fragment links also point back there because outgoing markup drops IDs.
	documentURL, documentErr := url.Parse(strings.TrimRight(publicURL, "/") + content.PublicPath(post))
	indices := map[string]int{}
	addImage := func(source string, cover bool, node *html.Node) {
		key := source
		if cover {
			key = "cover:" + source
		}
		index, exists := indices[key]
		if !exists {
			index = len(p.images)
			indices[key] = index
			p.images = append(p.images, syncImage{key: key, source: source, cover: cover})
		}
		if node != nil {
			p.images[index].nodes = append(p.images[index].nodes, node)
		}
	}
	if post.CoverURL != "" {
		addImage(post.CoverURL, true, nil)
	} else if !validRemoteID(p.article.Thumb) {
		issue("cover", "请设置本站封面，或在账号设置中填写有效的默认永久封面素材 ID")
	}
	root, err := html.Parse(strings.NewReader(post.ContentHTML))
	if err != nil {
		return nil, errors.New("文章正文无法解析")
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "body" {
				p.body = n
			}
			if n.Data == "img" {
				var source, alt string
				for _, attr := range n.Attr {
					if attr.Key == "src" {
						source = attr.Val
					}
					if attr.Key == "alt" {
						alt = attr.Val
					}
				}
				n.Attr = []html.Attribute{{Key: "src", Val: ""}, {Key: "alt", Val: alt}}
				addImage(source, false, n)
			} else {
				attrs := []html.Attribute{}
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						u, err := url.Parse(attr.Val)
						if err == nil && u.Scheme == "" && documentErr == nil {
							attr.Val = documentURL.ResolveReference(u).String()
						}
						attrs = append(attrs, attr)
					} else if attr.Key == "colspan" || attr.Key == "rowspan" {
						attrs = append(attrs, attr)
					}
				}
				n.Attr = attrs
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	if len(p.images) > 40 {
		issue("images", "单篇最多同步 40 项图片，封面与正文分别计数")
	}
	if err = p.render(); err != nil {
		return nil, err
	}
	if err = validSyncBody(p.article.Content); err != nil {
		issue("content", err.Error())
	}
	return p, nil
}

func (p *syncPlan) render() error {
	if p.body == nil {
		return errors.New("文章正文为空")
	}
	var out strings.Builder
	for ch := p.body.FirstChild; ch != nil; ch = ch.NextSibling {
		if err := html.Render(&out, ch); err != nil {
			return err
		}
	}
	p.article.Content = out.String()
	return nil
}

func validSyncBody(body string) error {
	if strings.TrimSpace(body) == "" || utf8.RuneCountInString(body) >= 20000 || len(body) >= 1<<20 {
		return errors.New("公众号正文须少于 2 万字符且小于 1 MiB，请先精简内容")
	}
	return nil
}

type stagedSyncImages struct {
	dir     string
	files   map[string]string
	bytes   int64
	checked int
}

func (s *stagedSyncImages) close() {
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// Stage every uncached image before the first upload. JPEGs are bounded to less
// than 1 MiB each and spooled to a private temporary directory, keeping only one
// decoded source image in memory. One local preparation runs per service.
func (s *Service) stageSyncImages(ctx context.Context, images []syncImage, cached map[string]string) (*stagedSyncImages, []CheckIssue, error) {
	if !s.localPreparation.TryLock() {
		return nil, nil, ErrCheckBusy
	}
	defer s.localPreparation.Unlock()
	staged := &stagedSyncImages{files: map[string]string{}}
	issues := []CheckIssue{}
	for i, ref := range images {
		if err := ctx.Err(); err != nil {
			staged.close()
			return nil, nil, err
		}
		if cached[ref.key] != "" {
			continue
		}
		var data []byte
		var err error
		if s.Media == nil {
			err = errors.New("媒体库暂不可用")
		} else {
			data, err = s.localImage(ref.source)
		}
		if err == nil {
			data, err = JPEG(data)
		}
		if canceled := ctx.Err(); canceled != nil {
			staged.close()
			return nil, nil, canceled
		}
		staged.checked++
		if err != nil {
			label := fmt.Sprintf("第 %d 项图片", i+1)
			if ref.cover {
				label = "封面"
			}
			issues = append(issues, CheckIssue{"images", label + "：" + err.Error()})
			continue
		}
		if staged.dir == "" {
			staged.dir, err = os.MkdirTemp("", "newpress-wechat-")
			if err != nil {
				return nil, nil, errors.New("图片准备目录暂不可用")
			}
		}
		name := filepath.Join(staged.dir, fmt.Sprintf("%02d.jpg", i))
		if err = os.WriteFile(name, data, 0600); err != nil {
			staged.close()
			return nil, nil, errors.New("图片准备文件暂不可写")
		}
		staged.files[ref.key] = name
		staged.bytes += int64(len(data))
	}
	return staged, issues, nil
}

func (s *Service) prepare(ctx context.Context, cfg Config, j *Job, post content.Post) (Article, error) {
	plan, err := buildSyncPlan(cfg, post, s.PublicURL)
	if err != nil {
		return Article{}, err
	}
	if len(plan.issues) > 0 {
		return plan.article, errors.New(plan.issues[0].Message)
	}
	staged, issues, err := s.stageSyncImages(ctx, plan.images, j.Images)
	if err != nil {
		return plan.article, err
	}
	defer staged.close()
	if len(issues) > 0 {
		return plan.article, errors.New(issues[0].Message)
	}
	if err = s.stillCurrent(ctx, cfg, j); err != nil {
		return plan.article, err
	}
	if j.Images == nil {
		j.Images = map[string]string{}
	}
	for _, ref := range plan.images {
		target := j.Images[ref.key]
		if target == "" {
			if len(j.Images) >= 40 {
				return plan.article, errors.New("单篇最多同步 40 项图片")
			}
			data, err := os.ReadFile(staged.files[ref.key])
			if err != nil {
				return plan.article, errors.New("准备好的图片暂不可读取，请重试")
			}
			if err = s.stillCurrent(ctx, cfg, j); err != nil {
				return plan.article, err
			}
			target, err = s.Client.upload(ctx, cfg, data, ref.cover, func(ctx context.Context) error { return s.stillCurrent(ctx, cfg, j) })
			if err != nil {
				return plan.article, err
			}
			j.Images[ref.key] = target
			if err = s.checkpoint(ctx, j); err != nil {
				return plan.article, err
			}
		}
		if ref.cover {
			plan.article.Thumb = target
		}
		for _, node := range ref.nodes {
			node.Attr[0].Val = target
		}
	}
	if err = plan.render(); err != nil {
		return plan.article, err
	}
	return plan.article, validSyncBody(plan.article.Content)
}
