package httpserver

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"newpress.local/newpress/internal/content"
	"strings"
	"time"
)

func relatedTopics(p content.Post) []aiRelatedTopic {
	raw, _ := p.Meta["ai_pro_max_topics"].(string)
	var items []aiRelatedTopic
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &items) != nil {
		return nil
	}
	return items
}
func (s *Server) withTopicLinks(p content.Post) content.Post {
	items := relatedTopics(p)
	if len(items) == 0 || !s.aiActive() {
		return p
	}
	var b strings.Builder
	b.WriteString("<section><h2>相关话题</h2><ul>")
	for _, t := range items {
		fmt.Fprintf(&b, "<li><a href=\"/ai-topics/%d/%s/\">%s</a></li>", p.ID, html.EscapeString(t.ID), html.EscapeString(t.Title))
	}
	b.WriteString("</ul></section>")
	p.ContentHTML += b.String()
	return p
}
func (s *Server) publicAITopic(w http.ResponseWriter, r *http.Request) {
	id, err := positivePathID(r.PathValue("id"))
	if err != nil || !s.aiActive() {
		s.renderError(w, 404, "没有找到话题")
		return
	}
	p, err := s.repository.GetByID(r.Context(), id)
	if err != nil || !content.PublicAt(p, time.Now()) {
		s.renderError(w, 404, "没有找到话题")
		return
	}
	for _, t := range relatedTopics(p) {
		if t.ID != r.PathValue("topic") {
			continue
		}
		link := s.contentPath(p)
		data := s.baseViewFor(w, t.Title, "", r.URL.Path)
		p.Title = t.Title
		p.Excerpt = ""
		p.CoverURL = t.Image
		p.ContentHTML = "<p>" + strings.ReplaceAll(html.EscapeString(t.Body), "\n", "</p><p>") + "</p><p><a href=\"" + html.EscapeString(link) + "\">返回原文</a></p>"
		data.Post = p
		data.BodyClass = "is-single is-page"
		s.render(w, 200, "page", data)
		return
	}
	s.renderError(w, 404, "没有找到话题")
}
