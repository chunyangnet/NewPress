package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

const (
	adminContentAPIRequestMaxBytes = 4 << 20
	adminContentAPIMaxPageSize     = 100
)

type adminSessionAPIResponse struct {
	Authenticated    bool       `json:"authenticated"`
	ReadOnly         bool       `json:"read_only"`
	UserID           int64      `json:"user_id,omitempty"`
	Username         string     `json:"username,omitempty"`
	DisplayName      string     `json:"display_name,omitempty"`
	Role             string     `json:"role,omitempty"`
	Roles            []string   `json:"roles,omitempty"`
	CSRFToken        string     `json:"csrf_token,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Capabilities     []string   `json:"capabilities"`
	RoleCapabilities []string   `json:"role_capabilities,omitempty"`
}

type adminContentSummaryAPIResponse struct {
	Pinned        bool                     `json:"pinned"`
	ArticleBadge  content.ArticleBadge     `json:"article_badge"`
	ID            int64                    `json:"id"`
	AuthorID      int64                    `json:"author_id"`
	Type          string                   `json:"type"`
	Status        string                   `json:"status"`
	Slug          string                   `json:"slug"`
	Title         string                   `json:"title"`
	Excerpt       string                   `json:"excerpt"`
	CoverURL      string                   `json:"cover_url,omitempty"`
	AuthorName    string                   `json:"author_name"`
	Category      string                   `json:"category"`
	Categories    []contentTermAPIResponse `json:"categories"`
	Tags          []contentTermAPIResponse `json:"tags"`
	CommentStatus string                   `json:"comment_status"`
	PublishedAt   *time.Time               `json:"published_at"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
	Version       int64                    `json:"version"`
	ETag          string                   `json:"etag"`
	Permalink     string                   `json:"permalink"`
	IsPublic      bool                     `json:"is_public"`
}

type adminContentAPIResponse struct {
	Resources     resources.Config         `json:"resources"`
	Pinned        bool                     `json:"pinned"`
	ArticleBadge  content.ArticleBadge     `json:"article_badge"`
	ID            int64                    `json:"id"`
	AuthorID      int64                    `json:"author_id"`
	Type          string                   `json:"type"`
	Status        string                   `json:"status"`
	Slug          string                   `json:"slug"`
	Title         string                   `json:"title"`
	Excerpt       string                   `json:"excerpt"`
	ContentSource string                   `json:"content_source"`
	ContentHTML   string                   `json:"content_html"`
	CoverURL      string                   `json:"cover_url,omitempty"`
	AuthorName    string                   `json:"author_name"`
	Category      string                   `json:"category"`
	Categories    []contentTermAPIResponse `json:"categories"`
	Tags          []contentTermAPIResponse `json:"tags"`
	CommentStatus string                   `json:"comment_status"`
	PublishedAt   *time.Time               `json:"published_at"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
	Version       int64                    `json:"version"`
	ETag          string                   `json:"etag"`
	Permalink     string                   `json:"permalink"`
	IsPublic      bool                     `json:"is_public"`
}

type adminContentPaginationAPIResponse struct {
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
	Total    int    `json:"total"`
	Pages    int    `json:"pages"`
	Previous string `json:"previous,omitempty"`
	Next     string `json:"next,omitempty"`
}

type adminContentsAPIResponse struct {
	Items        []adminContentSummaryAPIResponse  `json:"items"`
	Pagination   adminContentPaginationAPIResponse `json:"pagination"`
	StatusCounts map[string]int                    `json:"status_counts"`
	TypeCounts   map[string]int                    `json:"type_counts"`
}

type adminRevisionAPIResponse struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	Version   int64     `json:"version"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

type adminRevisionsAPIResponse struct {
	Items          []adminRevisionAPIResponse `json:"items"`
	Count          int                        `json:"count"`
	CurrentVersion int64                      `json:"current_version"`
	CurrentETag    string                     `json:"current_etag"`
}

type apiOptionalString struct {
	Present bool
	Value   string
}

func (value *apiOptionalString) UnmarshalJSON(data []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("must be a JSON string, not null")
	}
	return json.Unmarshal(data, &value.Value)
}

type apiOptionalTimeText struct {
	Present bool
	Null    bool
	Value   string
}

type apiOptionalInt64Slice struct {
	Present bool
	Value   []int64
}

func (value *apiOptionalInt64Slice) UnmarshalJSON(data []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("must be a JSON array, not null")
	}
	return json.Unmarshal(data, &value.Value)
}

type apiOptionalStringSlice struct {
	Present bool
	Value   []string
}

func (value *apiOptionalStringSlice) UnmarshalJSON(data []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("must be a JSON array, not null")
	}
	return json.Unmarshal(data, &value.Value)
}

type contentTermAPIResponse struct {
	ID       int64  `json:"id"`
	Taxonomy string `json:"taxonomy"`
	ParentID int64  `json:"parent_id,omitempty"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Version  int64  `json:"version"`
}

func (value *apiOptionalTimeText) UnmarshalJSON(data []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.Null = true
		value.Value = ""
		return nil
	}
	return json.Unmarshal(data, &value.Value)
}

type adminContentMutationAPIRequest struct {
	Resources    *resources.Config     `json:"resources"`
	Pinned       *bool                 `json:"pinned"`
	ArticleBadge *content.ArticleBadge `json:"article_badge"`
	Meta         *struct {
		AIProMax apiOptionalString `json:"ai_pro_max"`
	} `json:"meta"`
	Type          apiOptionalString      `json:"type"`
	Status        apiOptionalString      `json:"status"`
	Slug          apiOptionalString      `json:"slug"`
	Title         apiOptionalString      `json:"title"`
	Excerpt       apiOptionalString      `json:"excerpt"`
	ContentSource apiOptionalString      `json:"content_source"`
	CoverURL      apiOptionalString      `json:"cover_url"`
	CategoryIDs   apiOptionalInt64Slice  `json:"category_ids"`
	Tags          apiOptionalStringSlice `json:"tags"`
	CommentStatus apiOptionalString      `json:"comment_status"`
	PublishedAt   apiOptionalTimeText    `json:"published_at"`
}

func (request adminContentMutationAPIRequest) Empty() bool {
	return request.Resources == nil && request.Pinned == nil && request.ArticleBadge == nil && (request.Meta == nil || !request.Meta.AIProMax.Present) && !request.Type.Present && !request.Status.Present && !request.Slug.Present &&
		!request.Title.Present && !request.Excerpt.Present && !request.ContentSource.Present &&
		!request.CoverURL.Present && !request.CategoryIDs.Present && !request.Tags.Present && !request.CommentStatus.Present &&
		!request.PublishedAt.Present
}
