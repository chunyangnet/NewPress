package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"slices"
	"strings"
	"unicode/utf8"
)

func (s *Server) proTopicPost(w http.ResponseWriter, r *http.Request, p auth.Principal) (content.Post, bool) {
	id, err := positivePathID(r.PathValue("id"))
	if err != nil || !s.aiActive() {
		s.renderError(w, 404, "话题管理不可用")
		return content.Post{}, false
	}
	post, err := s.repository.GetByID(r.Context(), id)
	if err != nil || !canWriteContentObject(p, post) {
		s.renderError(w, 404, "没有找到文章")
		return post, false
	}
	return post, true
}
func (s *Server) proTopicEditor(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	post, ok := s.proTopicPost(w, r, p)
	if !ok {
		return
	}
	data := s.baseViewFor(w, "管理相关话题", "", r.URL.Path)
	data.IsAdmin = true
	data.BodyClass = "is-admin"
	data.Admin = s.adminView("content", p, ro)
	data.Post = post
	data.RelatedTopics = relatedTopics(post)
	s.render(w, 200, "admin-ai-related", data)
}
func (s *Server) proTopicSave(w http.ResponseWriter, r *http.Request) {
	p, f, ok := s.requireContentWriteLimit(w, r, 32<<10, "ai_topic_form_size")
	if !ok {
		return
	}
	if !s.sameOriginPublicWrite(r) {
		s.renderError(w, 403, "请从本站编辑器操作")
		return
	}
	post, ok := s.proTopicPost(w, r, p)
	if !ok {
		return
	}
	version, err := positivePathID(f.Get("version"))
	if err != nil || version != post.Version {
		s.renderError(w, 409, "原文已变化，请刷新后再编辑话题")
		return
	}
	topics := relatedTopics(post)
	id := f.Get("id")
	i := slices.IndexFunc(topics, func(t aiRelatedTopic) bool { return t.ID == id })
	if i < 0 && id != "" {
		s.renderError(w, 404, "话题不存在")
		return
	}
	switch f.Get("action") {
	case "remove":
		if i < 0 {
			s.renderError(w, 422, "没有选择话题")
			return
		}
		topics = slices.Delete(topics, i, i+1)
	case "save":
		title, body := strings.TrimSpace(f.Get("title")), strings.TrimSpace(f.Get("body"))
		if title == "" || body == "" || utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(body) > 5000 {
			s.renderError(w, 422, "话题需要标题和正文")
			return
		}
		if i < 0 {
			if len(topics) >= 10 {
				s.renderError(w, 422, "每篇最多十个话题")
				return
			}
			newID := aiDigest(title)[:16]
			if slices.ContainsFunc(topics, func(t aiRelatedTopic) bool { return t.ID == newID }) {
				s.renderError(w, 409, "已有同名话题")
				return
			}
			topics = append(topics, aiRelatedTopic{ID: newID})
			i = len(topics) - 1
		}
		topics[i].Title = title
		topics[i].Body = body
	default:
		s.renderError(w, 400, "无效的话题操作")
		return
	}
	raw, _ := json.Marshal(topics)
	in := inputFromPost(post)
	in.AITopics = string(raw)
	in.Actor = p.Username
	if _, err := s.aiBaseContent().Update(r.Context(), post.ID, content.Mutation{ExpectedVersion: version, Actor: p.Username}, in); err != nil {
		s.renderError(w, 409, "话题未保存，请重新核对版本")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/plugins/ai-pro-max/articles/%d/topics/", post.ID), 303)
}
