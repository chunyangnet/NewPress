package content

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	TaxonomyCategory = "category"
	TaxonomyTag      = "post_tag"

	MaxTermNameRunes        = 80
	MaxTermSlugBytes        = 160
	MaxTermDescriptionRunes = 500
	MaxTermSearchRunes      = 100
	MaxCategoriesPerPost    = 8
	MaxTagsPerPost          = 20
)

var (
	ErrTermNotFound          = errors.New("term not found")
	ErrTermVersionConflict   = errors.New("term version conflict")
	ErrTermSlugConflict      = errors.New("term slug conflict")
	ErrTermProtected         = errors.New("system term cannot be deleted")
	ErrTermHierarchyConflict = errors.New("term hierarchy conflict")
)

// Term is a reusable classification record. Categories are hierarchical while
// post tags are flat. Count is calculated from linked, non-trashed posts.
type Term struct {
	ID          int64     `json:"id"`
	Taxonomy    string    `json:"taxonomy"`
	ParentID    int64     `json:"parent_id,omitempty"`
	ParentName  string    `json:"parent_name,omitempty"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Count       int       `json:"count"`
	Depth       int       `json:"depth,omitempty"`
	System      bool      `json:"system,omitempty"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TermInput struct {
	Taxonomy    string
	ParentID    int64
	Slug        string
	Name        string
	Description string
}

type TermMutation struct {
	ExpectedVersion int64
	Actor           string
}

type TermQuery struct {
	Taxonomy string
	Search   string
	Limit    int
	Offset   int
}

type TermList struct {
	Items  []Term         `json:"items"`
	Total  int            `json:"total"`
	Counts map[string]int `json:"counts"`
}

func ValidTaxonomy(value string) bool {
	return value == TaxonomyCategory || value == TaxonomyTag
}

func NormalizeTermInput(input TermInput) (TermInput, map[string]string) {
	fields := make(map[string]string)
	if !validateUTF8Fields(fields, map[string]string{
		"taxonomy": input.Taxonomy, "name": input.Name, "slug": input.Slug, "description": input.Description,
	}) {
		return input, fields
	}
	input.Taxonomy = strings.ToLower(strings.TrimSpace(input.Taxonomy))
	if !ValidTaxonomy(input.Taxonomy) {
		fields["taxonomy"] = "分类法必须是分类目录或文章标签"
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		fields["name"] = "名称不能为空"
	} else if utf8.RuneCountInString(input.Name) > MaxTermNameRunes || containsControl(input.Name) {
		fields["name"] = fmt.Sprintf("名称不能超过 %d 个字符或包含控制字符", MaxTermNameRunes)
	}
	input.Slug = NormalizeTermSlug(input.Slug)
	if input.Slug == "" {
		input.Slug = NormalizeTermSlug(input.Name)
	}
	if !ValidTermSlug(input.Slug) {
		fields["slug"] = "别名只能包含字母、数字和连字符，且不能超过 160 字节"
	}
	input.Description = strings.TrimSpace(input.Description)
	if utf8.RuneCountInString(input.Description) > MaxTermDescriptionRunes || containsControlExceptWhitespace(input.Description) {
		fields["description"] = fmt.Sprintf("说明不能超过 %d 个字符或包含非法控制字符", MaxTermDescriptionRunes)
	}
	if input.ParentID < 0 {
		fields["parent_id"] = "父级分类不正确"
	}
	if input.Taxonomy == TaxonomyTag && input.ParentID != 0 {
		fields["parent_id"] = "文章标签不支持父级关系"
	}
	return input, fields
}

func NormalizeTermQuery(query TermQuery) (TermQuery, error) {
	query.Taxonomy = strings.ToLower(strings.TrimSpace(query.Taxonomy))
	query.Search = strings.TrimSpace(query.Search)
	if query.Taxonomy != "" && !ValidTaxonomy(query.Taxonomy) {
		return TermQuery{}, errors.New("invalid taxonomy")
	}
	if !utf8.ValidString(query.Search) || containsControl(query.Search) {
		return TermQuery{}, errors.New("invalid term search query")
	}
	if utf8.RuneCountInString(query.Search) > MaxTermSearchRunes {
		return TermQuery{}, errors.New("term search query is too long")
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 500 {
		query.Limit = 500
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return query, nil
}

func NormalizeTermSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	result.Grow(min(len(value), MaxTermSlugBytes))
	separator := false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			if separator && result.Len() > 0 && result.Len() < MaxTermSlugBytes {
				result.WriteByte('-')
			}
			separator = false
			if result.Len()+utf8.RuneLen(character) <= MaxTermSlugBytes {
				result.WriteRune(character)
			}
		case character == '-' || character == '_' || unicode.IsSpace(character):
			separator = result.Len() > 0
		}
		if result.Len() >= MaxTermSlugBytes {
			break
		}
	}
	return strings.Trim(result.String(), "-")
}

func ValidTermSlug(value string) bool {
	if len(value) < 1 || len(value) > MaxTermSlugBytes || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' {
			continue
		}
		return false
	}
	return true
}

// TermSlugWithSuffix reserves room for a collision suffix without splitting a
// Unicode rune or leaving a second hyphen at the truncation boundary. Both
// storage implementations use this for automatically created terms.
func TermSlugWithSuffix(base string, suffix int) string {
	ending := "-" + strconv.Itoa(suffix)
	for len(base)+len(ending) > MaxTermSlugBytes && base != "" {
		_, size := utf8.DecodeLastRuneInString(base)
		base = strings.TrimRight(base[:len(base)-size], "-")
	}
	if base == "" {
		base = "term"
	}
	return base + ending
}

func NormalizeTagNames(values []string) ([]string, error) {
	result := make([]string, 0, min(len(values), MaxTagsPerPost))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		if !utf8.ValidString(raw) {
			return nil, errors.New("标签名称必须使用有效的 UTF-8 编码")
		}
		for _, candidate := range strings.FieldsFunc(raw, func(character rune) bool {
			return character == ',' || character == '，' || character == '\n' || character == '\r'
		}) {
			name := strings.TrimSpace(candidate)
			if name == "" {
				continue
			}
			if utf8.RuneCountInString(name) > MaxTermNameRunes || containsControl(name) {
				return nil, fmt.Errorf("标签名称不能超过 %d 个字符或包含控制字符", MaxTermNameRunes)
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, name)
			if len(result) > MaxTagsPerPost {
				return nil, fmt.Errorf("每篇文章最多设置 %d 个标签", MaxTagsPerPost)
			}
		}
	}
	return result, nil
}

func normalizePositiveIDs(values []int64) ([]int64, bool) {
	result := make([]int64, 0, len(values))
	seen := make(map[int64]bool, len(values))
	valid := true
	for _, value := range values {
		if value <= 0 || seen[value] {
			valid = false
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, valid
}

func termNames(terms []Term) []string {
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		result = append(result, term.Name)
	}
	return result
}
