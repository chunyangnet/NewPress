package httpserver

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/presentation"
)

type circleCard = presentation.Circle
type circleMemberView = presentation.CircleMember
type circleReplyView = presentation.CircleReply
type circleTopicView = presentation.CircleTopic
type circlesPageView = presentation.Circles

func (s *Server) SetCommunityStore(store community.Store) {
	s.circles = store
	if s.circleFiles == nil {
		s.circleFiles, _ = community.NewFiles("")
	}
	s.bindCircleNotifications()
}
func (s *Server) SetCommunityFiles(files *community.Files) { s.circleFiles = files }
func (s *Server) bindCircleNotifications() {
	if s.notifications == nil || s.circles == nil {
		return
	}
	enabled := func() bool { return s.config.CommunityEnabled && s.moduleEnabled("community") }
	s.notifications.Community, s.notifications.CommunityEnabled = s.circles, enabled
	for _, source := range s.notifications.Sources {
		if existing, ok := source.(*community.Source); ok {
			existing.Store, existing.Enabled = s.circles, enabled
			return
		}
	}
	s.notifications.Sources = append(s.notifications.Sources, &community.Source{Store: s.circles, Enabled: enabled})
}
func (s *Server) registerCircleRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/circles/{$}", s.circlesAdminPage)
	mux.HandleFunc("GET /admin/circles/{circle}/{$}", s.circlesAdminPage)
	mux.HandleFunc("GET /admin/circles/{circle}/topics/{topic}", s.circlesAdminPage)
	mux.HandleFunc("POST /api/v1/community/circles/{circle}/attachments", s.circleUpload)
	mux.HandleFunc("GET /api/v1/community/circles/{circle}/attachments/{attachment}", s.circleDownload)
	mux.HandleFunc("GET /api/v1/community/circles/{circle}/export", s.circleExport)
	mux.HandleFunc("POST /api/v1/community/circles/{circle}/preview", s.circlePreview)
	mux.HandleFunc("GET /community/{circle}/{$}", s.circlesPage)
	mux.HandleFunc("GET /community/{circle}/topics/{topic}", s.circlesPage)
	mux.HandleFunc("GET /api/v1/community/circles", s.circlesAPI)
	mux.HandleFunc("GET /api/v1/community/circles/{circle}", s.circlesAPI)
	mux.HandleFunc("GET /api/v1/community/circles/{circle}/topics/{topic}", s.circlesAPI)
	mux.HandleFunc("POST /api/v1/community/circles/{circle}", s.circlesWrite)
}
func (s *Server) circleActor(r *http.Request) (community.Actor, string) {
	p, ok := s.currentAdmin(r)
	if !ok || p.UserID <= 0 {
		return community.Actor{}, ""
	}
	return community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}, p.CSRFToken
}
func strictCircleQuery(r *http.Request) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, community.ErrInvalid
	}
	q, ok := strictQueryShape(q, map[string]bool{"page": true, "members_page": true, "reports_page": true, "history_page": true, "q": true, "scope": true, "kind": true, "filter": true, "sort": true})
	if !ok {
		return nil, community.ErrInvalid
	}
	for _, key := range []string{"page", "members_page", "reports_page", "history_page"} {
		if _, e = positiveQueryInt(q.Get(key), 1, 100000); e != nil {
			return nil, community.ErrInvalid
		}
	}
	if !utf8.ValidString(q.Get("q")) || utf8.RuneCountInString(q.Get("q")) > 80 || strings.IndexFunc(q.Get("q"), unicode.IsControl) >= 0 {
		return nil, community.ErrInvalid
	}
	for key, options := range map[string]map[string]bool{
		"scope":  {"": true, "all": true, "mine": true, "archived": true},
		"kind":   {"": true, "discussion": true, "question": true},
		"filter": {"": true, "featured": true, "unanswered": true, "archived": true, "mine": true, "participated": true},
		"sort":   {"": true, "newest": true, "active": true},
	} {
		if !options[q.Get(key)] {
			return nil, community.ErrInvalid
		}
	}
	return q, nil
}
func circlePageURL(path string, query url.Values, key string, value int, fragment string) string {
	next := url.Values{}
	for k, v := range query {
		if len(v) > 0 && v[0] != "" {
			next.Set(k, v[0])
		}
	}
	next.Set(key, strconv.Itoa(value))
	return path + "?" + next.Encode() + fragment
}
func circleRoleLabel(role string) string {
	return map[string]string{"owner": "圈主", "moderator": "版主", "member": "成员", "pending": "申请待审核", "banned": "已被移出", "": "未加入"}[role]
}
func (s *Server) circleName(ctx context.Context, id int64) string {
	u, err := s.auth.GetUser(ctx, id)
	if err != nil || u.Status != auth.UserStatusActive {
		return fmt.Sprintf("账号 #%d", id)
	}
	return u.DisplayName
}
func (s *Server) circleCard(ctx context.Context, c community.Circle, a community.Actor) circleCard {
	role := community.Role(c, a)
	v := circleCard{Slug: c.Slug, Name: c.Name, Description: c.Description, Rules: c.Rules, URL: "/community/" + c.Slug + "/", JoinPolicy: c.JoinPolicy, JoinLabel: map[string]string{"open": "开放加入", "approval": "申请审核", "closed": "暂停加入"}[c.JoinPolicy], Visibility: c.Visibility, VisibilityLabel: map[string]string{"public": "公开阅读", "members": "成员可见"}[c.Visibility], Role: role, RoleLabel: circleRoleLabel(role), Owner: s.circleName(ctx, c.OwnerID), Version: c.Version, CanRead: community.CanRead(c, a), CanPost: community.CanPost(c, a), Manage: community.Manages(c, a), Own: community.Owns(c, a)}
	v.PostPolicy, v.PostLabel, v.JoinReason = c.PostPolicy, "成员可发布", c.JoinReason
	if c.PostPolicy == "moderators" {
		v.PostLabel = "仅圈主与版主发布"
	}
	v.CanJoin = a.ID > 0 && role == "" && c.JoinPolicy != "closed"
	v.CanLeave = a.ID > 0 && role != "" && role != "owner" && role != "banned"
	v.Archived, v.CanReport = c.Archived, a.ID > 0 && role != "banned" && v.CanRead
	v.CanJoin = v.CanJoin && !c.Archived
	if v.Manage {
		v.ManageURL = "/admin/circles/" + c.Slug + "/"
		v.TopicsUsed, v.TopicLimit, v.FilesUsed, v.FilesLimit = len(c.Topics), community.MaxTopics, len(c.Attachments), community.MaxCircleAttachmentBytes
		for _, file := range c.Attachments {
			v.FilesBytes += file.Size
		}
		for _, report := range c.Reports {
			if report.Status == "open" {
				v.OpenReports++
			}
		}
	}
	if v.CanRead {
		v.MemberCount = 1
		for _, m := range c.Members {
			if m.Role == "member" || m.Role == "moderator" {
				v.MemberCount++
			}
			if v.Manage && m.Role == "pending" {
				v.PendingCount++
			}
		}
		for _, topic := range c.Topics {
			if topic.Hidden && !v.Manage {
				continue
			}
			v.TopicCount++
			for _, reply := range topic.Replies {
				if !reply.Hidden || v.Manage {
					v.ReplyCount++
				}
			}
		}
	}
	return v
}
func (s *Server) circleProjection(r *http.Request) (circlesPageView, error) {
	return s.circleProjectionMode(r, true)
}

// Public screens only load personal records. The authenticated workspace and API
// may project the full moderation data according to the circle's own roles.
func (s *Server) circleProjectionMode(r *http.Request, management bool) (circlesPageView, error) {
	a, csrf := s.circleActor(r)
	out := circlesPageView{SignedIn: a.ID > 0, CanCreate: a.Admin, CanManage: a.Admin, UserID: a.ID, CSRF: csrf, Items: []circleCard{}, Topics: []circleTopicView{}, Members: []circleMemberView{}, Page: 1, Pages: 1, MemberPage: 1, MemberPages: 1}
	query, err := strictCircleQuery(r)
	if err != nil {
		return out, err
	}
	page, _ := positiveQueryInt(query.Get("page"), 1, 100000)
	mpage, _ := positiveQueryInt(query.Get("members_page"), 1, 100000)
	out.Query, out.Scope, out.Kind, out.Filter, out.Sort = strings.TrimSpace(query.Get("q")), query.Get("scope"), query.Get("kind"), query.Get("filter"), query.Get("sort")
	search := strings.ToLower(out.Query)
	slug := r.PathValue("circle")
	if slug == "" {
		out.Directory = true
		items, err := s.circles.List(r.Context())
		if err != nil {
			return out, err
		}
		for _, c := range items {
			role := community.Role(c, a)
			out.CanManage = out.CanManage || community.Manages(c, a)
			if c.Archived {
				if out.Scope != "archived" || !community.Manages(c, a) && role != "member" {
					continue
				}
			} else if out.Scope == "archived" {
				continue
			}
			if out.Scope == "mine" && (role == "" || role == "banned") || search != "" && !strings.Contains(strings.ToLower(c.Name+" "+c.Description), search) {
				continue
			}
			out.Items = append(out.Items, s.circleCard(r.Context(), c, a))
		}
		out.Total = len(out.Items)
		return out, nil
	}
	if !community.ValidSlug(slug) {
		return out, community.ErrNotFound
	}
	c, err := s.circles.Get(r.Context(), slug)
	if err != nil {
		return out, err
	}
	out.Circle = s.circleCard(r.Context(), c, a)
	s.circleManagementProjection(r, c, a, query, &out, management)
	tid := int64(0)
	if raw := r.PathValue("topic"); raw != "" {
		tid, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || tid <= 0 || strconv.FormatInt(tid, 10) != raw {
			return out, community.ErrNotFound
		}
	}
	if !out.Circle.CanRead {
		if tid > 0 {
			return out, community.ErrNotFound
		}
		return out, nil
	}
	location := s.siteLocation(r.Context())
	names := map[int64]string{}
	name := func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := s.circleName(r.Context(), id)
		names[id] = n
		return n
	}
	topicView := func(t community.Topic) circleTopicView {
		v := circleTopicView{ID: t.ID, Title: t.Title, Kind: t.Kind, Author: name(t.AuthorID), URL: out.Circle.URL + "topics/" + strconv.FormatInt(t.ID, 10), Date: siteTime(location, t.CreatedAt, "2006.01.02"), Pinned: t.Pinned, Featured: t.Featured, Locked: t.Locked, Hidden: t.Hidden, AcceptedID: t.AcceptedID, CanAccept: out.Circle.CanPost && t.Kind == "question" && !t.Hidden && (out.Circle.Manage || t.AuthorID == a.ID)}
		v.CanEdit = community.CanEditTopic(c, t, a)
		if !management {
			v.CanEdit = v.CanEdit && t.AuthorID == a.ID
			v.CanAccept = v.CanAccept && t.AuthorID == a.ID
		}
		v.Format, v.Archived, v.CanDelete = t.Format, t.Archived, out.Circle.Own && (t.Hidden || t.Archived)
		if !t.EditedAt.IsZero() {
			v.Edited = siteTime(location, t.EditedAt, "2006.01.02 15:04")
		}
		for _, reply := range t.Replies {
			if !reply.Hidden || out.Circle.Manage {
				v.ReplyCount++
			}
		}
		return v
	}
	if tid > 0 {
		index := -1
		for i, t := range c.Topics {
			if t.ID == tid && (!t.Hidden || out.Circle.Manage) {
				index = i
				break
			}
		}
		if index < 0 {
			return out, community.ErrNotFound
		}
		t := c.Topics[index]
		out.Topic = topicView(t)
		out.Topic.Body = t.Body
		out.Topic.Attachments = circleAttachments(c, t.Attachments)
		out.Topic.BodyHTML = template.HTML(community.RenderBody(c.Slug, t.Body, t.Format, circleAttachmentFacts(c, t.Attachments)))
		replies := []community.Reply{}
		for _, reply := range t.Replies {
			if !reply.Hidden || out.Circle.Manage {
				replies = append(replies, reply)
			}
		}
		out.Total = len(replies)
		out.Pages = max(1, (out.Total+24)/25)
		out.Page = min(page, out.Pages)
		start := (out.Page - 1) * 25
		for _, reply := range replies[start:min(start+25, len(replies))] {
			view := circleReplyView{ID: reply.ID, Author: name(reply.AuthorID), Body: reply.Body, Date: siteTime(location, reply.CreatedAt, "2006.01.02 15:04"), Hidden: reply.Hidden, Accepted: reply.ID == t.AcceptedID, CanEdit: community.CanEditReply(c, t, reply, a)}
			view.Format, view.Attachments, view.CanDelete = reply.Format, circleAttachments(c, reply.Attachments), out.Circle.Own && reply.Hidden
			if !management {
				view.CanEdit = view.CanEdit && reply.AuthorID == a.ID
			}
			view.BodyHTML = template.HTML(community.RenderBody(c.Slug, reply.Body, reply.Format, circleAttachmentFacts(c, reply.Attachments)))
			if !reply.EditedAt.IsZero() {
				view.Edited = siteTime(location, reply.EditedAt, "2006.01.02 15:04")
			}
			out.Topic.Replies = append(out.Topic.Replies, view)
		}
		if out.Page > 1 {
			out.Previous = circlePageURL(out.Topic.URL, query, "page", out.Page-1, "")
		}
		if out.Page < out.Pages {
			out.Next = circlePageURL(out.Topic.URL, query, "page", out.Page+1, "")
		}
	} else {
		topics := []community.Topic{}
		for _, t := range c.Topics {
			if out.Filter == "mine" && (a.ID == 0 || t.AuthorID != a.ID) {
				continue
			}
			if out.Filter == "participated" {
				participated := false
				for _, reply := range t.Replies {
					if a.ID > 0 && reply.AuthorID == a.ID && !reply.Hidden {
						participated = true
						break
					}
				}
				if !participated {
					continue
				}
			}
			if t.Archived != (out.Filter == "archived") {
				continue
			}
			if t.Hidden && !out.Circle.Manage || out.Kind != "" && t.Kind != out.Kind || out.Filter == "featured" && !t.Featured || out.Filter == "unanswered" && (t.Kind != "question" || t.AcceptedID != 0) || search != "" && !strings.Contains(strings.ToLower(t.Title+" "+t.Body), search) {
				continue
			}
			topics = append(topics, t)
		}
		sortCircleTopics(topics, out.Sort, out.Circle.Manage)
		out.Total = len(topics)
		out.Pages = max(1, (out.Total+19)/20)
		out.Page = min(page, out.Pages)
		start := (out.Page - 1) * 20
		for _, t := range topics[start:min(start+20, len(topics))] {
			out.Topics = append(out.Topics, topicView(t))
		}
		if out.Page > 1 {
			out.Previous = circlePageURL(out.Circle.URL, query, "page", out.Page-1, "")
		}
		if out.Page < out.Pages {
			out.Next = circlePageURL(out.Circle.URL, query, "page", out.Page+1, "")
		}
		if management && out.Circle.Manage {
			members := append([]community.Member{}, c.Members...)
			sort.SliceStable(members, func(i, j int) bool {
				if (members[i].Role == "pending") != (members[j].Role == "pending") {
					return members[i].Role == "pending"
				}
				return members[i].UserID < members[j].UserID
			})
			out.MemberPages = max(1, (len(members)+49)/50)
			out.MemberPage = min(mpage, out.MemberPages)
			start := (out.MemberPage - 1) * 50
			for _, m := range members[start:min(start+50, len(members))] {
				view := circleMemberView{ID: m.UserID, Name: name(m.UserID), Role: m.Role, Label: circleRoleLabel(m.Role), Change: m.UserID != a.ID && (out.Circle.Own || m.Role != "moderator"), Transfer: out.Circle.Own && (m.Role == "member" || m.Role == "moderator"), Reason: m.Reason}
				if !m.JoinedAt.IsZero() {
					view.Joined = siteTime(location, m.JoinedAt, "2006.01.02")
				}
				out.Members = append(out.Members, view)
			}
			if out.MemberPage > 1 {
				out.MemberPrevious = circlePageURL(out.Circle.URL, query, "members_page", out.MemberPage-1, "#circle-members")
			}
			if out.MemberPage < out.MemberPages {
				out.MemberNext = circlePageURL(out.Circle.URL, query, "members_page", out.MemberPage+1, "#circle-members")
			}
		}
	}
	return out, nil
}
func (s *Server) circleError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "community.unavailable", "社区暂时不可用，请保留输入后重试"
	switch {
	case errors.Is(err, community.ErrNotFound):
		status, code, message = 404, "community.not_found", "圈子或话题当前不可用"
	case errors.Is(err, community.ErrForbidden):
		status, code, message = 403, "community.forbidden", "当前身份不能执行此操作"
	case errors.Is(err, community.ErrInvalid):
		status, code, message = 422, "community.invalid", "请检查字段、长度及操作选项"
	case errors.Is(err, community.ErrConflict):
		status, code, message = 409, "community.conflict", "圈子已更新或请求标识冲突，请核对最新状态后再提交"
	case errors.Is(err, community.ErrCapacity):
		status, code, message = 422, "community.capacity", "圈子容量已达到上限，请联系管理员"
	default:
		slog.Error("community request failed", "error", err)
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
func (s *Server) circleEnabled(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	if !s.config.CommunityEnabled || s.circles == nil {
		s.circleError(w, r, community.ErrNotFound)
		return false
	}
	return true
}
func (s *Server) circlesAPI(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	v, err := s.circleProjection(r)
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) circlesPage(w http.ResponseWriter, r *http.Request) {
	if !s.config.CommunityEnabled {
		s.renderServiceStatus(w, 404, "圈子", "disabled")
		return
	}
	if s.circles == nil {
		s.renderServiceStatus(w, 404, "圈子", "unavailable")
		return
	}
	if !s.circleEnabled(w, r) {
		return
	}
	v, err := s.circleProjectionMode(r, false)
	if err != nil {
		status := 503
		if errors.Is(err, community.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, community.ErrInvalid) {
			status = 400
		}
		s.renderError(w, status, "圈子或话题暂时不可用")
		return
	}
	title, description := "圈子", "找到共同兴趣，分享值得继续的讨论。"
	canonical := "/community/"
	if v.Circle.Slug != "" {
		title = v.Circle.Name
		description = v.Circle.Description
		canonical = v.Circle.URL
	}
	if v.Topic.ID > 0 {
		title = v.Topic.Title
		canonical = v.Topic.URL
	}
	data := s.baseViewFor(w, title, description, canonical)
	data.BodyClass = "is-circles"
	data.ActiveNav = "community"
	data.Circles = v
	if v.Circle.Visibility == "members" {
		data.Site.Robots = "noindex, nofollow, noarchive"
		w.Header().Set("X-Robots-Tag", data.Site.Robots)
	}
	s.render(w, 200, "circles", data)
}
func (s *Server) circlesWrite(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var cmd community.Command
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &cmd, 64<<10, "64 KiB"); !ok {
		return
	}
	if cmd.Action == "add_attachment" || cmd.Attachment != nil {
		s.circleError(w, r, community.ErrInvalid)
		return
	}
	if cmd.Action == "report" {
		if allowed, _ := s.accessRate.allow("circle-report:"+strconv.FormatInt(p.UserID, 10), 10, time.Now()); !allowed {
			s.writeAPIError(w, r, 429, "community.rate_limited", "举报提交较频繁，请稍后再试", nil)
			return
		}
	}
	if allowed, retry := s.accessRate.allow("community:"+strconv.FormatInt(p.UserID, 10), 120, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "community.rate_limited", "操作过于频繁，请稍后重试", nil)
		return
	}
	actor := community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}
	if cmd.Action == "create" {
		if !actor.Admin {
			s.circleError(w, r, community.ErrForbidden)
			return
		}
		if cmd.OwnerID == 0 {
			cmd.OwnerID = actor.ID
		}
	}
	id := cmd.OwnerID
	if cmd.Action == "transfer" || cmd.Action == "member" && cmd.Role != "banned" {
		id = cmd.UserID
	}
	if id > 0 {
		user, err := s.auth.GetUser(r.Context(), id)
		if err != nil || user.Status != auth.UserStatusActive {
			s.circleError(w, r, community.ErrInvalid)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var previous community.Circle
	if cmd.Action == "delete_topic" || cmd.Action == "delete_reply" || cmd.Action == "remove_attachment" {
		previous, _ = s.circles.Get(ctx, r.PathValue("circle"))
	}
	result, err := s.circles.Apply(ctx, r.PathValue("circle"), actor, cmd, time.Now().UTC())
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	if len(previous.Attachments) > 0 {
		s.removeCircleOrphans(ctx, previous)
	}
	writeJSON(w, 200, result)
}
