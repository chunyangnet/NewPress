package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

type accountView struct {
	Authenticated          bool
	ID                     int64
	Name, Username, Joined string
	AvatarURL              string
	Initial, Bio, RoleName string
	CanWrite               bool
	Badges                 []progressionBadge
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/account/" {
		s.renderError(w, 404, "个人中心没有找到")
		return
	}
	data := s.baseViewFor(w, "个人中心", "收藏值得重读的内容，关注你喜欢的创作者。", r.URL.RequestURI())
	// Account pages need navigation state without publishing a canonical URL.
	data.Canonical = ""
	data.BodyClass, data.ActiveNav = "is-account", "account"
	data.Site.Robots = "noindex, nofollow, noarchive"
	w.Header().Add("Vary", "Cookie")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if p, ok := s.currentAdmin(r); ok && p.UserID > 0 {
		data.Account = accountView{
			Authenticated: true, ID: p.UserID, Name: p.Label(), Username: p.Username, RoleName: p.RoleName,
			CanWrite: p.Can(auth.CapabilityAdminAccess) && (p.Can(auth.CapabilityContentWrite) || p.Can(auth.CapabilityContentWriteOwn)),
		}
		for _, initial := range p.Label() {
			data.Account.Initial = string(initial)
			break
		}
		if user, err := s.auth.GetUser(r.Context(), p.UserID); err == nil {
			data.Account.Joined = s.monthLabel(user.CreatedAt, r.Context())
			data.Account.AvatarURL = s.avatarURL(user.AvatarMediaID)
			data.Account.Bio = user.Bio
		}
		data.Account.Badges = s.progressionBadges(r.Context(), p.UserID, false)
	}
	s.render(w, 200, "account", data)
}

func targetIDs(value string) ([]int64, error) {
	ids := []int64{}
	if value == "" {
		return ids, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 100 {
		return nil, engagement.ErrInvalid
	}
	seen := map[int64]bool{}
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 || seen[id] || strconv.FormatInt(id, 10) != part {
			return nil, engagement.ErrInvalid
		}
		ids = append(ids, id)
		seen[id] = true
	}
	return ids, nil
}

func (s *Server) socialEngagement(w http.ResponseWriter, r *http.Request) {
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"posts": true, "authors": true})
	if !ok {
		return
	}
	posts, err := targetIDs(values.Get("posts"))
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	authors, err := targetIDs(values.Get("authors"))
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	publicPosts := []int64{}
	publicAuthors := []int64{}
	for _, id := range posts {
		post, err := s.repository.GetByID(r.Context(), id)
		if errors.Is(err, content.ErrNotFound) {
			continue
		}
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		if s.publicEngagementTarget(post) {
			publicPosts = append(publicPosts, id)
		}
	}
	for _, id := range authors {
		u, err := s.auth.GetUser(r.Context(), id)
		if errors.Is(err, auth.ErrUserNotFound) {
			continue
		}
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		if u.Status == auth.UserStatusActive {
			publicAuthors = append(publicAuthors, id)
		}
	}
	var userID int64
	if p, ok := s.currentAdmin(r); ok {
		userID = p.UserID
	}
	result := map[string][]engagement.State{}
	for _, kind := range []string{engagement.Bookmark, engagement.Like, engagement.Follow} {
		ids := publicPosts
		if kind == engagement.Follow {
			ids = publicAuthors
		}
		states, err := s.engagements.States(r.Context(), userID, kind, ids)
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		result[kind] = states
	}
	writeJSON(w, 200, result)
}

func (s *Server) socialSetEngagement(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Kind       string `json:"kind"`
		TargetID   int64  `json:"target_id"`
		Active     *bool  `json:"active"`
		Collection string `json:"collection"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 2048, "2 KiB"); !ok {
		return
	}
	if body.Active == nil {
		s.engagementError(w, r, engagement.ErrInvalid)
		return
	}
	input, err := engagement.Normalize(engagement.Input{UserID: p.UserID, TargetID: body.TargetID, Kind: body.Kind, Active: *body.Active, Collection: body.Collection})
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	if input.Active {
		if input.Kind == engagement.Follow {
			u, err := s.auth.GetUser(r.Context(), input.TargetID)
			if errors.Is(err, auth.ErrUserNotFound) || err == nil && u.Status != auth.UserStatusActive {
				s.writeAPIError(w, r, 404, "engagement.target_missing", "这个创作者当前不可用", nil)
				return
			}
			if err != nil {
				s.engagementError(w, r, err)
				return
			}
		} else {
			post, err := s.repository.GetByID(r.Context(), input.TargetID)
			if errors.Is(err, content.ErrNotFound) || err == nil && !s.publicEngagementTarget(post) {
				s.writeAPIError(w, r, 404, "engagement.target_missing", "这篇文章当前不可用", nil)
				return
			}
			if err != nil {
				s.engagementError(w, r, err)
				return
			}
		}
	}
	if err := s.engagements.Set(r.Context(), input); err != nil {
		s.engagementError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "active": input.Active, "target_id": input.TargetID, "collection": input.Collection})
}

type libraryPost struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Excerpt     string    `json:"excerpt"`
	URL         string    `json:"url"`
	CoverURL    string    `json:"cover_url"`
	AuthorID    int64     `json:"author_id"`
	AuthorName  string    `json:"author_name"`
	AuthorURL   string    `json:"author_url"`
	Collection  string    `json:"collection"`
	PublishedAt time.Time `json:"published_at"`
}

func (s *Server) socialLibrary(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"kind": true, "page": true, "q": true, "collection": true})
	if !ok {
		return
	}
	kind := values.Get("kind")
	if kind == "" {
		kind = engagement.Bookmark
	}
	storeKind := kind
	if kind == "feed" {
		storeKind = engagement.Follow
	}
	page, err := positiveQueryInt(values.Get("page"), 1, 10000)
	collection := strings.TrimSpace(values.Get("collection"))
	if err != nil || engagement.Capacity(storeKind) == 0 || !engagement.ValidCollection(collection) || (collection != "" && kind != engagement.Bookmark) {
		s.engagementError(w, r, engagement.ErrInvalid)
		return
	}
	query, err := content.NormalizePublishedQuery(content.PublishedQuery{Search: values.Get("q"), Limit: 24, Offset: (page - 1) * 24})
	if err != nil {
		s.engagementError(w, r, engagement.ErrInvalid)
		return
	}
	entries, err := s.engagements.Entries(r.Context(), p.UserID, storeKind)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	ids := []int64{}
	collections := map[string]int{}
	byID := map[int64]string{}
	contacts := []socialContact{}
	for _, e := range entries {
		if storeKind == engagement.Bookmark {
			collections[e.Collection]++
		}
		if collection != "" && collection != e.Collection {
			continue
		}
		if storeKind == engagement.Follow {
			u, err := s.auth.GetUser(r.Context(), e.TargetID)
			if errors.Is(err, auth.ErrUserNotFound) || err == nil && u.Status != auth.UserStatusActive {
				continue
			}
			if err != nil {
				s.engagementError(w, r, err)
				return
			}
			if kind == engagement.Follow && (query.Search == "" || strings.Contains(strings.ToLower(u.DisplayName+"\n"+u.Username), strings.ToLower(query.Search))) {
				contacts = append(contacts, socialContact{ID: u.ID, Name: u.DisplayName, Username: u.Username, Available: true})
			}
		}
		ids = append(ids, e.TargetID)
		byID[e.TargetID] = e.Collection
	}
	if kind == engagement.Follow {
		start := min(query.Offset, len(contacts))
		end := min(start+query.Limit, len(contacts))
		writeJSON(w, 200, map[string]any{"items": contacts[start:end], "total": len(contacts), "page": page, "pages": max(1, (len(contacts)+23)/24), "collections": collections})
		return
	}
	if kind == "feed" {
		query.AuthorIDs = ids
	} else {
		query.PostIDs = ids
		query.Sort = "selection"
	}
	query.IncludeFlash = kind != "feed" && s.moduleEnabled("flash")
	posts, err := s.repository.SearchPublished(r.Context(), query)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	items := []libraryPost{}
	for _, post := range posts.Items {
		items = append(items, libraryPost{ID: post.ID, Title: post.Title, Excerpt: post.Excerpt, URL: s.contentPath(post), CoverURL: post.CoverURL, AuthorID: post.AuthorID, AuthorName: post.AuthorName, AuthorURL: post.AuthorURL(), Collection: byID[post.ID], PublishedAt: post.PublishedAt})
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": posts.Total, "page": page, "pages": max(1, (posts.Total+23)/24), "collections": collections})
}

func (s *Server) engagementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, engagement.ErrInvalid):
		s.writeAPIError(w, r, 422, "engagement.invalid", "请检查操作类型、目标编号和收藏夹名称（最多 40 字）", nil)
	case errors.Is(err, engagement.ErrLimit):
		s.writeAPIError(w, r, 409, "engagement.limit", "已达到收藏、点赞或关注上限，请先整理已有记录", nil)
	default:
		slog.Error("account engagement", "error", err)
		s.writeAPIError(w, r, 503, "engagement.unavailable", "个人内容库暂时不可用，请重试", nil)
	}
}

func (s *Server) socialProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	writeJSON(w, 200, s.profileDTO(u))
}

func (s *Server) socialUpdateProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		DisplayName  string  `json:"display_name"`
		Locale       string  `json:"locale"`
		Version      int64   `json:"version"`
		Bio          *string `json:"bio"`
		RemoveAvatar bool    `json:"remove_avatar"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	if input.Version <= 0 {
		s.writeAPIError(w, r, 422, "profile.version_required", "请刷新个人资料后再保存", nil)
		return
	}
	if strings.TrimSpace(input.DisplayName) == "" || strings.TrimSpace(input.Locale) == "" {
		s.writeAPIError(w, r, 422, "profile.invalid", "请填写公开昵称和语言标识", nil)
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	profile := auth.ProfileInput{DisplayName: input.DisplayName, Locale: input.Locale, Bio: u.Bio, AvatarMediaID: u.AvatarMediaID}
	if input.Bio != nil {
		profile.Bio = *input.Bio
	}
	if input.RemoveAvatar {
		profile.AvatarMediaID = 0
	}
	release, err := s.mediaUsage.HoldAvatar(profile.AvatarMediaID)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	updated, err := s.auth.UpdateProfile(r.Context(), p, input.Version, profile)
	release()
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	if input.RemoveAvatar {
		s.cleanupProfileAvatar(r, u.AvatarMediaID, p.UserID)
	}
	response := s.profileDTO(updated)
	response["ok"] = true
	writeJSON(w, 200, response)
}
