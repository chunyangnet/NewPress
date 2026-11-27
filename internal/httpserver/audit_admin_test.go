package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
)

type stubAuditReader struct {
	page  audit.Page
	err   error
	query audit.Query
}

func (reader *stubAuditReader) List(_ context.Context, query audit.Query) (audit.Page, error) {
	reader.query = query
	if reader.err != nil {
		return audit.Page{}, reader.err
	}
	return reader.page, nil
}

func TestAdminAuditEventsCapabilitySafeProjectionAndCursor(t *testing.T) {
	server, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader := &stubAuditReader{page: audit.Page{
		Total: 3, HasMore: true, NextCursor: audit.EncodeCursor(time.Date(2026, 8, 22, 1, 2, 3, 0, time.UTC), 2),
		Events: []audit.Entry{
			{ID: 3, CreatedAt: time.Date(2026, 8, 22, 1, 2, 5, 0, time.UTC), Action: "comment.edit", ObjectType: "comment", ObjectID: "9", Actor: "operator"},
			{ID: 2, CreatedAt: time.Date(2026, 8, 22, 1, 2, 4, 0, time.UTC), Action: "user.updated", ObjectType: "user", ObjectID: "4", Actor: "system"},
		},
	},
	}
	server.SetAuditReader(reader)
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/audit-events?per_page=2&object_type=comment", ownerCookie, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("audit API response = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	if reader.query.Limit != 2 || reader.query.ObjectType != "comment" {
		t.Fatalf("audit query = %#v", reader.query)
	}
	var payload auditEventsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 || payload.Pagination.Total != 3 || !payload.Pagination.HasMore || payload.Pagination.NextCursor == "" || payload.Pagination.Next == "" {
		t.Fatalf("audit API payload = %#v", payload)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"private@example.test", "request_id", "metadata", "password", "Cookie", "csrf", "previous_hash", "fact_hash", "transaction"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("audit API leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"action":"comment.edit"`) || !strings.Contains(body, `"actor":"operator"`) {
		t.Fatalf("safe audit fields missing: %s", body)
	}

	invalid := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/audit-events?cursor=bad", ownerCookie, nil)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "audit.invalid_query") {
		t.Fatalf("invalid audit cursor = %d %s", invalid.Code, invalid.Body.String())
	}
	unknown := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/audit-events?metadata=1", ownerCookie, nil)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "request.invalid_query") {
		t.Fatalf("unknown audit query = %d %s", unknown.Code, unknown.Body.String())
	}

	_, editorCookie, _ := createAndLoginTestUser(t, manager, owner, "audit-editor", "audit-editor@example.test", "审计编辑", "editor")
	editor := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/audit-events", editorCookie, nil)
	if editor.Code != http.StatusOK {
		t.Fatalf("editor audit API = %d %s", editor.Code, editor.Body.String())
	}
	_, authorCookie, _ := createAndLoginTestUser(t, manager, owner, "audit-author", "audit-author@example.test", "审计作者", "author")
	author := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/admin/audit-events", authorCookie, nil)
	if author.Code != http.StatusForbidden {
		t.Fatalf("author audit API = %d %s", author.Code, author.Body.String())
	}
	toolsPage := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/?audit_object_type=comment", ownerCookie, nil)
	if toolsPage.Code != http.StatusOK || !strings.Contains(toolsPage.Body.String(), "操作记录") ||
		!strings.Contains(toolsPage.Body.String(), "评论") || !strings.Contains(toolsPage.Body.String(), "/admin/tools/audit-events.json") {
		t.Fatalf("audit tools projection = %d %s", toolsPage.Code, toolsPage.Body.String())
	}
}

func TestAdminAuditExportIsBoundedAndSecretFree(t *testing.T) {
	server, handler, _, _, ownerCookie, _ := newPersistentAdminTestServer(t)
	server.SetAuditReader(&stubAuditReader{page: audit.Page{
		Total: 101, HasMore: true, NextCursor: audit.EncodeCursor(time.Now().UTC().Add(-time.Minute), 1),
		Events: []audit.Entry{{ID: 1, CreatedAt: time.Now().UTC(), Action: "user.created", ObjectType: "user", ObjectID: "1", Actor: "operator"}},
	}})
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/audit-events.json?limit=100&action=user.created", ownerCookie, nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Disposition") != `attachment; filename="newpress-audit-events.json"` || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("audit export = %d %#v %s", response.Code, response.Header(), response.Body.String())
	}
	var document auditExportDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != 1 || document.Kind != auditExportDocumentKind || document.Pagination.Limit != 100 || len(document.Events) != 1 || !document.Pagination.HasMore {
		t.Fatalf("audit export document = %#v", document)
	}
	for _, forbidden := range []string{"metadata", "request_id", "actor_user_id", "password", "email", "user_agent", "token", "session", "previous_hash", "fact_hash", "absolute"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("audit export leaked %q: %s", forbidden, response.Body.String())
		}
	}
	invalid := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/audit-events.json?limit=101", ownerCookie, nil)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "request.invalid_query") {
		t.Fatalf("audit export invalid limit = %d %s", invalid.Code, invalid.Body.String())
	}
	unknown := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/audit-events.json?metadata=1", ownerCookie, nil)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "request.invalid_query") {
		t.Fatalf("audit export unknown query = %d %s", unknown.Code, unknown.Body.String())
	}
}

func TestAdminToolsAuditUnavailableDoesNotEchoReaderError(t *testing.T) {
	server, handler, _, _, ownerCookie, _ := newPersistentAdminTestServer(t)
	server.SetAuditReader(&stubAuditReader{err: errors.New("DSN=password-secret metadata=private@example.test")})
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/", ownerCookie, nil)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "脱敏审计事件导出") || !strings.Contains(body, "暂时无法读取操作记录") {
		t.Fatalf("audit unavailable page = %d %s", page.Code, body)
	}
	for _, forbidden := range []string{"password-secret", "private@example.test", "DSN="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("audit reader error leaked %q: %s", forbidden, body)
		}
	}
}
