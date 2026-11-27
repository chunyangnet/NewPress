package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/content"
	"strings"
	"testing"
)

func TestSeriesAPIPermissionsConcurrencyAndPublicPages(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "series-reader", "series-reader@example.test", "读者", "subscriber")
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "series-author", "series-author@example.test", "作者", "author")
	public, err := s.repository.Create(t.Context(), content.PostInput{Title: "系列的公开章节", Slug: "series-public-chapter", Status: content.StatusPublish, AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.repository.Create(t.Context(), content.PostInput{Title: "绝不公开的章节标题", Slug: "series-secret-chapter", Status: content.StatusPrivate, AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	input := content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusPublish, Title: "连续阅读专题", Slug: "reading-sequence", Description: "<script>unsafe</script>", PostIDs: []int64{secret.ID, public.ID}}
	encoded, _ := json.Marshal(input)
	body := string(encoded)
	base := "https://example.test/api/v1/admin/series"
	headers := authenticatedAPIHeaders(owner)
	if r := serveContentAPI(handler, nil, http.MethodGet, base, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	for _, c := range []*http.Cookie{readerCookie, authorCookie} {
		if r := serveContentAPI(handler, c, http.MethodGet, base, "", nil); r.Code != 403 {
			t.Fatal(r.Code)
		}
	}
	if r := serveContentAPI(handler, authorCookie, http.MethodPost, base, body, authenticatedAPIHeaders(author)); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r := serveContentAPI(handler, cookie, http.MethodPost, base, body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}); r.Code != 403 {
		t.Fatal("missing CSRF", r.Code)
	}
	foreign := authenticatedAPIHeaders(owner)
	foreign["Origin"] = "https://foreign.test"
	if r := serveContentAPI(handler, cookie, http.MethodPost, base, body, foreign); r.Code != 403 {
		t.Fatal("foreign origin", r.Code)
	}
	nullBody := `{"kind":"topic","status":"draft","title":"Null","slug":"null-series","post_ids":[],"description":null}`
	if r := serveContentAPI(handler, cookie, http.MethodPost, base, nullBody, headers); r.Code != 400 {
		t.Fatal("null string accepted", r.Code, r.Body.String())
	}
	created := serveContentAPI(handler, cookie, http.MethodPost, base, body, headers)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var item seriesAdminResponse
	if err = json.Unmarshal(created.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	itemURL := fmt.Sprintf("%s/%d", base, item.ID)
	if r := serveContentAPI(handler, cookie, http.MethodPut, itemURL, body, headers); r.Code != 428 {
		t.Fatal("missing version", r.Code)
	}
	headers["If-Match"] = item.ETag
	input.PostIDs = []int64{public.ID, secret.ID}
	encoded, _ = json.Marshal(input)
	updated := serveContentAPI(handler, cookie, http.MethodPut, itemURL, string(encoded), headers)
	if updated.Code != 200 {
		t.Fatal(updated.Body.String())
	}
	if r := serveContentAPI(handler, cookie, http.MethodPut, itemURL, body, headers); r.Code != 412 {
		t.Fatal("stale write accepted", r.Code)
	}
	for _, path := range []string{"/series/", "/series/reading-sequence/", "/api/v1/series", "/api/v1/series/reading-sequence", "/posts/series-public-chapter"} {
		r := serveContentAPI(handler, nil, http.MethodGet, "https://example.test"+path, "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), secret.Title) || strings.Contains(r.Body.String(), secret.Slug) || strings.Contains(r.Body.String(), "<script>unsafe</script>") {
			t.Fatal(path, r.Code, r.Body.String())
		}
		if strings.Contains(path, "reading-sequence") && !strings.Contains(r.Body.String(), public.Title) {
			t.Fatal("public chapter missing", path)
		}
		if strings.HasPrefix(path, "/series/") && r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("series response cached", path)
		}
	}
	for _, path := range []string{"/admin/series/", "/admin/series/?edit=new", fmt.Sprintf("/admin/series/?edit=%d", item.ID)} {
		if r := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+path, "", nil); r.Code != 200 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	current, _ := s.repository.GetSeries(t.Context(), item.ID)
	current.Status = content.StatusDraft
	if _, err = s.repository.SaveSeries(t.Context(), item.ID, content.Mutation{ExpectedVersion: current.Version}, current.SeriesInput); err != nil {
		t.Fatal(err)
	}
	if r := serveContentAPI(handler, nil, http.MethodGet, "https://example.test/series/reading-sequence/", "", nil); r.Code != 404 {
		t.Fatal("draft exposed", r.Code)
	}
	for _, bad := range []string{`{"kind":"topic","kind":"series"}`, strings.Replace(body, `"post_ids":`, `"unknown":1,"post_ids":`, 1), strings.Replace(body, fmt.Sprintf(`[%d,%d]`, secret.ID, public.ID), `null`, 1)} {
		if r := serveContentAPI(handler, cookie, http.MethodPost, base, bad, headers); r.Code < 400 {
			t.Fatal("invalid payload accepted", bad)
		}
	}
}
