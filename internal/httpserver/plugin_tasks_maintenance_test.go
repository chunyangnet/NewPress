package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskHTTPMaintenanceInspectsDamagedFileWithoutExposingEvidence(t *testing.T) {
	s, handler, cookie, principal := newReviewedTaskHTTPTestServer(t)
	path := filepath.Join(t.TempDir(), "plugin-jobs.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	now := time.Now().UTC().Add(-time.Minute)
	job, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-original-command", ActorID: principal.UserID, Payload: json.RawMessage(`{"private":"private maintenance payload"}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPluginTaskStore(q)
	body, _ := os.ReadFile(path)
	body = bytes.Replace(body, []byte(`"status":"pending"`), []byte(`"status":"broken"`), 1)
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	url := "https://example.test/api/v1/admin/plugin-task-records"
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, url, "", "", "https://example.test")
	var page pluginjobs.DiagnosticPage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].Fault != "invalid_data" || page.Items[0].TaskID != job.ID {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, private := range []string{"private-original-command", "private maintenance payload", `"payload"`, `"epoch"`, `"actor_id"`, `"fingerprint"`, `"metadata"`, `"original"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("maintenance diagnostic exposed private data", private)
		}
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private diagnostics can be cached")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(body, after) {
		t.Fatal("HTTP inspection repaired the file")
	}
	m, _ := pluginjobs.NewFileMaintenance(path)
	if _, err = m.QuarantineRecord(t.Context(), pluginjobs.QuarantineRequest{Reference: page.Items[0].Reference, Type: pluginjobs.RecordTask, Key: "offline-maintenance", Operator: "local-person", Reason: "checked bad status"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, "https://example.test/api/v1/admin/plugin-task-quarantines", "", "", "https://example.test")
	var quarantines pluginjobs.QuarantinePage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &quarantines) != nil || quarantines.Total != 1 || quarantines.Items[0].Operator != "local-person" {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, private := range []string{"private-original-command", "private maintenance payload", `"payload"`, `"epoch"`, `"actor_id"`, `"fingerprint"`, `"metadata"`, `"original"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("quarantine list exposed original evidence", private)
		}
	}
}

func TestPluginTaskHTTPMaintenanceRequiresPersistentAccountAndStrictReadQuery(t *testing.T) {
	s, handler, cookie, principal := newReviewedTaskHTTPTestServer(t)
	backend, _ := pluginjobs.NewFileBackend("")
	s.SetPluginTaskStore(&pluginjobs.Queue{Backend: backend})
	url := "https://example.test/api/v1/admin/plugin-task-records"
	response := serveCommentRequest(handler, nil, http.MethodGet, url, "", "https://example.test", "127.0.0.1:54321")
	if response.Code != 401 && response.Code != 403 {
		t.Fatal("anonymous maintenance inspection accepted", response.Code)
	}
	for _, query := range []string{"?type=anything", "?limit=0", "?limit=1001", "?offset=-1", "?offset=no", "?payload=true", "?type=task&type=fanout"} {
		response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, url+query, "", "", "https://example.test")
		if response.Code != 422 && response.Code != 400 {
			t.Fatal("ambiguous maintenance query accepted", query, response.Code)
		}
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, url, `{}`, "", "https://example.test")
	if response.Code != 405 {
		t.Fatal("online maintenance mutation route exists", response.Code)
	}
	legacy, legacyHandler, _, legacyCookie, legacyPrincipal := newCommentHTTPTestServer(t)
	legacy.SetPluginTaskStore(&pluginjobs.Queue{Backend: backend})
	response = serveAdminCommentAPI(legacyHandler, legacyCookie, legacyPrincipal.CSRFToken, http.MethodGet, url, "", "", "https://example.test")
	if response.Code != 403 {
		t.Fatal("account zero was allowed to inspect maintenance evidence", response.Code)
	}
}
