package notification

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
)

func (s *Service) resolveCommunity(ctx context.Context, d Delivery) (View, bool, error) {
	if s.Community == nil || s.CommunityEnabled != nil && !s.CommunityEnabled() {
		return View{}, false, nil
	}
	e := d.Event
	u, err := s.Users.GetUser(ctx, d.UserID)
	if errors.Is(err, auth.ErrUserNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, err
	}
	if u.Status != auth.UserStatusActive {
		return View{}, false, nil
	}
	c, err := s.Community.Get(ctx, e.Circle)
	if errors.Is(err, community.ErrNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, err
	}
	a := community.Actor{ID: u.ID, Admin: u.Can(auth.CapabilitySettingsManage)}
	base := "/community/" + c.Slug + "/"
	if d.Kind == Moderation {
		if !slices.Contains(e.Recipients, d.UserID) {
			return View{}, false, nil
		}
		switch e.Status {
		case "join":
			if !community.Manages(c, a) {
				return View{}, false, nil
			}
			return View{"圈子有新的加入申请 · " + c.Name, base + "#circle-members"}, true, nil
		case "report":
			if !community.Manages(c, a) {
				return View{}, false, nil
			}
			return View{"圈子有新的举报待处理 · " + c.Name, base + "#circle-reports"}, true, nil
		case "review_report":
			return View{"你的圈子举报已有处理结果 · " + c.Name, base + "#circle-reports"}, true, nil
		case "member", "remove_member", "transfer":
			return View{"你的圈子成员身份已更新 · " + c.Name, base}, true, nil
		case "hide_topic", "hide_reply":
			return View{"你的圈子内容展示状态已更新 · " + c.Name, base}, true, nil
		default:
			return View{}, false, nil
		}
	}
	if !community.CanRead(c, a) || community.Role(c, a) == "banned" {
		return View{}, false, nil
	}
	// Participation notifications stop after leaving a members-only circle;
	// publication also requires current membership in a public circle.
	if d.Kind == Publication && community.Role(c, a) != "member" && !community.Manages(c, a) {
		return View{}, false, nil
	}
	t, ok := community.TopicByID(c, e.TopicID)
	if !ok || t.Hidden {
		return View{}, false, nil
	}
	url := base + fmt.Sprintf("topics/%d", t.ID)
	body := t.Body
	if e.ReplyID > 0 {
		r, found := community.ReplyByID(t, e.ReplyID)
		if !found || r.Hidden {
			return View{}, false, nil
		}
		body = r.Body
		visible := 0
		for _, item := range t.Replies {
			if !item.Hidden {
				visible++
			}
			if item.ID == r.ID {
				break
			}
		}
		url += fmt.Sprintf("?page=%d#reply-%d", max(1, (visible+24)/25), r.ID)
		if e.Status == "accept" && t.AcceptedID != r.ID {
			return View{}, false, nil
		}
	}
	if d.Kind == Mention {
		matched := false
		for _, name := range activity.Mentions(body) {
			if strings.EqualFold(name, u.Username) {
				matched = true
			}
		}
		if !matched {
			return View{}, false, nil
		}
		return View{"有人在圈子中提到了你 · " + t.Title, url}, true, nil
	}
	if !slices.Contains(e.Recipients, d.UserID) {
		return View{}, false, nil
	}
	title := "圈子发布了新话题 · " + t.Title
	if d.Kind == Reply {
		title = "你参与的话题有新回应 · " + t.Title
		if e.Status == "accept" {
			title = "你的回答已被采纳 · " + t.Title
		}
	}
	return View{title, url}, true, nil
}
