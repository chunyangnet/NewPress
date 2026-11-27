package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
)

func TestCircleAttachmentsReportsNotificationsAndArchiveHTTP(t *testing.T) {
	s, handler, users, _, cookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "rich-reader", "rich-reader@example.test", "图文读者", "subscriber")
	_, otherCookie, _ := createAndLoginTestUser(t, users, owner, "rich-other", "rich-other@example.test", "旁观者", "subscriber")
	store, _ := community.NewFileStore("")
	s.config.CommunityEnabled = true
	s.SetCommunityStore(store)
	base := "https://example.test/api/v1/community/circles/rich"
	seq := 0
	write := func(ck *http.Cookie, p auth.Principal, cmd community.Command, want int) community.Result {
		t.Helper()
		seq++
		cmd.Key = fmt.Sprintf("rich-http-key-%016d", seq)
		if c, e := store.Get(t.Context(), "rich"); e == nil {
			cmd.Version = c.Version
		}
		b, _ := json.Marshal(cmd)
		r := serveContentAPI(handler, ck, "POST", base, string(b), authenticatedAPIHeaders(p))
		if r.Code != want {
			t.Fatalf("%s: %d %s", cmd.Action, r.Code, r.Body.String())
		}
		var result community.Result
		if want == 200 {
			decodeAPIResponse(t, r, &result)
		}
		return result
	}
	write(cookie, owner, community.Command{Action: "create", Name: "私密图文圈", OwnerID: owner.UserID, JoinPolicy: "open", Visibility: "members"}, 200)
	write(readerCookie, reader, community.Command{Action: "join"}, 200)
	c, _ := store.Get(t.Context(), "rich")
	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	_ = multipartWriter.WriteField("key", "http-upload-key-123456")
	_ = multipartWriter.WriteField("version", fmt.Sprint(c.Version))
	part, _ := multipartWriter.CreateFormFile("file", "example.txt")
	_, _ = part.Write([]byte("PRIVATE ATTACHMENT"))
	_ = multipartWriter.Close()
	uploadBody := bytes.Clone(body.Bytes())
	upload := func(csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", base+"/attachments", bytes.NewReader(uploadBody))
		req.AddCookie(readerCookie)
		req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
		req.Header.Set("Origin", "https://example.test")
		req.Header.Set("X-CSRF-Token", csrf)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	if out := upload("bad"); out.Code != 403 {
		t.Fatal("upload CSRF", out.Code, out.Body.String())
	}
	out := upload(reader.CSRFToken)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var saved struct {
		Version      int64  `json:"version"`
		AttachmentID string `json:"attachment_id"`
		Replay       bool   `json:"replay"`
	}
	decodeAPIResponse(t, out, &saved)
	repeated := upload(reader.CSRFToken)
	if repeated.Code != 200 || !strings.Contains(repeated.Body.String(), `"replay":true`) {
		t.Fatal("upload replay", repeated.Code, repeated.Body.String())
	}
	download := base + "/attachments/" + saved.AttachmentID
	if r := serveContentAPI(handler, otherCookie, "GET", download, "", nil); r.Code != 404 {
		t.Fatal("draft attachment exposed", r.Code)
	}
	topic := write(readerCookie, reader, community.Command{Action: "topic", Title: "图文问题", Body: "**私密正文**\n\n![追踪](https://evil.test/a.png)", Format: "markdown", Kind: "question", Attachments: []string{saved.AttachmentID}}, 200)
	for _, ck := range []*http.Cookie{cookie, readerCookie} {
		r := serveContentAPI(handler, ck, "GET", download, "", nil)
		if r.Code != 200 || r.Body.String() != "PRIVATE ATTACHMENT" || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("Content-Disposition"), "attachment") {
			t.Fatal("download", r.Code, r.Header(), r.Body.String())
		}
	}
	for _, ck := range []*http.Cookie{nil, otherCookie} {
		if r := serveContentAPI(handler, ck, "GET", download, "", nil); r.Code != 404 {
			t.Fatal("private file exposed", r.Code)
		}
	}
	page := serveContentAPI(handler, readerCookie, "GET", fmt.Sprintf("https://example.test/community/rich/topics/%d", topic.TopicID), "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "<strong>私密正文</strong>") || strings.Contains(page.Body.String(), `src="https://evil.test`) {
		t.Fatal("rich render", page.Code, page.Body.String())
	}
	preview := serveContentAPI(handler, readerCookie, "POST", base+"/preview", `{"body":"**preview**","format":"markdown","attachments":[]}`, authenticatedAPIHeaders(reader))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "preview") {
		t.Fatal(preview.Code, preview.Body.String())
	}
	report := write(readerCookie, reader, community.Command{Action: "report", TopicID: topic.TopicID, Category: "privacy", Reason: "PRIVATE REPORT DETAILS"}, 200)
	if r := serveContentAPI(handler, otherCookie, "GET", base, "", nil); strings.Contains(r.Body.String(), "PRIVATE REPORT DETAILS") {
		t.Fatal("report leaked")
	}
	if r := serveContentAPI(handler, readerCookie, "GET", base, "", nil); !strings.Contains(r.Body.String(), "PRIVATE REPORT DETAILS") {
		t.Fatal("reporter cannot see report")
	}
	yes := true
	write(cookie, owner, community.Command{Action: "review_report", ReportID: report.ReportID, Status: "resolved", Note: "已核对并隐藏", Enabled: &yes}, 200)
	if err := s.notifications.Tick(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	notices := serveContentAPI(handler, readerCookie, "GET", "https://example.test/api/v1/social/notice-center", "", nil)
	if notices.Code != 200 || !strings.Contains(notices.Body.String(), "你的圈子举报已有处理结果") {
		t.Fatal("notice center integration", notices.Code, notices.Body.String())
	}
	if pending, err := store.PendingActivity(t.Context(), time.Now(), 100); err != nil || len(pending) != 0 {
		t.Fatal("notification event not processed", pending, err)
	}
	if r := serveContentAPI(handler, readerCookie, "GET", download, "", nil); r.Code != 404 {
		t.Fatal("hidden attachment exposed", r.Code)
	}
	if r := serveContentAPI(handler, readerCookie, "GET", base+"/export", "", nil); r.Code != 403 {
		t.Fatal("export owner-only", r.Code)
	}
	if r := serveContentAPI(handler, cookie, "GET", base+"/export", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "PRIVATE REPORT DETAILS") {
		t.Fatal("export", r.Code)
	}
	write(cookie, owner, community.Command{Action: "delete_topic", TopicID: topic.TopicID}, 200)
	if r := serveContentAPI(handler, cookie, "GET", download, "", nil); r.Code != 404 {
		t.Fatal("deleted attachment live", r.Code)
	}
	write(cookie, owner, community.Command{Action: "archive_circle", Enabled: &yes}, 200)
	if r := serveContentAPI(handler, cookie, "GET", base, "", nil); !strings.Contains(r.Body.String(), `"archived":true`) {
		t.Fatal("archive missing")
	}
	if r := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/circles/", "", nil); r.Code != 200 {
		t.Fatal("admin entry", r.Code, r.Body.String())
	}
}
