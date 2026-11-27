package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
)

func TestCirclesApplicationPrivacyEditingAndFilters(t *testing.T) {
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "circle-flow-reader", "circle-flow-reader@example.test", "圈子成员", "subscriber")
	s.config.CommunityEnabled = true
	store, _ := community.NewFileStore("")
	s.SetCommunityStore(store)
	base := "https://example.test/api/v1/community/circles/flow"
	seq := 0
	write := func(cookie *http.Cookie, principal auth.Principal, cmd community.Command, status int) community.Result {
		t.Helper()
		seq++
		cmd.Key = fmt.Sprintf("circle-flow-%016d", seq)
		if c, err := store.Get(t.Context(), "flow"); err == nil {
			cmd.Version = c.Version
		}
		b, _ := json.Marshal(cmd)
		response := serveContentAPI(handler, cookie, "POST", base, string(b), authenticatedAPIHeaders(principal))
		if response.Code != status {
			t.Fatalf("%s: %d %s", cmd.Action, response.Code, response.Body.String())
		}
		var result community.Result
		if status == 200 {
			decodeAPIResponse(t, response, &result)
		}
		return result
	}
	read := func(cookie *http.Cookie, target string) circlesPageView {
		t.Helper()
		response := serveContentAPI(handler, cookie, "GET", target, "", nil)
		if response.Code != 200 {
			t.Fatalf("GET %s: %d %s", target, response.Code, response.Body.String())
		}
		var view circlesPageView
		decodeAPIResponse(t, response, &view)
		return view
	}
	write(ownerCookie, owner, community.Command{Action: "create", Name: "阅读交流", OwnerID: owner.UserID, JoinPolicy: "approval", Visibility: "public", JoinReason: true, PostPolicy: "members"}, 200)
	write(readerCookie, reader, community.Command{Action: "join"}, 422)
	write(readerCookie, reader, community.Command{Action: "join", Reason: "PRIVATE APPLICATION REASON"}, 200)
	for _, cookie := range []*http.Cookie{nil, readerCookie} {
		response := serveContentAPI(handler, cookie, "GET", base, "", nil)
		if strings.Contains(response.Body.String(), "PRIVATE APPLICATION REASON") {
			t.Fatal("application reason leaked")
		}
		view := read(cookie, base)
		if view.Circle.PendingCount != 0 {
			t.Fatal("pending count exposed")
		}
	}
	view := read(ownerCookie, base)
	if view.Circle.PendingCount != 1 || len(view.Members) != 1 || view.Members[0].Reason != "PRIVATE APPLICATION REASON" {
		t.Fatal("moderator application missing", view.Members)
	}
	write(ownerCookie, owner, community.Command{Action: "remove_member", UserID: reader.UserID}, 200)
	view = read(readerCookie, base)
	if view.Circle.Role != "" || !view.Circle.CanJoin {
		t.Fatal("rejection blocks reapplication")
	}
	write(readerCookie, reader, community.Command{Action: "join", Reason: "重新申请"}, 200)
	write(ownerCookie, owner, community.Command{Action: "member", UserID: reader.UserID, Role: "member"}, 200)
	question := write(readerCookie, reader, community.Command{Action: "topic", Kind: "question", Title: "如何阅读", Body: "寻找阅读方法"}, 200)
	discussion := write(ownerCookie, owner, community.Command{Action: "topic", Kind: "discussion", Title: "阅读心得", Body: "本周的阅读"}, 200)
	yes := true
	write(ownerCookie, owner, community.Command{Action: "feature", TopicID: discussion.TopicID, Enabled: &yes}, 200)
	hidden := write(ownerCookie, owner, community.Command{Action: "topic", Kind: "question", Title: "PRIVATE HIDDEN QUESTION", Body: "PRIVATE BODY"}, 200)
	write(ownerCookie, owner, community.Command{Action: "hide_topic", TopicID: hidden.TopicID, Enabled: &yes}, 200)
	view = read(readerCookie, base+"?filter=unanswered&q="+"阅读")
	if view.Total != 1 || view.Topics[0].ID != question.TopicID || view.Circle.TopicCount != 2 {
		t.Fatal("question filter/privacy", view)
	}
	view = read(nil, base+"?filter=featured")
	if view.Total != 1 || view.Topics[0].ID != discussion.TopicID {
		t.Fatal("featured filter", view)
	}
	view = read(nil, base+"?q=PRIVATE")
	if view.Total != 0 || len(view.Topics) != 0 {
		t.Fatal("hidden body searchable")
	}
	directory := "https://example.test/api/v1/community/circles?scope=mine"
	if len(read(readerCookie, directory).Items) != 1 || len(read(nil, directory).Items) != 0 {
		t.Fatal("my circles filter")
	}
	view = read(readerCookie, base+fmt.Sprintf("/topics/%d", question.TopicID))
	if !view.Topic.CanEdit {
		t.Fatal("author edit missing")
	}
	write(readerCookie, reader, community.Command{Action: "edit_topic", TopicID: question.TopicID, Title: "如何高效阅读", Body: "补充上下文"}, 200)
	view = read(readerCookie, base+fmt.Sprintf("/topics/%d", question.TopicID))
	if view.Topic.Title != "如何高效阅读" || view.Topic.Edited == "" {
		t.Fatal("edited projection", view.Topic)
	}
	write(readerCookie, reader, community.Command{Action: "edit_topic", TopicID: discussion.TopicID, Title: "他人话题", Body: "不允许"}, 403)
	reply := write(ownerCookie, owner, community.Command{Action: "reply", TopicID: question.TopicID, Body: "回答"}, 200)
	view = read(readerCookie, base+"?filter=mine")
	if view.Total != 1 || view.Topics[0].ID != question.TopicID {
		t.Fatal("personal topics must belong to current account", view)
	}
	view = read(ownerCookie, base+"?filter=participated")
	if view.Total != 1 || view.Topics[0].ID != question.TopicID {
		t.Fatal("participated topics must contain current account replies", view)
	}
	for _, filter := range []string{"mine", "participated"} {
		if guest := read(nil, base+"?filter="+filter); guest.Total != 0 {
			t.Fatal("guest personal filter must be empty", guest)
		}
	}
	write(readerCookie, reader, community.Command{Action: "accept", TopicID: question.TopicID, ReplyID: reply.ReplyID}, 200)
	view = read(readerCookie, base+"?filter=unanswered")
	if view.Total != 0 {
		t.Fatal("accepted question still unresolved")
	}
	write(ownerCookie, owner, community.Command{Action: "edit_reply", TopicID: question.TopicID, ReplyID: reply.ReplyID, Body: "补充后的回答"}, 200)
	view = read(readerCookie, base+"?filter=unanswered")
	if view.Total != 1 {
		t.Fatal("changed answer must be accepted again")
	}
	write(ownerCookie, owner, community.Command{Action: "settings", Name: "阅读交流", JoinPolicy: "approval", Visibility: "public", PostPolicy: "moderators", JoinReason: true}, 200)
	view = read(readerCookie, base)
	if view.Circle.CanPost || view.Circle.PostLabel != "仅圈主与版主发布" {
		t.Fatal("post policy missing")
	}
	write(readerCookie, reader, community.Command{Action: "reply", TopicID: question.TopicID, Body: "不能发布"}, 403)
	for _, suffix := range []string{"?kind=unknown", "?scope=other", "?sort=foreign", "?filter=private", "?q=a&q=b", "?q=%00", "?q=" + strings.Repeat("a", 81)} {
		r := serveContentAPI(handler, ownerCookie, "GET", base+suffix, "", nil)
		if r.Code != 422 {
			t.Fatalf("invalid filter %s: %d", suffix, r.Code)
		}
	}
	for _, target := range []string{"https://example.test/community/?scope=mine", "https://example.test/community/flow/?kind=question", fmt.Sprintf("https://example.test/community/flow/topics/%d", question.TopicID)} {
		r := serveContentAPI(handler, ownerCookie, "GET", target, "", nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "data-circle-page") {
			t.Fatalf("render %s: %d", target, r.Code)
		}
	}
}

func TestCirclesFilteredPaginationPreservesSelection(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	s.config.CommunityEnabled = true
	store, _ := community.NewFileStore("")
	s.SetCommunityStore(store)
	actor := community.Actor{ID: principal.UserID, Admin: true}
	now := time.Now().UTC()
	last, err := store.Apply(t.Context(), "filters", actor, community.Command{Action: "create", Key: "filter-create-circle", Name: "筛选", JoinPolicy: "open", Visibility: "public", OwnerID: principal.UserID}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 22 {
		last, err = store.Apply(t.Context(), "filters", actor, community.Command{Action: "topic", Key: fmt.Sprintf("filter-topic-%016d", i), Version: last.Version, Title: fmt.Sprintf("匹配 %d", i), Body: "正文", Kind: "question"}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	r := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/community/circles/filters?q=匹配&kind=question&filter=unanswered&sort=active&page=2", "", nil)
	var view circlesPageView
	decodeAPIResponse(t, r, &view)
	if view.Total != 22 || len(view.Topics) != 2 || !strings.Contains(view.Previous, "kind=question") || !strings.Contains(view.Previous, "filter=unanswered") || !strings.Contains(view.Previous, "sort=active") || !strings.Contains(view.Previous, "page=1") {
		t.Fatal(view)
	}
}
