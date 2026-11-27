package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestAccountLibraryIsolationVisibilityAndProfile(t *testing.T) {
	_, handler, manager, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader, cookie, principal := createAndLoginTestUser(t, manager, owner, "library-reader", "private-reader@example.test", "读者", "subscriber")
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": principal.CSRFToken}
	base := "https://example.test/api/v1/social"
	post, err := repo.Create(t.Context(), content.PostInput{Title: "账号收藏文章", Slug: "account-library-post", AuthorID: owner.UserID, AuthorName: owner.Label(), Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bookmark", "like", "follow"} {
		id := post.ID
		if kind == "follow" {
			id = owner.UserID
		}
		body := fmt.Sprintf(`{"kind":%q,"target_id":%d,"active":true}`, kind, id)
		for i := 0; i < 2; i++ {
			r := serveContentAPI(handler, cookie, "POST", base+"/engagement", body, headers)
			if r.Code != 200 {
				t.Fatalf("set %s: %d %s", kind, r.Code, r.Body.String())
			}
		}
	}
	state := serveContentAPI(handler, cookie, "GET", fmt.Sprintf("%s/engagement?posts=%d&authors=%d", base, post.ID, owner.UserID), "", nil)
	if state.Code != 200 || strings.Count(state.Body.String(), `"count":1`) != 3 || strings.Count(state.Body.String(), `"active":true`) != 3 {
		t.Fatalf("state %s", state.Body.String())
	}
	for _, kind := range []string{"bookmark", "like", "feed"} {
		r := serveContentAPI(handler, cookie, "GET", base+"/library?kind="+kind, "", nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), post.Title) || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("library %d %s", r.Code, r.Body.String())
		}
		other := serveContentAPI(handler, ownerCookie, "GET", base+"/library?kind="+kind, "", nil)
		if other.Code != 200 || !strings.Contains(other.Body.String(), `"items":[]`) {
			t.Fatalf("cross account leak %s", other.Body.String())
		}
	}
	_, err = repo.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bookmark", "like", "feed"} {
		r := serveContentAPI(handler, cookie, "GET", base+"/library?kind="+kind, "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), post.Title) {
			t.Fatalf("private content leaked %s", r.Body.String())
		}
	}
	state = serveContentAPI(handler, cookie, "GET", fmt.Sprintf("%s/engagement?posts=%d", base, post.ID), "", nil)
	if strings.Contains(state.Body.String(), `"target_id"`) {
		t.Fatal("private state leaked")
	}
	remove := serveContentAPI(handler, cookie, "POST", base+"/engagement", fmt.Sprintf(`{"kind":"bookmark","target_id":%d,"active":false}`, post.ID), headers)
	if remove.Code != 200 {
		t.Fatal(remove.Body.String())
	}
	profile := serveContentAPI(handler, cookie, "GET", base+"/profile", "", nil)
	var data struct {
		Version int64 `json:"version"`
	}
	decodeAPIResponse(t, profile, &data)
	if strings.Contains(profile.Body.String(), reader.Email) || strings.Contains(profile.Body.String(), "password") {
		t.Fatal("profile leaked private credentials")
	}
	update := fmt.Sprintf(`{"display_name":"新的读者名","locale":"zh-CN","version":%d}`, data.Version)
	r := serveContentAPI(handler, cookie, "POST", base+"/profile", update, headers)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", base+"/profile", update, headers), 409, "profile.conflict")
	fresh, err := manager.GetUser(t.Context(), reader.ID)
	if err != nil || fresh.DisplayName != "新的读者名" || fresh.Email != reader.Email || len(fresh.Roles) != 1 || fresh.Roles[0].Slug != "subscriber" {
		t.Fatalf("profile changed identity %#v %v", fresh, err)
	}
	account := serveContentAPI(handler, cookie, "GET", "https://example.test/account/", "", nil)
	if account.Code != 200 || account.Header().Get("Cache-Control") != "no-store" || account.Header().Get("X-Robots-Tag") == "" || !strings.Contains(account.Body.String(), "个人中心") {
		t.Fatalf("account page %d %s", account.Code, account.Body.String())
	}
	if !strings.Contains(account.Body.String(), `data-account-can-write="false"`) || !strings.Contains(account.Body.String(), `data-profile-heading>新的读者名</h1>`) {
		t.Fatal("reader profile must use the current identity without a writing capability")
	}
	writerAccount := serveContentAPI(handler, ownerCookie, "GET", "https://example.test/account/", "", nil)
	if writerAccount.Code != 200 || !strings.Contains(writerAccount.Body.String(), `data-account-can-write="true"`) {
		t.Fatal("account writing entry must follow the authenticated capability")
	}
}

func TestEngagementRequestBoundaries(t *testing.T) {
	_, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	peer, _, _ := createAndLoginTestUser(t, manager, owner, "library-peer", "peer@example.test", "目标", "subscriber")
	base := "https://example.test/api/v1/social"
	valid := fmt.Sprintf(`{"kind":"follow","target_id":%d,"active":true}`, peer.ID)
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": owner.CSRFToken}
	for _, test := range []struct {
		path, body string
		code       int
	}{
		{"/engagement", `{"kind":"follow","target_id":2}`, 422},
		{"/engagement", `{"kind":"follow","target_id":2,"active":null}`, 422},
		{"/engagement", `{"kind":"follow","target_id":2,"active":true,"user_id":99}`, 400},
		{"/engagement", `{"kind":"follow","kind":"like","target_id":2,"active":true}`, 400},
		{"/engagement?owner=99", valid, 400},
		{"/engagement", fmt.Sprintf(`{"kind":"follow","target_id":%d,"active":true}`, owner.UserID), 422},
		{"/engagement", `{"kind":"follow","target_id":99999999,"active":true}`, 404},
		{"/profile", `{"display_name":"hack","locale":"zh-CN","version":1,"role":"administrator"}`, 400},
	} {
		r := serveContentAPI(handler, cookie, "POST", base+test.path, test.body, headers)
		if r.Code != test.code {
			t.Fatalf("%s %s: %d %s", test.path, test.body, r.Code, r.Body.String())
		}
	}
	for _, h := range []map[string]string{{"Content-Type": "application/json", "Origin": "https://evil.test", "X-CSRF-Token": owner.CSRFToken}, {"Content-Type": "application/json", "Origin": "https://example.test"}} {
		assertAPIError(t, serveContentAPI(handler, cookie, "POST", base+"/engagement", valid, h), 403, "auth.csrf_invalid")
	}
	assertAPIError(t, serveContentAPI(handler, &http.Cookie{}, "GET", base+"/library", "", nil), 401, "auth.authentication_required")
	// Revoked account status is reloaded for every request.
	roles := []string{"subscriber"}
	_, err := manager.UpdateUser(t.Context(), owner, peer.ID, peer.Version, auth.UserInput{Username: peer.Username, Email: peer.Email, DisplayName: peer.DisplayName, Status: auth.UserStatusSuspended, Locale: peer.Locale, RoleSlugs: roles})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", base+"/engagement", valid, headers), 404, "engagement.target_missing")
}
