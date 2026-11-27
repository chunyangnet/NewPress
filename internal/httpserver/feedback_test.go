package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/feedback"
)

func TestFeedbackHTTPWorkflowAndPrivacy(t *testing.T) {
	s, handler, users, _, adminCookie, admin := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, admin, "feedback-reader", "reader@example.test", "反馈读者", "subscriber")
	_, otherCookie, other := createAndLoginTestUser(t, users, admin, "feedback-other", "other@example.test", "其他读者", "subscriber")
	_, editorCookie, editor := createAndLoginTestUser(t, users, admin, "feedback-editor", "editor@example.test", "板块编辑", "editor")
	base := "https://example.test/api/v1/feedback/ideas"
	seq := 0
	write := func(cookie *http.Cookie, p auth.Principal, cmd feedback.Command, want int) feedback.Result {
		t.Helper()
		seq++
		cmd.Key = fmt.Sprintf("http-feedback-%016d", seq)
		if b, e := s.feedback.Get(t.Context(), "ideas"); e == nil {
			cmd.Version = b.Version
		}
		body, _ := json.Marshal(cmd)
		r := serveContentAPI(handler, cookie, "POST", base, string(body), authenticatedAPIHeaders(p))
		if r.Code != want || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", cmd.Action, r.Code, r.Body.String())
		}
		var result feedback.Result
		_ = json.Unmarshal(r.Body.Bytes(), &result)
		return result
	}
	settings := feedback.Defaults()
	settings.Moderation = true
	write(readerCookie, reader, feedback.Command{Action: "create", Settings: &settings}, 403)
	write(adminCookie, admin, feedback.Command{Action: "create", Settings: &settings}, 200)
	private := write(readerCookie, reader, feedback.Command{Action: "submit", Title: "PRIVATE REPORT TITLE", Body: "SECRET BODY <script>alert(1)</script>", Kind: "issue", Private: true}, 200)
	pending := write(readerCookie, reader, feedback.Command{Action: "submit", Title: "PENDING REPORT TITLE", Body: "待审核内容，其他读者不应该看到。", Kind: "suggestion"}, 200)
	public := write(adminCookie, admin, feedback.Command{Action: "submit", Title: "公开的搜索建议", Body: "按分类搜索的功能很有帮助。", Kind: "suggestion", Category: "功能需求"}, 200)
	for _, cookie := range []*http.Cookie{nil, otherCookie} {
		for _, path := range []string{base, base + "?q=REPORT", base + "?view=roadmap", "https://example.test/feedback/ideas/"} {
			r := serveContentAPI(handler, cookie, "GET", path, "", nil)
			if r.Code != 200 || strings.Contains(r.Body.String(), "REPORT TITLE") || strings.Contains(r.Body.String(), "SECRET BODY") || strings.Contains(r.Body.String(), "reader@example.test") {
				t.Fatalf("privacy %d: %s", r.Code, r.Body.String())
			}
		}
		for _, id := range []int64{private.ItemID, pending.ItemID} {
			r := serveContentAPI(handler, cookie, "GET", base+"/items/"+fmt.Sprint(id), "", nil)
			if r.Code != 404 {
				t.Fatal("private detail accessible", r.Code)
			}
		}
	}
	write(editorCookie, editor, feedback.Command{Action: "visibility", ItemID: private.ItemID, Visibility: "public"}, 200)
	r := serveContentAPI(handler, otherCookie, "GET", base+"/items/"+fmt.Sprint(private.ItemID), "", nil)
	if r.Code != 404 {
		t.Fatal("approval leaked private report")
	}
	r = serveContentAPI(handler, readerCookie, "GET", "https://example.test/feedback/ideas/items/"+fmt.Sprint(private.ItemID), "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "<script>alert(1)</script>") || !strings.Contains(r.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(r.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("escaped private HTML", r.Code, r.Body.String())
	}
	yes := true
	write(otherCookie, other, feedback.Command{Action: "vote", ItemID: public.ItemID, Enabled: &yes}, 200)
	write(readerCookie, reader, feedback.Command{Action: "reply", ItemID: public.ItemID, Body: "读者补充意见"}, 200)
	official := write(editorCookie, editor, feedback.Command{Action: "reply", ItemID: public.ItemID, Body: "OFFICIAL HIDDEN REPLY", Official: true}, 200)
	write(editorCookie, editor, feedback.Command{Action: "hide_reply", ItemID: public.ItemID, ReplyID: official.ReplyID, Enabled: &yes}, 200)
	r = serveContentAPI(handler, readerCookie, "GET", base+"/items/"+fmt.Sprint(public.ItemID), "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "OFFICIAL HIDDEN REPLY") || strings.Contains(r.Body.String(), "author_id") || strings.Contains(r.Body.String(), "receipts") {
		t.Fatal("reply projection", r.Body.String())
	}
	write(readerCookie, reader, feedback.Command{Action: "status", ItemID: public.ItemID, Status: "done"}, 403)
	write(editorCookie, editor, feedback.Command{Action: "status", ItemID: public.ItemID, Status: "progress", Body: "已经开始实现"}, 200)
	for _, path := range []string{"https://example.test/admin/features/", "https://example.test/admin/features/?edit=new", "https://example.test/admin/features/?edit=ideas", "https://example.test/feedback/", "https://example.test/feedback/ideas/?view=roadmap", "https://example.test/feedback/ideas/items/" + fmt.Sprint(public.ItemID)} {
		r = serveContentAPI(handler, adminCookie, "GET", path, "", nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "</html>") || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("template render", path, r.Code, r.Body.String())
		}
	}
	for _, cookie := range []*http.Cookie{readerCookie, editorCookie} {
		r = serveContentAPI(handler, cookie, "GET", "https://example.test/admin/features/", "", nil)
		if r.Code != 403 {
			t.Fatal("configuration permission", r.Code)
		}
	}
	for _, query := range []string{"?page=0", "?q=a&q=b", "?foreign=1", "?status=unknown", "?kind=unknown", "?view=unknown", "?scope=pending", "?scope=hidden"} {
		r = serveContentAPI(handler, readerCookie, "GET", base+query, "", nil)
		if r.Code != 422 && r.Code != 403 {
			t.Fatal("query shape", query, r.Code)
		}
	}
	r = serveContentAPI(handler, readerCookie, "GET", base+"/export", "", nil)
	if r.Code != 403 {
		t.Fatal("reader export")
	}
	r = serveContentAPI(handler, editorCookie, "GET", base+"/export", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "PRIVATE REPORT TITLE") || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") {
		t.Fatal("manager export", r.Code, r.Body.String())
	}
	groups, e := s.navigationSources(newFeedbackRequest(t))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, group := range groups {
		if group.ID == "features" {
			found = len(group.Sources) == 1 && group.Sources[0].URL == "/feedback/ideas/"
		}
	}
	if !found {
		t.Fatal("missing navigation source")
	}
	settings.Voting = false
	settings.Comments = false
	settings.OfficialReplies = false
	settings.Submissions = false
	write(adminCookie, admin, feedback.Command{Action: "configure", Settings: &settings}, 200)
	write(readerCookie, reader, feedback.Command{Action: "submit", Title: "现在无法提交的反馈", Body: "这项功能已经关闭。", Kind: "issue"}, 403)
	write(otherCookie, other, feedback.Command{Action: "vote", ItemID: public.ItemID, Enabled: &yes}, 403)
	write(editorCookie, editor, feedback.Command{Action: "reply", ItemID: public.ItemID, Body: "官方回复暂停", Official: true}, 403)
	settings.Enabled = false
	write(adminCookie, admin, feedback.Command{Action: "configure", Settings: &settings}, 200)
	r = serveContentAPI(handler, readerCookie, "GET", base, "", nil)
	if r.Code != 404 {
		t.Fatal("disabled board visible")
	}
}
func newFeedbackRequest(t *testing.T) *http.Request {
	t.Helper()
	r, e := http.NewRequestWithContext(t.Context(), "GET", "https://example.test/admin/navigation/", nil)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestFeedbackHTTPStrictSecurityAndConflict(t *testing.T) {
	s, handler, _, _, cookie, p := newPersistentAdminTestServer(t)
	settings := feedback.Defaults()
	base := "https://example.test/api/v1/feedback/security"
	c := feedback.Command{Action: "create", Key: "security-create-key", Settings: &settings}
	payload, _ := json.Marshal(c)
	for _, tc := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		body    string
		want    int
	}{
		{nil, authenticatedAPIHeaders(p), string(payload), 401},
		{cookie, map[string]string{"Content-Type": "application/json"}, string(payload), 403},
		{cookie, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": p.CSRFToken, "Origin": "https://evil.test"}, string(payload), 403},
		{cookie, authenticatedAPIHeaders(p), `{"action":"create","action":"configure"}`, 400},
		{cookie, authenticatedAPIHeaders(p), `{"action":"create","extra":true}`, 400},
	} {
		r := serveContentAPI(handler, tc.cookie, "POST", base, tc.body, tc.headers)
		if r.Code != tc.want {
			t.Fatalf("strict write: %d want %d %s", r.Code, tc.want, r.Body.String())
		}
	}
	r := serveContentAPI(handler, cookie, "POST", base, string(payload), authenticatedAPIHeaders(p))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	c = feedback.Command{Action: "configure", Version: 1, Key: "security-config-key", Settings: &settings}
	payload, _ = json.Marshal(c)
	r = serveContentAPI(handler, cookie, "POST", base, string(payload), authenticatedAPIHeaders(p))
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	c.Key = "security-stale-key"
	payload, _ = json.Marshal(c)
	r = serveContentAPI(handler, cookie, "POST", base, string(payload), authenticatedAPIHeaders(p))
	if r.Code != 409 {
		t.Fatal("stale config overwrote", r.Code)
	}
	b, _ := s.feedback.Get(t.Context(), "security")
	if b.Version != 2 {
		t.Fatal("unexpected state changes")
	}
	for _, path := range []string{"https://example.test/feedback/security/unknown", "https://example.test/feedback/security/items/01", "https://example.test/feedback/security/items/1/unknown"} {
		r = serveContentAPI(handler, cookie, "GET", path, "", nil)
		if r.Code != 404 {
			t.Fatal("unknown route", path, r.Code)
		}
	}
}
