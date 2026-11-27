package community

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestCircleApplicationAndEditingLifecycle(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint("disk=", disk), func(t *testing.T) {
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "circles.json")
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			admin, owner, member, stranger, moderator := Actor{ID: 1, Admin: true}, Actor{ID: 2}, Actor{ID: 3}, Actor{ID: 4}, Actor{ID: 5}
			now := time.Now().UTC()
			sequence := 0
			apply := func(actor Actor, cmd Command, want error) Result {
				t.Helper()
				sequence++
				cmd.Key = fmt.Sprintf("circle-lifecycle-%016d", sequence)
				if c, e := store.Get(t.Context(), "reading"); e == nil {
					cmd.Version = c.Version
				}
				now = now.Add(time.Second)
				result, e := store.Apply(t.Context(), "reading", actor, cmd, now)
				if !errors.Is(e, want) {
					t.Fatalf("%s by %d: %v; want %v", cmd.Action, actor.ID, e, want)
				}
				return result
			}
			apply(admin, Command{Action: "create", Name: "读书会", OwnerID: owner.ID, JoinPolicy: "approval", Visibility: "members", JoinReason: true, PostPolicy: "members"}, nil)
			apply(member, Command{Action: "join"}, ErrInvalid)
			apply(member, Command{Action: "join", Reason: "private join reason"}, nil)
			c, _ := store.Get(t.Context(), "reading")
			if Role(c, member) != "pending" || CanPost(c, member) || CanRead(c, member) || c.Members[0].Reason != "private join reason" || c.Members[0].JoinedAt.IsZero() {
				t.Fatal("application state", c.Members)
			}
			apply(stranger, Command{Action: "remove_member", UserID: member.ID}, ErrForbidden)
			apply(owner, Command{Action: "remove_member", UserID: member.ID}, nil)
			c, _ = store.Get(t.Context(), "reading")
			if Role(c, member) != "" {
				t.Fatal("rejection did not release membership")
			}
			apply(member, Command{Action: "join", Reason: "ready to discuss"}, nil)
			apply(owner, Command{Action: "member", UserID: member.ID, Role: "member"}, nil)
			apply(moderator, Command{Action: "join", Reason: "help moderate"}, nil)
			apply(owner, Command{Action: "member", UserID: moderator.ID, Role: "moderator"}, nil)
			apply(moderator, Command{Action: "remove_member", UserID: owner.ID}, ErrForbidden)
			apply(member, Command{Action: "remove_member", UserID: moderator.ID}, ErrForbidden)
			topic := apply(member, Command{Action: "topic", Kind: "question", Title: "原问题", Body: "原始问题正文"}, nil)
			apply(stranger, Command{Action: "edit_topic", TopicID: topic.TopicID, Title: "篡改", Body: "篡改"}, ErrForbidden)
			apply(member, Command{Action: "edit_topic", TopicID: topic.TopicID, Title: "补充后的问题", Body: "补充上下文"}, nil)
			reply := apply(owner, Command{Action: "reply", TopicID: topic.TopicID, Body: "原始答案"}, nil)
			apply(member, Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID}, nil)
			apply(member, Command{Action: "edit_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Body: "改写别人的答案"}, ErrForbidden)
			edited := apply(owner, Command{Action: "edit_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Body: "补充后的答案"}, nil)
			if edited.ReplyID != reply.ReplyID || edited.ReplyPage != 1 {
				t.Fatal("edit permalink", edited)
			}
			c, _ = store.Get(t.Context(), "reading")
			if c.Topics[0].AcceptedID != 0 || c.Topics[0].Title != "补充后的问题" || c.Topics[0].EditedAt.IsZero() || c.Topics[0].Replies[0].EditedAt.IsZero() {
				t.Fatal("edit facts not persisted", c.Topics[0])
			}
			yes := true
			apply(owner, Command{Action: "lock", TopicID: topic.TopicID, Enabled: &yes}, nil)
			apply(member, Command{Action: "edit_topic", TopicID: topic.TopicID, Title: "已锁话题", Body: "不能修改"}, ErrForbidden)
			apply(owner, Command{Action: "edit_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Body: "不能修改"}, ErrForbidden)
			apply(owner, Command{Action: "settings", Name: "读书会", JoinPolicy: "approval", Visibility: "members", PostPolicy: "moderators", JoinReason: true}, nil)
			apply(member, Command{Action: "topic", Kind: "discussion", Title: "普通成员", Body: "不能发布"}, ErrForbidden)
			apply(moderator, Command{Action: "topic", Kind: "discussion", Title: "版主通知", Body: "可发布"}, nil)
			apply(owner, Command{Action: "member", UserID: member.ID, Role: "banned"}, nil)
			apply(member, Command{Action: "join", Reason: "仍然被限制"}, ErrForbidden)
			apply(owner, Command{Action: "remove_member", UserID: member.ID}, nil)
			apply(member, Command{Action: "join", Reason: "解除限制后重新申请"}, nil)
			if disk {
				store, err = NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			c, err = store.Get(t.Context(), "reading")
			if err != nil || !c.JoinReason || c.PostPolicy != "moderators" || c.Topics[0].Replies[0].Body != "补充后的答案" || Role(c, member) != "pending" {
				t.Fatal("reopened lifecycle", err, c)
			}
		})
	}
}

func TestCircleEditIdempotencyAndValidation(t *testing.T) {
	s, _ := NewFileStore("")
	actor, now := Actor{ID: 1, Admin: true}, time.Now().UTC()
	created, _ := s.Apply(t.Context(), "edit", actor, Command{Action: "create", Key: "edit-circle-create", Name: "编辑", JoinPolicy: "open", Visibility: "public", OwnerID: actor.ID}, now)
	topic, _ := s.Apply(t.Context(), "edit", actor, Command{Action: "topic", Key: "edit-circle-topic", Version: created.Version, Title: "标题", Body: "正文", Kind: "discussion"}, now)
	cmd := Command{Action: "edit_topic", Key: "edit-circle-request", Version: topic.Version, TopicID: topic.TopicID, Title: "新标题", Body: "新正文"}
	first, err := s.Apply(t.Context(), "edit", actor, cmd, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Apply(t.Context(), "edit", actor, cmd, now.Add(2*time.Minute))
	if err != nil || !replay.Replay || replay.Version != first.Version {
		t.Fatal("repeated edit", replay, err)
	}
	cmd.Body = "再次修改"
	if _, err := s.Apply(t.Context(), "edit", actor, cmd, now); !errors.Is(err, ErrConflict) {
		t.Fatal("different body reused idempotency key", err)
	}
	c, _ := s.Get(t.Context(), "edit")
	c.Topics[0].EditedAt = c.Topics[0].CreatedAt.Add(-time.Second)
	if Validate(c) == nil {
		t.Fatal("invalid edit timestamp accepted")
	}
}
