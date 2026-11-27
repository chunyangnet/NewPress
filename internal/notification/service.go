package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/maildelivery"
	"newpress.local/newpress/internal/questions"
)

type Users interface {
	GetUser(context.Context, int64) (auth.User, error)
	FindUserByUsername(context.Context, string) (auth.User, error)
}
type Settings struct{ Title, PublicURL, Timezone string }
type Service struct {
	Questions        questions.Store
	QuestionsEnabled func() bool
	FlashEnabled     func() bool
	Store            Store
	Users            Users
	Content          content.Repository
	Comments         comment.Store
	Engagement       engagement.Store
	Sources          []activity.Source
	Community        community.Store
	CommunityEnabled func() bool
	Sender           maildelivery.MessageSender
	Settings         func() Settings
	BeforeAck        func(context.Context, activity.Event) error
	lastDigestCheck  time.Time
	// Scheduling hints only; events remain durable until successful ACK.
	activityCursors map[int]string
	activityPages   map[int]int
}

type View struct {
	Title string
	URL   string
}

// Run processes durable source events before mail. Cancellation is joined by
// the application before stores or their database pool can be closed.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Error("notification worker failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	started := time.Now()
	var pendingErr error
	if len(s.activityCursors) > len(s.Sources) {
		s.activityCursors = nil
		s.activityPages = nil
	}
	for index, source := range s.Sources {
		if err := ctx.Err(); err != nil {
			return errors.Join(pendingErr, err)
		}
		var events []activity.Event
		var err error
		after := s.activityCursors[index]
		// Probe old failures periodically without discarding scan progress.
		// Resetting the cursor here would starve pages beyond the reset budget.
		probe := s.activityPages[index] >= 10 && after != ""
		if probe {
			after = ""
		}
		events, err = source.PendingActivityAfter(ctx, now, 50, after)
		if err == nil && len(events) == 0 && after != "" {
			events, err = source.PendingActivityAfter(ctx, now, 50, "")
		}
		if err == nil {
			if s.activityCursors == nil {
				s.activityCursors = make(map[int]string)
			}
			if s.activityPages == nil {
				s.activityPages = make(map[int]int)
			}
			if probe {
				s.activityPages[index] = 0
			} else {
				s.activityPages[index]++
				s.activityCursors[index] = ""
				if len(events) == 50 {
					s.activityCursors[index] = events[len(events)-1].Key
				} else {
					s.activityPages[index] = 0
				}
			}
		}
		if err != nil {
			pendingErr = errors.Join(pendingErr, err)
			continue
		}
		for _, e := range events {
			if err := ctx.Err(); err != nil {
				return errors.Join(pendingErr, err)
			}
			if err = s.project(ctx, e, now); err != nil {
				pendingErr = errors.Join(pendingErr, err)
				continue
			}
			if s.BeforeAck != nil {
				if err = s.BeforeAck(ctx, e); err != nil {
					pendingErr = errors.Join(pendingErr, err)
					continue
				}
			}
			if err = source.AckActivity(ctx, e.Key); err != nil {
				pendingErr = errors.Join(pendingErr, err)
			}
		}
	}
	if s.Sender == nil {
		return pendingErr
	}
	if s.lastDigestCheck.IsZero() || now.Sub(s.lastDigestCheck) >= time.Hour {
		if err := s.queueWeekly(ctx, now); err != nil {
			return errors.Join(pendingErr, err)
		}
		s.lastDigestCheck = now
	}
	for i := 0; i < 10; i++ {
		claimTime := now.Add(time.Since(started))
		j, ok, err := s.Store.ClaimMail(ctx, claimTime)
		if err != nil {
			return errors.Join(pendingErr, err)
		}
		if !ok {
			break
		}
		// ClaimMail can wait for persistence or a database lock. That time has
		// already consumed the lease and must not be granted again to delivery.
		if err = s.deliver(ctx, j, now.Add(time.Since(started))); err != nil {
			return errors.Join(pendingErr, err)
		}
	}
	return pendingErr
}
func (s *Service) project(ctx context.Context, e activity.Event, now time.Time) error {
	if err := e.Validate(); err != nil {
		return err
	}
	// Content edits feed extension observers, not notification receipts/mail.
	if e.Kind == activity.ContentUpdated {
		return nil
	}
	receipt := e.Key
	deliveries := []Delivery{}
	add := func(user int64, kind string) error {
		if user <= 0 {
			return nil
		}
		if kind != Moderation && user == e.UserID {
			return nil
		}
		for _, d := range deliveries {
			if d.UserID == user {
				return nil
			}
		}
		u, err := s.Users.GetUser(ctx, user)
		if errors.Is(err, auth.ErrUserNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Status != auth.UserStatusActive {
			return nil
		}
		d := Delivery{UserID: user, Kind: kind, Event: e}
		if e.PostID > 0 {
			post, err := s.Content.GetByID(ctx, e.PostID)
			if err != nil && !errors.Is(err, content.ErrNotFound) {
				return err
			}
			d.InboxOnly = err == nil && post.Type == content.TypeFlash
		}
		_, visible, err := s.Resolve(ctx, d, now)
		if err != nil {
			return err
		}
		if visible {
			deliveries = append(deliveries, d)
		}
		return nil
	}
	switch e.Kind {
	case "questions":
		for _, user := range e.Recipients {
			kind := Reply
			if e.Status == "moderation" {
				kind = Moderation
			}
			if err := add(user, kind); err != nil {
				return err
			}
		}
	case activity.Community:
		// An explicit mention takes precedence over a general participation
		// update, so the user's mention preference governs that notification.
		for _, name := range e.Mentions {
			u, err := s.Users.FindUserByUsername(ctx, name)
			if errors.Is(err, auth.ErrUserNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err = add(u.ID, Mention); err != nil {
				return err
			}
		}
		for _, user := range e.Recipients {
			kind := Moderation
			if e.Status == "topic" {
				kind = Publication
			} else if e.Status == "reply" || e.Status == "accept" {
				kind = Reply
			}
			if err := add(user, kind); err != nil {
				return err
			}
		}
	case activity.Publication:
		post, err := s.Content.GetByID(ctx, e.PostID)
		if errors.Is(err, content.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !content.PublicAt(post, now) || post.Type != content.TypePost || post.AuthorID != e.UserID || post.PublishedAt.After(e.Due) {
			return nil
		}
		receipt = fmt.Sprintf("publication:%d", e.ObjectID)
		followers, ok := s.Engagement.(engagement.FollowerSource)
		if !ok {
			return errors.New("follower source unavailable")
		}
		var after int64
		for {
			page, err := followers.Followers(ctx, e.UserID, after, 100)
			if err != nil {
				return err
			}
			for _, entry := range page {
				after = entry.UserID
				if !entry.CreatedAt.After(e.Due) {
					if err = add(entry.UserID, Publication); err != nil {
						return err
					}
				}
			}
			if len(page) < 100 {
				break
			}
		}
	case activity.CommentPublic:
		receipt = fmt.Sprintf("comment_public:%d", e.ObjectID)
		if e.ParentID > 0 {
			parent, err := s.Comments.Get(ctx, e.ParentID)
			if err != nil && !errors.Is(err, comment.ErrNotFound) {
				return err
			}
			if err == nil {
				if err = add(parent.UserID, Reply); err != nil {
					return err
				}
			}
		}
		for _, name := range e.Mentions {
			u, err := s.Users.FindUserByUsername(ctx, name)
			if errors.Is(err, auth.ErrUserNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err = add(u.ID, Mention); err != nil {
				return err
			}
		}
	case activity.CommentModeration, activity.PostModeration:
		if err := add(e.UserID, Moderation); err != nil {
			return err
		}
	}
	return s.Store.Project(ctx, receipt, deliveries, now)
}

// Resolve is used independently for inbox rendering and immediately before
// delivery. A queued identifier never grants access to a private article.
func (s *Service) Resolve(ctx context.Context, d Delivery, now time.Time) (View, bool, error) {
	if d.Event.Kind == "questions" {
		return s.resolveQuestion(ctx, d)
	}
	if d.Event.Kind == activity.Community {
		return s.resolveCommunity(ctx, d)
	}
	if d.Kind == Moderation {
		if d.Event.Kind == activity.PostModeration {
			post, err := s.Content.GetByID(ctx, d.Event.ObjectID)
			if errors.Is(err, content.ErrNotFound) {
				return View{}, false, nil
			}
			if err != nil {
				return View{}, false, err
			}
			if post.AuthorID != d.UserID {
				return View{}, false, nil
			}
			if post.Type == content.TypeFlash {
				if s.FlashEnabled != nil && !s.FlashEnabled() {
					return View{}, false, nil
				}
				return View{"你的快报审核结果已更新：" + statusLabel(d.Event.Status), fmt.Sprintf("/flash/submit/?edit=%d", post.ID)}, true, nil
			}
			return View{"你的文章审核结果已更新：" + statusLabel(d.Event.Status), "/account/?view=posts"}, true, nil
		}
		item, err := s.Comments.Get(ctx, d.Event.ObjectID)
		if errors.Is(err, comment.ErrNotFound) {
			return View{}, false, nil
		}
		if err != nil {
			return View{}, false, err
		}
		if item.UserID != d.UserID {
			return View{}, false, nil
		}
		return View{"你的评论审核结果已更新：" + statusLabel(d.Event.Status), "/account/?view=comments"}, true, nil
	}
	post, err := s.Content.GetByID(ctx, d.Event.PostID)
	if errors.Is(err, content.ErrNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, err
	}
	if (post.Type != content.TypePost && post.Type != content.TypeFlash) || post.Type == content.TypeFlash && s.FlashEnabled != nil && !s.FlashEnabled() || !content.PublicAt(post, now) {
		return View{}, false, nil
	}
	url := content.PublicPath(post)
	if d.Kind == Publication {
		if post.AuthorID != d.Event.UserID {
			return View{}, false, nil
		}
		states, err := s.Engagement.States(ctx, d.UserID, engagement.Follow, []int64{post.AuthorID})
		if err != nil {
			return View{}, false, err
		}
		if len(states) != 1 || !states[0].Active {
			return View{}, false, nil
		}
		return View{"关注的作者发布了《" + post.Title + "》", url}, true, nil
	}
	item, err := s.Comments.Get(ctx, d.Event.ObjectID)
	if errors.Is(err, comment.ErrNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, err
	}
	if item.PostID != post.ID || item.Status != comment.StatusApproved {
		return View{}, false, nil
	}
	ancestor := item
	for depth := 0; ancestor.ParentID > 0; depth++ {
		if depth >= comment.MaxThreadDepth {
			return View{}, false, nil
		}
		ancestor, err = s.Comments.Get(ctx, ancestor.ParentID)
		if errors.Is(err, comment.ErrNotFound) {
			return View{}, false, nil
		}
		if err != nil {
			return View{}, false, err
		}
		if ancestor.PostID != post.ID || ancestor.Status != comment.StatusApproved {
			return View{}, false, nil
		}
	}
	if d.Kind == Reply {
		parent, err := s.Comments.Get(ctx, item.ParentID)
		if errors.Is(err, comment.ErrNotFound) {
			return View{}, false, nil
		}
		if err != nil {
			return View{}, false, err
		}
		if parent.UserID != d.UserID {
			return View{}, false, nil
		}
		return View{item.AuthorName + " 回复了你的评论 ·《" + post.Title + "》", url + fmt.Sprintf("#comment-%d", item.ID)}, true, nil
	}
	u, err := s.Users.GetUser(ctx, d.UserID)
	if errors.Is(err, auth.ErrUserNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, err
	}
	for _, name := range activity.Mentions(item.ContentSource) {
		if strings.EqualFold(name, u.Username) {
			return View{item.AuthorName + " 在评论中提到了你 ·《" + post.Title + "》", url + fmt.Sprintf("#comment-%d", item.ID)}, true, nil
		}
	}
	return View{}, false, nil
}
func statusLabel(status string) string {
	switch status {
	case "publish", "approved":
		return "已通过"
	case "future":
		return "已安排发布"
	case "pending":
		return "待审核"
	case "draft":
		return "退回草稿"
	case "private":
		return "转为私密"
	default:
		return "未通过"
	}
}

func WeekEnd(now time.Time, zone string) time.Time {
	zone = strings.TrimSpace(zone)
	if zone == "" || zone == "Local" {
		zone = "UTC"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	days := (int(local.Weekday()) + 6) % 7
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -days)
}
func (s *Service) queueWeekly(ctx context.Context, now time.Time) error {
	settings := s.Settings()
	end := WeekEnd(now, settings.Timezone)
	trigger := time.Date(end.Year(), end.Month(), end.Day(), 9, 0, 0, 0, end.Location())
	if now.Before(trigger) {
		return nil
	}
	var after int64
	for {
		page, err := s.Store.Subscribers(ctx, after, 100)
		if err != nil {
			return err
		}
		for _, p := range page {
			after = p.UserID
			if err = s.Store.QueueWeekly(ctx, p.UserID, end, now); err != nil {
				return err
			}
		}
		if len(page) < 100 {
			return nil
		}
	}
}
func UnsubscribeToken(p Preferences) string {
	h := hmac.New(sha256.New, []byte(p.UnsubscribeKey))
	fmt.Fprintf(h, "unsubscribe:%d", p.UserID)
	return hex.EncodeToString(h.Sum(nil))
}
func ValidUnsubscribe(p Preferences, token string) bool {
	return p.UnsubscribeKey != "" && hmac.Equal([]byte(token), []byte(UnsubscribeToken(p)))
}

func (s *Service) weeklyBody(ctx context.Context, j Job, p Preferences, settings Settings) (string, bool, error) {
	start := j.Event.At
	if p.WeeklySince.After(start) {
		start = p.WeeklySince
	}
	lines := []string{}
	base := strings.TrimRight(settings.PublicURL, "/")
	for offset := 0; ; offset += 100 {
		page, err := s.Content.SearchPublished(ctx, content.PublishedQuery{Sort: "published", IgnorePinning: true, Limit: 100, Offset: offset})
		if err != nil {
			return "", false, err
		}
		done := false
		for _, post := range page.Items {
			if post.PublishedAt.Before(start) {
				done = true
				break
			}
			if !post.PublishedAt.Before(j.Event.Due) {
				continue
			}
			lines = append(lines, post.Title+"\n"+base+"/posts/"+post.Slug)
			if len(lines) == 10 {
				done = true
				break
			}
		}
		if done || offset+len(page.Items) >= page.Total || len(page.Items) == 0 {
			break
		}
	}
	if len(lines) == 0 {
		return "", false, nil
	}
	return "本周值得回看的新文章\n\n" + strings.Join(lines, "\n\n"), true, nil
}
func (s *Service) deliver(ctx context.Context, j Job, now time.Time) error {
	started := time.Now()
	if !validMailClaim(j, now) {
		return ErrConflict
	}
	// Keep preparation and SMTP inside the remaining persisted lease, even
	// when the caller supplies a logical clock for this tick.
	ctx, cancel := context.WithDeadline(ctx, started.Add(j.LeaseUntil.Sub(now)))
	defer cancel()
	// Any failure before the SMTP call is safe to retry. A stopped process after
	// ClaimMail is recovered as uncertain, never blindly resent.
	finish := func(state string) error {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.Store.FinishMail(finishCtx, j, state, now.Add(time.Since(started)))
	}
	p, err := s.Store.Preferences(ctx, j.UserID)
	if err != nil {
		return errors.Join(err, finish("retry"))
	}
	if j.Kind == Weekly {
		if !p.Weekly || !p.WeeklySince.Before(j.Event.Due) {
			return finish("cancelled")
		}
	} else if !WantsEmail(p, j.Delivery) {
		return finish("cancelled")
	}
	u, err := s.Users.GetUser(ctx, j.UserID)
	if errors.Is(err, auth.ErrUserNotFound) {
		return finish("cancelled")
	}
	if err != nil {
		return errors.Join(err, finish("retry"))
	}
	if u.Status != auth.UserStatusActive || u.EmailVerifiedAt == nil {
		return finish("cancelled")
	}
	settings := s.Settings()
	body := ""
	subject := settings.Title + " · 通知"
	if j.Kind == Weekly {
		var ok bool
		body, ok, err = s.weeklyBody(ctx, j, p, settings)
		if err != nil {
			return errors.Join(err, finish("retry"))
		}
		if !ok {
			return finish("cancelled")
		}
		subject = settings.Title + " · 每周精选"
	} else {
		view, ok, resolveErr := s.Resolve(ctx, j.Delivery, now)
		if resolveErr != nil {
			return errors.Join(resolveErr, finish("retry"))
		}
		if !ok {
			return finish("cancelled")
		}
		body = view.Title + "\n\n" + strings.TrimRight(settings.PublicURL, "/") + view.URL
	}
	body += "\n\n你已在站点订阅此类邮件。通知设置：\n" + strings.TrimRight(settings.PublicURL, "/") + "/account/?view=notifications\n停止邮件订阅：\n" + strings.TrimRight(settings.PublicURL, "/") + fmt.Sprintf("/account/notifications/unsubscribe/?u=%d&t=%s", u.ID, UnsubscribeToken(p))
	id := sha256.Sum256([]byte(p.UnsubscribeKey + "\n" + j.Key))
	if err := ctx.Err(); err != nil {
		return errors.Join(err, finish("retry"))
	}
	err = s.Sender.SendMessage(ctx, maildelivery.Message{To: u.Email, Subject: subject, Body: body, ID: hex.EncodeToString(id[:])})
	if errors.Is(err, maildelivery.ErrUncertain) {
		return finish("uncertain")
	}
	if err != nil {
		return finish("retry")
	}
	return finish("sent")
}
