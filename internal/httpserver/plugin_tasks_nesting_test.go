package httpserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskHTTPRejectsUnreadableJSONNesting(t *testing.T) {
	server, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	installTaskFixture(t, server)
	path := filepath.Join(t.TempDir(), "tasks.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	server.SetPluginTaskStore(&pluginjobs.Queue{Backend: backend})
	url := "https://example.test/api/v1/admin/plugins/task-fixture/tasks"
	request := func(depth int) string {
		return fmt.Sprintf(`{"expected_user_id":%d,"key":"nested","payload":%s0%s}`, principal.UserID, strings.Repeat("[", depth), strings.Repeat("]", depth))
	}
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, request(9998), "", "https://example.test")
	if response.Code != http.StatusUnprocessableEntity {
		t.Errorf("unreadable state nesting was accepted: %d %s", response.Code, response.Body.String())
	}
	if _, err := pluginjobs.NewFileBackend(path); err != nil {
		t.Fatal("rejected nesting broke restart", err)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, request(9997), "", "https://example.test")
	if response.Code != http.StatusAccepted {
		t.Fatalf("readable boundary could not reuse the key: %d %s", response.Code, response.Body.String())
	}
	backend, err = pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal("accepted nesting cannot reopen", err)
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Jobs) != 1 {
		t.Fatal("boundary request was not durably stored", err)
	}
}
