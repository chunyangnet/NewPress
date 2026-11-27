package content

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SeriesKindTopic    = "topic"
	SeriesKindSequence = "series"
	MaxSeriesChapters  = 200
	MaxSeries          = 1000
)

// Series is an editorial selection. PostIDs preserve the editor's order;
// public reads return only the IDs of currently public articles.
type Series struct {
	SeriesInput
	ID        int64     `json:"id"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SeriesInput struct {
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Title       string  `json:"title"`
	Slug        string  `json:"slug"`
	Description string  `json:"description"`
	CoverURL    string  `json:"cover_url"`
	PostIDs     []int64 `json:"post_ids"`
}

type SeriesQuery struct {
	Kind, Search  string
	PublicOnly    bool
	PostID        int64
	Limit, Offset int
}

type SeriesList struct {
	Items []Series `json:"items"`
	Total int      `json:"total"`
}

type SeriesRepository interface {
	ListSeries(context.Context, SeriesQuery) (SeriesList, error)
	GetSeries(context.Context, int64) (Series, error)
	SaveSeries(context.Context, int64, Mutation, SeriesInput) (Series, error)
	DeleteSeries(context.Context, int64, Mutation) error
	PublishedSeries(context.Context, string) (Series, []Post, error)
}

func (s Series) URL() string { return "/series/" + s.Slug + "/" }
func (s Series) Count() int  { return len(s.PostIDs) }
func (s Series) Label() string {
	if s.Kind == SeriesKindSequence {
		return "系列"
	}
	return "专题"
}

func NormalizeSeriesInput(input SeriesInput) (SeriesInput, map[string]string) {
	fields := map[string]string{}
	if !validateUTF8Fields(fields, map[string]string{
		"kind": input.Kind, "status": input.Status, "title": input.Title,
		"slug": input.Slug, "description": input.Description, "cover_url": input.CoverURL,
	}) {
		return input, fields
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Slug = NormalizeTermSlug(input.Slug)
	input.Description = strings.TrimSpace(input.Description)
	input.CoverURL = strings.TrimSpace(input.CoverURL)
	input.PostIDs = append([]int64{}, input.PostIDs...)
	if input.Kind != SeriesKindTopic && input.Kind != SeriesKindSequence {
		fields["kind"] = "请选择专题或系列"
	}
	if input.Status != StatusDraft && input.Status != StatusPublish {
		fields["status"] = "请选择草稿或公开"
	}
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 120 || containsControl(input.Title) {
		fields["title"] = "标题需要 1–120 个字符，且不能包含控制字符"
	}
	if !ValidTermSlug(input.Slug) {
		fields["slug"] = "请填写有效的访问别名（中文、字母、数字与连字符）"
	}
	if utf8.RuneCountInString(input.Description) > 2000 || containsControlExceptWhitespace(input.Description) {
		fields["description"] = "介绍不能超过 2000 个字符或包含非法控制字符"
	}
	if err := validateCoverURL(input.CoverURL); err != nil {
		fields["cover_url"] = err.Error()
	}
	if len(input.PostIDs) > MaxSeriesChapters {
		fields["post_ids"] = "每个专题或系列最多收录 200 篇文章"
	}
	seen := map[int64]bool{}
	for _, id := range input.PostIDs {
		if id <= 0 || seen[id] {
			fields["post_ids"] = "文章编号必须有效且不能重复"
		}
		seen[id] = true
	}
	return input, fields
}

func NormalizeSeriesQuery(q SeriesQuery) (SeriesQuery, error) {
	q.Search = strings.TrimSpace(q.Search)
	if q.Kind != "" && q.Kind != SeriesKindTopic && q.Kind != SeriesKindSequence || q.PostID < 0 || !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > 100 || containsControl(q.Search) {
		return q, errors.New("invalid series query")
	}
	if q.Limit <= 0 {
		q.Limit = 24
	}
	q.Limit = min(q.Limit, 100)
	q.Offset = max(q.Offset, 0)
	return q, nil
}

func EqualSeriesInput(a, b SeriesInput) bool {
	return a.Kind == b.Kind && a.Status == b.Status && a.Title == b.Title && a.Slug == b.Slug && a.Description == b.Description && a.CoverURL == b.CoverURL && slices.Equal(a.PostIDs, b.PostIDs)
}

func cloneSeries(s Series) Series { s.PostIDs = append([]int64{}, s.PostIDs...); return s }
