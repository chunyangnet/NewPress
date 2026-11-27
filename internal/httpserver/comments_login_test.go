package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func TestCommentsRequireRegisteredAccount(t *testing.T) {
	s, h, users, repo, _, owner := newPersistentAdminTestServer(t)
	u, cookie, _ := createAndLoginTestUser(t, users, owner, "comment-member", "member@example.test", "评论读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Title: "评论权限", Slug: "comment-login-policy", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	settings := s.siteSettings.Snapshot()
	settings.Settings.Discussion.RequireLogin = true
	settings.Settings.Operations.PublicCacheSeconds = 300
	if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	token := s.commentGuard.formToken(post.ID, time.Now())
	endpoint := "https://example.test/api/v1/posts/" + post.Slug + "/comments"
	article := "https://example.test/posts/" + post.Slug
	body := fmt.Sprintf(`{"form_token":%q,"author_name":"伪造访客","email":"forged@example.test","content":"保留这段输入"}`, token)
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"}
	guest := serveContentAPI(h, nil, "GET", article, "", nil)
	if guest.Code != 200 || !strings.Contains(guest.Body.String(), `data-comment-locked="true"`) || !strings.Contains(guest.Body.String(), `inert aria-hidden="true"`) || guest.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("guest composer must be gated and never cached", guest.Code)
	}
	projection := serveContentAPI(h, nil, "GET", endpoint, "", nil)
	if projection.Code != 200 || !strings.Contains(projection.Body.String(), `"requires_login":true`) {
		t.Fatal(projection.Code, projection.Body.String())
	}
	for _, session := range []*http.Cookie{nil, {Name: cookie.Name, Value: "expired-session"}} {
		got := serveContentAPI(h, session, "POST", endpoint, body, headers)
		if got.Code != 401 || !strings.Contains(got.Body.String(), "comments.login_required") {
			t.Fatal("guest API bypass", got.Code, got.Body.String())
		}
	}
	form := url.Values{"parent_id": {"0"}, "website": {""}, "form_token": {token}, "author_name": {"访客"}, "email": {"guest@example.test"}, "content": {"保留 HTML 评论"}}
	got := serveContentAPI(h, nil, "POST", article+"/comments", form.Encode(), map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"})
	if got.Code != 401 || !strings.Contains(got.Body.String(), "保留 HTML 评论") || !strings.Contains(got.Body.String(), `data-comment-locked="true"`) {
		t.Fatal("guest HTML bypass or discarded draft", got.Code)
	}
	before, err := s.comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID})
	if err != nil || len(before.Items) != 0 {
		t.Fatal("guest comments were stored", before, err)
	}
	for _, asJSON := range []bool{true, false} {
		if asJSON {
			got = serveContentAPI(h, cookie, "POST", endpoint, body, headers)
		} else {
			got = serveContentAPI(h, cookie, "POST", article+"/comments", form.Encode(), map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"})
		}
		if got.Code != 202 && got.Code != 303 {
			t.Fatal("member comment rejected", got.Code, got.Body.String())
		}
	}
	own, err := s.comments.ListAdmin(t.Context(), comment.Query{UserID: u.ID, OwnOnly: true})
	if err != nil || len(own.Items) != 2 || own.Items[0].AuthorEmail != u.Email {
		t.Fatal("member ownership lost", own, err)
	}
	member := serveContentAPI(h, cookie, "GET", article, "", nil)
	if !strings.Contains(member.Body.String(), `data-comment-locked="false"`) || member.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("member remains locked or cacheable")
	}
	latest := s.siteSettings.Snapshot()
	latest.Settings.Discussion.RequireLogin = false
	if _, _, err = s.siteSettings.Save(latest.Revision, latest.Settings); err != nil {
		t.Fatal(err)
	}
	got = serveContentAPI(h, nil, "POST", endpoint, body, headers)
	if got.Code != 202 {
		t.Fatal("guest mode was not restored", got.Code, got.Body.String())
	}
}

func TestMobileDockUsesFourConfiguredOrFallbackDestinations(t *testing.T) {
	for _, count := range []int{0, 1, 3, 4, 5} {
		items := make([]navigationItemView, count)
		for i := range items {
			items[i] = navigationItemView{Label: fmt.Sprint(i), URL: fmt.Sprintf("/custom/%d/", i)}
		}
		dock := mobileDockItems(items, "/", false)
		if len(dock) != 4 {
			t.Fatalf("%d configured: got %d slots", count, len(dock))
		}
		for i := 0; i < min(count, 4); i++ {
			if dock[i].URL != items[i].URL {
				t.Fatal("configured order changed")
			}
		}
		seen := map[string]bool{}
		for _, item := range dock {
			if seen[item.URL] || item.URL == "/community/" {
				t.Fatal("duplicate or disabled fallback", dock)
			}
			seen[item.URL] = true
		}
	}
}
