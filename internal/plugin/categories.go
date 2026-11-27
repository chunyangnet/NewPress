package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// Category is presentation metadata, independent of runtime permissions.
type Category struct {
	Slug string
	Name string
	Icon string
}

var categoryPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// Categories returns a copy in the stable order used by the plugin directory.
func Categories() []Category {
	return []Category{
		{Slug: "content", Name: "内容创作", Icon: "pen-line"},
		{Slug: "comments", Name: "评论互动", Icon: "message-circle"},
		{Slug: "appearance", Name: "外观展示", Icon: "panels-top-left"},
		{Slug: "seo", Name: "搜索优化", Icon: "search"},
		{Slug: "analytics", Name: "统计分析", Icon: "activity"},
		{Slug: "integration", Name: "服务集成", Icon: "link"},
		{Slug: "tools", Name: "站点工具", Icon: "sliders-horizontal"},
		{Slug: "other", Name: "其他插件", Icon: "code-2"},
	}
}

// CategoryFor keeps existing signed packages unchanged. Unknown future category
// IDs remain valid metadata and are displayed under Other by older hosts.
func CategoryFor(manifest Manifest) Category {
	slug := manifest.Category
	if slug == "" {
		switch manifest.Slug {
		case CommentOrigin:
			slug = "comments"
		case "content-age":
			slug = "content"
		}
	}
	categories := Categories()
	for _, category := range categories {
		if category.Slug == slug {
			return category
		}
	}
	return categories[len(categories)-1]
}

func validateDiscoveryMetadata(manifest Manifest) error {
	if manifest.Category != "" && (len(manifest.Category) > 40 || !categoryPattern.MatchString(manifest.Category)) {
		return fmt.Errorf("plugin category must be a lowercase ID of at most 40 characters")
	}
	if len(manifest.Tags) > 8 {
		return fmt.Errorf("plugin tags must contain at most 8 entries")
	}
	seen := make(map[string]bool, len(manifest.Tags))
	for _, tag := range manifest.Tags {
		key := strings.ToLower(tag)
		if !validManifestText(tag, 24, true) || strings.ContainsAny(tag, "\n\r\t") || seen[key] {
			return fmt.Errorf("plugin tags must be unique, single-line labels of 1–24 characters")
		}
		seen[key] = true
	}
	return nil
}
