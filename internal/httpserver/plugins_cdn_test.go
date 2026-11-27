package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/plugin"
)

func TestCommentCDNSourceCapturePrivateDiagnosticsAndDeletion(t *testing.T) {
	s, h, comments, cookie, principal := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	s.clientIPs = cdnTestResolver()
	for _, valid := range []bool{true, false} {
		item, err := comments.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "CDN visitor", AuthorEmail: "cdn@example.test", ContentSource: "CDN source test", Status: comment.StatusApproved})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", "47.246.1.1")
		r.Header.Set("User-Agent", "Windows NT 10.0")
		if valid {
			r.Header.Set("Ali-Real-Client-IP", "8.8.8.8")
		}
		s.captureCommentOrigin(r, item.ID)
		record, err := s.commentOrigin.PrivateRecord(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if valid && (record.IP != "8.8.8.8" || record.Source != "esa") {
			t.Fatalf("wrong CDN capture: %+v", record)
		}
		if !valid && (record.IP != "" || record.Status != "unavailable" || record.SourceIssue != "missing_cdn_header") {
			t.Fatalf("fallback captured: %+v", record)
		}
		public, _ := json.Marshal(s.publicCommentWithOrigin(r, item))
		for _, secret := range []string{"8.8.8.8", "47.246.1.1", "missing_cdn_header", "Windows NT"} {
			if strings.Contains(string(public), secret) {
				t.Fatalf("public source leak: %s", public)
			}
		}
		item, err = comments.ChangeStatus(t.Context(), item.ID, comment.Mutation{ExpectedVersion: item.Version}, comment.StatusTrash)
		if err != nil {
			t.Fatal(err)
		}
		deleteURL := "https://example.test/api/v1/admin/comments/" + strconv.FormatInt(item.ID, 10)
		w := serveAdminCommentAPI(h, cookie, principal.CSRFToken, http.MethodDelete, deleteURL, "", commentETag(item), "https://example.test")
		if w.Code != http.StatusNoContent {
			t.Fatalf("delete failed: %d %s", w.Code, w.Body.String())
		}
		if _, err := s.commentOrigin.PrivateRecord(item.ID); !os.IsNotExist(err) {
			t.Fatalf("origin survives delete: %v", err)
		}
	}
}

func TestCDNDiagnosticsDoNotDiscloseAddressWithoutModerationPermission(t *testing.T) {
	s := &Server{clientIPs: cdnTestResolver()}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "47.246.1.1:80"
	r.Header.Set("Ali-Real-Client-IP", "8.8.8.8")
	if got := s.clientIPDiagnostic(r, false); got.Address != "" || got.Method != "阿里云 ESA" {
		t.Fatalf("diagnostic=%+v", got)
	}
	if got := s.clientIPDiagnostic(r, true); got.Address != "8.8.8.8" {
		t.Fatalf("missing authorized address: %+v", got)
	}
}

func TestPluginAdminAuthenticationResponsesNeverCache(t *testing.T) {
	_, h, _, cookie, _ := newCommentHTTPTestServer(t)
	for _, path := range []string{"/admin/plugins/", "/admin/plugins/comment-origin/", "/api/v1/admin/plugins/comment-origin/records/1"} {
		w := serveCommentRequest(h, nil, http.MethodGet, "https://example.test"+path, "", "", "9.9.9.9:80")
		if w.Code == 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("authentication response cached: %s %d %+v", path, w.Code, w.Header())
		}
	}
	loggedInRedirect := serveCommentRequest(h, cookie, http.MethodGet, "https://example.test/admin/login/", "", "", "9.9.9.9:80")
	if loggedInRedirect.Code != http.StatusSeeOther || loggedInRedirect.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated login redirect cached: %d %+v", loggedInRedirect.Code, loggedInRedirect.Header())
	}
}
