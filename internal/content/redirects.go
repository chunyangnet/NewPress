package content

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxRedirects = 10000

var ErrRedirectConflict = errors.New("redirect path is already occupied")

type RedirectInput struct {
	Path   string `json:"path"`
	PostID int64  `json:"post_id"`
}

type Redirect struct {
	RedirectInput
	ID        int64     `json:"id"`
	Version   int64     `json:"version"`
	Automatic bool      `json:"automatic"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RedirectQuery struct {
	Search        string
	Limit, Offset int
}
type RedirectList struct {
	Items []Redirect `json:"items"`
	Total int        `json:"total"`
}

type RedirectRepository interface {
	ListRedirects(context.Context, RedirectQuery) (RedirectList, error)
	GetRedirect(context.Context, int64) (Redirect, error)
	SaveRedirect(context.Context, int64, Mutation, RedirectInput) (Redirect, error)
	DeleteRedirect(context.Context, int64, Mutation) error
	ResolveRedirect(context.Context, string) (string, error)
}

func PublicPath(p Post) string {
	if p.Type == TypeFlash {
		return "/flash/" + p.Slug + "/"
	}
	if p.Type == TypePage {
		return "/pages/" + p.Slug
	}
	return "/posts/" + p.Slug
}

// Paths are stored decoded, exactly as net/url.URL.Path. Encoded separators,
// queries and route namespaces are never a redirect instruction.
func ValidRedirectPath(value string) bool {
	if len(value) < 2 || len(value) > 2048 || value[0] != '/' || !utf8.ValidString(value) || strings.ContainsAny(value, "\\%?#") || strings.Contains(value, "//") {
		return false
	}
	for _, ch := range value {
		if unicode.IsControl(ch) || unicode.IsSpace(ch) {
			return false
		}
	}
	parts := strings.Split(strings.TrimSuffix(value[1:], "/"), "/")
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(parts[0]) {
	case "posts", "pages":
		return (parts[0] == "posts" || parts[0] == "pages") && len(parts) == 2 && !strings.HasSuffix(value, "/") && ValidSlug(parts[1])
	case "admin", "api", "account", "health", "media", "theme-assets", "core-assets", "theme-settings.css", "navigation.css", "favicon.ico", "robots.txt", "sitemap.xml", "feed.xml", "explore", "community", "authors", "categories", "tags", "series", "login", "logout", "register", "assets", "plugins", "preview":
		return false
	}
	return true
}

func NormalizeRedirectInput(input RedirectInput) (RedirectInput, map[string]string) {
	input.Path = strings.TrimSpace(input.Path)
	fields := map[string]string{}
	if !ValidRedirectPath(input.Path) {
		fields["path"] = "请输入站内旧路径；不能包含查询、片段、转义字符或系统保留路径"
	}
	if input.PostID < 1 {
		fields["post_id"] = "请选择有效的文章或页面编号"
	}
	return input, fields
}

func NormalizeRedirectQuery(q RedirectQuery) (RedirectQuery, error) {
	q.Search = strings.TrimSpace(q.Search)
	if !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > 100 || containsControl(q.Search) || q.Limit < 0 || q.Limit > 100 || q.Offset < 0 || q.Offset > MaxRedirects {
		return q, &ValidationError{Fields: map[string]string{"query": "旧链接筛选参数不正确"}}
	}
	if q.Limit == 0 {
		q.Limit = 30
	}
	return q, nil
}

func RedirectLimitError() error {
	return &ValidationError{Fields: map[string]string{"path": "旧链接已达到 10000 条上限，请清理不再需要的规则"}}
}
