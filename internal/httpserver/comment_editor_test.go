package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/comment"
)

func TestAdminCommentInspectorSafeEditStaffReplyAndRevisionAPI(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	root, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "原评论者", AuthorEmail: "before@example.test", Status: comment.StatusApproved,
		ContentSource: "原始审核正文。", AuthorIPHash: strings.Repeat("b", 64), UserAgent: "inspector-test/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	basePath := "/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/edit/"
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test"+basePath+"?status=approved", "", "", "198.51.100.30:4300")
	body := page.Body.String()
	assertAdminSavebarBindings(t, body)
	writeAdminLayoutSnapshot(t, basePath, body)
	for _, marker := range []string{
		"编辑评论", "data-comment-edit-form", "data-comment-reply-form",
		"评论内容", "修订记录", "before@example.test", "原始审核正文。",
		"/api/v1/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/revisions",
	} {
		if page.Code != http.StatusOK || !strings.Contains(body, marker) {
			t.Fatalf("inspector missing %q: status=%d body=%s", marker, page.Code, body)
		}
	}

	edit := commentEditorEditValues(principal.CSRFToken, 1, "修订评论者", "AFTER@example.test", "修订正文 <script>alert(1)</script>", "approved")
	editedResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, "https://example.test"+basePath, edit, "https://example.test")
	if editedResponse.Code != http.StatusSeeOther || !strings.Contains(editedResponse.Header().Get("Location"), "notice=edited") {
		t.Fatalf("edit response=%d location=%q body=%s", editedResponse.Code, editedResponse.Header().Get("Location"), editedResponse.Body.String())
	}
	edited, err := comments.Get(t.Context(), root.ID)
	if err != nil || edited.Version != 2 || edited.AuthorEmail != "after@example.test" || edited.EditedBy != "moderator" ||
		strings.Contains(edited.ContentHTML, "<script>") || !strings.Contains(edited.ContentHTML, "&lt;script&gt;") {
		t.Fatalf("edited comment=%#v err=%v", edited, err)
	}
	revisions, err := comments.ListRevisions(t.Context(), root.ID, 20)
	if err != nil || len(revisions) != 1 || revisions[0].Version != 1 || revisions[0].Actor != "moderator" || revisions[0].ContentSource != "原始审核正文。" {
		t.Fatalf("edit revisions=%#v err=%v", revisions, err)
	}

	stale := cloneValues(edit)
	stale.Set("author_name", "不能覆盖")
	staleResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, "https://example.test"+basePath, stale, "https://example.test")
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "未覆盖任何内容") {
		t.Fatalf("stale edit=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	afterStale, _ := comments.Get(t.Context(), root.ID)
	if afterStale.Version != 2 || afterStale.AuthorName != "修订评论者" {
		t.Fatalf("stale edit mutated comment: %#v", afterStale)
	}

	replyValues := commentEditorReplyValues(principal.CSRFToken, 2, "这是站方回复 <script>bad()</script>。", "approved")
	repliedResponse := serveCommentEditorForm(handler, cookie, http.MethodPost,
		"https://example.test/admin/comments/"+strconv.FormatInt(root.ID, 10)+"/reply/", replyValues, "https://example.test")
	if repliedResponse.Code != http.StatusSeeOther || !strings.Contains(repliedResponse.Header().Get("Location"), "notice=replied") {
		t.Fatalf("staff reply=%d location=%q body=%s", repliedResponse.Code, repliedResponse.Header().Get("Location"), repliedResponse.Body.String())
	}
	staffList, err := comments.ListAdmin(t.Context(), comment.Query{PostID: 1, Search: "这是站方回复", Limit: 20})
	if err != nil || staffList.Total != 1 {
		t.Fatalf("staff list=%#v err=%v", staffList, err)
	}
	staffReply := staffList.Items[0]
	if !staffReply.StaffReply || staffReply.ParentID != root.ID || staffReply.Status != comment.StatusApproved || staffReply.AuthorName != "moderator" ||
		staffReply.AuthorEmail != "admin@localhost.invalid" || staffReply.CreatedBy != "moderator" || strings.Contains(staffReply.ContentHTML, "<script>") {
		t.Fatalf("staff reply record=%#v", staffReply)
	}
	public := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.31:4300")
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "站方回复") || !strings.Contains(public.Body.String(), "这是站方回复") ||
		strings.Contains(public.Body.String(), "admin@localhost.invalid") || strings.Contains(public.Body.String(), "<script>bad()") {
		t.Fatalf("public staff reply boundary=%d body=%s", public.Code, public.Body.String())
	}

	apiTarget := "https://example.test/api/v1/admin/comments/" + strconv.FormatInt(root.ID, 10)
	get := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, apiTarget, "", "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"comment-`+strconv.FormatInt(root.ID, 10)+`-v2"` || !strings.Contains(get.Body.String(), `"edited_by":"moderator"`) {
		t.Fatalf("comment GET API=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	apiEdit := `{"version":2,"author_name":"API 修订者","author_email":"api-edit@example.test","content_source":"API 安全修订"}`
	put := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut, apiTarget, apiEdit, "", "https://example.test")
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"comment-`+strconv.FormatInt(root.ID, 10)+`-v3"` {
		t.Fatalf("comment PUT API=%d etag=%q body=%s", put.Code, put.Header().Get("ETag"), put.Body.String())
	}
	revisionAPI := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, apiTarget+"/revisions", "", "", "")
	if revisionAPI.Code != http.StatusOK || !strings.Contains(revisionAPI.Body.String(), `"version":2`) || !strings.Contains(revisionAPI.Body.String(), `"version":1`) || strings.Contains(revisionAPI.Body.String(), "author_ip") {
		t.Fatalf("revision API=%d body=%s", revisionAPI.Code, revisionAPI.Body.String())
	}
	staleReplyAPI := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiTarget+"/replies", `{"parent_version":2,"content_source":"过期 API 回复"}`, "", "https://example.test")
	assertAPIError(t, staleReplyAPI, http.StatusConflict, "comments.version_conflict")
	validReplyAPI := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiTarget+"/replies", `{"parent_version":3,"content_source":"有效 API 站方回复"}`, "", "https://example.test")
	if validReplyAPI.Code != http.StatusCreated || validReplyAPI.Header().Get("Location") == "" || !strings.Contains(validReplyAPI.Body.String(), `"staff_reply":true`) {
		t.Fatalf("reply API=%d location=%q body=%s", validReplyAPI.Code, validReplyAPI.Header().Get("Location"), validReplyAPI.Body.String())
	}
}

func TestAdminCommentRevisionRestoreHTMLAndAPI(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	root, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "原始称呼", AuthorEmail: "original@example.test", Status: comment.StatusApproved,
		ContentSource: "原始正文 <script>restore()</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := comments.Edit(t.Context(), root.ID, comment.Mutation{ExpectedVersion: 1, Actor: "moderator"}, comment.EditInput{
		AuthorName: "当前称呼", AuthorEmail: "current@example.test", ContentSource: "当前正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := comments.ChangeStatus(t.Context(), root.ID, comment.Mutation{ExpectedVersion: edited.Version, Actor: "moderator"}, comment.StatusPending)
	if err != nil || pending.Version != 3 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}

	basePath := "/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/edit/"
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test"+basePath, "", "", "198.51.100.33:4300")
	for _, marker := range []string{
		"恢复范围", "data-comment-revision-restore", "恢复为下一版本",
		"/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/revisions/1/restore/",
		"历史状态“已批准”不会覆盖当前“待审核”",
	} {
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("restore inspector missing %q: status=%d body=%s", marker, page.Code, page.Body.String())
		}
	}

	restorePath := "https://example.test/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/revisions/1/restore/"
	values := commentEditorRestoreValues(principal.CSRFToken, pending.Version, "pending")
	crossSite := serveCommentEditorForm(handler, cookie, http.MethodPost, restorePath, values, "https://attacker.test")
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site restore=%d body=%s", crossSite.Code, crossSite.Body.String())
	}
	unknown := cloneValues(values)
	unknown.Set("status", "approved")
	unknownResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, restorePath, unknown, "https://example.test")
	if unknownResponse.Code != http.StatusBadRequest || !strings.Contains(unknownResponse.Body.String(), "invalid_comment_revision_restore_form_shape") {
		t.Fatalf("unknown restore field=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}
	restoredResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, restorePath, values, "https://example.test")
	if restoredResponse.Code != http.StatusSeeOther || !strings.Contains(restoredResponse.Header().Get("Location"), "notice=restored") ||
		!strings.Contains(restoredResponse.Header().Get("Location"), "#comment-revision-ledger") {
		t.Fatalf("restore response=%d location=%q body=%s", restoredResponse.Code, restoredResponse.Header().Get("Location"), restoredResponse.Body.String())
	}
	restored, err := comments.Get(t.Context(), root.ID)
	if err != nil || restored.Version != 4 || restored.Status != comment.StatusPending || restored.AuthorName != "原始称呼" ||
		restored.AuthorEmail != "original@example.test" || restored.EditedBy != "moderator" ||
		strings.Contains(restored.ContentHTML, "<script>") || !strings.Contains(restored.ContentHTML, "&lt;script&gt;") {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	revisions, err := comments.ListRevisions(t.Context(), root.ID, 20)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 3 || revisions[0].Status != comment.StatusPending ||
		revisions[0].ContentSource != "当前正文" || revisions[1].Version != 1 {
		t.Fatalf("restore revisions=%#v err=%v", revisions, err)
	}

	stale := commentEditorRestoreValues(principal.CSRFToken, pending.Version, "pending")
	staleResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, restorePath, stale, "https://example.test")
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "修订恢复未写入") {
		t.Fatalf("stale restore=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	noOp := commentEditorRestoreValues(principal.CSRFToken, restored.Version, "pending")
	noOpResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, restorePath, noOp, "https://example.test")
	if noOpResponse.Code != http.StatusSeeOther || !strings.Contains(noOpResponse.Header().Get("Location"), "notice=unchanged") {
		t.Fatalf("no-op restore=%d location=%q", noOpResponse.Code, noOpResponse.Header().Get("Location"))
	}

	apiTarget := "https://example.test/api/v1/admin/comments/" + strconv.FormatInt(root.ID, 10)
	apiEdit := `{"version":4,"author_name":"API 当前称呼","author_email":"api-current@example.test","content_source":"API 当前正文"}`
	put := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut, apiTarget, apiEdit, "", "https://example.test")
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"comment-`+strconv.FormatInt(root.ID, 10)+`-v5"` {
		t.Fatalf("pre-restore API edit=%d etag=%q body=%s", put.Code, put.Header().Get("ETag"), put.Body.String())
	}
	apiRestore := apiTarget + "/revisions/1/restore"
	missingPrecondition := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiRestore, "", "", "https://example.test")
	assertAPIError(t, missingPrecondition, http.StatusPreconditionRequired, "comments.precondition_required")
	stalePrecondition := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiRestore, "", `"comment-`+strconv.FormatInt(root.ID, 10)+`-v4"`, "https://example.test")
	assertAPIError(t, stalePrecondition, http.StatusConflict, "comments.version_conflict")
	validPrecondition := `"comment-` + strconv.FormatInt(root.ID, 10) + `-v5"`
	withBody := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiRestore, `{}`, validPrecondition, "https://example.test")
	assertAPIError(t, withBody, http.StatusBadRequest, "request.body_not_allowed")
	apiRestored := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, apiRestore, "", validPrecondition, "https://example.test")
	if apiRestored.Code != http.StatusOK || apiRestored.Header().Get("ETag") != `"comment-`+strconv.FormatInt(root.ID, 10)+`-v6"` ||
		!strings.Contains(apiRestored.Body.String(), `"author_name":"原始称呼"`) || !strings.Contains(apiRestored.Body.String(), `"status":"pending"`) {
		t.Fatalf("API restore=%d etag=%q body=%s", apiRestored.Code, apiRestored.Header().Get("ETag"), apiRestored.Body.String())
	}
	missingSource := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost,
		apiTarget+"/revisions/2/restore", "", `"comment-`+strconv.FormatInt(root.ID, 10)+`-v6"`, "https://example.test")
	assertAPIError(t, missingSource, http.StatusNotFound, "comments.revision_not_found")
	finalRevisions, err := comments.ListRevisions(t.Context(), root.ID, 20)
	if err != nil || len(finalRevisions) != 4 || finalRevisions[0].Version != 5 || finalRevisions[1].Version != 4 ||
		finalRevisions[2].Version != 3 || finalRevisions[3].Version != 1 {
		t.Fatalf("final restore revisions=%#v err=%v", finalRevisions, err)
	}
}

func TestAdminCommentEditorStrictBoundariesAndPendingReplyInheritance(t *testing.T) {
	_, handler, comments, cookie, principal := newCommentHTTPTestServer(t)
	pending, err := comments.Create(t.Context(), comment.CreateInput{
		PostID: 1, AuthorName: "待审目标", AuthorEmail: "pending@example.test", Status: comment.StatusPending, ContentSource: "尚未公开",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "https://example.test/admin/comments/" + strconv.FormatInt(pending.ID, 10) + "/reply/"
	valid := commentEditorReplyValues(principal.CSRFToken, pending.Version, "待审站方回复", "pending")
	crossSite := serveCommentEditorForm(handler, cookie, http.MethodPost, path, valid, "https://attacker.test")
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site reply status=%d body=%s", crossSite.Code, crossSite.Body.String())
	}
	badCSRF := cloneValues(valid)
	badCSRF.Set("csrf_token", "invalid")
	badCSRFResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, path, badCSRF, "https://example.test")
	if badCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF reply status=%d body=%s", badCSRFResponse.Code, badCSRFResponse.Body.String())
	}
	unknown := cloneValues(valid)
	unknown.Set("status", "approved")
	unknownResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, path, unknown, "https://example.test")
	if unknownResponse.Code != http.StatusBadRequest || !strings.Contains(unknownResponse.Body.String(), "invalid_comment_reply_form_shape") {
		t.Fatalf("unknown reply field status=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}
	validResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, path, valid, "https://example.test")
	if validResponse.Code != http.StatusSeeOther {
		t.Fatalf("pending reply status=%d body=%s", validResponse.Code, validResponse.Body.String())
	}
	list, err := comments.ListAdmin(t.Context(), comment.Query{Search: "待审站方回复", Limit: 20})
	if err != nil || list.Total != 1 || list.Items[0].Status != comment.StatusPending || !list.Items[0].StaffReply {
		t.Fatalf("pending staff reply=%#v err=%v", list, err)
	}
	public := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.32:4300")
	if strings.Contains(public.Body.String(), "待审站方回复") {
		t.Fatal("pending staff reply leaked publicly")
	}

	root, err := comments.Get(t.Context(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	editPath := "https://example.test/admin/comments/" + strconv.FormatInt(root.ID, 10) + "/edit/"
	duplicate := commentEditorEditValues(principal.CSRFToken, root.Version, root.AuthorName, root.AuthorEmail, root.ContentSource, "pending")
	duplicate["author_name"] = []string{"一", "二"}
	duplicateResponse := serveCommentEditorForm(handler, cookie, http.MethodPost, editPath, duplicate, "https://example.test")
	if duplicateResponse.Code != http.StatusBadRequest || !strings.Contains(duplicateResponse.Body.String(), "invalid_comment_edit_form_shape") {
		t.Fatalf("duplicate edit field status=%d body=%s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
	apiUnknown := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut,
		"https://example.test/api/v1/admin/comments/"+strconv.FormatInt(root.ID, 10),
		`{"version":1,"author_name":"A","author_email":"a@example.test","content_source":"B","status":"approved"}`, "", "https://example.test")
	assertAPIError(t, apiUnknown, http.StatusBadRequest, "request.invalid_json")
	if _, err := comments.Get(t.Context(), root.ID); errors.Is(err, comment.ErrNotFound) {
		t.Fatal("strict boundary unexpectedly deleted comment")
	}
}

func serveCommentEditorForm(handler http.Handler, cookie *http.Cookie, method, target string, values url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func commentEditorEditValues(csrf string, version int64, name, email, contentSource, returnStatus string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "version": {strconv.FormatInt(version, 10)}, "author_name": {name}, "author_email": {email},
		"content_source": {contentSource}, "return_status": {returnStatus}, "return_q": {""}, "return_post": {"0"}, "return_page": {"1"},
	}
}

func commentEditorReplyValues(csrf string, parentVersion int64, contentSource, returnStatus string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "parent_version": {strconv.FormatInt(parentVersion, 10)}, "content_source": {contentSource},
		"return_status": {returnStatus}, "return_q": {""}, "return_post": {"0"}, "return_page": {"1"},
	}
}

func commentEditorRestoreValues(csrf string, version int64, returnStatus string) url.Values {
	return url.Values{
		"csrf_token": {csrf}, "version": {strconv.FormatInt(version, 10)}, "return_status": {returnStatus},
		"return_q": {""}, "return_post": {"0"}, "return_page": {"1"},
	}
}
