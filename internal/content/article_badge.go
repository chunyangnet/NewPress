package content

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ArticleBadge uses a bounded vocabulary, never arbitrary CSS or HTML. A nil
// PostInput badge preserves metadata; an empty Kind explicitly removes it.
type ArticleBadge struct {
	Kind         string `json:"kind"`
	Text         string `json:"text,omitempty"`
	Style        string `json:"style,omitempty"`
	Tone         string `json:"tone,omitempty"`
	Icon         string `json:"icon,omitempty"`
	Placement    string `json:"placement,omitempty"`
	HideOnMobile bool   `json:"hide_on_mobile,omitempty"`
}

type ArticleBadgePreset struct {
	ArticleBadge
	Label string
}

type ArticleBadgeGroup struct {
	Name    string
	Presets []ArticleBadgePreset
}

type ArticleBadgeOption struct{ Value, Label string }

func ArticleBadgeStyles() []ArticleBadgeOption {
	return []ArticleBadgeOption{{"glass", "琉璃胶囊"}, {"soft", "柔光胶囊"}, {"solid", "经典实色"}, {"ribbon", "立体飘带"}, {"corner", "折角缎带"}, {"seal", "独家印章"}}
}

func ArticleBadgeTones() []ArticleBadgeOption {
	return []ArticleBadgeOption{{"blue", "海盐蓝"}, {"violet", "鸢尾紫"}, {"gold", "琥珀金"}, {"rose", "莓果粉"}, {"red", "珊瑚红"}, {"green", "薄荷绿"}, {"orange", "日落橙"}, {"ink", "曜石黑"}, {"silver", "珍珠白"}}
}

func ArticleBadgeIcons() []ArticleBadgeOption {
	return []ArticleBadgeOption{{"none", "无图标"}, {"newspaper", "文稿"}, {"star-fill", "星星"}, {"award", "奖章"}, {"diamond", "钻石"}, {"zap", "闪电"}, {"sparkles", "闪耀"}, {"megaphone", "活动"}, {"clock-3", "时间"}, {"pen-line", "创作"}, {"message-circle", "讨论"}, {"rocket", "火箭"}, {"cpu", "科技"}, {"chart-no-axes-column", "财经"}, {"library", "教育"}, {"coffee", "生活"}, {"play", "播放"}, {"activity", "运动"}, {"arrow-up-to-line", "置顶"}, {"check-circle", "完成"}, {"lock-keyhole", "私密"}, {"shield-check", "审核"}, {"trash-2", "回收站"}}
}

func ArticleBadgePresets() []ArticleBadgeGroup {
	preset := func(kind, text, style, tone, icon string) ArticleBadgePreset {
		return ArticleBadgePreset{ArticleBadge: ArticleBadge{Kind: kind, Text: text, Style: style, Tone: tone, Icon: icon, Placement: "top-left"}, Label: text}
	}
	return []ArticleBadgeGroup{
		{Name: "内容属性", Presets: []ArticleBadgePreset{
			preset("original", "原创", "glass", "blue", "newspaper"), preset("featured", "精选", "glass", "violet", "star-fill"),
			preset("quality", "优质", "glass", "gold", "award"), preset("recommended", "推荐", "glass", "ink", "diamond"),
			preset("popular", "热门", "glass", "silver", "zap"), preset("trending", "爆款", "glass", "red", "sparkles"),
		}},
		{Name: "活动运营", Presets: []ArticleBadgePreset{
			preset("event", "活动", "ribbon", "red", "megaphone"), preset("limited", "限时", "ribbon", "rose", "clock-3"),
			preset("debate", "限时热议", "ribbon", "orange", "zap"), preset("submission", "征稿", "ribbon", "blue", "pen-line"),
			preset("topic", "话题", "ribbon", "violet", "message-circle"), preset("challenge", "挑战赛", "ribbon", "green", "star-fill"),
		}},
		{Name: "内容领域", Presets: []ArticleBadgePreset{
			preset("technology", "科技", "soft", "blue", "cpu"), preset("finance", "财经", "soft", "gold", "chart-no-axes-column"),
			preset("education", "教育", "soft", "green", "library"), preset("life", "生活", "soft", "rose", "coffee"),
			preset("entertainment", "娱乐", "soft", "violet", "play"), preset("sports", "体育", "soft", "orange", "activity"),
		}},
		{Name: "风格创意", Presets: []ArticleBadgePreset{
			preset("essential", "精华", "corner", "violet", "star-fill"),
			preset("hot", "HOT", "corner", "red", "zap"), preset("new", "最新", "corner", "blue", "sparkles"),
			preset("exclusive", "独家", "seal", "violet", "diamond"), preset("custom", "自定义", "glass", "blue", "none"),
		}},
		{Name: "自动状态", Presets: []ArticleBadgePreset{preset("pinned", "置顶", "ribbon", "gold", "arrow-up-to-line"), preset("status", "跟随发布状态", "soft", "green", "check-circle")}},
	}
}

func NormalizeArticleBadge(b ArticleBadge) (ArticleBadge, map[string]string) {
	fields := make(map[string]string)
	if b.Kind == "" {
		return ArticleBadge{}, fields
	}
	var preset ArticleBadge
	for _, group := range ArticleBadgePresets() {
		for _, p := range group.Presets {
			if p.Kind == b.Kind {
				preset = p.ArticleBadge
			}
		}
	}
	if preset.Kind == "" {
		fields["article_badge_kind"] = "请选择支持的文章角标类型"
	}
	if !utf8.ValidString(b.Text) || utf8.RuneCountInString(b.Text) > 12 || strings.IndexFunc(b.Text, unicode.IsControl) >= 0 {
		fields["article_badge_text"] = "角标文字最多 12 个字符，不能包含控制字符"
	}
	b.Text = strings.TrimSpace(b.Text)
	if b.Text == "" {
		b.Text = preset.Text
	}
	validate := func(value *string, fallback, field string, options []ArticleBadgeOption) {
		if *value == "" {
			*value = fallback
		}
		if !slices.ContainsFunc(options, func(o ArticleBadgeOption) bool { return o.Value == *value }) {
			fields[field] = "请选择支持的角标选项"
		}
	}
	validate(&b.Style, preset.Style, "article_badge_style", ArticleBadgeStyles())
	validate(&b.Tone, preset.Tone, "article_badge_tone", ArticleBadgeTones())
	validate(&b.Icon, preset.Icon, "article_badge_icon", ArticleBadgeIcons())
	validate(&b.Placement, "top-left", "article_badge_placement", []ArticleBadgeOption{{Value: "top-left"}, {Value: "top-right"}})
	return b, fields
}

// ArticleBadgeConfig returns validated stored configuration, without projecting
// status text. Both typed values and JSON-decoded file/database maps are accepted.
func ArticleBadgeConfig(post Post) ArticleBadge {
	if stored, ok := post.Meta["article_badge"].(ArticleBadge); ok {
		b, fields := NormalizeArticleBadge(stored)
		if len(fields) == 0 {
			return b
		}
		return ArticleBadge{}
	}
	raw, err := json.Marshal(post.Meta["article_badge"])
	if err != nil || len(raw) > 2048 {
		return ArticleBadge{}
	}
	var b ArticleBadge
	if json.Unmarshal(raw, &b) != nil {
		return ArticleBadge{}
	}
	b, fields := NormalizeArticleBadge(b)
	if len(fields) != 0 {
		return ArticleBadge{}
	}
	return b
}

// ArticleBadge is the safe display projection. State labels always use the
// canonical post state, including after scheduling and revision restoration.
func (post Post) ArticleBadge() ArticleBadge {
	b := ArticleBadgeConfig(post)
	if post.IsPinned() {
		if b.Kind != "pinned" {
			placement, hide := b.Placement, b.HideOnMobile
			b, _ = NormalizeArticleBadge(ArticleBadge{Kind: "pinned", Placement: placement, HideOnMobile: hide})
		}
		b.Text = "置顶"
		return b
	}
	if b.Kind == "pinned" {
		return ArticleBadge{}
	}
	if b.Kind == "status" {
		switch post.Status {
		case StatusPublish:
			b.Text, b.Tone, b.Icon = "已发布", "green", "check-circle"
		case StatusDraft:
			b.Text, b.Tone, b.Icon = "草稿", "silver", "pen-line"
		case StatusPending:
			b.Text, b.Tone, b.Icon = "待审核", "gold", "shield-check"
		case StatusFuture:
			b.Text, b.Tone, b.Icon = "定时发布", "blue", "clock-3"
			if PublicAt(post, time.Now()) {
				b.Text, b.Tone, b.Icon = "已发布", "green", "check-circle"
			}
		case StatusPrivate:
			b.Text, b.Tone, b.Icon = "私密", "violet", "lock-keyhole"
		case StatusTrash:
			b.Text, b.Tone, b.Icon = "已下架", "orange", "trash-2"
		default:
			return ArticleBadge{}
		}
	}
	return b
}

// IsPinned is independent of badge selection. Only article records participate
// in pin ordering; draft/private visibility is still enforced by PublicAt.
func (post Post) IsPinned() bool {
	pinned, _ := post.Meta["pinned"].(bool)
	return post.Type == TypePost && pinned
}

func ApplyArticleBadgeMetadata(meta map[string]any, input PostInput) {
	if input.Pinned != nil {
		if *input.Pinned && input.Type == TypePost {
			meta["pinned"] = true
		} else {
			delete(meta, "pinned")
		}
	}
	if input.ArticleBadge == nil {
		return
	}
	if input.ArticleBadge.Kind == "" {
		delete(meta, "article_badge")
		return
	}
	meta["article_badge"] = *input.ArticleBadge
}
