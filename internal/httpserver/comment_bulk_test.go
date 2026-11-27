package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/comment"
)

func TestAdminCommentBulkStatusIsAtomicVersionedAndFilterPreserving(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	create := func(name string) comment.Comment {
		item, err := comments.Create(t.Context(), comment.CreateInput{
			PostID: 1, AuthorName: name, AuthorEmail: strings.ToLower(name) + "@example.test",
			Status: comment.StatusPending, ContentSource: "原子评论审核 " + name,
		})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	first := create("BulkOne")
	second := create("BulkTwo")

	list := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/comments/?q=%E5%8E%9F%E5%AD%90", "", "", "198.51.100.30:4300")
	body := list.Body.String()
	if list.Code != http.StatusOK {
		t.Fatalf("bulk comment list = %d", list.Code)
	}
	for _, marker := range []string{
		`id="admin-comment-bulk-form" action="/admin/comments/bulk/" method="post" data-comment-bulk`,
		`form="admin-comment-bulk-form" type="checkbox" name="selection" value="` + strconv.FormatInt(first.ID, 10) + `:1" data-comment-select`,
		`form="admin-comment-bulk-form" type="checkbox" name="selection" value="` + strconv.FormatInt(second.ID, 10) + `:1" data-comment-select`,
		`data-comment-select-all aria-label="选择当前页全部评论"`,
		`<select name="action" required data-comment-bulk-action>`,
		`<button type="submit" data-comment-bulk-submit>`,
		`id="bulk-comment-status" role="status" aria-live="polite" aria-atomic="true" data-comment-bulk-live`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("bulk comment list missing %q", marker)
		}
	}

	stale := bulkCommentValues(principal.CSRFToken, comment.StatusApproved, first, second)
	stale["selection"][1] = strconv.FormatInt(second.ID, 10) + ":2"
	staleResponse := serveCommentBulkForm(handler, cookie, stale, "https://example.test")
	if staleResponse.Code != http.StatusSeeOther || !strings.Contains(staleResponse.Header().Get("Location"), "notice=bulk-conflict") {
		t.Fatalf("stale bulk = %d %q %s", staleResponse.Code, staleResponse.Header().Get("Location"), staleResponse.Body.String())
	}
	for _, original := range []comment.Comment{first, second} {
		current, err := comments.Get(t.Context(), original.ID)
		if err != nil || current.Status != original.Status || current.Version != original.Version || current.ModeratedBy != "" {
			t.Fatalf("stale HTTP bulk partially mutated %#v, err=%v", current, err)
		}
	}

	valid := bulkCommentValues(principal.CSRFToken, comment.StatusSpam, second, first)
	response := serveCommentBulkForm(handler, cookie, valid, "https://example.test")
	location := response.Header().Get("Location")
	if response.Code != http.StatusSeeOther || !strings.Contains(location, "notice=bulk-spam") ||
		!strings.Contains(location, "q=%E5%8E%9F%E5%AD%90") || !strings.Contains(location, "post=1") {
		t.Fatalf("valid bulk = %d %q %s", response.Code, location, response.Body.String())
	}
	for _, original := range []comment.Comment{first, second} {
		current, err := comments.Get(t.Context(), original.ID)
		if err != nil || current.Status != comment.StatusSpam || current.Version != 2 || current.ModeratedBy != principal.Username || current.ModeratedAt.IsZero() {
			t.Fatalf("bulk current = %#v, err=%v", current, err)
		}
	}
}

func TestAdminCommentBulkProtocolBoundaries(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	item, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "Boundary", AuthorEmail: "boundary@example.test",
		Status: comment.StatusPending, ContentSource: "批量协议边界",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := bulkCommentValues(principal.CSRFToken, comment.StatusTrash, item)

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
		{name: "single-only restore action", values: func() url.Values { value := cloneValues(base); value.Set("action", "restore"); return value }(), origin: "https://example.test", status: http.StatusBadRequest},
		{name: "bad csrf", values: func() url.Values { value := cloneValues(base); value.Set("csrf_token", "invalid"); return value }(), origin: "https://example.test", status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveCommentBulkForm(handler, cookie, test.values, test.origin)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}

	wrongType := httptest.NewRequest(http.MethodPost, "https://example.test/admin/comments/bulk/", strings.NewReader(base.Encode()))
	wrongType.Header.Set("Content-Type", "text/plain")
	wrongType.Header.Set("Origin", "https://example.test")
	wrongType.Header.Set("Sec-Fetch-Site", "same-origin")
	wrongType.AddCookie(cookie)
	wrongTypeResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTypeResponse, wrongType)
	if wrongTypeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong bulk comment type = %d", wrongTypeResponse.Code)
	}

	oversized := cloneValues(base)
	oversized.Set("return_q", strings.Repeat("x", adminCommentBulkFormMaxBytes+1))
	oversizedResponse := serveCommentBulkForm(handler, cookie, oversized, "https://example.test")
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversizedResponse.Body.String(), "comment_bulk_form_too_large") {
		t.Fatalf("oversized bulk = %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}
	current, _ := comments.Get(t.Context(), item.ID)
	if current.Status != item.Status || current.Version != item.Version {
		t.Fatalf("invalid bulk protocol mutated comment: %#v", current)
	}
}

func serveCommentBulkForm(handler http.Handler, cookie *http.Cookie, values url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/comments/bulk/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func bulkCommentValues(csrfToken, action string, comments ...comment.Comment) url.Values {
	values := url.Values{
		"csrf_token": {csrfToken}, "action": {action}, "return_status": {comment.StatusPending},
		"return_q": {"原子"}, "return_post": {"1"}, "return_page": {"1"},
	}
	for _, item := range comments {
		values.Add("selection", strconv.FormatInt(item.ID, 10)+":"+strconv.FormatInt(item.Version, 10))
	}
	return values
}
