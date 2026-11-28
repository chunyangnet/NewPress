package httpserver

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/presentation"
)

type feedbackChoice = presentation.FeedbackChoice
type feedbackReplyView = presentation.FeedbackReply
type feedbackEventView = presentation.FeedbackEvent
type feedbackItemView = presentation.FeedbackItem
type feedbackColumn = presentation.FeedbackColumn
type feedbackBoardCard = presentation.FeedbackBoard

type feedbackPageView struct {
	Directory    bool                `json:"directory"`
	SignedIn     bool                `json:"signed_in"`
	CanRead      bool                `json:"can_read"`
	CanSubmit    bool                `json:"can_submit"`
	Manage       bool                `json:"manage"`
	Configure    bool                `json:"configure"`
	Roadmap      bool                `json:"roadmap"`
	Slug         string              `json:"slug"`
	URL          string              `json:"url"`
	API          string              `json:"api"`
	CSRF         string              `json:"csrf"`
	Search       string              `json:"search"`
	Status       string              `json:"status"`
	Kind         string              `json:"kind"`
	Category     string              `json:"category"`
	Sort         string              `json:"sort"`
	Scope        string              `json:"scope"`
	View         string              `json:"view"`
	Previous     string              `json:"previous"`
	Next         string              `json:"next"`
	ListURL      string              `json:"list_url"`
	RoadmapURL   string              `json:"roadmap_url"`
	UserID       int64               `json:"user_id"`
	Version      int64               `json:"version"`
	Settings     feedback.Settings   `json:"settings"`
	Boards       []feedbackBoardCard `json:"boards"`
	Items        []feedbackItemView  `json:"items"`
	Item         feedbackItemView    `json:"item"`
	Columns      []feedbackColumn    `json:"columns"`
	Kinds        []feedbackChoice    `json:"kinds"`
	Statuses     []feedbackChoice    `json:"statuses"`
	Categories   []feedbackChoice    `json:"categories"`
	Page         int                 `json:"page"`
	Pages        int                 `json:"pages"`
	Total        int                 `json:"total"`
	AllCount     int                 `json:"all_count"`
	DoneCount    int                 `json:"done_count"`
	ActiveCount  int                 `json:"active_count"`
	PendingCount int                 `json:"pending_count"`
}
type featureGrantView struct {
	feedback.Grant
	Label string
	Guest bool
}
type featureToggle struct {
	Name, Label, Help string
	Enabled           bool
}
type featurePageView struct {
	Name, Description, URL, EditURL string
	UnavailableReason               string
	Kind, KindLabel, Icon           string
	Enabled, Available              bool
}

type featureAdminView struct {
	UnavailableReason, LinksUnavailableReason string
	LinksAvailable                            bool
	Pages                                     []featurePageView
	LinkCount                                 int
	Editing, Creating, Available              bool
	Current                                   feedback.Board
	Boards                                    []feedbackBoardCard
	Roles                                     []featureGrantView
	Kinds                                     []feedbackChoice
	Toggles                                   []featureToggle
	CategoryText                              string
}

func (s *Server) SetFeedbackStore(store feedback.Store) { s.feedback = store }
func (s *Server) registerFeedbackRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/features/{$}", s.adminFeatures)
	mux.HandleFunc("GET /feedback/{$}", s.feedbackPage)
	mux.HandleFunc("GET /feedback/{board}/{$}", s.feedbackPage)
	mux.HandleFunc("GET /feedback/{board}/items/{item}", s.feedbackPage)
	mux.HandleFunc("GET /api/v1/feedback/{board}", s.feedbackAPI)
	mux.HandleFunc("GET /api/v1/feedback/{board}/items/{item}", s.feedbackAPI)
	mux.HandleFunc("POST /api/v1/feedback/{board}", s.feedbackWrite)
	mux.HandleFunc("GET /api/v1/feedback/{board}/export", s.feedbackExport)
}
func feedbackActor(p auth.Principal) feedback.Actor {
	return feedback.Actor{ID: p.UserID, Roles: p.RoleSlugs, Admin: p.Can(auth.CapabilitySettingsManage)}
}
func feedbackURL(slug string) string { return "/feedback/" + slug + "/" }
func feedbackKindLabel(k string) string {
	return map[string]string{"suggestion": "功能建议", "issue": "问题报告", "feedback": "体验反馈"}[k]
}
func feedbackStatusLabel(k string) string {
	return map[string]string{"new": "待评估", "planned": "已计划", "progress": "进行中", "done": "已完成", "closed": "已关闭"}[k]
}
func feedbackPriorityLabel(k string) string {
	return map[string]string{"low": "低优先级", "normal": "普通", "high": "高优先级", "urgent": "紧急"}[k]
}
func boardCard(b feedback.Board) feedbackBoardCard {
	return feedbackBoardCard{Slug: b.Slug, Name: b.Settings.Name, Description: b.Settings.Description, Enabled: b.Settings.Enabled, URL: feedbackURL(b.Slug)}
}
func feedbackNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
}
func (s *Server) feedbackError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 503, "unavailable", "暂时无法保存或读取，请保留输入后重试。"
	switch {
	case errors.Is(e, feedback.ErrInvalid):
		status, code, message = 422, "invalid", "请检查字段长度、功能选项和权限设置。"
	case errors.Is(e, feedback.ErrForbidden):
		status, code, message = 403, "forbidden", "当前身份没有权限，或这项功能已暂停。"
	case errors.Is(e, feedback.ErrNotFound):
		status, code, message = 404, "not_found", "这个板块或反馈当前不可用。"
	case errors.Is(e, feedback.ErrConflict):
		status, code, message = 409, "conflict", "内容已被更新。当前输入已保留，请先查看最新内容，再重新编辑。"
	case errors.Is(e, feedback.ErrCapacity):
		status, code, message = 422, "capacity", "板块容量已达到上限，请联系管理员。"
	default:
		slog.Error("feedback request failed", "error", e)
	}
	s.writeAPIError(w, r, status, "feedback."+code, message, nil)
}
func (s *Server) adminFeatures(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	feedbackNoStore(w)
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"edit": true})
	if !ok {
		return
	}
	v := featureAdminView{UnavailableReason: s.pageUnavailableReason("feedback"), LinksUnavailableReason: s.pageUnavailableReason("links")}
	v.Available, v.LinksAvailable = v.UnavailableReason == "", v.LinksUnavailableReason == ""
	boards, e := s.feedback.List(r.Context())
	if e != nil {
		s.feedbackError(w, r, e)
		return
	}
	for _, b := range boards {
		v.Boards = append(v.Boards, boardCard(b))
	}
	if q.Get("edit") == "" {
		for _, b := range boards {
			v.Pages = append(v.Pages, featurePageView{
				Name: b.Settings.Name, Description: b.Settings.Description, URL: feedbackURL(b.Slug),
				EditURL: "/admin/features/?edit=" + b.Slug, Kind: "feedback", KindLabel: "建议板块", Icon: "message-circle",
				Enabled: b.Settings.Enabled, Available: v.Available,
				UnavailableReason: v.UnavailableReason,
			})
		}
		links, err := s.linkPages.List(r.Context())
		if err != nil {
			s.linkError(w, r, err)
			return
		}
		v.LinkCount = len(links)
		for _, b := range links {
			v.Pages = append(v.Pages, featurePageView{
				Name: b.Settings.Name, Description: b.Settings.Description, URL: "/links/" + b.Slug + "/",
				EditURL: "/admin/features/links/?edit=" + b.Slug, Kind: "links", KindLabel: "友链页面", Icon: "globe-2",
				Enabled: b.Settings.Enabled, Available: v.LinksAvailable,
				UnavailableReason: v.LinksUnavailableReason,
			})
		}
		// Put accessible pages first, then keep each template's stable store order.
		sort.SliceStable(v.Pages, func(i, j int) bool {
			return v.Pages[i].Enabled && v.Pages[i].Available && !(v.Pages[j].Enabled && v.Pages[j].Available)
		})
	}
	if edit := q.Get("edit"); edit != "" {
		v.Editing = true
		v.Creating = edit == "new"
		v.Current = feedback.Board{Settings: feedback.Defaults()}
		if !v.Creating {
			v.Current, e = s.feedback.Get(r.Context(), edit)
			if e != nil {
				s.feedbackError(w, r, e)
				return
			}
		}
		roles := auth.BuiltInRoles()
		if s.auth.PersistentUsers() {
			roles, e = s.auth.ListRoles(r.Context())
			if e != nil {
				s.feedbackError(w, r, e)
				return
			}
		}
		roles = append([]auth.Role{{Slug: "guest", Name: "访客（未登录）"}}, roles...)
		for _, role := range roles {
			if role.Slug == "administrator" {
				continue
			}
			g := feedback.Grant{Role: role.Slug}
			for _, configured := range v.Current.Settings.Permissions {
				if configured.Role == role.Slug {
					g = configured
					break
				}
			}
			v.Roles = append(v.Roles, featureGrantView{Grant: g, Label: role.Name, Guest: role.Slug == "guest"})
		}
		c := v.Current.Settings
		v.CategoryText = strings.Join(c.Categories, "\n")
		for _, k := range []string{"suggestion", "issue", "feedback"} {
			v.Kinds = append(v.Kinds, feedbackChoice{Value: k, Label: feedbackKindLabel(k), Selected: slices.Contains(c.Kinds, k)})
		}
		v.Toggles = []featureToggle{
			{"enabled", "开放板块", "关闭后仅站点管理员可预览；已有内容保留。", c.Enabled},
			{"submissions", "接受新反馈", "暂停投稿时仍可浏览和处理已有反馈。", c.Submissions},
			{"voting", "支持投票", "每个登录账号一票，可随时撤回。", c.Voting},
			{"comments", "开放讨论", "允许有权限的账号补充意见。", c.Comments},
			{"official_replies", "官方回复", "管理者可独立发布带标识的答复。", c.OfficialReplies},
			{"roadmap", "进度看板", "按处理状态展示计划与进展。", c.Roadmap},
			{"history", "状态记录", "在详情展示状态变更与处理说明。", c.History},
			{"moderation", "投稿先审核", "普通成员的新投稿或修改需审核后公开。", c.Moderation},
			{"private_submissions", "允许私密反馈", "作者可选择仅自己和板块管理者可见。", c.PrivateSubmissions},
		}
	}
	data := s.baseViewFor(w, "增加功能", "启用主题附带的功能页面。", "/admin/features/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-features"
	data.Admin = s.adminView("features", p, readOnly)
	data.Features = v
	s.render(w, 200, "admin-features", data)
}
func (s *Server) feedbackProjection(r *http.Request) (feedbackPageView, error) {
	p, authenticated := s.currentAdmin(r)
	a := feedback.Actor{}
	if authenticated {
		a = feedbackActor(p)
	}
	v := feedbackPageView{SignedIn: a.ID > 0, UserID: a.ID, CSRF: p.CSRFToken, Configure: a.Admin, Page: 1, Pages: 1, Items: []feedbackItemView{}, Boards: []feedbackBoardCard{}}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return v, feedback.ErrInvalid
	}
	q, ok := strictQueryShape(q, map[string]bool{"q": true, "status": true, "kind": true, "category": true, "sort": true, "scope": true, "view": true, "page": true})
	if !ok {
		return v, feedback.ErrInvalid
	}
	page, e := positiveQueryInt(q.Get("page"), 1, 100000)
	if e != nil || utf8.RuneCountInString(q.Get("q")) > 120 || utf8.RuneCountInString(q.Get("category")) > 30 {
		return v, feedback.ErrInvalid
	}
	v.Search = strings.TrimSpace(q.Get("q"))
	v.Status = q.Get("status")
	v.Kind = q.Get("kind")
	v.Category = q.Get("category")
	v.Sort = q.Get("sort")
	v.Scope = q.Get("scope")
	v.View = q.Get("view")
	if v.Status != "" && !feedback.ValidStatus(v.Status) || v.Kind != "" && !feedback.ValidKind(v.Kind) || !slices.Contains([]string{"", "newest", "votes", "updated"}, v.Sort) || !slices.Contains([]string{"", "mine", "pending", "hidden"}, v.Scope) || !slices.Contains([]string{"", "list", "roadmap"}, v.View) {
		return v, feedback.ErrInvalid
	}
	v.Slug = r.PathValue("board")
	if v.Slug == "" {
		v.Directory = true
		boards, e := s.feedback.List(r.Context())
		if e != nil {
			return v, e
		}
		for _, b := range boards {
			if feedback.Can(b, a, "read") {
				v.Boards = append(v.Boards, boardCard(b))
			}
		}
		return v, nil
	}
	if !feedback.ValidSlug(v.Slug) {
		return v, feedback.ErrNotFound
	}
	b, e := s.feedback.Get(r.Context(), v.Slug)
	if e != nil {
		return v, e
	}
	if !b.Settings.Enabled && !a.Admin {
		return v, feedback.ErrNotFound
	}
	v.CanRead = feedback.Can(b, a, "read")
	if !v.CanRead {
		return v, feedback.ErrNotFound
	}
	v.Settings = b.Settings
	v.Settings.Permissions = nil
	v.Version = b.Version
	v.URL = feedbackURL(v.Slug)
	v.API = "/api/v1/feedback/" + v.Slug
	v.Manage = feedback.Can(b, a, "moderate")
	v.CanSubmit = b.Settings.Submissions && feedback.Can(b, a, "submit")
	if (v.Scope == "pending" || v.Scope == "hidden") && !v.Manage {
		return v, feedback.ErrForbidden
	}
	if v.Scope == "mine" && a.ID == 0 {
		return v, feedback.ErrForbidden
	}
	if v.View == "" {
		v.View = b.Settings.DefaultView
	}
	v.Roadmap = b.Settings.Roadmap && v.View == "roadmap"
	if v.Roadmap {
		v.View = "roadmap"
	} else {
		v.View = "list"
	}
	zone := s.siteLocation(r.Context())
	stamp := func(t time.Time) string { return siteTime(zone, t, "2006.01.02 15:04") }
	names := map[int64]string{}
	name := func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := s.circleName(r.Context(), id)
		names[id] = n
		return n
	}
	project := func(t feedback.Item, detail bool) feedbackItemView {
		i := feedbackItemView{Voting: b.Settings.Voting, ID: t.ID, Title: t.Title, Kind: t.Kind, KindLabel: feedbackKindLabel(t.Kind), Category: t.Category, Status: t.Status, StatusLabel: feedbackStatusLabel(t.Status), Priority: t.Priority, PriorityLabel: feedbackPriorityLabel(t.Priority), Visibility: t.Visibility, Author: name(t.AuthorID), Date: siteTime(zone, t.CreatedAt, "2006.01.02"), Updated: stamp(t.UpdatedAt), URL: v.URL + "items/" + strconv.FormatInt(t.ID, 10), Pinned: t.Pinned, Locked: t.Locked, Private: t.Private, Pending: t.Visibility == "pending", Hidden: t.Visibility == "hidden", Own: a.ID == t.AuthorID, Votes: len(t.Votes), Voted: slices.Contains(t.Votes, a.ID), CanVote: b.Settings.Voting && feedback.Can(b, a, "vote") && !t.Locked && !t.Private && t.Visibility == "public", CanEdit: feedback.CanEdit(b, a, t), CanComment: b.Settings.Comments && feedback.Can(b, a, "comment") && !t.Locked}
		for _, reply := range t.Replies {
			if !reply.Hidden || v.Manage {
				i.ReplyCount++
			}
		}
		if detail {
			i.Body = t.Body
			if b.Settings.History {
				for _, ev := range t.History {
					i.History = append(i.History, feedbackEventView{Author: name(ev.ActorID), Status: ev.Status, Label: feedbackStatusLabel(ev.Status), Note: ev.Note, Date: stamp(ev.At)})
				}
			}
		}
		return i
	}
	link := func(updates map[string]string) string {
		copy := url.Values{}
		for k, values := range q {
			copy[k] = slices.Clone(values)
		}
		copy.Del("page")
		for k, val := range updates {
			copy.Set(k, val)
		}
		return v.URL + "?" + copy.Encode()
	}
	v.ListURL = link(map[string]string{"view": "list"})
	v.RoadmapURL = link(map[string]string{"view": "roadmap"})
	for _, k := range b.Settings.Kinds {
		v.Kinds = append(v.Kinds, feedbackChoice{Value: k, Label: feedbackKindLabel(k), Selected: v.Kind == k})
	}
	for _, k := range b.Settings.Categories {
		v.Categories = append(v.Categories, feedbackChoice{Value: k, Label: k, Selected: v.Category == k})
	}
	if raw := r.PathValue("item"); raw != "" {
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id < 1 || strconv.FormatInt(id, 10) != raw {
			return v, feedback.ErrNotFound
		}
		for _, t := range b.Items {
			if t.ID != id || !feedback.CanSee(b, a, t) {
				continue
			}
			v.Item = project(t, true)
			replies := []feedback.Reply{}
			for _, reply := range t.Replies {
				if !reply.Hidden || v.Manage {
					replies = append(replies, reply)
				}
			}
			v.Total = len(replies)
			v.Pages = max(1, (v.Total+29)/30)
			v.Page = min(page, v.Pages)
			start := (v.Page - 1) * 30
			for _, reply := range replies[start:min(start+30, len(replies))] {
				v.Item.Replies = append(v.Item.Replies, feedbackReplyView{ID: reply.ID, Author: name(reply.AuthorID), Body: reply.Body, Date: stamp(reply.CreatedAt), Official: reply.Official, Hidden: reply.Hidden})
			}
			if v.Page > 1 {
				v.Previous = v.Item.URL + "?page=" + strconv.Itoa(v.Page-1) + "#discussion"
			}
			if v.Page < v.Pages {
				v.Next = v.Item.URL + "?page=" + strconv.Itoa(v.Page+1) + "#discussion"
			}
			return v, nil
		}
		return v, feedback.ErrNotFound
	}
	filtered := []feedback.Item{}
	counts := map[string]int{}
	for _, t := range b.Items {
		if !feedback.CanSee(b, a, t) {
			continue
		}
		v.AllCount++
		if t.Status == "done" {
			v.DoneCount++
		}
		if t.Status == "progress" || t.Status == "planned" {
			v.ActiveCount++
		}
		if t.Visibility == "pending" {
			v.PendingCount++
		}
		if v.Scope == "mine" && t.AuthorID != a.ID || v.Scope == "pending" && t.Visibility != "pending" || v.Scope == "hidden" && t.Visibility != "hidden" || v.Scope == "" && t.Visibility == "hidden" {
			continue
		}
		if v.Kind != "" && t.Kind != v.Kind || v.Category != "" && t.Category != v.Category || v.Search != "" && !strings.Contains(strings.ToLower(t.Title+"\n"+t.Body), strings.ToLower(v.Search)) {
			continue
		}
		counts[t.Status]++
		if v.Status != "" && t.Status != v.Status {
			continue
		}
		filtered = append(filtered, t)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if v.Sort == "votes" && len(a.Votes) != len(b.Votes) {
			return len(a.Votes) > len(b.Votes)
		}
		if v.Sort == "updated" && !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID > b.ID
	})
	v.Total = len(filtered)
	v.Pages = max(1, (v.Total+b.Settings.PageSize-1)/b.Settings.PageSize)
	v.Page = min(page, v.Pages)
	start := (v.Page - 1) * b.Settings.PageSize
	for _, t := range filtered[start:min(start+b.Settings.PageSize, len(filtered))] {
		v.Items = append(v.Items, project(t, false))
	}
	if v.Page > 1 {
		v.Previous = link(map[string]string{"page": strconv.Itoa(v.Page - 1)})
	}
	if v.Page < v.Pages {
		v.Next = link(map[string]string{"page": strconv.Itoa(v.Page + 1)})
	}
	for _, k := range []string{"new", "planned", "progress", "done", "closed"} {
		v.Statuses = append(v.Statuses, feedbackChoice{Value: k, Label: feedbackStatusLabel(k), Selected: v.Status == k, Count: counts[k]})
		if v.Roadmap {
			column := feedbackColumn{Value: k, Label: feedbackStatusLabel(k), URL: link(map[string]string{"view": "list", "status": k})}
			for _, t := range filtered {
				if t.Status == k {
					column.Count++
					if len(column.Items) < 12 {
						column.Items = append(column.Items, project(t, false))
					}
				}
			}
			v.Columns = append(v.Columns, column)
		}
	}

	return v, nil
}
func (s *Server) feedbackPage(w http.ResponseWriter, r *http.Request) {
	feedbackNoStore(w)
	if !s.pageAvailable("feedback") {
		s.renderError(w, 404, "当前主题未提供这个功能页面")
		return
	}
	v, e := s.feedbackProjection(r)
	if e != nil {
		status := 503
		if errors.Is(e, feedback.ErrNotFound) {
			status = 404
		}
		if errors.Is(e, feedback.ErrForbidden) {
			status = 403
		}
		if errors.Is(e, feedback.ErrInvalid) {
			status = 400
		}
		s.renderError(w, status, "板块暂不可用，请检查地址或登录后重试。")
		return
	}
	title, description, canonical := "建议与反馈", "参与讨论，关注每一个改变。", "/feedback/"
	if v.Slug != "" {
		title, description, canonical = v.Settings.Name, v.Settings.Description, v.URL
	}
	if v.Item.ID > 0 {
		title, canonical = v.Item.Title, v.Item.URL
	}
	data := s.baseViewFor(w, title, description, canonical)
	data.BodyClass = "is-feedback"
	data.Feedback = v
	// These pages are personalized and can contain confidential reports.
	data.Site.Robots = "noindex, nofollow, noarchive"
	w.Header().Set("X-Robots-Tag", data.Site.Robots)
	s.render(w, 200, "feedback", data)
}
func (s *Server) feedbackAPI(w http.ResponseWriter, r *http.Request) {
	feedbackNoStore(w)
	v, e := s.feedbackProjection(r)
	if e != nil {
		s.feedbackError(w, r, e)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) feedbackWrite(w http.ResponseWriter, r *http.Request) {
	feedbackNoStore(w)
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		feedback.Command
		VerificationToken string `json:"verification_token,omitempty"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 64<<10, "64 KiB"); !ok {
		return
	}
	cmd := input.Command
	if allowed, retry := s.accessRate.allow("feedback:"+strconv.FormatInt(p.UserID, 10), 90, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "feedback.rate_limited", "操作过于频繁，请稍后重试。", nil)
		return
	}
	if cmd.Settings != nil {
		if !p.Can(auth.CapabilitySettingsManage) {
			s.feedbackError(w, r, feedback.ErrForbidden)
			return
		}
		roles, e := s.auth.ListRoles(r.Context())
		if e != nil {
			s.feedbackError(w, r, e)
			return
		}
		valid := map[string]bool{"guest": true}
		for _, role := range roles {
			valid[role.Slug] = true
		}
		for _, g := range cmd.Settings.Permissions {
			if !valid[g.Role] {
				s.feedbackError(w, r, feedback.ErrInvalid)
				return
			}
		}
	}
	if (cmd.Action == "submit" || cmd.Action == "reply") && !s.requireVerification(w, r, "feedback", input.VerificationToken) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, e := s.feedback.Apply(ctx, r.PathValue("board"), feedbackActor(p), cmd, time.Now().UTC())
	if e != nil {
		s.feedbackError(w, r, e)
		return
	}
	replyPage := 0
	if result.ReplyID > 0 {
		b, e := s.feedback.Get(ctx, result.Slug)
		if e != nil {
			s.feedbackError(w, r, e)
			return
		}
		a := feedbackActor(p)
		count := 0
		for _, item := range b.Items {
			if item.ID != result.ItemID {
				continue
			}
			for _, reply := range item.Replies {
				if !reply.Hidden || feedback.Can(b, a, "moderate") {
					count++
				}
				if reply.ID == result.ReplyID {
					replyPage = max(1, (count+29)/30)
					break
				}
			}
		}
	}
	votes := 0
	voted := false
	if cmd.Action == "vote" {
		b, e := s.feedback.Get(ctx, result.Slug)
		if e != nil {
			s.feedbackError(w, r, e)
			return
		}
		for _, item := range b.Items {
			if item.ID == result.ItemID {
				votes = len(item.Votes)
				voted = slices.Contains(item.Votes, p.UserID)
				break
			}
		}
	}
	writeJSON(w, 200, struct {
		feedback.Result
		ReplyPage int  `json:"reply_page,omitempty"`
		VoteCount int  `json:"vote_count"`
		Voted     bool `json:"voted"`
	}{result, replyPage, votes, voted})
}
func (s *Server) feedbackExport(w http.ResponseWriter, r *http.Request) {
	feedbackNoStore(w)
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	b, e := s.feedback.Get(r.Context(), r.PathValue("board"))
	if e != nil {
		s.feedbackError(w, r, e)
		return
	}
	r, ok = s.freshSessionRequest(w, r)
	if !ok {
		return
	}
	p, ok = s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	a := feedbackActor(p)
	if !feedback.Can(b, a, "moderate") {
		s.feedbackError(w, r, feedback.ErrForbidden)
		return
	}
	access := func() error { return s.feedbackExportAccess(r, b.Slug) }
	if e = access(); e != nil {
		if !s.privateReadAuthError(w, r, e) {
			s.feedbackError(w, r, e)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="feedback-`+b.Slug+`.csv"`)
	protected := privateResponse(w, r, access)
	_, _ = protected.Write([]byte{0xef, 0xbb, 0xbf})
	c := csv.NewWriter(protected)
	_ = c.Write([]string{"编号", "标题", "内容", "类型", "分类", "状态", "可见性", "私密", "票数", "提交时间"})
	cell := func(v string) string {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "=") || strings.HasPrefix(v, "+") || strings.HasPrefix(v, "-") || strings.HasPrefix(v, "@") {
			return "'" + v
		}
		return v
	}
	for _, t := range b.Items {
		if !feedback.CanSee(b, a, t) {
			continue
		}
		_ = c.Write([]string{fmt.Sprint(t.ID), cell(t.Title), cell(t.Body), feedbackKindLabel(t.Kind), cell(t.Category), feedbackStatusLabel(t.Status), t.Visibility, strconv.FormatBool(t.Private), strconv.Itoa(len(t.Votes)), t.CreatedAt.Format(time.RFC3339)})
	}
	protected.check(true)
	c.Flush()
	if c.Error() != nil {
		panic(http.ErrAbortHandler)
	}
}
