package httpserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskHTTPRejectsPayloadExpansionBeforePersistence(t *testing.T) {
	server, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	installTaskFixture(t, server)
	path := filepath.Join(t.TempDir(), "jobs.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	server.SetPluginTaskStore(&pluginjobs.Queue{Backend: backend})
	url := "https://example.test/api/v1/admin/plugins/task-fixture/tasks"
	body := fmt.Sprintf(`{"expected_user_id":%d,"key":"expanded-request","payload":{"text":"%s"}}`, principal.UserID, strings.Repeat("<", 12000))
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, body, "", "https://example.test")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expanded task must fail before persistence: %d %s", response.Code, response.Body.String())
	}
	backend, err = pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal("rejected payload broke queue restart", err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 0 {
		t.Fatal("rejected payload created a durable task", err)
	}
	body = fmt.Sprintf(`{"expected_user_id":%d,"key":"expanded-request","payload":{"text":"valid"}}`, principal.UserID)
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, body, "", "https://example.test")
	if response.Code != http.StatusAccepted {
		t.Fatalf("corrected request cannot reuse an uncommitted key: %d %s", response.Code, response.Body.String())
	}
	if _, err := pluginjobs.NewFileBackend(path); err != nil {
		t.Fatal("valid request cannot survive restart", err)
	}
}
