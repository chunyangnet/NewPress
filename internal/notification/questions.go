package notification

import (
	"context"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/questions"
	"slices"
)

func (s *Service) resolveQuestion(ctx context.Context, d Delivery) (View, bool, error) {
	if s.Questions == nil || s.QuestionsEnabled != nil && !s.QuestionsEnabled() {
		return View{}, false, nil
	}
	if !slices.Contains(d.Event.Recipients, d.UserID) {
		return View{}, false, nil
	}
	u, e := s.Users.GetUser(ctx, d.UserID)
	if errors.Is(e, auth.ErrUserNotFound) {
		return View{}, false, nil
	}
	if e != nil {
		return View{}, false, e
	}
	if u.Status != auth.UserStatusActive {
		return View{}, false, nil
	}
	state, e := s.Questions.Snapshot(ctx)
	if e != nil {
		return View{}, false, e
	}
	q, ok := questions.Get(state, d.Event.ObjectID)
	if !ok {
		return View{}, false, nil
	}
	if q.Hidden {
		if d.Event.Status == "moderation" {
			return View{Title: "问答内容或举报的处理状态已更新", URL: "/questions/?scope=mine"}, true, nil
		}
		return View{}, false, nil
	}
	target := questions.URL(q.ID)
	title := "你的问题收到新回答"
	if d.Event.ReplyID > 0 {
		visible := 0
		found := false
		for _, a := range q.Answers {
			if a.Hidden {
				continue
			}
			visible++
			if a.ID == d.Event.ReplyID {
				target += fmt.Sprintf("?page=%d#answer-%d", (visible+19)/20, a.ID)
				found = true
				break
			}
		}
		if !found {
			return View{}, false, nil
		}
	}
	switch d.Event.Status {
	case "note":
		title = "参与的问答有新补充"
	case "accept":
		if q.AcceptedID != d.Event.ReplyID {
			return View{}, false, nil
		}
		title = "你的回答被采纳"
	case "moderation":
		title = "问答处理状态已更新"
	}
	return View{Title: title + " · " + q.Title, URL: target}, true, nil
}
