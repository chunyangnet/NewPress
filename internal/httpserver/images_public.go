package httpserver

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/media"
)

type responsiveImageView struct {
	Src            string
	SrcSet         string
	CardSrc        string
	CardSrcSet     string
	Width, Height  int
	FocalX, FocalY int
}

func responsiveImage(item media.Item) responsiveImageView {
	result := responsiveImageView{FocalX: item.Image.FocalX, FocalY: item.Image.FocalY}
	result.CardSrcSet = variantSrcSet(item, []string{"card", "card-large"})
	result.SrcSet = variantSrcSet(item, []string{"small", "medium", "large"})
	result.CardSrc = item.VariantURL("card", "")
	result.Src = item.VariantURL("large", "")
	variant, _ := item.Variant("large")
	result.Width, result.Height = variant.Width, variant.Height
	return result
}

func variantSrcSet(item media.Item, names []string) string {
	parts := []string{}
	seen := map[int]bool{}
	for _, name := range names {
		variant, ok := item.Variant(name)
		if !ok || seen[variant.Width] {
			continue
		}
		seen[variant.Width] = true
		parts = append(parts, fmt.Sprintf("%s %dw", item.VariantURL(name, ""), variant.Width))
	}
	return strings.Join(parts, ", ")
}

func (s *Server) prepareResponsiveImages(data *pageView) {
	data.Images = map[string]*responsiveImageView{}
	data.BodyImages = map[string]contenthtml.ResponsiveImage{}
	if s.media == nil {
		return
	}
	resolver := assetref.New(s.config.PublicURL)
	checked := map[string]bool{}
	add := func(value string) {
		if value == "" || checked[value] {
			return
		}
		checked[value] = true
		// An explicit variant is an author's chosen representation.
		if strings.ContainsAny(value, "?#") {
			return
		}
		// Relative article URLs depend on the document path; leave those as
		// authored rather than resolving them against the site's root.
		if !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
			return
		}
		link, ok := resolver.URL(value, "/")
		if !ok {
			return
		}
		item, err := s.media.Get(link.ID)
		if err != nil || item.Status() != media.StatusActive || item.Image.Recipe == 0 || item.PublicURL != link.Path {
			return
		}
		view := responsiveImage(item)
		data.Images[value] = &view
		data.BodyImages[value] = contenthtml.ResponsiveImage{Src: view.Src, SrcSet: view.SrcSet, Width: view.Width, Height: view.Height}
	}
	add(data.Post.CoverURL)
	for _, post := range data.Posts {
		add(post.CoverURL)
	}
	for _, post := range data.Featured {
		add(post.CoverURL)
	}
	add(data.Series.Current.CoverURL)
	for _, series := range data.Series.Items {
		add(series.CoverURL)
	}
	add(homeString(data.ThemeValues, "home_hero_cover", ""))
	for _, category := range data.Home.Categories {
		add(category.CoverURL)
	}
	for _, series := range data.Home.Series {
		add(series.CoverURL)
	}
	tokens := html.NewTokenizer(strings.NewReader(data.Post.ContentHTML))
	for token := tokens.Next(); token != html.ErrorToken; token = tokens.Next() {
		if token != html.StartTagToken && token != html.SelfClosingTagToken {
			continue
		}
		value := tokens.Token()
		if value.Data != "img" {
			continue
		}
		for _, attr := range value.Attr {
			if attr.Key == "src" {
				add(attr.Val)
			}
		}
	}
}
