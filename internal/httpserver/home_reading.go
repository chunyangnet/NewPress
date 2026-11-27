package httpserver

import (
	"fmt"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/layout"
)

func (s *Server) homeGuideLinks(values map[string]any) []homeLink {
	var links []homeLink
	for i := 1; i <= 3; i++ {
		prefix := fmt.Sprintf("home_guide_%d_", i)
		title := strings.TrimSpace(homeString(values, prefix+"title", ""))
		raw := strings.TrimSpace(homeString(values, prefix+"url", ""))
		target, err := url.Parse(raw)
		// Only root-relative same-site destinations are accepted, including after decoding.
		if title == "" || err != nil || target.IsAbs() || target.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(target.Path, "//") || strings.ContainsAny(raw+target.Path, "\\\r\n\t") || !s.moduleURLAvailable(raw) {
			continue
		}
		links = append(links, homeLink{Title: title, Description: homeString(values, prefix+"description", ""), URL: raw})
	}
	return links
}

// Allocate picks independently for each device: a hidden or minimal hero consumes none.
func homeSpotlight(values map[string]any, posts []content.Post) ([]content.Post, []content.Post) {
	desktop, mobile := posts, posts
	if homeString(values, "hero_visual", "") == "minimal" {
		return desktop, mobile
	}
	for _, region := range layout.Resolve(values) {
		if region.Key == "aside" && !homeEnabled(values, "sidebar_enabled") {
			continue
		}
		for _, block := range region.Modules {
			if block.Key != "hero" {
				continue
			}
			rest := posts[min(2, len(posts)):]
			if block.Visibility == "both" || block.Visibility == "desktop" {
				desktop = rest
			}
			if block.Visibility == "both" || block.Visibility == "mobile" {
				mobile = rest
			}
		}
	}
	return desktop, mobile
}
