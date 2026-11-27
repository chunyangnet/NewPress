package httpserver

import (
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/shop"
)

func (s *Server) shopReviewQuery(w http.ResponseWriter, r *http.Request, admin bool) (shop.ReviewQuery, bool) {
	allowed := map[string]bool{"rating": true, "page": true, "sort": true, "replied": true}
	if admin {
		allowed["q"], allowed["status"], allowed["product"] = true, true, true
	}
	values, ok := s.strictAPIQuery(w, r, allowed)
	if !ok {
		return shop.ReviewQuery{}, false
	}
	q := shop.ReviewQuery{Product: r.PathValue("product"), Page: 1, Sort: values.Get("sort"), Search: values.Get("q"), Status: values.Get("status")}
	if admin {
		q.Product = values.Get("product")
	}
	for _, field := range []string{"page", "rating"} {
		if value, exists := values[field]; exists {
			n, err := strconv.Atoi(value[0])
			if err != nil || field == "page" && (n < 1 || n > 10000) || field == "rating" && (n < 0 || n > 5) {
				s.shopError(w, r, shop.ErrReviewInvalid)
				return q, false
			}
			if field == "page" {
				q.Page = n
			} else {
				q.Rating = n
			}
		}
	}
	if values.Has("replied") {
		if values.Get("replied") != "true" && values.Get("replied") != "false" {
			s.shopError(w, r, shop.ErrReviewInvalid)
			return q, false
		}
		q.WithReply = values.Get("replied") == "true"
	}
	return q, true
}
func (s *Server) shopProductReviews(w http.ResponseWriter, r *http.Request) {
	q, ok := s.shopReviewQuery(w, r, false)
	if !ok {
		return
	}
	result, err := s.shop.ProductReviews(r.Context(), q)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) shopOrderReview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, false)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	review, eligible, note, err := s.shop.OrderReview(r.Context(), p.UserID, r.PathValue("order"))
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"review": review, "eligible": eligible, "note": note})
}
func (s *Server) shopCreateReview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsUser(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var in shop.ReviewInput
	if !s.decodeShop(w, r, &in, 16<<10) {
		return
	}
	review, err := s.shop.CreateReview(r.Context(), p.UserID, r.PathValue("order"), p.Label(), in)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"review": review})
}
func (s *Server) adminShopReviews(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	q, ok := s.shopReviewQuery(w, r, true)
	if !ok {
		return
	}
	result, err := s.shop.AdminReviews(r.Context(), q)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) adminShopReview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.paymentsAdmin(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var in shop.ReviewModeration
	if !s.decodeShop(w, r, &in, 20<<10) {
		return
	}
	review, err := s.shop.ModerateReview(r.Context(), p.UserID, r.PathValue("review"), in)
	if err != nil {
		s.shopError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"review": review})
}
