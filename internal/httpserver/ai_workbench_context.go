package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/content"
)

func workbenchText(value string, limit int) string {
	text, _ := workbenchTextPrefix(value, limit)
	return text
}

func workbenchTextPrefix(value string, limit int) (string, bool) {
	if limit < 0 {
		panic("negative workbench text limit")
	}
	if len(value) <= limit {
		return value, false
	}
	// Scan only the retained prefix; articles may be much larger than the
	// preview. Keep the former malformed UTF-8 normalization on truncation.
	for end := range value {
		if limit == 0 {
			prefix := value[:end]
			if !utf8.ValidString(prefix) {
				prefix = string([]rune(prefix))
			}
			return prefix + "…", true
		}
		limit--
	}
	return value, false
}

type workbenchArticle struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Excerpt     string    `json:"excerpt"`
	Category    string    `json:"category"`
	Tags        []string  `json:"tags"`
	HasCover    bool      `json:"has_cover"`
	PublishedAt time.Time `json:"published_at"`
}

func workbenchArticleView(post content.Post) workbenchArticle {
	tags := []string{}
	for _, tag := range post.Tags {
		if len(tags) < 10 {
			tags = append(tags, workbenchText(tag.Name, 80))
		}
	}
	return workbenchArticle{post.ID, workbenchText(post.Title, 200), workbenchText(post.Excerpt, 300), workbenchText(post.Category, 80), tags, post.CoverURL != "", post.PublishedAt}
}

// Construct a separate allowlisted projection. Never marshal the full system
// status, configuration, article, analytics report or request records upstream.
func (s *Server) workbenchPreview(ctx context.Context, request aiworkbench.Request) (aiworkbench.Preview, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	task, _ := aiworkbench.FindTask(request.Task)
	location := s.siteLocation(ctx)
	now := time.Now().In(location)
	days := request.Days
	if task.ID == "operations" || task.ID == "summary" || task.ID == "seo" {
		days = 1
	}
	preview := aiworkbench.Preview{Task: task.ID, Title: task.Title, Scope: task.Scope, Start: now.AddDate(0, 0, 1-days).Format("2006-01-02"), End: now.Format("2006-01-02"), Timezone: location.String(), Focus: strings.TrimSpace(request.Focus)}
	facts := map[string]any{"snapshot_at": now.Format(time.RFC3339), "scope": task.Scope}
	sources := &workbenchSources{server: s, versions: make(map[int64]int64)}
	switch request.Task {
	case "operations":
		system, err := s.systemStatusReportChecked(ctx)
		if err != nil {
			return preview, errors.New("系统状态暂时无法采集，请稍后重试")
		}
		checks := []map[string]any{}
		for _, c := range system.Checks {
			checks = append(checks, map[string]any{"service": c.Label, "status": c.Status, "latency_ms": c.LatencyMS})
		}
		metrics := []map[string]any{}
		for _, m := range system.Metrics {
			metrics = append(metrics, map[string]any{"metric": m.Label, "available": m.Available, "value": m.Value, "unit": m.Unit, "status": m.Tone})
		}
		support := s.buildSupportReport(ctx)
		facts["system"] = map[string]any{"version": system.Version, "uptime": system.Uptime, "checks": checks, "metrics": metrics, "storage": support.Storage, "configuration": support.Configuration, "counts": support.Counts, "count_checks": support.Checks}
		facts["limitations"] = "当前采样；不包含历史资源趋势、服务器日志、备份结果或外网连通性探测。"
	case "summary":
		if request.PostID == 0 {
			facts["material"] = strings.TrimSpace(request.Material)
		} else {
			post, err := s.repository.GetByID(ctx, request.PostID)
			if err != nil || !content.PublicAt(post, now) || post.Type != content.TypePost {
				return preview, errors.New("文章不存在或已不再公开，请重新选择")
			}
			body := post.ContentSource
			if body == "" {
				body = post.ContentHTML
			}
			facts["article"] = workbenchArticleView(post)
			facts["body"], facts["truncated"] = workbenchTextPrefix(body, 10000)
			facts["source_version"] = post.Version
			sources.add(post.ID, post.Version)
		}
	case "seo", "planning":
		posts, err := s.repository.ListPublishedPage(ctx, 20, 0)
		if err != nil {
			return preview, errors.New("公开文章暂时无法读取，请稍后重试")
		}
		articles := []workbenchArticle{}
		for _, post := range posts.Items {
			if post.Type == content.TypePost && content.PublicAt(post, now) {
				articles = append(articles, workbenchArticleView(post))
				sources.add(post.ID, post.Version)
			}
		}
		facts["recent_articles"], facts["public_article_total"] = articles, posts.Total
		facts["sample_note"] = "最近最多 20 篇公开文章的当前元数据；非所选日期内新增量，不含全文。"
	}
	if request.Task == "briefing" || request.Task == "planning" {
		if lease, enabled := s.siteSettings.ModuleContext("analytics"); !enabled {
			facts["analytics"] = map[string]any{"available": false, "reason": "运营统计模块未启用"}
		} else if report, err := s.analyticsReport(ctx, request.Days); err != nil {
			facts["analytics"] = map[string]any{"available": false, "reason": "访问统计暂时不可用，不能将缺失数据解释为零"}
		} else {
			sources.analytics = lease
			articles := []map[string]any{}
			for i, a := range report.Articles {
				if i < 10 {
					sources.add(a.ID, a.sourceVersion)
					articles = append(articles, map[string]any{"id": a.ID, "title": workbenchText(a.Title, 200), "views_in_period": a.Views, "likes_current": a.Likes, "bookmarks_current": a.Bookmarks, "approved_comments_current": a.Comments})
				}
			}
			facts["analytics"] = map[string]any{"available": true, "collecting": report.Enabled, "start": report.Start, "end": report.End, "views": report.Views, "searches": report.Searches, "misses": report.Misses, "trend": report.Trend, "sources": report.Sources, "articles": articles, "dropped_since_start": report.Dropped, "note": "访问量不是独立访客；互动计数是当前累计值，不是区间新增。采集关闭、丢弃、保留期及缺失历史可能影响完整性。"}
		}
	}
	if request.Task == "costs" || request.Task == "briefing" {
		report := s.aiTelemetry.Report(aieditor.UsageFilter{Start: preview.Start, End: preview.End, Limit: 1})
		models := []map[string]any{}
		for i, m := range report.Models {
			if i < 10 {
				models = append(models, map[string]any{"provider": m.Provider, "model": m.Model, "totals": m.Totals})
			}
		}
		trend := []map[string]any{}
		for _, day := range report.Trend {
			trend = append(trend, map[string]any{"day": day.Day, "requests": day.Requests, "tokens": day.Tokens, "usage_known": day.UsageKnown, "cost_known": day.CostKnown, "costs": day.Costs})
		}
		facts["ai_usage"] = map[string]any{"start": preview.Start, "end": preview.End, "collecting": report.Config.Enabled, "retention_days": report.Config.RetentionDays, "totals": report.Totals, "models_top10": models, "trend": trend, "collapsed_buckets": report.Collapsed, "persist_error": report.PersistError, "note": "总量包含失败与取消。仅汇总已报告用量；unknown 不等于 0，费用为分币种估算，不含余额与凭据。模型排名受最多 50 个聚合结果限制。"}
	}
	if ctx.Err() != nil {
		return preview, errors.New("资料采集超时，请稍后重试")
	}
	encoded, err := json.Marshal(facts)
	if err != nil || len(encoded) > aiworkbench.MaxContextBytes {
		return preview, errors.New("资料超出上限，请缩短日期范围或材料")
	}
	preview.Context = string(encoded)
	if len(sources.versions) != 0 || sources.analytics != nil {
		preview = preview.WithSourceCheck(sources.check)
	}
	return preview, nil
}
