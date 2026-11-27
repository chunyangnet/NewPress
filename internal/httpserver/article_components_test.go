package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

func componentSource(c articlewidgets.Config) string {
	return `<aside data-np-widget="` + c.Kind + `" data-np-config="` + c.Encode() + `"><p>fallback</p></aside>`
}
func testComponent(kind string) articlewidgets.Config {
	return articlewidgets.Config{Version: 1, ID: "component_http_test", Kind: kind, Title: "参与测试", Mode: "both", Max: 5, Items: []articlewidgets.Item{{Title: "甲"}, {Title: "乙"}}}
}
func TestArticleComponentsGuestCSRFVisibilityAndRealVotes(t *testing.T) {
	s, h, _, repo, _, owner := newPersistentAdminTestServer(t)
	c := testComponent("poll")
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "投票", Slug: "poll-test", Status: content.StatusPublish, ContentSource: componentSource(c)})
	url := fmt.Sprintf("https://example.test/api/v1/article-components/%d/%s", p.ID, c.ID)
	get := serveContentAPI(h, nil, "GET", url, "", nil)
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	var body struct {
		Token  string               `json:"csrf_token"`
		Config string               `json:"config"`
		State  articlewidgets.State `json:"state"`
	}
	decodeAPIResponse(t, get, &body)
	var cookie *http.Cookie
	for _, ck := range get.Result().Cookies() {
		if ck.Name == componentCookie {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || body.Token == "" || body.Config != c.Encode() {
		t.Fatal(get.Header(), body)
	}
	payload := currencyJSON(t, map[string]any{"action": "vote", "value": 1, "fingerprint": c.Fingerprint()})
	for _, headers := range []map[string]string{{"Origin": "https://example.test", "Content-Type": "application/json"}, {"Origin": "https://evil.test", "Content-Type": "application/json", "X-CSRF-Token": body.Token}} {
		r := serveContentAPI(h, cookie, "POST", url, payload, headers)
		if r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": body.Token}
	r := serveContentAPI(h, cookie, "POST", url, payload, headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	decodeAPIResponse(t, r, &body)
	if body.State.Total != 1 || body.State.Counts[1] != 1 {
		t.Fatal(body.State)
	}
	r = serveContentAPI(h, cookie, "POST", url, payload, headers)
	if r.Code != 409 {
		t.Fatal("duplicate vote accepted", r.Code)
	}
	if _, err := repo.ChangeStatus(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, cookie, "GET", url, "", nil)
	if r.Code != 404 {
		t.Fatal("private results leaked", r.Code)
	}
	_ = s
}
func TestArticleComponentsPacketAuthorConsentAndLedger(t *testing.T) {
	s, h, manager, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	ledger, cfg := currencyHTTPStore(t, s, false)
	reader, readerCookie, principal := createAndLoginTestUser(t, manager, owner, "packet-reader", "packet-reader@example.test", "读者", "subscriber")
	_, err := ledger.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: owner.UserID, Delta: 10000, ExpectedVersion: cfg.Version, RequestID: "component-initial-balance", Reason: "组件红包测试"})
	if err != nil {
		t.Fatal(err)
	}
	c := testComponent("redpacket")
	c.Mode = "login"
	c.Amount = "1.00"
	c.Count = 2
	c.Currency = "main"
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "红包", Slug: "packet-test", Status: content.StatusPublish, ContentSource: componentSource(c)})
	url := fmt.Sprintf("https://example.test/api/v1/article-components/%d/%s", p.ID, c.ID)
	send := func(cookie *http.Cookie, token, action string) int {
		r := serveContentAPI(h, cookie, "POST", url, currencyJSON(t, map[string]any{"action": action, "fingerprint": c.Fingerprint()}), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": token})
		if r.Code >= 500 {
			t.Fatal(r.Body.String())
		}
		return r.Code
	}
	if got := send(readerCookie, principal.CSRFToken, "activate"); got != 403 {
		t.Fatal("nonowner activated", got)
	}
	if got := send(readerCookie, principal.CSRFToken, "claim"); got != 409 {
		t.Fatal("unconsented spend", got)
	}
	if got := send(ownerCookie, owner.CSRFToken, "activate"); got != 200 {
		t.Fatal("activation", got)
	}
	for range 2 {
		if got := send(readerCookie, principal.CSRFToken, "claim"); got != 200 {
			t.Fatal("claim", got)
		}
	}
	from, _ := ledger.Balance(t.Context(), owner.UserID)
	to, _ := ledger.Balance(t.Context(), reader.ID)
	if from != 9900 || to != 100 {
		t.Fatal("incorrect debit or duplicate transfer", from, to)
	}
	if got := send(ownerCookie, owner.CSRFToken, "pause"); got != 200 {
		t.Fatal("pause", got)
	}
}
func TestArticleComponentsRenderAssetsAndFramePolicy(t *testing.T) {
	_, h, _, repo, _, owner := newPersistentAdminTestServer(t)
	c := testComponent("video")
	c.Provider = "youtube"
	c.UID = "dQw4w9WgXcQ"
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "视频", Slug: "widget-video", Status: content.StatusPublish, ContentSource: componentSource(c)})
	r := serveContentAPI(h, nil, "GET", "https://example.test/posts/"+p.Slug, "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "newpress-components.js") || !strings.Contains(r.Header().Get("Content-Security-Policy"), "frame-src 'self' https://www.youtube-nocookie.com") {
		t.Fatal(r.Code, r.Header(), r.Body.String()[:min(500, r.Body.Len())])
	}
	if strings.Contains(r.Body.String(), `<iframe`) {
		t.Fatal("external frame eagerly loaded")
	}
	plain := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "普通文章", Slug: "plain-test", Status: content.StatusPublish})
	r = serveContentAPI(h, nil, "GET", "https://example.test/posts/"+plain.Slug, "", nil)
	if strings.Contains(r.Body.String(), "newpress-components.js") || strings.Contains(r.Header().Get("Content-Security-Policy"), "youtube") {
		t.Fatal("ordinary page pays component cost")
	}
}

func TestArticleComponentsValidateBeforeInsertion(t *testing.T) {
	_, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	url := "https://example.test/api/v1/admin/article-components/validate"
	c := testComponent("callout")
	body := currencyJSON(t, map[string]string{"config": c.Encode()})
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": owner.CSRFToken}
	if r := serveContentAPI(h, nil, "POST", url, body, headers); r.Code != http.StatusUnauthorized {
		t.Fatal("anonymous validation", r.Code, r.Body.String())
	}
	r := serveContentAPI(h, cookie, "POST", url, body, headers)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), c.Encode()) {
		t.Fatal("valid config", r.Code, r.Body.String())
	}
	c.Image = "https://untrusted.example/image.png"
	r = serveContentAPI(h, cookie, "POST", url, currencyJSON(t, map[string]string{"config": c.Encode()}), headers)
	if r.Code != http.StatusBadRequest {
		t.Fatal("invalid config accepted", r.Code, r.Body.String())
	}
	headers["X-CSRF-Token"] = "invalid"
	if r := serveContentAPI(h, cookie, "POST", url, body, headers); r.Code != http.StatusForbidden {
		t.Fatal("missing CSRF", r.Code, r.Body.String())
	}
}

func TestArticleComponentsMergeExistingFramePolicy(t *testing.T) {
	c := testComponent("video")
	c.Provider, c.UID = "youtube", "dQw4w9WgXcQ"
	for _, existing := range []string{"default-src 'self'; frame-src https://existing.example; object-src 'none'", "default-src 'self'; frame-src 'none'", "default-src 'self'"} {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", existing)
		prepareComponentFrames(w, componentSource(c))
		prepareComponentFrames(w, componentSource(c))
		policy := w.Header().Get("Content-Security-Policy")
		if strings.Count(policy, "frame-src") != 1 || strings.Count(policy, "https://www.youtube-nocookie.com") != 1 || strings.Contains(policy, "frame-src 'none'") || !strings.Contains(policy, "default-src 'self'") {
			t.Fatal(policy)
		}
		if strings.Contains(existing, "existing.example") && !strings.Contains(policy, "https://existing.example") {
			t.Fatal("lost preexisting frame source", policy)
		}
	}
}

func TestArticleComponentsRatingParticipationModes(t *testing.T) {
	_, h, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	for _, mode := range []string{"login", "guest", "both"} {
		t.Run(mode, func(t *testing.T) {
			c := testComponent("rating")
			c.Mode = mode
			p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "评分", Slug: "rating-" + mode, Status: content.StatusPublish, ContentSource: componentSource(c)})
			url := fmt.Sprintf("https://example.test/api/v1/article-components/%d/%s", p.ID, c.ID)
			get := serveContentAPI(h, nil, "GET", url, "", nil)
			var body struct {
				Token string               `json:"csrf_token"`
				State articlewidgets.State `json:"state"`
			}
			decodeAPIResponse(t, get, &body)
			vote := currencyJSON(t, map[string]any{"action": "vote", "value": 4, "fingerprint": c.Fingerprint()})
			headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": body.Token}
			var visitor *http.Cookie
			for _, ck := range get.Result().Cookies() {
				if ck.Name == componentCookie {
					visitor = ck
				}
			}
			r := serveContentAPI(h, visitor, "POST", url, vote, headers)
			if mode == "login" {
				if r.Code != http.StatusUnauthorized {
					t.Fatal("anonymous login-only vote", r.Code)
				}
				headers["X-CSRF-Token"] = owner.CSRFToken
				r = serveContentAPI(h, cookie, "POST", url, vote, headers)
			}
			if r.Code != http.StatusOK {
				t.Fatal(r.Code, r.Body.String())
			}
			decodeAPIResponse(t, r, &body)
			if body.State.Total != 1 || body.State.Average != 4 || !body.State.Participated {
				t.Fatal(body.State)
			}
			if mode == "guest" {
				headers["X-CSRF-Token"] = owner.CSRFToken
				if r := serveContentAPI(h, cookie, "POST", url, vote, headers); r.Code != http.StatusForbidden {
					t.Fatal("guest mode must use browser credential", r.Code)
				}
			}
		})
	}
}
