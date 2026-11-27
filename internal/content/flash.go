package content

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// FlashFields is the validated editorial metadata of a short news item.
// A nil input preserves metadata on ordinary status/revision operations.
type FlashFields struct {
	SourceName string `json:"source_name"`
	SourceURL  string `json:"source_url"`
	Important  bool   `json:"important"`
	ReviewNote string `json:"review_note,omitempty"`
}

func FlashMetadata(p Post) FlashFields {
	text := func(key string) string { v, _ := p.Meta[key].(string); return v }
	important, _ := p.Meta["flash_important"].(bool)
	return FlashFields{SourceName: text("flash_source_name"), SourceURL: text("flash_source_url"), Important: important, ReviewNote: text("flash_review_note")}
}

func normalizeFlash(input *PostInput, fields map[string]string) {
	if input.Type != TypeFlash {
		if input.Flash != nil {
			fields["flash"] = "只有快报可以设置来源与审核信息"
		}
		return
	}
	if strings.TrimSpace(input.ContentSource) == "" || utf8.RuneCountInString(input.ContentSource) > 5000 {
		fields["content_source"] = "快报正文应为 1 至 5000 个字符"
	}
	if input.Flash == nil {
		return
	}
	f := *input.Flash
	if !validateUTF8Fields(fields, map[string]string{
		"source_name": f.SourceName, "source_url": f.SourceURL, "review_note": f.ReviewNote,
	}) {
		return
	}
	f.SourceName, f.SourceURL, f.ReviewNote = strings.TrimSpace(f.SourceName), strings.TrimSpace(f.SourceURL), strings.TrimSpace(f.ReviewNote)
	if utf8.RuneCountInString(f.SourceName) > 100 || containsControl(f.SourceName) {
		fields["source_name"] = "来源名称不能超过 100 个字符或包含控制字符"
	}
	if utf8.RuneCountInString(f.ReviewNote) > 500 || containsControlExceptWhitespace(f.ReviewNote) {
		fields["review_note"] = "审核说明不能超过 500 个字符"
	}
	if f.SourceURL != "" {
		u, err := url.Parse(f.SourceURL)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(f.SourceURL) > 2048 || containsControl(f.SourceURL) {
			fields["source_url"] = "来源必须是有效的 HTTP 或 HTTPS 链接"
		}
	}
	input.Flash = &f
}

func ApplyFlashMetadata(meta map[string]any, input PostInput) {
	if input.Type != TypeFlash || input.Flash == nil {
		return
	}
	meta["flash_source_name"] = input.Flash.SourceName
	meta["flash_source_url"] = input.Flash.SourceURL
	meta["flash_important"] = input.Flash.Important
	meta["flash_review_note"] = input.Flash.ReviewNote
}
