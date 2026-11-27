package shop

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

var (
	ErrReviewInvalid     = errors.New("invalid shop review")
	ErrReviewEligibility = errors.New("shop review requires a completed purchase")
	ErrReviewExists      = errors.New("shop order already reviewed")
	ErrReviewConflict    = errors.New("shop review changed")
)

type ReviewInput struct {
	Rating    int    `json:"rating"`
	Content   string `json:"content"`
	Anonymous bool   `json:"anonymous"`
}

type Review struct {
	ID      string `json:"id"`
	OrderID string `json:"order_id"`
	UserID  int64  `json:"user_id,string"`
	Product string `json:"product"`
	Title   string `json:"title"`
	Variant string `json:"variant"`
	Author  string `json:"author"`
	ReviewInput
	CreatedAt    time.Time `json:"created_at"`
	Reply        string    `json:"reply"`
	RepliedAt    time.Time `json:"replied_at"`
	Hidden       bool      `json:"hidden"`
	HiddenReason string    `json:"hidden_reason"`
	UpdatedAt    time.Time `json:"updated_at"`
	ModeratorID  int64     `json:"moderator_id,string"`
	Version      int64     `json:"version,string"`
}

// PublicReview deliberately excludes account IDs, order IDs and moderation notes.
type PublicReview struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Variant string `json:"variant"`
	ReviewInput
	CreatedAt time.Time `json:"created_at"`
	Reply     string    `json:"reply"`
	RepliedAt time.Time `json:"replied_at"`
}

type ReviewSummary struct {
	Count        int     `json:"count"`
	Average      float64 `json:"average"`
	Positive     int     `json:"positive"`
	Distribution [5]int  `json:"distribution"`
}
type ProductStats struct {
	ReviewSummary
	Sold int `json:"sold"`
}
type ReviewQuery struct {
	Product, Search, Status, Sort string
	Rating, Page                  int
	WithReply                     bool
}
type ReviewPage struct {
	Reviews  []PublicReview `json:"reviews"`
	Summary  ReviewSummary  `json:"summary"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}
type AdminReviewPage struct {
	Reviews  []Review `json:"reviews"`
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}
type ReviewModeration struct {
	Version int64  `json:"version,string"`
	Reply   string `json:"reply"`
	Hidden  bool   `json:"hidden"`
	Reason  string `json:"reason"`
}

func reviewInputOK(in ReviewInput) bool {
	return in.Rating >= 1 && in.Rating <= 5 && strings.TrimSpace(in.Content) != "" && textOK(in.Content, 2000)
}
func validateReviews(st State) error {
	if len(st.Reviews) > 100000 {
		return ErrInvalid
	}
	orders := map[string]bool{}
	for id, r := range st.Reviews {
		o, ok := st.Orders[r.OrderID]
		if id != r.ID || !strings.HasPrefix(id, "sr_") || !ok || orders[r.OrderID] || r.UserID != o.UserID || r.Product != o.Product || r.Title != o.Title || !nameOK(r.Author, 160) || !textOK(r.Variant, 160) || !reviewInputOK(r.ReviewInput) || r.Version < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || o.CompletedAt.IsZero() || r.CreatedAt.Before(o.CompletedAt) || o.PaidAmount != o.Total || !textOK(r.Reply, 2000) || (r.Reply == "") != r.RepliedAt.IsZero() || !r.RepliedAt.IsZero() && r.RepliedAt.Before(r.CreatedAt) || !textOK(r.HiddenReason, 500) || r.Hidden && strings.TrimSpace(r.HiddenReason) == "" || !r.Hidden && r.HiddenReason != "" || r.ModeratorID < 0 {
			return ErrInvalid
		}
		orders[r.OrderID] = true
	}
	return nil
}
func reviewEligible(o Order) bool {
	return o.Status == "completed" && o.PaidAmount == o.Total && !o.CompletedAt.IsZero()
}
func publicReview(r Review) PublicReview {
	author := "匿名买家"
	if !r.Anonymous {
		runes := []rune(r.Author)
		if len(runes) > 0 {
			author = string(runes[0]) + "***"
		}
		if len(runes) > 1 {
			author += string(runes[len(runes)-1])
		}
	}
	return PublicReview{ID: r.ID, Author: author, Variant: r.Variant, ReviewInput: r.ReviewInput, CreatedAt: r.CreatedAt, Reply: r.Reply, RepliedAt: r.RepliedAt}
}

func (s *Service) CreateReview(ctx context.Context, user int64, orderID, author string, in ReviewInput) (PublicReview, error) {
	in.Content = strings.TrimSpace(in.Content)
	if user < 1 || !reviewInputOK(in) || !nameOK(author, 160) {
		return PublicReview{}, ErrReviewInvalid
	}
	var result Review
	err := s.Backend.Update(ctx, func(st *State) error {
		o, ok := st.Orders[orderID]
		if !ok || o.UserID != user {
			return ErrNotFound
		}
		for _, r := range st.Reviews {
			if r.OrderID == orderID {
				return ErrReviewExists
			}
		}
		if !reviewEligible(o) {
			return ErrReviewEligibility
		}
		variant := ""
		for _, v := range o.Snapshot.Variants {
			if v.Key == o.Variant {
				variant = v.Title
			}
		}
		now := s.now()
		result = Review{ID: newID("sr_"), OrderID: o.ID, UserID: user, Product: o.Product, Title: o.Title, Variant: variant, Author: author, ReviewInput: in, CreatedAt: now, UpdatedAt: now, Version: 1}
		st.Reviews[result.ID] = result
		return nil
	})
	return publicReview(result), err
}

func (s *Service) OrderReview(ctx context.Context, user int64, id string) (*PublicReview, bool, string, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return nil, false, "", err
	}
	o, ok := st.Orders[id]
	if !ok || user < 1 || o.UserID != user {
		return nil, false, "", ErrNotFound
	}
	for _, r := range st.Reviews {
		if r.OrderID == id {
			v, note := publicReview(r), ""
			if r.Hidden {
				note = "此评价已隐藏：" + r.HiddenReason
			}
			return &v, false, note, nil
		}
	}
	return nil, reviewEligible(o), "订单完成后可评价，每笔订单可提交一次。", nil
}

func summarizeReviews(st State) map[string]ProductStats {
	stats := map[string]ProductStats{}
	for _, p := range st.Catalog.Products {
		if st.Catalog.Enabled && p.Enabled {
			stats[p.Key] = ProductStats{}
		}
	}
	for _, o := range st.Orders {
		if v, ok := stats[o.Product]; ok && o.Status == "completed" {
			v.Sold += o.Quantity
			stats[o.Product] = v
		}
	}
	for _, r := range st.Reviews {
		if v, ok := stats[r.Product]; ok && !r.Hidden {
			v.Count++
			v.Average += float64(r.Rating)
			v.Distribution[r.Rating-1]++
			if r.Rating >= 4 {
				v.Positive++
			}
			stats[r.Product] = v
		}
	}
	for key, v := range stats {
		if v.Count > 0 {
			v.Average = math.Round(v.Average/float64(v.Count)*10) / 10
			stats[key] = v
		}
	}
	return stats
}
func (s *Service) ProductStatistics(ctx context.Context) (map[string]ProductStats, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return nil, err
	}
	return summarizeReviews(st), nil
}

func reviewSelection(st State, q ReviewQuery, admin bool) ([]Review, error) {
	if q.Rating < 0 || q.Rating > 5 || q.Page < 1 || q.Page > 10000 || !member(q.Sort, "", "latest", "high", "low") || !member(q.Status, "", "public", "hidden", "unreplied") || !textOK(q.Search, 100) {
		return nil, ErrReviewInvalid
	}
	list := []Review{}
	for _, r := range st.Reviews {
		if !admin && r.Hidden || q.Product != "" && r.Product != q.Product || q.Rating > 0 && r.Rating != q.Rating || q.WithReply && r.Reply == "" || q.Status == "public" && r.Hidden || q.Status == "hidden" && !r.Hidden || q.Status == "unreplied" && r.Reply != "" {
			continue
		}
		if q.Search != "" && !strings.Contains(strings.ToLower(r.Content+" "+r.Title+" "+r.OrderID), strings.ToLower(q.Search)) {
			continue
		}
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if q.Sort == "high" && list[i].Rating != list[j].Rating {
			return list[i].Rating > list[j].Rating
		}
		if q.Sort == "low" && list[i].Rating != list[j].Rating {
			return list[i].Rating < list[j].Rating
		}
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID > list[j].ID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list, nil
}
func reviewWindow(list []Review, page int) []Review {
	start := min((page-1)*20, len(list))
	return list[start:min(start+20, len(list))]
}
func (s *Service) ProductReviews(ctx context.Context, q ReviewQuery) (ReviewPage, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return ReviewPage{}, err
	}
	p, ok := product(st.Catalog, q.Product)
	if !ok || !st.Catalog.Enabled || !p.Enabled {
		return ReviewPage{}, ErrNotFound
	}
	q.Search, q.Status = "", ""
	list, err := reviewSelection(st, q, false)
	if err != nil {
		return ReviewPage{}, err
	}
	result := ReviewPage{Reviews: []PublicReview{}, Summary: summarizeReviews(st)[q.Product].ReviewSummary, Total: len(list), Page: q.Page, PageSize: 20}
	for _, r := range reviewWindow(list, q.Page) {
		result.Reviews = append(result.Reviews, publicReview(r))
	}
	return result, nil
}
func (s *Service) AdminReviews(ctx context.Context, q ReviewQuery) (AdminReviewPage, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return AdminReviewPage{}, err
	}
	list, err := reviewSelection(st, q, true)
	if err != nil {
		return AdminReviewPage{}, err
	}
	return AdminReviewPage{Reviews: reviewWindow(list, q.Page), Total: len(list), Page: q.Page, PageSize: 20}, nil
}
func (s *Service) ModerateReview(ctx context.Context, admin int64, id string, in ReviewModeration) (Review, error) {
	in.Reply, in.Reason = strings.TrimSpace(in.Reply), strings.TrimSpace(in.Reason)
	if admin < 1 || in.Version < 1 || !textOK(in.Reply, 2000) || !textOK(in.Reason, 500) || in.Hidden && in.Reason == "" {
		return Review{}, ErrReviewInvalid
	}
	var result Review
	err := s.Backend.Update(ctx, func(st *State) error {
		r, ok := st.Reviews[id]
		if !ok {
			return ErrNotFound
		}
		if r.Version != in.Version {
			return ErrReviewConflict
		}
		if r.Reply != in.Reply {
			r.Reply = in.Reply
			r.RepliedAt = s.now()
			if r.Reply == "" {
				r.RepliedAt = time.Time{}
			}
		}
		r.Hidden, r.HiddenReason = in.Hidden, in.Reason
		if !r.Hidden {
			r.HiddenReason = ""
		}
		r.Version++
		r.UpdatedAt = s.now()
		r.ModeratorID = admin
		st.Reviews[id] = r
		result = r
		return nil
	})
	return result, err
}
