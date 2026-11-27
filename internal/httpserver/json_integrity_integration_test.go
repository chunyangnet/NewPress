package httpserver

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

func TestContentAPIRejectsAmbiguousJSONWithoutPersistence(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	for index, test := range []struct{ name, fields string }{
		{"case alias", `"title":"first","TITLE":"second"`},
		{"Unicode alias", `"title":"safe","status":"draft","ſtatus":"publish"`},
		{"nested alias", `"title":"safe","meta":{"ai_pro_max":"off","AI_PRO_MAX":"on"}`},
		{"invalid UTF8", "\"title\":\"private-\xff\""},
		{"high surrogate", `"title":"private-\ud800"`},
		{"low surrogate", `"title":"private-\udfff"`},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(test.name+"/"+method, func(t *testing.T) {
				filename := filepath.Join(t.TempDir(), "content.json")
				repository, err := content.NewFileRepository(filename, false)
				if err != nil {
					t.Fatal(err)
				}
				s.repository = repository
				target := atomicContentAPITarget
				headers := authenticatedAPIHeaders(principal)
				var post content.Post
				if method == http.MethodPatch {
					post, err = repository.Create(t.Context(), content.PostInput{Title: "Original", Slug: "original"})
					if err != nil {
						t.Fatal(err)
					}
					target += fmt.Sprintf("/%d", post.ID)
					headers["If-Match"] = contentAPIETag(post)
				} else {
					headers["Idempotency-Key"] = fmt.Sprintf("json-integrity-%d", index)
				}
				before, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				response := serveContentAPI(handler, cookie, method, target, "{"+test.fields+"}", headers)
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"request.invalid_json"`) {
					t.Errorf("invalid input reached content write: %d %s", response.Code, response.Body.String())
				}
				if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(before, after) {
					t.Errorf("invalid request changed content or submission records: %v", err)
				}
				reopened, err := content.NewFileRepository(filename, false)
				if err != nil {
					t.Fatal(err)
				}
				list, err := reopened.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
				wantCount := 0
				if method == http.MethodPatch {
					wantCount = 1
					got, err := reopened.GetByID(t.Context(), post.ID)
					if err != nil || got.Version != post.Version || got.Title != post.Title || got.Status != post.Status {
						t.Errorf("invalid patch changed stored content: %#v %v", got, err)
					}
					revisions, err := reopened.ListRevisions(t.Context(), post.ID, 10)
					if err != nil || len(revisions) != 1 {
						t.Errorf("invalid patch created a revision: %d %v", len(revisions), err)
					}
				}
				if err != nil || list.Total != wantCount {
					t.Errorf("invalid request changed persisted post count: %d %v", list.Total, err)
				}
				if method == http.MethodPost {
					valid := serveContentAPI(handler, cookie, method, target, `{"title":"合法😀","slug":"valid-retry"}`, headers)
					if valid.Code != http.StatusCreated {
						t.Errorf("invalid JSON consumed the idempotency key: %d %s", valid.Code, valid.Body.String())
					}
				}
			})
		}
	}
}

func TestPublicCommentJSONRejectsAliasesAndLossyUnicodeWithoutWrites(t *testing.T) {
	_, handler, comments, _, _ := newCommentHTTPTestServer(t)
	get := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/api/v1/posts/welcome-to-newpress/comments", "", "", "203.0.113.100:4100")
	var projection publicCommentsAPIResponse
	decodeAPIResponse(t, get, &projection)
	if projection.Submission.FormToken == "" {
		t.Fatal("fixture has no submission token")
	}
	for index, test := range []struct{ name, fields string }{
		{"case alias", `"content":"first","CONTENT":"second"`},
		{"invalid UTF8", "\"content\":\"private-\xff\""},
		{"high surrogate", `"content":"private-\ud800"`},
		{"low surrogate", `"content":"private-\udfff"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := comments.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"form_token":%q,"author_name":"访客","email":"reader@example.test","website":"",%s}`, projection.Submission.FormToken, test.fields)
			response := servePublicCommentAPI(handler, "welcome-to-newpress", body, "https://example.test", fmt.Sprintf("203.0.113.%d:4100", index+101))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"request.invalid_json"`) {
				t.Errorf("invalid comment accepted: %d %s", response.Code, response.Body.String())
			}
			after, err := comments.Snapshot(t.Context())
			if err != nil || after.Total != before.Total {
				t.Errorf("invalid comment modified the store: total=%d %v", after.Total, err)
			}
		})
	}
}

func TestNavigationImportJSONIntegrityBeforeReplacement(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	base := marshalNavigationImportDocument(t, validNavigationImportDocument())
	for _, test := range []struct{ name, from, to string }{
		{"schema alias", `"schema":1`, `"schema":0,"SCHEMA":1`},
		{"nested field alias", `"label":"工程分类"`, `"label":"different","LABEL":"工程分类"`},
		{"invalid UTF8", `"name":"迁移主导航"`, "\"name\":\"private-\xff\""},
		{"high surrogate", `"name":"迁移主导航"`, `"name":"private-\ud800"`},
		{"low surrogate", `"name":"迁移主导航"`, `"name":"private-\udfff"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := s.navigation.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.Replace(base, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(base, body) {
				t.Fatal("fixture did not inject invalid JSON")
			}
			preview := serveNavigationImportPreview(handler, cookie, url.Values{
				"csrf_token": {principal.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
			}, "invalid.json", body, "https://example.test")
			if preview.Code != http.StatusUnprocessableEntity {
				t.Errorf("invalid navigation preview accepted: %d", preview.Code)
			}
			apply := serveNavigationImportApply(handler, cookie, url.Values{
				"csrf_token": {principal.CSRFToken}, "action": {"apply"}, "expected_revision": {navigation.SnapshotRevision(before)},
				"object_policy": {navigationImportDetachObjects}, "document": {base64.RawURLEncoding.EncodeToString(body)}, "confirm": {"replace"},
			}, "https://example.test")
			if apply.Code != http.StatusUnprocessableEntity {
				t.Errorf("invalid navigation apply accepted: %d", apply.Code)
			}
			after, err := s.navigation.Snapshot(t.Context())
			if err != nil || navigation.SnapshotRevision(after) != navigation.SnapshotRevision(before) {
				t.Errorf("invalid import replaced the navigation registry: %v", err)
			}
		})
	}
}
