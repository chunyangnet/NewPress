package httpserver

import (
	"context"
	"html"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/shop"
)

func shopDescriptionSource(p shop.Product) string {
	if strings.TrimSpace(p.DescriptionSource) != "" {
		if strings.Contains(p.DescriptionSource, "<") {
			return contenthtml.Sanitize(p.DescriptionSource)
		}
		return "<p>" + strings.ReplaceAll(html.EscapeString(p.DescriptionSource), "\n", "<br>") + "</p>"
	}
	text := p.Description
	if text == "" {
		text = p.Summary
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "<p>" + strings.ReplaceAll(html.EscapeString(text), "\n", "<br>") + "</p>"
}

func (s *Server) prepareShopDescription(ctx context.Context, data *pageView, p shop.Product) {
	// Resolve public article cards with the same visibility checks as an article,
	// while keeping the product separate from article metadata and SEO.
	fragment := pageView{Post: content.Post{ContentHTML: shopDescriptionSource(p)}}
	s.prepareArticleWidgets(ctx, &fragment)
	skin, sender, avatar := "inherit", "", ""
	if s.themes != nil && s.themes.Active().View == "chunnel/v1" {
		skin = "chunnel"
		fragment.Post.ContentHTML = articlewidgets.EnhanceCode(fragment.Post.ContentHTML, 0)
	}
	if s.auth != nil && p.DescriptionAuthorID > 0 {
		if user, err := s.auth.GetUser(ctx, p.DescriptionAuthorID); err == nil && user.Status == auth.UserStatusActive {
			sender, avatar = user.DisplayName, s.avatarURL(user.AvatarMediaID)
			if sender == "" {
				sender = user.Username
			}
		}
	}
	data.ShopProductKey = p.Key
	data.ShopDescriptionHTML = articlewidgets.ExpandForTarget(fragment.Post.ContentHTML, "shop:"+p.Key, skin, sender, avatar)
}

func (s *Server) validateShopComponent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.paymentsAdmin(w, r, true); !ok {
		return
	}
	s.validateComponentConfig(w, r)
}

func (s *Server) shopArticleWidgets(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	s.publishedArticleWidgets(w, r)
}

func (s *Server) shopComponentTarget(w http.ResponseWriter, r *http.Request) (componentOwner, articlewidgets.Config, bool) {
	key := strings.TrimPrefix(r.PathValue("post"), "shop:")
	if s.shop != nil && articlewidgets.ValidID(r.PathValue("widget")) {
		if catalog, err := s.shop.Catalog(r.Context()); err == nil && catalog.Enabled {
			for _, p := range catalog.Products {
				if p.Key == key && p.Enabled {
					if c, found := articlewidgets.Find(shopDescriptionSource(p), r.PathValue("widget")); found {
						return componentOwner{ID: 1, AuthorID: p.DescriptionAuthorID, Scope: "shop:" + p.Key}, c, true
					}
					break
				}
			}
		}
	}
	s.writeAPIError(w, r, http.StatusNotFound, "widget.unavailable", "商品或组件暂不可用", nil)
	return componentOwner{}, articlewidgets.Config{}, false
}
