package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/notification"
)

type noticeBoundary struct {
	MessageID  int64 `json:"message_id"`
	ActivityID int64 `json:"activity_id"`
}
type centerNotice struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	PeerID    int64     `json:"peer_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
	numeric   int64
}

func parseNoticeCursor(raw string) (int64, int64, error) {
	if raw == "" {
		return 0, 0, nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 44 {
		return 0, 0, notification.ErrInvalid
	}
	a, err := strconv.ParseInt(parts[0], 10, 64)
	b, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || err2 != nil || a < -1 || b < -1 || fmt.Sprintf("%d.%d", a, b) != raw {
		return 0, 0, notification.ErrInvalid
	}
	return a, b, nil
}
func (s *Server) noticeCenter(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"filter": true, "before": true})
	if !ok {
		return
	}
	filter := q.Get("filter")
	if filter != "" && filter != "all" && filter != "interaction" && filter != "activity" && filter != "unread" {
		s.notificationError(w, r, notification.ErrInvalid)
		return
	}
	mb, ab, err := parseNoticeCursor(q.Get("before"))
	if err != nil {
		s.notificationError(w, r, err)
		return
	}
	messages := messaging.NoticePage{}
	if s.moduleEnabled("messaging") {
		messages, err = s.messages.Notices(r.Context(), p.UserID, messaging.NoticeQuery{Before: max(mb, 0), UnreadOnly: filter == "unread"})
		if err != nil {
			s.socialError(w, r, err)
			return
		}
	}
	activities, err := s.notifications.Store.Notices(r.Context(), p.UserID, notification.Query{Before: max(ab, 0), UnreadOnly: filter == "unread"})
	if err != nil {
		s.notificationError(w, r, err)
		return
	}
	items := []centerNotice{}
	messageCount, activityCount := 0, 0
	if mb != -1 && filter != "activity" {
		for _, n := range messages.Items {
			c := s.socialContact(r, n.PeerID)
			items = append(items, centerNotice{ID: fmt.Sprintf("m%d", n.ID), numeric: n.ID, Kind: "message", Title: c.Name + " 发来私信", PeerID: n.PeerID, CreatedAt: n.CreatedAt, Read: n.ReadAt != nil})
			messageCount++
		}
	}
	if ab != -1 && filter != "interaction" {
		for _, n := range activities.Items {
			view, visible, err := s.notifications.Resolve(r.Context(), n.Delivery, time.Now())
			if err != nil {
				s.notificationError(w, r, err)
				return
			}
			if !visible {
				view = notification.View{Title: "这条内容当前不可用"}
			}
			items = append(items, centerNotice{ID: fmt.Sprintf("a%d", n.ID), numeric: n.ID, Kind: n.Kind, Title: view.Title, URL: view.URL, CreatedAt: n.CreatedAt, Read: n.ReadAt != nil})
			activityCount++
		}
	}
	// Merge heads without reordering either source. Each source cursor follows
	// its own IDs, even if clocks moved backwards or instances have clock skew.
	messagesToMerge, activitiesToMerge := items[:messageCount], items[messageCount:]
	merged := make([]centerNotice, 0, min(len(items), 50))
	for mi, ai := 0, 0; len(merged) < 50 && (mi < len(messagesToMerge) || ai < len(activitiesToMerge)); {
		if mi < len(messagesToMerge) && (ai == len(activitiesToMerge) || !messagesToMerge[mi].CreatedAt.Before(activitiesToMerge[ai].CreatedAt)) {
			merged = append(merged, messagesToMerge[mi])
			mi++
		} else {
			merged = append(merged, activitiesToMerge[ai])
			ai++
		}
	}
	items = merged
	usedM, usedA := 0, 0
	for _, n := range items {
		if n.Kind == "message" {
			mb = n.numeric
			usedM++
		} else {
			ab = n.numeric
			usedA++
		}
	}
	moreM := filter != "activity" && mb != -1 && (usedM < messageCount || messages.HasMore)
	moreA := filter != "interaction" && ab != -1 && (usedA < activityCount || activities.HasMore)
	if !moreM {
		mb = -1
	}
	if !moreA {
		ab = -1
	}
	writeJSON(w, 200, map[string]any{"items": items, "unread": messages.Unread + activities.Unread, "has_more": moreM || moreA, "next_cursor": fmt.Sprintf("%d.%d", mb, ab), "through": noticeBoundary{messages.Through, activities.Through}})
}
func (s *Server) updateNoticeCenter(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Action  string         `json:"action"`
		ID      string         `json:"id"`
		Through noticeBoundary `json:"through"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	if !s.moduleEnabled("messaging") && (strings.HasPrefix(input.ID, "m") || input.Through.MessageID > 0) {
		s.writeAPIError(w, r, http.StatusNotFound, "module.disabled", "此功能已由站点管理员停用", map[string]string{"module": "messaging"})
		return
	}
	if input.Action == "read" {
		if len(input.ID) < 2 || input.Through != (noticeBoundary{}) {
			s.notificationError(w, r, notification.ErrInvalid)
			return
		}
		id, err := positivePathID(input.ID[1:])
		if err != nil {
			s.notificationError(w, r, notification.ErrInvalid)
			return
		}
		switch input.ID[0] {
		case 'm':
			err = s.messages.UpdateNotices(r.Context(), p.UserID, id, "read")
		case 'a':
			err = s.notifications.Store.UpdateNotices(r.Context(), p.UserID, id, "read", time.Now().UTC())
		default:
			err = notification.ErrInvalid
		}
		if err != nil {
			s.notificationError(w, r, err)
			return
		}
	} else {
		if input.ID != "" || input.Through.MessageID < 0 || input.Through.ActivityID < 0 || input.Through == (noticeBoundary{}) || input.Action != "read_all" && input.Action != "clear" {
			s.notificationError(w, r, notification.ErrInvalid)
			return
		}
		if input.Through.MessageID > 0 {
			if err := s.messages.UpdateNotices(r.Context(), p.UserID, input.Through.MessageID, input.Action); err != nil {
				s.notificationError(w, r, err)
				return
			}
		}
		if input.Through.ActivityID > 0 {
			if err := s.notifications.Store.UpdateNotices(r.Context(), p.UserID, input.Through.ActivityID, input.Action, time.Now().UTC()); err != nil {
				s.notificationError(w, r, err)
				return
			}
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func preferenceDTO(p notification.Preferences) map[string]any {
	return map[string]any{"version": p.Version, "reply": p.Reply, "mention": p.Mention, "publication": p.Publication, "moderation": p.Moderation, "email": p.Email, "weekly": p.Weekly}
}
func (s *Server) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.socialPrincipal(w, r, r.Method == http.MethodPost)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		if _, ok = s.strictAPIQuery(w, r, nil); !ok {
			return
		}
	}
	u, err := s.auth.GetUser(r.Context(), principal.UserID)
	if err != nil {
		s.notificationError(w, r, err)
		return
	}
	p, err := s.notifications.Store.Preferences(r.Context(), principal.UserID)
	if err != nil {
		s.notificationError(w, r, err)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			Version     *int64 `json:"version"`
			Reply       *bool  `json:"reply"`
			Mention     *bool  `json:"mention"`
			Publication *bool  `json:"publication"`
			Moderation  *bool  `json:"moderation"`
			Email       *bool  `json:"email"`
			Weekly      *bool  `json:"weekly"`
		}
		if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 2048, "2 KiB"); !ok {
			return
		}
		if input.Version == nil || input.Reply == nil || input.Mention == nil || input.Publication == nil || input.Moderation == nil || input.Email == nil || input.Weekly == nil || *input.Version < 0 {
			s.notificationError(w, r, notification.ErrInvalid)
			return
		}
		if (*input.Email && !p.Email || *input.Weekly && !p.Weekly) && (!s.accessDeliveryReady() || u.EmailVerifiedAt == nil) {
			s.writeAPIError(w, r, 422, "notifications.email_unavailable", "需要已验证的邮箱，且站点已开启邮件服务", nil)
			return
		}
		p, err = s.notifications.Store.SavePreferences(r.Context(), notification.Preferences{UserID: principal.UserID, Version: *input.Version, Reply: *input.Reply, Mention: *input.Mention, Publication: *input.Publication, Moderation: *input.Moderation, Email: *input.Email, Weekly: *input.Weekly}, time.Now().UTC())
		if err != nil {
			s.notificationError(w, r, err)
			return
		}
	}
	status, err := s.notifications.Store.MailStatus(r.Context(), principal.UserID)
	if err != nil {
		s.notificationError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"preferences": preferenceDTO(p), "email_available": s.accessDeliveryReady() && u.EmailVerifiedAt != nil, "mail_status": status, "timezone": s.siteSettings.Snapshot().Settings.Identity.Timezone})
}
func (s *Server) retryNotificationMail(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct{}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	allowed, _ := s.accessRate.allow(fmt.Sprintf("notice-retry:%d", p.UserID), 3, time.Now())
	if !allowed {
		s.writeAPIError(w, r, 429, "notifications.retry_limited", "请稍后再重试投递", nil)
		return
	}
	if err := s.notifications.Store.RetryFailed(r.Context(), p.UserID, time.Now().UTC()); err != nil {
		s.notificationError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) notificationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, notification.ErrInvalid) {
		s.writeAPIError(w, r, 422, "notifications.invalid", "请检查通知筛选、操作和偏好设置", nil)
	} else if errors.Is(err, notification.ErrConflict) {
		s.writeAPIError(w, r, 409, "notifications.conflict", "设置已在其他页面修改，请重新载入后重试", nil)
	} else {
		s.socialError(w, r, err)
	}
}

type unsubscribeView struct {
	UserID int64
	Token  string
	Done   bool
}

func (s *Server) unsubscribeNotifications(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	var user, token string
	if r.Method == http.MethodGet {
		q, ok := s.strictAPIQuery(w, r, map[string]bool{"u": true, "t": true})
		if !ok {
			return
		}
		user, token = q.Get("u"), q.Get("t")
	} else {
		if _, ok := s.strictAPIQuery(w, r, nil); !ok {
			return
		}
		// The no-referrer confirmation page can produce Origin: null on a
		// native form POST. The unguessable per-account token is the CSRF
		// credential here; no ambient login cookie authorizes this operation.
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" && !s.sameOriginRequest(r) {
			s.renderError(w, 403, "请从退订页面确认操作")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if err := r.ParseForm(); err != nil || len(r.PostForm) != 2 || len(r.PostForm["u"]) != 1 || len(r.PostForm["t"]) != 1 {
			s.renderError(w, 400, "退订请求格式无效")
			return
		}
		user, token = r.PostForm.Get("u"), r.PostForm.Get("t")
	}
	id, err := positivePathID(user)
	if err != nil || len(token) != 64 {
		s.renderError(w, 400, "退订链接无效")
		return
	}
	p, err := s.notifications.Store.Preferences(r.Context(), id)
	if err != nil {
		s.renderError(w, 503, "暂时无法处理退订，请稍后重试")
		return
	}
	if !notification.ValidUnsubscribe(p, token) {
		s.renderError(w, 400, "退订链接已失效，请到阅读室管理订阅")
		return
	}
	done := !p.Email && !p.Weekly
	if r.Method == http.MethodPost {
		if err = s.notifications.Store.Unsubscribe(r.Context(), id, p.UnsubscribeKey, time.Now().UTC()); err != nil {
			s.renderError(w, 409, "订阅已变化，请使用最新邮件中的链接")
			return
		}
		done = true
	}
	data := s.baseViewFor(w, "邮件订阅", "管理你的邮件订阅", "")
	data.BodyClass = "is-account-access"
	data.Site.Robots = "noindex, nofollow, noarchive"
	data.Unsubscribe = unsubscribeView{id, token, done}
	s.render(w, 200, "notification-unsubscribe", data)
}
