package content

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// TextPatch changes only explicitly supplied text. In particular an empty
// excerpt clears it; omitted text is neither trimmed nor re-sanitized.
type TextPatch struct {
	Title         *string `json:"title,omitempty"`
	Excerpt       *string `json:"excerpt,omitempty"`
	ContentSource *string `json:"content_source,omitempty"`
}

func (patch TextPatch) Apply(current Post) (Post, bool, error) {
	fields := map[string]string{}
	if patch.Title == nil && patch.Excerpt == nil && patch.ContentSource == nil {
		fields["patch"] = "请明确指定要修改的文字字段"
	}
	next := current
	if patch.Title != nil {
		next.Title = strings.TrimSpace(*patch.Title)
		if !utf8.ValidString(*patch.Title) || next.Title == "" || utf8.RuneCountInString(next.Title) > MaxTitleRunes || containsControl(next.Title) {
			fields["title"] = fmt.Sprintf("标题须为 1 至 %d 个字符且不能包含控制字符", MaxTitleRunes)
		}
	}
	if patch.Excerpt != nil {
		next.Excerpt = strings.TrimSpace(*patch.Excerpt)
		if !utf8.ValidString(*patch.Excerpt) || utf8.RuneCountInString(next.Excerpt) > MaxExcerptRunes || containsControlExceptWhitespace(next.Excerpt) {
			fields["excerpt"] = fmt.Sprintf("摘要不能超过 %d 个字符或包含控制字符", MaxExcerptRunes)
		}
	}
	if patch.ContentSource != nil {
		if !utf8.ValidString(*patch.ContentSource) || len(*patch.ContentSource) > MaxContentBytes {
			fields["content_source"] = "正文须为有效 UTF-8，且不能超过 2 MiB"
		} else {
			next.ContentSource = strings.TrimSpace(*patch.ContentSource)
			next.ContentHTML = sanitizeContent(next.ContentSource)
			normalizeFlash(&PostInput{Type: current.Type, ContentSource: next.ContentSource}, fields)
		}
	}
	if len(fields) > 0 {
		return Post{}, false, &ValidationError{Fields: fields}
	}
	changed := next.Title != current.Title || next.Excerpt != current.Excerpt ||
		next.ContentSource != current.ContentSource || next.ContentHTML != current.ContentHTML
	return next, changed, nil
}
