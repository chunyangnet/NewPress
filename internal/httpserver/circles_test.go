package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type unavailableCircles struct{ community.Store }

func (unavailableCircles) Ready(context.Context) error {
	return errors.New("unavailable community storage")
}
func TestCirclesReadinessRequiresEnabledStore(t *testing.T) {
	s, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.CommunityEnabled = true
	for _, store := range []community.Store{nil, unavailableCircles{}} {
		s.SetCommunityStore(store)
		r := serveContentAPI(handler, nil, "GET", "https://example.test/health/ready", "", nil)
		if r.Code != 503 {
			t.Fatalf("enabled unavailable community ready: %d", r.Code)
		}
	}
	s.config.CommunityEnabled = false
	r := serveContentAPI(handler, nil, "GET", "https://example.test/health/ready", "", nil)
	if r.Code != 200 {
		t.Fatalf("disabled community gates readiness: %d", r.Code)
	}
}

func TestCirclesHTTPPermissionsAndPrivacy(t *testing.T) {
	s, handler, users, _, adminCookie, admin := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, admin, "circle-reader", "circle-reader@example.test", "圈内读者", "subscriber")
	_, otherCookie, other := createAndLoginTestUser(t, users, admin, "circle-other", "circle-other@example.test", "其他读者", "subscriber")
	store, err := community.NewFileStore(filepath.Join(t.TempDir(), "community.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(store)
	base := "https://example.test/api/v1/community/circles"
	r := serveContentAPI(handler, adminCookie, "GET", base, "", nil)
	if r.Code != 404 {
		t.Fatal("disabled", r.Code)
	}
	s.config.CommunityEnabled = true
	seq := 0
	write := func(cookie *http.Cookie, p auth.Principal, slug string, cmd community.Command, want int) community.Result {
		t.Helper()
		seq++
		cmd.Key = fmt.Sprintf("http-circle-%016d", seq)
		if c, e := store.Get(t.Context(), slug); e == nil {
			cmd.Version = c.Version
		}
		b, _ := json.Marshal(cmd)
		r := serveContentAPI(handler, cookie, "POST", base+"/"+slug, string(b), authenticatedAPIHeaders(p))
		if r.Code != want || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", cmd.Action, r.Code, r.Body.String())
		}
		var result community.Result
		if want == 200 {
			json.Unmarshal(r.Body.Bytes(), &result)
		}
		return result
	}
	create := community.Command{Action: "create", Name: "私密读书会", Description: "公开介绍", Rules: "加入后讨论", JoinPolicy: "approval", Visibility: "members", OwnerID: admin.UserID}
	write(readerCookie, reader, "private-reading", create, 403)
	write(adminCookie, admin, "private-reading", create, 200)
	topic := write(adminCookie, admin, "private-reading", community.Command{Action: "topic", Title: "PRIVATE TITLE NEVER LEAK", Body: "<script>privatePayload()</script>", Kind: "question"}, 200)
	topicURL := base + "/private-reading/topics/" + fmt.Sprint(topic.TopicID)
	for _, cookie := range []*http.Cookie{nil, readerCookie} {
		for _, path := range []string{base, base + "/private-reading"} {
			r = serveContentAPI(handler, cookie, "GET", path, "", nil)
			if r.Code != 200 || strings.Contains(r.Body.String(), "PRIVATE TITLE") || strings.Contains(r.Body.String(), "privatePayload") || strings.Contains(r.Body.String(), "circle-reader@example.test") {
				t.Fatal("private projection", r.Code, r.Body.String())
			}
		}
		r = serveContentAPI(handler, cookie, "GET", topicURL, "", nil)
		if r.Code != 404 {
			t.Fatal("private topic readable", r.Code)
		}
	}
	write(readerCookie, reader, "private-reading", community.Command{Action: "join"}, 200)
	write(readerCookie, reader, "private-reading", community.Command{Action: "topic", Title: "Pending", Body: "No", Kind: "discussion"}, 403)
	write(adminCookie, admin, "private-reading", community.Command{Action: "member", UserID: reader.UserID, Role: "member"}, 200)
	r = serveContentAPI(handler, readerCookie, "GET", topicURL, "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "PRIVATE TITLE") {
		t.Fatal("member read", r.Code, r.Body.String())
	}
	reply := write(readerCookie, reader, "private-reading", community.Command{Action: "reply", TopicID: topic.TopicID, Body: "PRIVATE REPLY"}, 200)
	write(readerCookie, reader, "private-reading", community.Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID}, 403)
	write(adminCookie, admin, "private-reading", community.Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID}, 200)
	yes := true
	write(readerCookie, reader, "private-reading", community.Command{Action: "pin", TopicID: topic.TopicID, Enabled: &yes}, 403)
	write(adminCookie, admin, "private-reading", community.Command{Action: "member", UserID: reader.UserID, Role: "moderator"}, 200)
	write(readerCookie, reader, "private-reading", community.Command{Action: "pin", TopicID: topic.TopicID, Enabled: &yes}, 200)
	write(readerCookie, reader, "private-reading", community.Command{Action: "hide_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Enabled: &yes}, 200)
	// A second circle must not grant moderation access to this circle.
	create.Name = "另一个圈"
	create.OwnerID = other.UserID
	create.Visibility = "public"
	create.JoinPolicy = "open"
	write(adminCookie, admin, "other-circle", create, 200)
	write(otherCookie, other, "private-reading", community.Command{Action: "feature", TopicID: topic.TopicID, Enabled: &yes}, 403)
	write(adminCookie, admin, "private-reading", community.Command{Action: "member", UserID: reader.UserID, Role: "member"}, 200)
	r = serveContentAPI(handler, readerCookie, "GET", topicURL, "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "PRIVATE REPLY") {
		t.Fatal("hidden reply leaks", r.Body.String())
	}
	pageURL := "https://example.test/community/private-reading/topics/" + fmt.Sprint(topic.TopicID)
	r = serveContentAPI(handler, readerCookie, "GET", pageURL, "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "<script>privatePayload()") || !strings.Contains(r.Body.String(), "&lt;script&gt;privatePayload()") || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("HTML escaping/cache", r.Code, r.Body.String())
	}
	write(readerCookie, reader, "private-reading", community.Command{Action: "leave"}, 200)
	r = serveContentAPI(handler, readerCookie, "GET", topicURL, "", nil)
	if r.Code != 404 {
		t.Fatal("leave did not revoke", r.Code)
	}
	for _, url := range []string{"https://example.test/community/private-reading/", "https://example.test/community/"} {
		r = serveContentAPI(handler, nil, "GET", url, "", nil)
		if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Body.String(), "data-circle-page") {
			t.Fatal("circle page", r.Code)
		}
	}
	for _, url := range []string{"https://example.test/community/private-reading/unknown", "https://example.test/community/private-reading/topics/1/unknown"} {
		r = serveContentAPI(handler, nil, "GET", url, "", nil)
		if r.Code != 404 {
			t.Fatal("unknown route", r.Code, url)
		}
	}
	for _, query := range []string{"?page=0", "?page=1&page=2", "?foreign=yes", "?members_page=-1"} {
		r = serveContentAPI(handler, adminCookie, "GET", base+"/private-reading"+query, "", nil)
		if r.Code != 422 {
			t.Fatal("bad query", query, r.Code)
		}
	}
	payload := `{"action":"join","key":"strict-boundary-key","version":1}`
	for _, tc := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		body    string
		want    int
	}{{nil, authenticatedAPIHeaders(admin), payload, 401}, {adminCookie, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, payload, 403}, {adminCookie, map[string]string{"Content-Type": "application/json", "Origin": "https://evil.test", "X-CSRF-Token": admin.CSRFToken}, payload, 403}, {adminCookie, authenticatedAPIHeaders(admin), `{"action":"join","action":"join"}`, 400}, {adminCookie, authenticatedAPIHeaders(admin), `{"action":"join","extra":true}`, 400}} {
		r = serveContentAPI(handler, tc.cookie, "POST", base+"/private-reading", tc.body, tc.headers)
		if r.Code != tc.want {
			t.Fatal("boundary", r.Code, r.Body.String())
		}
	}
	// Disabling a user invalidates a previously authenticated community actor.
	u, e := users.GetUser(t.Context(), reader.UserID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = users.UpdateUser(t.Context(), admin, u.ID, u.Version, auth.UserInput{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Locale: u.Locale, Status: auth.UserStatusSuspended, RoleSlugs: []string{"subscriber"}})
	if e != nil {
		t.Fatal(e)
	}
	r = serveContentAPI(handler, readerCookie, "POST", base+"/other-circle", payload, authenticatedAPIHeaders(reader))
	if r.Code != 401 {
		t.Fatal("disabled session", r.Code, r.Body.String())
	}
}

func TestCirclesPaginationAndVersion(t *testing.T) {
	s, handler, _, _, cookie, p := newPersistentAdminTestServer(t)
	s.config.CommunityEnabled = true
	store, _ := community.NewFileStore("")
	s.SetCommunityStore(store)
	now := time.Now()
	a := community.Actor{ID: p.UserID, Admin: true}
	result, e := store.Apply(t.Context(), "paged", a, community.Command{Action: "create", Key: "create-paged-circle", Name: "分页", JoinPolicy: "open", Visibility: "public", OwnerID: p.UserID}, now)
	if e != nil {
		t.Fatal(e)
	}
	for i := range 23 {
		result, e = store.Apply(t.Context(), "paged", a, community.Command{Action: "topic", Key: fmt.Sprintf("paged-topic-%016d", i), Version: result.Version, Title: fmt.Sprint("topic-", i), Body: "body", Kind: "discussion"}, now)
		if e != nil {
			t.Fatal(e)
		}
	}
	r := serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/community/circles/paged?page=2", "", nil)
	var view circlesPageView
	decodeAPIResponse(t, r, &view)
	if view.Total != 23 || len(view.Topics) != 3 || view.Topics[0].ID != 3 || view.Previous != "/community/paged/?page=1" {
		t.Fatal(view)
	}
	body := fmt.Sprintf(`{"action":"topic","key":"stale-version-http","version":1,"title":"lost write","body":"no","kind":"discussion"}`)
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/community/circles/paged", body, authenticatedAPIHeaders(p))
	if r.Code != 409 {
		t.Fatal(r.Code)
	}
	c, _ := store.Get(t.Context(), "paged")
	if len(c.Topics) != 23 {
		t.Fatal("conflict wrote topic")
	}
}
