package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestContentAutosaveRecoveryPreviewInvalidationAndCanonicalClear(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: "editorial-safety-net", Title: "正式标题",
		Excerpt: "正式摘要", ContentSource: "<p>正式正文</p>", AuthorName: principal.Label(), Actor: principal.Username,
		CategoryIDs: []int64{1}, CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionsBefore, _ := repository.ListRevisions(t.Context(), post.ID, 20)
	tagsBefore, _ := repository.ListTerms(t.Context(), content.TermQuery{Taxonomy: content.TaxonomyTag, Limit: 500})
	target := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/autosave/"
	form := autosaveTestForm(principal.CSRFToken, post.Version, "恢复点 A", "autosave-a", `<h2>安全正文</h2><pre><code>preview()</code></pre><script>window.__autosaveXSS=true</script>`)

	response := serveContentForm(handler, cookie, target, "https://example.test", form)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("autosave = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var saved autosaveAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || !saved.Available || saved.PreviewURL == "" || saved.RecoveryURL == "" {
		t.Fatalf("autosave response=%#v err=%v body=%s", saved, err, response.Body.String())
	}
	canonical, _ := repository.GetByID(t.Context(), post.ID)
	revisionsAfter, _ := repository.ListRevisions(t.Context(), post.ID, 20)
	tagsAfter, _ := repository.ListTerms(t.Context(), content.TermQuery{Taxonomy: content.TaxonomyTag, Limit: 500})
	if canonical.Title != "正式标题" || canonical.Version != post.Version || len(revisionsAfter) != len(revisionsBefore) || tagsAfter.Total != tagsBefore.Total {
		t.Fatalf("autosave caused domain side effects: post=%#v revisions=%d/%d tags=%d/%d", canonical, len(revisionsAfter), len(revisionsBefore), tagsAfter.Total, tagsBefore.Total)
	}
	ownerKey, _ := autosaveOwnerKey(principal)
	stored, err := repository.GetAutosave(t.Context(), post.ID, ownerKey)
	if err != nil || stored.Snapshot.Title != "恢复点 A" || strings.Contains(stored.Snapshot.ContentHTML, "script") || len(stored.Snapshot.Tags) != 1 || stored.Snapshot.Tags[0].ID != 0 {
		t.Fatalf("stored autosave=%#v err=%v", stored, err)
	}

	preview := serveCookieRequest(handler, http.MethodGet, "https://example.test"+saved.PreviewURL, cookie, nil)
	previewBody := preview.Body.String()
	if !strings.Contains(previewBody, "npw-skin-chunnel") {
		t.Fatal("preview fixture must include a component skin alongside its preview body classes")
	}
	if !strings.Contains(previewBody, "chunnel-reader.css") || !strings.Contains(previewBody, "chunnel-reader.js") {
		t.Fatal("draft preview must retain the article reading controls and styles")
	}
	if preview.Code != http.StatusOK || !strings.Contains(previewBody, "安全草稿预览") || !strings.Contains(previewBody, "恢复点 A") ||
		!strings.Contains(previewBody, "安全正文") || strings.Contains(previewBody, "window.__autosaveXSS") || strings.Contains(previewBody, `<script>window.__autosaveXSS`) ||
		strings.Contains(previewBody, `rel="canonical"`) || strings.Contains(previewBody, `property="og:`) || strings.Contains(previewBody, `action="/posts/`) {
		t.Fatalf("preview = %d %s", preview.Code, previewBody)
	}
	if preview.Header().Get("Cache-Control") != "no-store" || preview.Header().Get("X-Robots-Tag") != "noindex, nofollow, noarchive" || preview.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("preview headers = %v", preview.Header())
	}

	editor := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/", cookie, nil)
	if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), "发现尚未写入正式版本的恢复点") || !strings.Contains(editor.Body.String(), `data-editor-preview`) {
		t.Fatalf("editor recovery card = %d %s", editor.Code, editor.Body.String())
	}
	recovery := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/?recovery=autosave", cookie, nil)
	if recovery.Code != http.StatusOK || !strings.Contains(recovery.Body.String(), "恢复点 A") || !strings.Contains(recovery.Body.String(), `data-editor-force-dirty="true"`) || !strings.Contains(recovery.Body.String(), "尚未改变") {
		t.Fatalf("loaded recovery = %d %s", recovery.Code, recovery.Body.String())
	}

	second := autosaveTestForm(principal.CSRFToken, post.Version, "恢复点 B", "autosave-b", "<p>第二个恢复点</p>")
	secondResponse := serveContentForm(handler, cookie, target, "https://example.test", second)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second autosave = %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	stalePreview := serveCookieRequest(handler, http.MethodGet, "https://example.test"+saved.PreviewURL, cookie, nil)
	if stalePreview.Code != http.StatusGone || !strings.Contains(stalePreview.Body.String(), "恢复点已更新") || strings.Contains(stalePreview.Body.String(), "安全草稿预览") {
		t.Fatalf("stale preview = %d %s", stalePreview.Code, stalePreview.Body.String())
	}

	discard := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {strconv.FormatInt(post.Version, 10)}, "action": {"discard"},
	}
	discarded := serveContentForm(handler, cookie, target, "https://example.test", discard)
	if discarded.Code != http.StatusSeeOther || !strings.Contains(discarded.Header().Get("Location"), "notice=autosave-discarded") {
		t.Fatalf("discard = %d %q %s", discarded.Code, discarded.Header().Get("Location"), discarded.Body.String())
	}
	if _, err := repository.GetAutosave(t.Context(), post.ID, ownerKey); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatalf("discarded autosave still exists: %v", err)
	}

	third := autosaveTestForm(principal.CSRFToken, post.Version, "正式保存后的标题", "canonical-after-autosave", "<p>正式保存正文</p>")
	if response := serveContentForm(handler, cookie, target, "https://example.test", third); response.Code != http.StatusOK {
		t.Fatalf("third autosave = %d %s", response.Code, response.Body.String())
	}
	third.Set("action", "save")
	savedCanonical := serveContentForm(handler, cookie, "https://example.test/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/", "https://example.test", third)
	if savedCanonical.Code != http.StatusSeeOther {
		t.Fatalf("canonical save = %d %s", savedCanonical.Code, savedCanonical.Body.String())
	}
	if _, err := repository.GetAutosave(t.Context(), post.ID, ownerKey); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatalf("canonical save did not clear autosave: %v", err)
	}
	canonical, _ = repository.GetByID(t.Context(), post.ID)
	if canonical.Title != "正式保存后的标题" || canonical.Version != 2 {
		t.Fatalf("canonical after save = %#v", canonical)
	}
}

func TestContentAutosaveRejectsCrossSiteCSRFShapeVersionAndAnonymousPreview(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePage, Status: content.StatusDraft, Slug: "preview-security", Title: "Preview Security",
		ContentSource: "Body", AuthorName: principal.Label(), Actor: principal.Username, CommentStatus: content.CommentClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/autosave/"
	base := autosaveTestForm(principal.CSRFToken, post.Version, "安全边界", "preview-security", "<p>Body</p>")
	base.Set("type", content.TypePage)
	base.Set("category", "")

	if response := serveContentForm(handler, cookie, target, "https://attacker.test", base); response.Code != http.StatusForbidden {
		t.Fatalf("cross-site autosave status = %d", response.Code)
	}
	badCSRF := cloneValues(base)
	badCSRF.Set("csrf_token", "bad")
	if response := serveContentForm(handler, cookie, target, "https://example.test", badCSRF); response.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF autosave status = %d", response.Code)
	}
	unknown := cloneValues(base)
	unknown.Set("is_admin", "true")
	if response := serveContentForm(handler, cookie, target, "https://example.test", unknown); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown autosave field status = %d", response.Code)
	}
	duplicate := cloneValues(base)
	duplicate["title"] = []string{"one", "two"}
	if response := serveContentForm(handler, cookie, target, "https://example.test", duplicate); response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate autosave field status = %d", response.Code)
	}
	stale := cloneValues(base)
	stale.Set("version", "99")
	if response := serveContentForm(handler, cookie, target, "https://example.test", stale); response.Code != http.StatusConflict {
		t.Fatalf("stale autosave status = %d body=%s", response.Code, response.Body.String())
	}
	invalid := cloneValues(base)
	invalid.Set("cover_url", "https://evil.test/a.jpg")
	if response := serveContentForm(handler, cookie, target, "https://example.test", invalid); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "cover_url") {
		t.Fatalf("invalid autosave = %d %s", response.Code, response.Body.String())
	}

	valid := serveContentForm(handler, cookie, target, "https://example.test", base)
	var payload autosaveAPIResponse
	if valid.Code != http.StatusOK || json.Unmarshal(valid.Body.Bytes(), &payload) != nil {
		t.Fatalf("valid autosave = %d %s", valid.Code, valid.Body.String())
	}
	staleDiscard := url.Values{
		"csrf_token": {principal.CSRFToken}, "version": {"99"}, "action": {"discard"},
	}
	if response := serveContentForm(handler, cookie, target, "https://example.test", staleDiscard); response.Code != http.StatusConflict {
		t.Fatalf("stale discard status = %d body=%s", response.Code, response.Body.String())
	}
	ownerKey, _ := autosaveOwnerKey(principal)
	if _, err := repository.GetAutosave(t.Context(), post.ID, ownerKey); err != nil {
		t.Fatalf("stale discard removed current recovery point: %v", err)
	}
	anonymous := serveCookieRequest(handler, http.MethodGet, "https://example.test"+payload.PreviewURL, nil, nil)
	if anonymous.Code != http.StatusSeeOther || anonymous.Header().Get("Referrer-Policy") != "no-referrer" || !strings.HasPrefix(anonymous.Header().Get("Location"), "/admin/login/?next=") {
		t.Fatalf("anonymous preview = %d %q", anonymous.Code, anonymous.Header().Get("Location"))
	}
	parsedPreviewURL, err := url.Parse(payload.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	previewQuery := parsedPreviewURL.Query()
	tokenParts := strings.SplitN(previewQuery.Get("token"), ".", 2)
	if len(tokenParts) != 2 || tokenParts[1] == "" {
		t.Fatalf("unexpected preview token: %q", previewQuery.Get("token"))
	}
	replacement := "A"
	if strings.HasPrefix(tokenParts[1], replacement) {
		replacement = "B"
	}
	tokenParts[1] = replacement + tokenParts[1][1:]
	previewQuery.Set("token", strings.Join(tokenParts, "."))
	parsedPreviewURL.RawQuery = previewQuery.Encode()
	tamperedURL := parsedPreviewURL.String()
	tampered := serveCookieRequest(handler, http.MethodGet, "https://example.test"+tamperedURL, cookie, nil)
	if tampered.Code != http.StatusForbidden {
		t.Fatalf("tampered preview = %d %s", tampered.Code, tampered.Body.String())
	}
	status := serveCookieRequest(handler, http.MethodGet, target, cookie, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"available":true`) {
		t.Fatalf("autosave status = %d %s", status.Code, status.Body.String())
	}
	badQuery := serveCookieRequest(handler, http.MethodGet, target+"?include=snapshot", cookie, nil)
	if badQuery.Code != http.StatusBadRequest {
		t.Fatalf("autosave status query = %d", badQuery.Code)
	}
}

func TestContentAutosaveHonorsAuthorObjectScopeAndBindsPreviewToIdentity(t *testing.T) {
	_, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	authorOne, authorOneCookie, authorOnePrincipal := createAndLoginTestUser(t, manager, owner, "autosave-author-one", "autosave-author-one@example.test", "恢复作者甲", "author")
	authorTwo, authorTwoCookie, authorTwoPrincipal := createAndLoginTestUser(t, manager, owner, "autosave-author-two", "autosave-author-two@example.test", "恢复作者乙", "author")
	_, editorCookie, editorPrincipal := createAndLoginTestUser(t, manager, owner, "autosave-editor", "autosave-editor@example.test", "恢复值班编辑", "editor")
	_, subscriberCookie, _ := createAndLoginTestUser(t, manager, owner, "autosave-reader", "autosave-reader@example.test", "恢复只读订阅者", "subscriber")

	one := createOwnedTestPost(t, repository, authorOne.ID, authorOne.DisplayName, "autosave-owner-one", "作者甲正文")
	two := createOwnedTestPost(t, repository, authorTwo.ID, authorTwo.DisplayName, "autosave-owner-two", "作者乙正文")
	oneTarget := "https://example.test/admin/content/" + strconv.FormatInt(one.ID, 10) + "/autosave/"
	twoTarget := "https://example.test/admin/content/" + strconv.FormatInt(two.ID, 10) + "/autosave/"

	oneForm := autosaveTestForm(authorOnePrincipal.CSRFToken, one.Version, "作者甲恢复点", one.Slug, "<p>作者甲未保存正文</p>")
	oneSaved := serveContentForm(handler, authorOneCookie, oneTarget, "https://example.test", oneForm)
	var onePayload autosaveAPIResponse
	if oneSaved.Code != http.StatusOK || json.Unmarshal(oneSaved.Body.Bytes(), &onePayload) != nil || onePayload.PreviewURL == "" {
		t.Fatalf("author own autosave = %d %s", oneSaved.Code, oneSaved.Body.String())
	}
	if response := serveContentForm(handler, authorOneCookie, twoTarget, "https://example.test", autosaveTestForm(authorOnePrincipal.CSRFToken, two.Version, "越权恢复点", two.Slug, "越权")); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "content_object_forbidden") {
		t.Fatalf("author autosave other content = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, twoTarget, authorOneCookie, nil); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "content_object_forbidden") {
		t.Fatalf("author read other autosave status = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+onePayload.PreviewURL, authorOneCookie, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "作者甲未保存正文") {
		t.Fatalf("author preview own autosave = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+onePayload.PreviewURL, authorTwoCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("other author opened preview token = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+onePayload.PreviewURL, editorCookie, nil); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "预览链接无效") {
		t.Fatalf("editor reused another identity preview token = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, oneTarget, subscriberCookie, nil); response.Code != http.StatusForbidden {
		t.Fatalf("subscriber autosave status = %d %s", response.Code, response.Body.String())
	}

	editorForm := autosaveTestForm(editorPrincipal.CSRFToken, two.Version, "编辑恢复点", two.Slug, "<p>编辑代管正文</p>")
	editorSaved := serveContentForm(handler, editorCookie, twoTarget, "https://example.test", editorForm)
	var editorPayload autosaveAPIResponse
	if editorSaved.Code != http.StatusOK || json.Unmarshal(editorSaved.Body.Bytes(), &editorPayload) != nil || editorPayload.PreviewURL == "" {
		t.Fatalf("editor autosave another author's content = %d %s", editorSaved.Code, editorSaved.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+editorPayload.PreviewURL, editorCookie, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "编辑代管正文") {
		t.Fatalf("editor preview managed autosave = %d %s", response.Code, response.Body.String())
	}
	if response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+editorPayload.PreviewURL, authorTwoCookie, nil); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "预览链接无效") {
		t.Fatalf("canonical author reused editor preview token = %d %s", response.Code, response.Body.String())
	}

	ownerOneKey, _ := autosaveOwnerKey(authorOnePrincipal)
	editorTwoKey, _ := autosaveOwnerKey(editorPrincipal)
	if _, err := repository.GetAutosave(t.Context(), one.ID, ownerOneKey); err != nil {
		t.Fatalf("author recovery point disappeared: %v", err)
	}
	if _, err := repository.GetAutosave(t.Context(), two.ID, editorTwoKey); err != nil {
		t.Fatalf("editor recovery point disappeared: %v", err)
	}
	ownerTwoKey, _ := autosaveOwnerKey(authorTwoPrincipal)
	if _, err := repository.GetAutosave(t.Context(), two.ID, ownerTwoKey); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatalf("editor recovery point leaked into canonical author scope: %v", err)
	}
}

func autosaveTestForm(csrf string, version int64, title, slug, source string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "version": {strconv.FormatInt(version, 10)}, "action": {"autosave"},
		"type": {content.TypePost}, "status": {content.StatusDraft}, "slug": {slug}, "title": {title},
		"excerpt": {""}, "content_source": {source}, "cover_url": {""}, "category_ids": {"1"},
		"tags": {"transient-preview-tag"}, "comment_status": {content.CommentOpen}, "published_at": {""},
		"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
	}
}
