package httpserver

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/questions"
)

func TestAnnouncementDirectoryFiltersPaginationAndPublicNeighbors(t *testing.T) {
	s, handler, _, _, _ := newAuthenticatedContentServer(t)
	state, err := s.announcements.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-24 * time.Hour)
	for i := 1; i <= 14; i++ {
		item := announcement.Item{Title: fmt.Sprintf("公告检索 %02d", i), Body: "正文 & <script>noticePayload()</script>", Published: i != 13, Pinned: i <= 11, Date: now.Add(-time.Duration(i) * time.Hour)}
		if i == 12 {
			item.Body += "\n一、公告事项\n补充说明"
		}
		if i == 14 {
			item.Date = time.Now().Add(24 * time.Hour)
		}
		state, err = s.announcements.Save(t.Context(), state.Revision, "save", item)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path, want, absent string
	}{
		{"/announcements/", "公告检索 01", "公告检索 11"},
		{"/announcements/?page=2", "公告检索 11", "公告检索 01"},
		{"/announcements/?scope=pinned&page=2&q=" + url.QueryEscape("公告检索"), "公告检索 11", "公告检索 12"},
		{"/announcements/?q=" + url.QueryEscape("公告检索 12"), "公告检索 12", "公告检索 01"},
		{"/announcements/?q=unmatched", "没有找到匹配的公告", "公告检索 01"},
		{"/announcements/12/", "已经是最后一篇公告", "公告检索 13"},
		{"/announcements/12/", `class="announcement-section-heading">一、公告事项</h2>`, "公告检索 13"},
	} {
		res := serveContentAPI(handler, nil, "GET", "https://example.test"+tc.path, "", nil)
		body := res.Body.String()
		if res.Code != 200 || !strings.Contains(body, tc.want) || strings.Contains(body, tc.absent) {
			t.Fatalf("%s: status=%d missing=%q or unexpected=%q", tc.path, res.Code, tc.want, tc.absent)
		}
		if strings.Contains(body, "公告检索 13") || strings.Contains(body, "公告检索 14") || strings.Contains(body, "<script>noticePayload()") {
			t.Fatalf("%s leaked hidden/future content or unescaped body", tc.path)
		}
		if strings.Contains(tc.path, "scope=pinned&page=2") && !strings.Contains(body, "scope=pinned") {
			t.Fatal("pagination lost pinned filter")
		}
	}
	for _, query := range []string{"page=-1", "page=abc", "scope=hidden", "q=a&q=b", "q=%FF", "q=%00", "unknown=true"} {
		res := serveContentAPI(handler, nil, "GET", "https://example.test/announcements/?"+query, "", nil)
		if res.Code != 400 {
			t.Errorf("%s: status=%d", query, res.Code)
		}
	}
}

func TestQuestionLandingPreviewPrivacyAndCombinedFilters(t *testing.T) {
	s, handler, _, _, _, owner := newPersistentAdminTestServer(t)
	store, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(store)
	actor := questions.Actor{ID: owner.UserID, Admin: true}
	apply := func(id int64, cmd questions.Command) questions.Result {
		t.Helper()
		result, err := store.Apply(t.Context(), id, actor, cmd, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	q := apply(0, questions.Command{Action: "create", Key: "channel-create-public", Title: "公开问题示例", Body: "问题背景", Tags: []string{"内容发布"}})
	a := apply(q.ID, questions.Command{Action: "answer", Key: "channel-answer-public", Version: q.Version, Body: "采纳摘要 <script>questionPayload()</script>"})
	accepted := apply(q.ID, questions.Command{Action: "accept", Key: "channel-accept-public", Version: a.Version, AnswerID: a.AnswerID})
	res := serveContentAPI(handler, nil, "GET", "https://example.test/questions/?q="+url.QueryEscape("公开")+"&tag="+url.QueryEscape("内容发布")+"&sort=active", "", nil)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, "采纳摘要") || strings.Contains(body, "<script>questionPayload()") {
		t.Fatalf("unsafe or missing accepted preview, status=%d", res.Code)
	}
	if !strings.Contains(body, "sort=active&amp;status=open&amp;tag=") || !strings.Contains(body, "q=%E5%85%AC%E5%BC%80") {
		t.Fatal("status tabs lost search/tag/sort")
	}
	apply(q.ID, questions.Command{Action: "hide", Key: "channel-hide-public", Version: accepted.Version, Enabled: true})
	res = serveContentAPI(handler, nil, "GET", "https://example.test/questions/", "", nil)
	for _, private := range []string{"公开问题示例", "采纳摘要", "内容发布"} {
		if strings.Contains(res.Body.String(), private) {
			t.Errorf("hidden question leaked through overview: %s", private)
		}
	}
}
