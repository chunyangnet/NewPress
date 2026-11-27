package httpserver

import (
	"fmt"
	"net/url"
	"strings"
)

type homeSlide struct {
	Index                      int
	Title, Summary, Image, URL string
}

func (s *Server) homeEntryCards(values map[string]any) []homeLink {
	var result []homeLink
	for i := 1; i <= 4; i++ {
		prefix := fmt.Sprintf("home_entry_%d_", i)
		title := strings.TrimSpace(homeString(values, prefix+"title", ""))
		link := strings.TrimSpace(homeString(values, prefix+"url", ""))
		if title == "" || link == "" || !s.moduleURLAvailable(link) {
			continue
		}
		result = append(result, homeLink{Title: title, URL: link, Icon: homeString(values, prefix+"icon", "compass")})
	}
	return result
}

func (s *Server) homeCreativeContent(values map[string]any) ([]homeSlide, []homeLink) {
	var slides []homeSlide
	for i := 1; i <= 4; i++ {
		prefix := fmt.Sprintf("home_slide_%d_", i)
		image := strings.TrimSpace(homeString(values, prefix+"image", ""))
		title := strings.TrimSpace(homeString(values, prefix+"title", ""))
		if image == "" || title == "" {
			continue
		}
		link := homeString(values, prefix+"url", "")
		if !s.moduleURLAvailable(link) {
			link = ""
		}
		slides = append(slides, homeSlide{Index: len(slides) + 1, Title: title, Summary: homeString(values, prefix+"summary", ""), Image: image, URL: link})
	}
	var terms []homeLink
	seen := map[string]bool{}
	for _, term := range strings.FieldsFunc(homeString(values, "home_search_terms", ""), func(r rune) bool { return r == ',' || r == '，' || r == '\n' }) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			continue
		}
		if len([]rune(term)) > 32 {
			term = string([]rune(term)[:32])
		}
		seen[term] = true
		terms = append(terms, homeLink{Title: term, URL: "/explore/?q=" + url.QueryEscape(term)})
		if len(terms) == 6 {
			break
		}
	}
	return slides, terms
}
