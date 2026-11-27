// Package layout is the shared catalog for homepage rendering and visual editing.
package layout

import (
	"slices"
	"strings"
)

// The registry is also the layout editor's catalog. Templates dispatch only
// these known keys; custom content is sanitized by the theme's HTML policy.
type Block struct {
	Custom                        *CustomBlock
	Unavailable                   string
	Key, Title, Description, Icon string
	Visibility                    string
	Requires                      string
	Setting                       string
}

type Region struct {
	Key, Title, Order string
	Modules           []Block
}

var regions = []Region{
	{Key: "top", Title: "首屏与频道", Modules: []Block{
		{Key: "hero", Title: "封面精选", Description: "主视觉与两篇推荐文章", Icon: "image"},
		{Key: "notice", Title: "社区公告", Description: "可编辑文案与跳转链接", Icon: "megaphone"},
		{Key: "channels", Title: "快捷频道", Description: "发现、专题、圈子与个人阅读入口", Icon: "compass"},
		{Key: "categories", Title: "分类导航", Description: "封面与公开文章数量", Icon: "layout-grid"},
		{Key: "reading_guide", Title: "入门导读", Description: "为新访客提供清晰的阅读入口", Icon: "compass", Visibility: "hidden"},
		{Key: "carousel", Title: "图片轮播", Description: "四张可编辑幻灯片，支持滑动与自动播放", Icon: "image", Visibility: "hidden"},
		{Key: "search_banner", Title: "背景搜索栏", Description: "动态渐变、图片或视频背景与热门搜索", Icon: "search", Visibility: "hidden"},
		{Key: "custom", Title: "自定义 HTML", Description: "图文、链接和表格组成的自定义内容块", Icon: "code-2", Visibility: "hidden"},
	}},
	{Key: "main", Title: "内容区域", Modules: []Block{
		{Key: "feed", Title: "文章信息流", Description: "分类、排序、列表 / 卡片与分页", Icon: "newspaper"},
		{Key: "series", Requires: "series", Title: "专题与系列", Description: "已公开且有文章的专题", Icon: "library"},
		{Key: "spotlight", Title: "推荐阅读", Description: "指定推荐文章或最新发布", Icon: "sparkles"},
		{Key: "community", Requires: "community", Title: "发现圈子", Description: "公开圈子、成员与加入入口", Icon: "orbit"},
	}},
	{Key: "aside", Title: "侧栏小工具", Modules: []Block{
		{Key: "focus_list", Setting: "home_focus_list_visibility", Title: "图文焦点与列表", Description: "小图配精选文章列表，适合侧栏", Icon: "panels-top-left", Visibility: "hidden"},
		{Key: "topic_banner", Setting: "home_topic_banner_visibility", Title: "专题横幅", Description: "短比例图片、专题文案与跳转入口", Icon: "image", Visibility: "hidden"},
		{Key: "entry_cards", Setting: "home_entry_cards_visibility", Title: "卡片入口", Description: "四个可编辑的图标入口，紧凑排列", Icon: "layout-grid", Visibility: "hidden"},
		{Key: "image_grid", Setting: "home_image_grid_visibility", Title: "网格图文推荐", Description: "精选文章封面，一大两小网格", Icon: "layout-grid", Visibility: "hidden"},
		{Key: "notice_list", Setting: "home_notice_list_visibility", Title: "公告列表", Description: "独立公告管理中的公开通知、日期与置顶", Icon: "megaphone", Visibility: "hidden"},
		{Key: "welcome", Title: "会员卡片", Description: "会员状态、成长进度与账号入口", Icon: "badge-check"},
		{Key: "checkin", Title: "签到卡片", Description: "展示真实奖励与下次可签到时间", Icon: "calendar-check"},
		{Key: "updated", Title: "最近更新", Description: "按更新时间展示公开文章", Icon: "clock-3", Visibility: "hidden"},
		{Key: "recent", Title: "最近发布", Description: "按发布时间排列的文章", Icon: "clock-3"},
		{Key: "authors", Title: "遇见创作者", Description: "近期公开内容的作者", Icon: "users"},
		{Key: "tags", Title: "探索标签", Description: "有公开文章的标签", Icon: "hash"},
		{Key: "stats", Title: "社区一览", Description: "文章、分类和专题的实际数量", Icon: "chart-no-axes-column"},
		{Key: "subscribe", Title: "订阅更新", Description: "RSS 与账号订阅设置", Icon: "rss"},
	}},
}

// Catalog returns independent descriptors; callers cannot mutate the registry.
func Catalog() []Region {
	result := slices.Clone(regions)
	for i := range result {
		result[i].Modules = slices.Clone(result[i].Modules)
	}
	return result
}

// Resolve appends unplaced blocks in catalog order.
// Each known block is placed exactly once, with explicit placements taking priority.
func Resolve(values map[string]any) []Region {
	result := catalogFor(values)
	defaults := catalogFor(values)
	blocks := map[string]Block{}
	used := map[string]bool{}
	for i := range result {
		for _, b := range result[i].Modules {
			if b.Visibility == "" {
				b.Visibility = "both"
			}
			if v, ok := values["home_"+b.Key+"_visibility"].(string); ok && b.Custom == nil && slices.Contains([]string{"both", "desktop", "mobile", "hidden"}, v) {
				b.Visibility = v
			}
			blocks[b.Key] = b
		}
		result[i].Modules = nil
	}
	for i := range result {
		order, _ := values["home_"+result[i].Key+"_order"].(string)
		for _, key := range strings.Split(order, ",") {
			key = strings.TrimSpace(key)
			if b, ok := blocks[key]; ok && !used[key] {
				result[i].Modules = append(result[i].Modules, b)
				used[key] = true
			}
		}
	}
	for i, region := range defaults {
		for _, b := range region.Modules {
			if !used[b.Key] {
				result[i].Modules = append(result[i].Modules, blocks[b.Key])
				used[b.Key] = true
			}
		}
	}
	for i := range result {
		keys := []string{}
		for _, b := range result[i].Modules {
			keys = append(keys, b.Key)
		}
		result[i].Order = strings.Join(keys, ",")
	}
	return result
}
func Validate(values map[string]any) map[string]string {
	errors := map[string]string{}
	if raw, ok := values["home_custom_blocks"].(string); ok {
		if _, err := ParseCustomBlocks(raw); err != nil {
			errors["home_custom_blocks"] = err.Error()
			return errors
		}
	}
	known, seen := map[string]bool{}, map[string]bool{}
	for _, r := range catalogFor(values) {
		for _, b := range r.Modules {
			known[b.Key] = true
		}
	}
	for _, r := range regions {
		field := "home_" + r.Key + "_order"
		raw, _ := values[field].(string)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		for _, key := range strings.Split(raw, ",") {
			key = strings.TrimSpace(key)
			if !known[key] {
				errors[field] = "包含未知布局模块"
			} else if seen[key] {
				errors[field] = "同一模块只能放置一次，请检查其他区域"
			}
			seen[key] = true
		}
	}
	return errors
}
func Requested(values map[string]any, key string) bool {
	for _, r := range Resolve(values) {
		for _, b := range r.Modules {
			if b.Key == key {
				if r.Key == "aside" {
					if enabled, ok := values["sidebar_enabled"].(bool); ok && !enabled {
						return false
					}
				}
				return b.Visibility != "hidden"
			}
		}
	}
	return false
}
