package httpserver

import (
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/questions"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type questionNoteView struct {
	questions.Note
	Author string
}
type questionAnswerView struct {
	questions.Answer
	Author            string
	HTML              template.HTML
	CanEdit, Accepted bool
	NotesView         []questionNoteView
}
type questionView struct {
	questions.Question
	URL, Author, AcceptedURL string
	HTML                     template.HTML
	Count                    int
	CanEdit, CanAccept       bool
	AnswersView              []questionAnswerView
	NotesView                []questionNoteView
	AnswerPreview            string
}
type questionTopicView struct {
	Name  string
	Count int
}
type questionsView struct {
	Items                               []questionView
	Question                            questionView
	SignedIn, Manage, New, Admin        bool
	UserID                              int64
	CSRF                                string
	Query, Status, Tag, Scope, Sort     string
	Tags                                []string
	Total, Page, Pages                  int
	Previous, Next                      string
	PublicTotal, OpenTotal, SolvedTotal int
	Topics                              []questionTopicView
	Featured                            []questionView
	AllURL, OpenURL, SolvedURL          string
}

func (s *Server) SetQuestionsStore(store questions.Store) {
	s.questions = store
	s.bindQuestionNotifications()
}
func (s *Server) bindQuestionNotifications() {
	if s.notifications == nil || s.questions == nil {
		return
	}
	s.notifications.Questions = s.questions
	s.notifications.QuestionsEnabled = func() bool { return s.moduleEnabled("questions") }
	for i, src := range s.notifications.Sources {
		if _, ok := src.(questions.Store); ok {
			s.notifications.Sources[i] = s.questions
			return
		}
	}
	s.notifications.Sources = append(s.notifications.Sources, s.questions)
}
func (s *Server) registerQuestionRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /questions/{$}", s.questionsPage)
	mux.HandleFunc("GET /questions/new/{$}", s.questionsPage)
	mux.HandleFunc("GET /questions/{question}/{$}", s.questionsPage)
	mux.HandleFunc("GET /admin/questions/{$}", s.questionsPage)
	mux.HandleFunc("GET /admin/questions/{question}/{$}", s.questionsPage)
	mux.HandleFunc("POST /api/v1/questions/{$}", s.questionsWrite)
	mux.HandleFunc("POST /api/v1/questions/{question}/{$}", s.questionsWrite)
}
func questionID(r *http.Request) (int64, error) {
	raw := r.PathValue("question")
	if raw == "" {
		return 0, nil
	}
	id, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, questions.ErrNotFound
	}
	return id, nil
}
func questionLink(base string, q url.Values, page int) string {
	v := url.Values{}
	for k, values := range q {
		if len(values) > 0 && values[0] != "" {
			v.Set(k, values[0])
		}
	}
	v.Set("page", strconv.Itoa(page))
	return base + "?" + v.Encode()
}
func (s *Server) questionsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	if s.questions == nil {
		if r.URL.Path == "/admin/questions/" {
			if p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilityCommentsModerate); ok {
				s.renderFeatureRecovery(w, r, "questions", "", s.adminView("questions", p, ro), 503, "")
			}
			return
		}
		s.renderServiceStatus(w, 503, "问答", "unavailable")
		return
	}
	v := questionsView{Items: []questionView{}, Page: 1, Pages: 1, New: r.URL.Path == "/questions/new/", Admin: strings.HasPrefix(r.URL.Path, "/admin/questions/")}
	p, signed := s.currentAdmin(r)
	v.SignedIn = signed && p.UserID > 0
	v.UserID = p.UserID
	v.CSRF = p.CSRFToken
	v.Manage = v.SignedIn && p.Can(auth.CapabilityCommentsModerate)
	if v.Admin {
		if _, _, ok := s.requireAdminPageCapability(w, r, auth.CapabilityCommentsModerate); !ok {
			return
		}
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		s.renderError(w, 400, "问答筛选参数不正确")
		return
	}
	query, ok := strictQueryShape(query, map[string]bool{"q": true, "status": true, "tag": true, "scope": true, "sort": true, "page": true})
	if !ok {
		s.renderError(w, 400, "问答筛选参数不正确")
		return
	}
	page, e := positiveQueryInt(query.Get("page"), 1, 100000)
	v.Query = strings.TrimSpace(query.Get("q"))
	v.Status = query.Get("status")
	v.Tag = query.Get("tag")
	v.Scope = query.Get("scope")
	v.Sort = query.Get("sort")
	if e != nil || !utf8.ValidString(v.Query) || !utf8.ValidString(v.Tag) || strings.ContainsAny(v.Query+v.Tag, "\x00\r\n\t") || utf8.RuneCountInString(v.Query) > 80 || utf8.RuneCountInString(v.Tag) > 24 || !oneOf(v.Status, "", "open", "solved", "unanswered") || !oneOf(v.Scope, "", "mine", "answered") || !oneOf(v.Sort, "", "newest", "active") {
		s.renderError(w, 400, "问答筛选参数不正确")
		return
	}
	id, e := questionID(r)
	if e != nil {
		s.renderError(w, 404, "问题不存在")
		return
	}
	state, e := s.questions.Snapshot(r.Context())
	if e != nil {
		s.renderServiceStatus(w, 503, "问答", "unavailable")
		return
	}
	notes := func(items []questions.Note) []questionNoteView {
		out := []questionNoteView{}
		for _, n := range items {
			out = append(out, questionNoteView{n, s.circleName(r.Context(), n.AuthorID)})
		}
		return out
	}
	project := func(q questions.Question, detail bool) questionView {
		out := questionView{Question: q, URL: questions.URL(q.ID), Author: s.circleName(r.Context(), q.AuthorID), CanEdit: v.SignedIn && (q.AuthorID == p.UserID || v.Admin) && (!q.Locked || v.Admin), CanAccept: v.SignedIn && q.AuthorID == p.UserID && !q.Locked && !q.Hidden}
		if v.Admin {
			out.URL = fmt.Sprintf("/admin/questions/%d/", q.ID)
		}
		out.Answers = nil
		out.Notes = nil
		if !v.Admin {
			out.Reports = nil
		}
		visible := 0
		for _, a := range q.Answers {
			if a.Hidden && !v.Admin {
				continue
			}
			visible++
			if a.ID == q.AcceptedID {
				out.AcceptedURL = out.URL + fmt.Sprintf("?page=%d#answer-%d", (visible+19)/20, a.ID)
				preview := []rune(strings.Join(strings.Fields(a.Body), " "))
				if len(preview) > 160 {
					out.AnswerPreview = string(preview[:160]) + "…"
				} else {
					out.AnswerPreview = string(preview)
				}
			}
		}
		out.Count = visible
		if detail {
			out.HTML = template.HTML(community.RenderBody("", q.Body, "markdown", nil))
			out.NotesView = notes(q.Notes)
		}
		return out
	}
	base := "/questions/"
	if v.Admin {
		base = "/admin/questions/"
	}
	if id > 0 {
		q, found := questions.Get(state, id)
		if !found || q.Hidden && !v.Admin {
			s.renderError(w, 404, "问题不存在或暂不可见")
			return
		}
		v.Question = project(q, true)
		v.Total = v.Question.Count
		v.Pages = max(1, (v.Total+19)/20)
		v.Page = min(page, v.Pages)
		position := 0
		for _, a := range q.Answers {
			if a.Hidden && !v.Admin {
				continue
			}
			position++
			if position <= (v.Page-1)*20 || position > v.Page*20 {
				continue
			}
			v.Question.AnswersView = append(v.Question.AnswersView, questionAnswerView{Answer: a, Author: s.circleName(r.Context(), a.AuthorID), HTML: template.HTML(community.RenderBody("", a.Body, "markdown", nil)), CanEdit: v.SignedIn && (a.AuthorID == p.UserID || v.Admin) && (!q.Locked || v.Admin) && !a.Hidden, Accepted: a.ID == q.AcceptedID, NotesView: notes(a.Notes)})
		}
		base = v.Question.URL
	} else {
		items := []questions.Question{}
		tags := map[string]bool{}
		topicCounts := map[string]int{}
		solved := []questions.Question{}
		for _, q := range state.Items {
			if q.Hidden && !v.Admin {
				continue
			}
			if !q.Hidden {
				v.PublicTotal++
				if q.AcceptedID != 0 {
					v.SolvedTotal++
					solved = append(solved, q)
				} else {
					v.OpenTotal++
				}
			}
			for _, tag := range q.Tags {
				tags[tag] = true
				if !q.Hidden {
					topicCounts[tag]++
				}
			}
			if v.Scope != "" && !v.SignedIn {
				continue
			}
			if v.Scope == "mine" && q.AuthorID != p.UserID {
				continue
			}
			if v.Scope == "answered" {
				found := false
				for _, a := range q.Answers {
					if a.AuthorID == p.UserID && !a.Hidden {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			if v.Status == "open" && q.AcceptedID != 0 || v.Status == "solved" && q.AcceptedID == 0 {
				continue
			}
			if v.Status == "unanswered" {
				hasAnswer := false
				for _, answer := range q.Answers {
					if !answer.Hidden {
						hasAnswer = true
						break
					}
				}
				if hasAnswer || q.Locked {
					continue
				}
			}
			if v.Tag != "" {
				found := false
				for _, tag := range q.Tags {
					if tag == v.Tag {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			if v.Query != "" && !strings.Contains(strings.ToLower(q.Title+" "+q.Body+" "+strings.Join(q.Tags, " ")), strings.ToLower(v.Query)) {
				continue
			}
			items = append(items, q)
		}
		for tag := range tags {
			v.Tags = append(v.Tags, tag)
		}
		sort.Strings(v.Tags)
		for name, count := range topicCounts {
			v.Topics = append(v.Topics, questionTopicView{Name: name, Count: count})
		}
		sort.Slice(v.Topics, func(i, j int) bool {
			if v.Topics[i].Count == v.Topics[j].Count {
				return v.Topics[i].Name < v.Topics[j].Name
			}
			return v.Topics[i].Count > v.Topics[j].Count
		})
		v.Topics = v.Topics[:min(6, len(v.Topics))]
		sort.Slice(solved, func(i, j int) bool { return solved[i].ID > solved[j].ID })
		for _, q := range solved[:min(3, len(solved))] {
			item := project(q, false)
			if item.AnswerPreview != "" {
				v.Featured = append(v.Featured, item)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if v.Sort == "active" && !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return items[i].UpdatedAt.After(items[j].UpdatedAt)
			}
			return items[i].ID > items[j].ID
		})
		v.Total = len(items)
		v.Pages = max(1, (v.Total+19)/20)
		v.Page = min(page, v.Pages)
		start := (v.Page - 1) * 20
		for _, q := range items[start:min(start+20, len(items))] {
			v.Items = append(v.Items, project(q, false))
		}
	}
	if v.Page > 1 {
		v.Previous = questionLink(base, query, v.Page-1)
	}
	if v.Page < v.Pages {
		v.Next = questionLink(base, query, v.Page+1)
	}
	statusQuery := maps.Clone(query)
	statusQuery.Del("status")
	v.AllURL = questionLink(base, statusQuery, 1)
	statusQuery.Set("status", "open")
	v.OpenURL = questionLink(base, statusQuery, 1)
	statusQuery.Set("status", "solved")
	v.SolvedURL = questionLink(base, statusQuery, 1)
	title := "问答"
	if v.New {
		title = "提问"
	}
	if id > 0 {
		title = v.Question.Title
	}
	if v.Admin {
		title = "问答管理"
	}
	data := s.baseViewFor(w, title, "提出具体问题，分享经验与解答。", base)
	data.Questions = v
	data.BodyClass = "is-questions"
	data.ActiveNav = "questions"
	if v.New || v.Scope != "" || v.Admin {
		data.Site.Robots = "noindex, nofollow"
	}
	if v.Admin {
		data.IsAdmin = true
		data.Admin = s.adminView("questions", p, false)
		data.BodyClass = "is-admin is-admin-questions"
		s.render(w, 200, "admin-questions", data)
		return
	}
	s.render(w, 200, "questions", data)
}
func oneOf(value string, choices ...string) bool {
	for _, c := range choices {
		if value == c {
			return true
		}
	}
	return false
}
func (s *Server) questionsWrite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.questions == nil {
		s.writeAPIError(w, r, 503, "questions.unavailable", "问答暂时不可用", nil)
		return
	}
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	id, e := questionID(r)
	if e != nil {
		s.questionError(w, r, e)
		return
	}
	var cmd questions.Command
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &cmd, 64<<10, "64 KiB"); !ok {
		return
	}
	if allowed, retry := s.accessRate.allow("questions:"+strconv.FormatInt(p.UserID, 10), 60, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "questions.rate_limited", "操作较频繁，请稍后重试", nil)
		return
	}
	result, e := s.questions.Apply(r.Context(), id, questions.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilityCommentsModerate)}, cmd, time.Now())
	if e != nil {
		s.questionError(w, r, e)
		return
	}
	target := questions.URL(result.ID)
	if result.AnswerID > 0 {
		state, err := s.questions.Snapshot(r.Context())
		if err == nil {
			q, _ := questions.Get(state, result.ID)
			n := 0
			for _, a := range q.Answers {
				if a.Hidden {
					continue
				}
				n++
				if a.ID == result.AnswerID {
					target += fmt.Sprintf("?page=%d#answer-%d", (n+19)/20, a.ID)
					break
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"id": result.ID, "version": result.Version, "url": target, "replay": result.Replay})
}
func (s *Server) questionError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 503, "questions.unavailable", "暂时无法保存，请保留输入后重试"
	switch {
	case errors.Is(e, questions.ErrInvalid):
		status, code, message = 422, "questions.invalid", "请检查标题、正文、标签及字段长度"
	case errors.Is(e, questions.ErrForbidden):
		status, code, message = 403, "questions.forbidden", "当前身份或问题状态不允许此操作"
	case errors.Is(e, questions.ErrNotFound):
		status, code, message = 404, "questions.not_found", "问题或回答不可用"
	case errors.Is(e, questions.ErrConflict):
		status, code, message = 409, "questions.conflict", "内容已更新。请另开页面核对最新内容，保留当前输入后再提交"
	case errors.Is(e, questions.ErrCapacity):
		status, code, message = 422, "questions.capacity", "内容数量已达到上限，请联系管理员"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
