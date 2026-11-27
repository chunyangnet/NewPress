package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func TestSEOWorkspaceStructure(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, path := range []string{"/admin/seo/", "/admin/seo/?edit=new"} {
		response := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("SEO workspace status = %d", response.Code)
		}
		body := response.Body.String()
		for _, marker := range []string{`data-seo-workspace`, `data-seo-task="redirects"`, `data-seo-task="link-check"`, `id="redirects" data-seo-panel`, `id="link-check" data-seo-panel`, `seo-policy-strip`} {
			if !strings.Contains(body, marker) {
				t.Fatalf("missing workspace marker %s", marker)
			}
		}
		if strings.Contains(path, "edit=new") {
			for _, marker := range []string{`data-seo-editing="true"`, `name="csrf_token"`, `name="version"`, `data-seo-target-picker`, `data-admin-native-save`} {
				if !strings.Contains(body, marker) {
					t.Fatalf("missing editor contract %s", marker)
				}
			}
		}
	}
}

func TestSEOPublicMetadataAndExactCSP(t *testing.T) {
	s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 100, 60)), media.UploadInput{Filename: "seo.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.media.ProcessImage(t.Context(), item.ID, item.Version, media.ImageInput{FocalX: 25, FocalY: 75})
	if err != nil {
		t.Fatal(err)
	}
	title := `A </script><script>alert("x")</script> & B`
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: title, Slug: "seo-public", Status: content.StatusPublish, CoverURL: item.PublicURL, Excerpt: "可分享的摘要", AuthorName: "作者"})
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`<script type="application/ld\+json">(.*?)</script>`)
	for _, path := range []string{"/", "/posts/seo-public"} {
		r := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
		match := pattern.FindSubmatch(r.Body.Bytes())
		if r.Code != 200 || len(match) != 2 {
			t.Fatal(path, r.Code, "missing JSON-LD")
		}
		var schema map[string]any
		if err = json.Unmarshal(match[1], &schema); err != nil {
			t.Fatal(err, string(match[1]))
		}
		digest := sha256.Sum256(match[1])
		policy := r.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") || strings.Contains(policy, "unsafe-inline") {
			t.Fatal("CSP did not cover exact rendered bytes", policy)
		}
		if strings.Contains(string(match[1]), "</script>") {
			t.Fatal("script escape failed")
		}
		if path != "/" {
			graph := schema["@graph"].([]any)
			article := graph[0].(map[string]any)
			if article["@type"] != "BlogPosting" || article["headline"] != title || len(graph) != 2 {
				t.Fatal(schema)
			}
			body := r.Body.String()
			if !strings.Contains(body, `property="og:type" content="article"`) || !strings.Contains(body, `format=native&amp;variant=card-large`) || !strings.Contains(body, `property="article:published_time"`) {
				t.Fatal("article/share metadata missing")
			}
		}
	}
	preview := s.editorSEOPreview(inputFromPost(p))
	if !strings.Contains(preview.Image, "variant=card-large") || preview.Title == "" {
		t.Fatal(preview)
	}
	for _, path := range []string{fmt.Sprintf("/admin/content/%d/edit/", p.ID), "/admin/seo/", "/missing-seo-page"} {
		r := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if pattern.Match(r.Body.Bytes()) {
			t.Fatal("private/error page schema", path)
		}
		if strings.Contains(path, "edit/") && !strings.Contains(r.Body.String(), "data-seo-preview") {
			t.Fatal("editor preview missing")
		}
	}
	settings := s.siteSettings.Snapshot()
	settings.Settings.SEO.SearchEngineVisible = false
	if _, _, err = s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(handler, nil, "GET", "https://example.test/posts/seo-public", "", nil)
	if pattern.Match(r.Body.Bytes()) || !strings.Contains(r.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("disabled indexing still emits schema")
	}
	if _, err = s.repository.ChangeStatus(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, nil, "GET", "https://example.test/posts/seo-public", "", nil)
	if r.Code != 404 || strings.Contains(r.Body.String(), "可分享的摘要") {
		t.Fatal("private metadata exposed")
	}
}

func TestSEORedirectHTTPPermissionsPreconditionsAndPrivacy(t *testing.T) {
	s, handler, users, _, cookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "seo-reader", "seo-reader@example.test", "读者", "subscriber")
	authorUser, authorCookie, author := createAndLoginTestUser(t, users, owner, "seo-author", "seo-author@example.test", "作者", "author")
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "Destination", Slug: "seo-destination", Status: content.StatusPublish, AuthorID: authorUser.ID})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"path":"/2020/legacy.html","post_id":%d}`, p.ID)
	base := "https://example.test/api/v1/admin/redirects"
	headers := authenticatedAPIHeaders(owner)
	for _, c := range []*http.Cookie{readerCookie, authorCookie} {
		if r := serveContentAPI(handler, c, "GET", base, "", nil); r.Code != 403 {
			t.Fatal(r.Code)
		}
	}
	if r := serveContentAPI(handler, nil, "GET", base, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := serveContentAPI(handler, cookie, "POST", base, body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}); r.Code != 403 {
		t.Fatal("missing csrf", r.Code)
	}
	foreign := authenticatedAPIHeaders(owner)
	foreign["Origin"] = "https://other.test"
	if r := serveContentAPI(handler, cookie, "POST", base, body, foreign); r.Code != 403 {
		t.Fatal(r.Code)
	}
	for _, bad := range []string{`{"path":null,"post_id":1}`, `{"path":"/a","path":"/b","post_id":1}`, `{"path":"/a","post_id":null}`, `{"path":"/a","post_id":1,"extra":true}`, `{"path":"/a","post_id":1.5}`} {
		if r := serveContentAPI(handler, cookie, "POST", base, bad, headers); r.Code != 400 {
			t.Fatal(bad, r.Code, r.Body.String())
		}
	}
	created := serveContentAPI(handler, cookie, "POST", base, body, headers)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var rule content.Redirect
	if err = json.Unmarshal(created.Body.Bytes(), &rule); err != nil {
		t.Fatal(err)
	}
	itemURL := fmt.Sprintf("%s/%d", base, rule.ID)
	if r := serveContentAPI(handler, cookie, "PUT", itemURL, body, headers); r.Code != 428 {
		t.Fatal(r.Code)
	}
	headers["If-Match"] = created.Header().Get("ETag")
	newBody := fmt.Sprintf(`{"path":"/2020/new-legacy.html","post_id":%d}`, p.ID)
	updated := serveContentAPI(handler, cookie, "PUT", itemURL, newBody, headers)
	if updated.Code != 200 {
		t.Fatal(updated.Body.String())
	}
	if r := serveContentAPI(handler, cookie, "PUT", itemURL, body, headers); r.Code != 412 {
		t.Fatal(r.Code)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := serveContentAPI(handler, nil, method, "https://example.test/2020/new-legacy.html?secret=discarded", "", nil)
		if r.Code != 301 || r.Header().Get("Location") != "/posts/seo-destination" || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(method, r.Code, r.Header())
		}
	}
	// Author is allowed to rename owned content; the alias is created by the
	// content transaction, without granting access to the global rule registry.
	apiPost := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d", p.ID)
	current := serveContentAPI(handler, authorCookie, "GET", apiPost, "", nil)
	authorHeaders := authenticatedAPIHeaders(author)
	authorHeaders["If-Match"] = current.Header().Get("ETag")
	changed := serveContentAPI(handler, authorCookie, "PATCH", apiPost, `{"slug":"seo-renamed"}`, authorHeaders)
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body.String())
	}
	for _, path := range []string{"/posts/seo-destination", "/2020/new-legacy.html"} {
		r := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
		if r.Code != 301 || r.Header().Get("Location") != "/posts/seo-renamed" {
			t.Fatal(path, r.Code, r.Header())
		}
	}
	for _, path := range []string{"/posts/seo-destination/comments", "/api/v1/posts/seo-destination/comments"} {
		r := serveContentAPI(handler, nil, "POST", "https://example.test"+path, "", nil)
		if r.Code == 301 || r.Header().Get("Location") != "" {
			t.Fatal("write redirected", path)
		}
	}
	p, _ = s.repository.GetByID(t.Context(), p.ID)
	p, err = s.repository.ChangeStatus(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if r := serveContentAPI(handler, nil, "GET", "https://example.test/2020/new-legacy.html", "", nil); r.Code != 404 || r.Header().Get("Location") != "" {
		t.Fatal("private redirect exposed", r.Code)
	}
	form := url.Values{"csrf_token": {owner.CSRFToken}, "action": {"save"}, "id": {strconvInt(rule.ID)}, "version": {"1"}, "path": {"/kept-input/"}, "post_id": {strconvInt(p.ID)}}
	r := serveContentForm(handler, cookie, "https://example.test/admin/seo/", "https://example.test", form)
	if r.Code != 409 || !strings.Contains(r.Body.String(), "/kept-input/") {
		t.Fatal("stale form lost input", r.Code)
	}
	if r := serveContentAPI(handler, authorCookie, "GET", "https://example.test/admin/seo/", "", nil); r.Code != 403 {
		t.Fatal(r.Code)
	}
	headers["If-Match"] = updated.Header().Get("ETag")
	if r := serveContentAPI(handler, cookie, "DELETE", itemURL, "", headers); r.Code != 204 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func strconvInt(v int64) string { return fmt.Sprintf("%d", v) }

func TestSEOContentLinkReportBoundedScopedAndNoNetwork(t *testing.T) {
	s, handler, users, underlying, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, _ := createAndLoginTestUser(t, users, owner, "link-author", "link-author@example.test", "作者", "author")
	target, err := s.repository.Create(t.Context(), content.PostInput{Title: "Public", Slug: "link-public", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.repository.Create(t.Context(), content.PostInput{Title: "SECRET TARGET TITLE", Slug: "link-private", Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.repository.SaveRedirect(t.Context(), 0, content.Mutation{}, content.RedirectInput{Path: "/old-link/", PostID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	source := `<p><a href="/posts/link-public">public</a><a href="/posts/link-private">private</a><a href="/posts/missing">missing</a><a href="/old-link/">old</a><a href="http://127.0.0.1:9/internal">external</a><a href="#anchor">anchor</a><a href="/explore/?q=test">search</a><a href="/theme-assets/images/favicon.svg">asset</a><a href="/media/999999/missing.png">media</a><a href="/posts/link-public">duplicate</a></p>`
	// Historical/imported state may contain an unavailable media reference;
	// the normal application guard correctly rejects introducing it today.
	p, err := underlying.Create(t.Context(), content.PostInput{Title: "Link source", Slug: "link-source", ContentSource: source, AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d/links", p.ID)
	if r := serveContentAPI(handler, authorCookie, "GET", endpoint, "", nil); r.Code != 403 {
		t.Fatal("author read other's report", r.Code)
	}
	if r := serveContentAPI(handler, nil, "GET", endpoint, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := serveContentAPI(handler, cookie, "GET", endpoint, "", nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var report contentLinksReport
	if err = json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 9 || report.Counts["ok"] != 2 || report.Counts["redirect"] != 1 || report.Counts["broken"] != 3 || report.Counts["unchecked"] != 3 {
		t.Fatal(report)
	}
	if strings.Contains(r.Body.String(), "SECRET TARGET TITLE") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("report privacy")
	}
	var many strings.Builder
	for i := range 250 {
		fmt.Fprintf(&many, `<a href="/missing-%d">link</a>`, i)
	}
	items, truncated := extractContentLinks(content.Post{ContentHTML: many.String()})
	if len(items) != 200 || !truncated {
		t.Fatal(len(items), truncated)
	}
	// Future content remains unavailable even when it has a publication timestamp.
	future, err := s.repository.Create(t.Context(), content.PostInput{Title: "Future", Slug: "future-links", Status: content.StatusFuture, PublishedAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.test/posts/source")
	result, err := s.checkContentLink(t.Context(), base, contentLinkResult{URL: content.PublicPath(future)})
	if err != nil || result.Status != "broken" {
		t.Fatal(result, err)
	}
}
