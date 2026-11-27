package httpserver

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"newpress.local/newpress/internal/content"
)

func TestAdminRevisionCompareRendersEscapedBoundedFieldAndBodyDiff(t *testing.T) {
	_, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: "revision-lab", Title: "修订实验室 v1",
		Excerpt: "第一版摘要", ContentSource: `<p>共同开头</p><script>alert(1)</script><p>旧结尾</p>`,
		Category: "项目日志", AuthorName: "编辑", Actor: "editor", CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	post, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "editor"}, content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: "revision-lab", Title: "修订实验室 v2",
		Excerpt: "第二版摘要", ContentSource: "<p>共同开头</p>\n<p>新增段落</p>\n<p>新结尾</p>",
		Category: "项目日志", AuthorName: "编辑", CommentStatus: content.CommentClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions = %#v, err=%v", revisions, err)
	}
	var first content.Revision
	for _, revision := range revisions {
		if revision.Version == 1 {
			first = revision
		}
	}
	if first.ID == 0 {
		t.Fatal("version 1 revision not found")
	}
	target := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/?from=" + strconv.FormatInt(first.ID, 10) + "&to=current"
	response := serveCookieRequest(handler, http.MethodGet, target, cookie, nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "<h1>比较历史版本</h1>") || !strings.Contains(body, "结构化字段摘要") ||
		!strings.Contains(body, "修订实验室 v1") || !strings.Contains(body, "修订实验室 v2") ||
		!strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(body, `<script>alert(1)</script>`) ||
		!strings.Contains(body, "新增段落") || !strings.Contains(body, "正文差异") {
		t.Fatalf("revision comparison = %d %s", response.Code, body)
	}

	edit := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/"+strconv.FormatInt(post.ID, 10)+"/edit/", cookie, nil)
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), "比较") ||
		!strings.Contains(edit.Body.String(), "/revisions/compare/?from=") {
		t.Fatalf("editor comparison entry = %d %s", edit.Code, edit.Body.String())
	}
}

func TestAdminRevisionCompareStrictQueryBindingAndOwnership(t *testing.T) {
	_, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	post := createOwnedTestPost(t, repository, 0, "编辑", "compare-primary", "主内容")
	post, err := repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "editor"}, content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: post.Slug, Title: "主内容二版",
		ContentSource: "二版", Category: "未分类", AuthorName: "编辑", CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisions, _ := repository.ListRevisions(t.Context(), post.ID, 20)
	fromID := revisions[len(revisions)-1].ID
	foreign := createOwnedTestPost(t, repository, 0, "编辑", "compare-foreign", "其他内容")
	foreignRevisions, _ := repository.ListRevisions(t.Context(), foreign.ID, 20)
	basePath := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/"

	tests := []struct {
		query  string
		status int
	}{
		{query: "", status: http.StatusBadRequest},
		{query: "?from=" + strconv.FormatInt(fromID, 10), status: http.StatusBadRequest},
		{query: "?from=0&to=current", status: http.StatusBadRequest},
		{query: "?from=" + strconv.FormatInt(fromID, 10) + "&to=wat", status: http.StatusBadRequest},
		{query: "?from=" + strconv.FormatInt(fromID, 10) + "&from=" + strconv.FormatInt(fromID, 10) + "&to=current", status: http.StatusBadRequest},
		{query: "?from=" + strconv.FormatInt(fromID, 10) + "&to=current&status=publish", status: http.StatusBadRequest},
		{query: "?from=" + strconv.FormatInt(foreignRevisions[0].ID, 10) + "&to=current", status: http.StatusNotFound},
	}
	for _, test := range tests {
		response := serveCookieRequest(handler, http.MethodGet, basePath+test.query, cookie, nil)
		if response.Code != test.status {
			t.Fatalf("query %q = %d %s", test.query, response.Code, response.Body.String())
		}
	}

	_, persistentHandler, manager, persistentRepository, _, owner := newPersistentAdminTestServer(t)
	authorOne, authorOneCookie, _ := createAndLoginTestUser(t, manager, owner, "compare-author-one", "compare-one@example.test", "作者一", "author")
	authorTwo, _, _ := createAndLoginTestUser(t, manager, owner, "compare-author-two", "compare-two@example.test", "作者二", "author")
	one := createOwnedTestPost(t, persistentRepository, authorOne.ID, "作者一", "author-one-revision", "作者一内容")
	two := createOwnedTestPost(t, persistentRepository, authorTwo.ID, "作者二", "author-two-revision", "作者二内容")
	oneRevisions, _ := persistentRepository.ListRevisions(t.Context(), one.ID, 20)
	twoRevisions, _ := persistentRepository.ListRevisions(t.Context(), two.ID, 20)
	allowed := serveCookieRequest(persistentHandler, http.MethodGet,
		"https://example.test/admin/content/"+strconv.FormatInt(one.ID, 10)+"/revisions/compare/?from="+strconv.FormatInt(oneRevisions[0].ID, 10)+"&to=current", authorOneCookie, nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("owned comparison = %d %s", allowed.Code, allowed.Body.String())
	}
	forbidden := serveCookieRequest(persistentHandler, http.MethodGet,
		"https://example.test/admin/content/"+strconv.FormatInt(two.ID, 10)+"/revisions/compare/?from="+strconv.FormatInt(twoRevisions[0].ID, 10)+"&to=current", authorOneCookie, nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("foreign comparison = %d %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestRevisionDiffBudgetIsDeterministicForMaximumContent(t *testing.T) {
	from := strings.Repeat("甲", content.MaxContentBytes/3) + "旧结尾"
	to := strings.Repeat("甲", content.MaxContentBytes/3) + "新结尾"
	rows, added, removed, changed, truncated := buildRevisionBodyDiff(from, to)
	if !truncated || len(rows) > revisionDiffMaxUnits*2 || added+removed+changed < 0 {
		t.Fatalf("bounded diff = rows:%d +%d -%d ~%d truncated:%v", len(rows), added, removed, changed, truncated)
	}
	fromUnits, fromTruncated := revisionDiffUnits(from)
	toUnits, toTruncated := revisionDiffUnits(to)
	if !fromTruncated || !toTruncated || len(fromUnits) > revisionDiffMaxUnits || len(toUnits) > revisionDiffMaxUnits {
		t.Fatalf("unit budgets = %d/%d truncated=%v/%v", len(fromUnits), len(toUnits), fromTruncated, toTruncated)
	}
	for _, unit := range append(fromUnits, toUnits...) {
		if utf8.RuneCountInString(unit) > revisionDiffMaxUnitRunes {
			t.Fatalf("unit exceeded rune budget: %d", utf8.RuneCountInString(unit))
		}
	}

	fromID, toValue, err := parseRevisionCompareQuery(url.Values{"from": {"7"}, "to": {"current"}}.Encode())
	if err != nil || fromID != 7 || toValue != "current" {
		t.Fatalf("valid compare query = %d %q %v", fromID, toValue, err)
	}
}
