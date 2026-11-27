package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestAdminContentPermanentDelete(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{Title: "永久删除", Slug: "permanent-delete"})
	if err != nil {
		t.Fatal(err)
	}
	response := serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", "https://example.test", bulkContentValues(principal.CSRFToken, "delete", post))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("active deletion: %d", response.Code)
	}
	post, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	list := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/?status=trash", cookie, nil)
	if !strings.Contains(list.Body.String(), `<option value="delete">永久删除</option>`) {
		t.Fatal("missing deletion action")
	}
	response = serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", "https://example.test", bulkContentValues(principal.CSRFToken, "delete", post))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "bulk-deleted") {
		t.Fatalf("deletion: %d %s", response.Code, response.Body.String())
	}
	if _, err := repository.GetByID(t.Context(), post.ID); !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("post remains: %v", err)
	}
}

func TestAdminContentBulkStatusIsAtomicAndClearsRecoveryPoints(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	create := func(slug, title string) content.Post {
		post, err := repository.Create(t.Context(), content.PostInput{
			Type: content.TypePost, Status: content.StatusDraft, Slug: slug, Title: title,
			ContentSource: "正文", Category: "未分类", CommentStatus: content.CommentOpen,
			AuthorName: "编辑", Actor: "editor",
		})
		if err != nil {
			t.Fatal(err)
		}
		return post
	}
	first := create("bulk-admin-first", "批量管理一")
	second := create("bulk-admin-second", "批量管理二")
	for index, post := range []content.Post{first, second} {
		snapshot := post
		snapshot.Title += "恢复点"
		if _, err := repository.SaveAutosave(t.Context(), content.Autosave{
			PostID: post.ID, OwnerKey: "user:" + strconv.Itoa(index+1), SourceVersion: post.Version,
			Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	list := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/?type=post&status=draft&q=%E6%89%B9%E9%87%8F", cookie, nil)
	body := list.Body.String()
	if list.Code != http.StatusOK {
		t.Fatalf("bulk content list = %d", list.Code)
	}
	for _, marker := range []string{
		`action="/admin/content/bulk/" method="post" data-content-bulk`,
		`type="checkbox" name="selection" value="` + strconv.FormatInt(first.ID, 10) + `:1" data-content-select`,
		`type="checkbox" name="selection" value="` + strconv.FormatInt(second.ID, 10) + `:1" data-content-select`,
		`data-content-select-all aria-label="选择当前页全部内容"`,
		`class="admin-content-select-all-copy"`,
		`class="admin-content-title-cell"`,
		`data-label="分类与标签"`,
		`<select name="action" required data-content-bulk-action>`,
		`<button type="submit" data-content-bulk-submit>`,
		`id="bulk-content-status" role="status" aria-live="polite" data-content-bulk-live`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("bulk content list missing %q", marker)
		}
	}

	stale := bulkContentValues(principal.CSRFToken, "publish", first, second)
	stale["selection"][1] = strconv.FormatInt(second.ID, 10) + ":2"
	staleResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", "https://example.test", stale)
	if staleResponse.Code != http.StatusSeeOther || !strings.Contains(staleResponse.Header().Get("Location"), "notice=bulk-conflict") {
		t.Fatalf("stale bulk = %d %q %s", staleResponse.Code, staleResponse.Header().Get("Location"), staleResponse.Body.String())
	}
	for _, original := range []content.Post{first, second} {
		current, _ := repository.GetByID(t.Context(), original.ID)
		if current.Version != original.Version || current.Status != original.Status {
			t.Fatalf("stale HTTP bulk partially mutated: %#v", current)
		}
	}
	if _, err := repository.GetAutosave(t.Context(), first.ID, "user:1"); err != nil {
		t.Fatalf("stale HTTP bulk cleared recovery point: %v", err)
	}

	valid := bulkContentValues(principal.CSRFToken, "pending", second, first)
	response := serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", "https://example.test", valid)
	location := response.Header().Get("Location")
	if response.Code != http.StatusSeeOther || !strings.Contains(location, "notice=bulk-pending") ||
		!strings.Contains(location, "q=%E6%89%B9%E9%87%8F") || !strings.Contains(location, "status=draft") || !strings.Contains(location, "type=post") {
		t.Fatalf("valid bulk = %d %q %s", response.Code, location, response.Body.String())
	}
	for index, original := range []content.Post{first, second} {
		current, err := repository.GetByID(t.Context(), original.ID)
		if err != nil || current.Status != content.StatusPending || current.Version != 2 {
			t.Fatalf("bulk current = %#v, err=%v", current, err)
		}
		if _, err := repository.GetAutosave(t.Context(), original.ID, "user:"+strconv.Itoa(index+1)); !errors.Is(err, content.ErrAutosaveNotFound) {
			t.Fatalf("bulk did not clear recovery point: %v", err)
		}
		revisions, err := repository.ListRevisions(t.Context(), original.ID, 20)
		if err != nil || len(revisions) != 2 || revisions[0].Snapshot.Status != content.StatusPending || revisions[0].Actor != principal.Username {
			t.Fatalf("bulk HTTP revisions = %#v, err=%v", revisions, err)
		}
	}
}

func TestAdminContentBulkProtocolBoundaries(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusDraft, Slug: "bulk-boundaries", Title: "批量边界",
		ContentSource: "正文", Category: "未分类", CommentStatus: content.CommentOpen, AuthorName: "编辑", Actor: "editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := bulkContentValues(principal.CSRFToken, "trash", post)

	tests := []struct {
		name   string
		values url.Values
		origin string
		status int
	}{
		{name: "cross site", values: cloneValues(base), origin: "https://attacker.test", status: http.StatusForbidden},
		{name: "unknown field", values: func() url.Values { value := cloneValues(base); value.Set("is_admin", "1"); return value }(), origin: "https://example.test", status: http.StatusBadRequest},
		{name: "duplicate selection", values: func() url.Values {
			value := cloneValues(base)
			value["selection"] = append(value["selection"], value.Get("selection"))
			return value
		}(), origin: "https://example.test", status: http.StatusBadRequest},
		{name: "missing selection", values: func() url.Values { value := cloneValues(base); value.Del("selection"); return value }(), origin: "https://example.test", status: http.StatusBadRequest},
		{name: "unknown action", values: func() url.Values { value := cloneValues(base); value.Set("action", "execute"); return value }(), origin: "https://example.test", status: http.StatusBadRequest},
		{name: "bad csrf", values: func() url.Values { value := cloneValues(base); value.Set("csrf_token", "invalid"); return value }(), origin: "https://example.test", status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", test.origin, test.values)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}

	wrongType := httptest.NewRequest(http.MethodPost, "https://example.test/admin/content/bulk/", strings.NewReader(base.Encode()))
	wrongType.Header.Set("Content-Type", "text/plain")
	wrongType.Header.Set("Origin", "https://example.test")
	wrongType.Header.Set("Sec-Fetch-Site", "same-origin")
	wrongType.AddCookie(cookie)
	wrongTypeResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTypeResponse, wrongType)
	if wrongTypeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong bulk content type = %d", wrongTypeResponse.Code)
	}

	oversized := cloneValues(base)
	oversized.Set("return_q", strings.Repeat("x", adminContentBulkFormMaxBytes+1))
	oversizedResponse := serveContentForm(handler, cookie, "https://example.test/admin/content/bulk/", "https://example.test", oversized)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversizedResponse.Body.String(), "content_bulk_form_too_large") {
		t.Fatalf("oversized bulk = %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}
	current, _ := repository.GetByID(t.Context(), post.ID)
	if current.Version != post.Version || current.Status != post.Status {
		t.Fatalf("invalid protocol mutated content: %#v", current)
	}
}

func TestAdminContentBulkHonorsAuthorObjectScope(t *testing.T) {
	_, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	authorOne, authorCookie, authorPrincipal := createAndLoginTestUser(t, manager, owner, "bulk-author-one", "bulk-one@example.test", "批量作者甲", "author")
	authorTwo, _, _ := createAndLoginTestUser(t, manager, owner, "bulk-author-two", "bulk-two@example.test", "批量作者乙", "author")
	one := createOwnedTestPost(t, repository, authorOne.ID, authorOne.DisplayName, "bulk-owned-one", "作者甲批量内容")
	two := createOwnedTestPost(t, repository, authorTwo.ID, authorTwo.DisplayName, "bulk-owned-two", "作者乙批量内容")

	list := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/content/", authorCookie, nil)
	listBody := list.Body.String()
	if list.Code != http.StatusOK || !strings.Contains(listBody, `value="`+strconv.FormatInt(one.ID, 10)+`:1"`) ||
		strings.Contains(listBody, `value="`+strconv.FormatInt(two.ID, 10)+`:1"`) || strings.Contains(listBody, two.Title) {
		t.Fatalf("author bulk list scope = %d %s", list.Code, listBody)
	}

	foreign := bulkContentValues(authorPrincipal.CSRFToken, "trash", one, two)
	foreignResponse := serveContentForm(handler, authorCookie, "https://example.test/admin/content/bulk/", "https://example.test", foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("author foreign bulk = %d %s", foreignResponse.Code, foreignResponse.Body.String())
	}
	for _, original := range []content.Post{one, two} {
		current, _ := repository.GetByID(t.Context(), original.ID)
		if current.Version != original.Version || current.Status != original.Status {
			t.Fatalf("forbidden bulk partially mutated: %#v", current)
		}
	}

	owned := bulkContentValues(authorPrincipal.CSRFToken, "trash", one)
	ownedResponse := serveContentForm(handler, authorCookie, "https://example.test/admin/content/bulk/", "https://example.test", owned)
	if ownedResponse.Code != http.StatusSeeOther || !strings.Contains(ownedResponse.Header().Get("Location"), "bulk-trashed") {
		t.Fatalf("author owned bulk = %d %q %s", ownedResponse.Code, ownedResponse.Header().Get("Location"), ownedResponse.Body.String())
	}
	changedOne, _ := repository.GetByID(t.Context(), one.ID)
	unchangedTwo, _ := repository.GetByID(t.Context(), two.ID)
	if changedOne.Status != content.StatusTrash || changedOne.Version != 2 || unchangedTwo.Status != content.StatusDraft || unchangedTwo.Version != 1 {
		t.Fatalf("author scoped result one=%#v two=%#v", changedOne, unchangedTwo)
	}
}

func bulkContentValues(csrfToken, action string, posts ...content.Post) url.Values {
	values := url.Values{
		"csrf_token": {csrfToken}, "action": {action}, "return_type": {content.TypePost},
		"return_status": {content.StatusDraft}, "return_q": {"批量"}, "return_page": {"1"},
	}
	for _, post := range posts {
		values.Add("selection", strconv.FormatInt(post.ID, 10)+":"+strconv.FormatInt(post.Version, 10))
	}
	return values
}
