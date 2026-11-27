package httpserver

import (
	"net/http"
	"strings"
)

func (s *Server) shopPage(w http.ResponseWriter, r *http.Request) {
	if s.shop == nil {
		s.renderServiceStatus(w, 503, "商城", "unavailable")
		return
	}
	if r.URL.Path != "/shop/orders/" {
		catalog, err := s.shop.Catalog(r.Context())
		if err != nil {
			s.renderServiceStatus(w, 503, "商城", "unavailable")
			return
		}
		if !catalog.Enabled {
			s.renderServiceStatus(w, 404, "商城", "disabled")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "form-action 'self'", "form-action 'self' https:", 1))
	title, description := "商城", "查看商品价格、购买方案与交付安排。"
	if strings.HasPrefix(r.URL.Path, "/shop/product/") {
		title, description = "商品详情", "查看商品规格、价格、优惠与交付安排。"
	} else if r.URL.Path == "/shop/orders/" {
		title, description = "我的订单", "查看付款记录、商品交付、物流与开奖结果。"
	}
	data := s.baseViewFor(w, title, description, r.URL.Path)
	data.BodyClass, data.ActiveNav = "is-shop", "shop"
	if key := r.PathValue("product"); key != "" && s.shop != nil {
		if catalog, err := s.shop.Catalog(r.Context()); err == nil && catalog.Enabled {
			for _, p := range catalog.Products {
				if p.Key == key && p.Enabled {
					s.prepareShopDescription(r.Context(), &data, p)
					break
				}
			}
		}
	}
	s.render(w, http.StatusOK, "shop", data)
}
