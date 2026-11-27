package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"strings"
	"time"
)

func (s *Server) socialImportPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var body struct {
		AccountID int64    `json:"account_id"`
		Bookmarks []string `json:"bookmarks"`
		Likes     []int64  `json:"likes"`
		Following []int64  `json:"following"`
		Preview   *bool    `json:"preview"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 64<<10, "64 KiB"); !ok {
		return
	}
	if body.AccountID != p.UserID {
		s.writeAPIError(w, r, 409, "account.changed", "登录账号已变化，请刷新后重新选择导入", nil)
		return
	}
	if body.Preview == nil || len(body.Bookmarks) > 200 || len(body.Likes) > 500 || len(body.Following) > 200 {
		s.engagementError(w, r, engagement.ErrInvalid)
		return
	}
	inputs := []engagement.Input{}
	skipped := 0
	for _, path := range body.Bookmarks {
		u, err := url.Parse(path)
		if err != nil || len(path) > 1024 || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/posts/") {
			skipped++
			continue
		}
		post, err := s.contentAtPublicPath(r.Context(), strings.TrimSuffix(u.Path, "/"))
		if errors.Is(err, content.ErrNotFound) || err == nil && post.Type != content.TypePost {
			skipped++
			continue
		}
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		inputs = append(inputs, engagement.Input{UserID: p.UserID, TargetID: post.ID, Kind: engagement.Bookmark, Active: true, Collection: "本机收藏"})
	}
	for _, id := range body.Likes {
		if id <= 0 {
			s.engagementError(w, r, engagement.ErrInvalid)
			return
		}
		post, err := s.repository.GetByID(r.Context(), id)
		if errors.Is(err, content.ErrNotFound) || err == nil && (post.Type != content.TypePost || !content.PublicAt(post, time.Now())) {
			skipped++
			continue
		}
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		inputs = append(inputs, engagement.Input{UserID: p.UserID, TargetID: id, Kind: engagement.Like, Active: true})
	}
	for _, id := range body.Following {
		if id <= 0 {
			s.engagementError(w, r, engagement.ErrInvalid)
			return
		}
		u, err := s.auth.GetUser(r.Context(), id)
		if errors.Is(err, auth.ErrUserNotFound) || err == nil && (u.Status != auth.UserStatusActive || id == p.UserID) {
			skipped++
			continue
		}
		if err != nil {
			s.engagementError(w, r, err)
			return
		}
		inputs = append(inputs, engagement.Input{UserID: p.UserID, TargetID: id, Kind: engagement.Follow, Active: true})
	}
	result, err := s.engagements.Merge(r.Context(), p.UserID, inputs, *body.Preview)
	if err != nil {
		s.engagementError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"preview": *body.Preview, "added": result.Added, "existing": result.Existing, "skipped": skipped})
}
