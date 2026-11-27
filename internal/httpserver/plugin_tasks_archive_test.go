package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskHTTPArchiveAndRedactedHistory(t *testing.T) {
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	now := time.Now().UTC().Add(-time.Hour)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-key", ActorID: principal.UserID, Payload: json.RawMessage(`{"secret":"archived payload"}`)}
	j, err := q.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Change(t.Context(), j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	base := "https://example.test/api/v1/admin/plugins/reader/tasks"
	before := time.Now().UTC().Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"expected_user_id":%d,"before":%q,"limit":100}`, principal.UserID, before)
	for _, test := range []struct {
		csrf, origin, url, body string
		want                    int
	}{
		{"", "https://example.test", base + "/archive", body, 403},
		{principal.CSRFToken, "https://attacker.test", base + "/archive", body, 403},
		{principal.CSRFToken, "https://example.test", base + "/archive", strings.Replace(body, fmt.Sprint(principal.UserID), "999999", 1), 409},
		{principal.CSRFToken, "https://example.test", base + "/archive", strings.Replace(body, `"limit":100`, `"limit":1001`, 1), 422},
	} {
		response := serveAdminCommentAPI(handler, cookie, test.csrf, http.MethodPost, test.url, test.body, "", test.origin)
		if response.Code != test.want {
			t.Fatalf("archive boundary: got=%d want=%d body=%s", response.Code, test.want, response.Body.String())
		}
	}
	wrong := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, strings.Replace(base, "/reader/", "/other/", 1)+"/archive", body, "", "https://example.test")
	if wrong.Code != 200 || !strings.Contains(wrong.Body.String(), `"tasks":0`) {
		t.Fatal("cross-plugin archive moved a record", wrong.Code, wrong.Body.String())
	}
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/archive", body, "", "https://example.test")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"tasks":1`) || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("archive response", response.Code, response.Body.String())
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base+"?archived=true", "", "", "https://example.test")
	var page pluginjobs.Page
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 1 || page.Items[0].ArchivedAt.IsZero() {
		t.Fatal("archived list", response.Code, response.Body.String())
	}
	for _, secret := range []string{"private-key", "archived payload", `"fingerprint"`, `"epoch"`, `"payload"`, `"actor_id"`, `"lease_token"`} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("archived query exposed %q", secret)
		}
	}
	for _, query := range []string{"?archived=1", "?archived=anything", "?archived=true&offset=100001", "?archived=true&archived=false"} {
		response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base+query, "", "", "https://example.test")
		if response.Code < 400 || response.Code >= 500 {
			t.Fatal("invalid archive query accepted", query, response.Code)
		}
	}
	if page, err := q.List(t.Context(), pluginjobs.Query{Plugin: in.Plugin}); err != nil || page.Total != 0 {
		t.Fatal("API did not reclaim live records", err)
	}
}

func TestPluginTaskHTTPArchiveGlobalFanoutsOnly(t *testing.T) {
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	now := time.Now().UTC().Add(-time.Hour)
	event := pluginjobs.Event{Key: "site-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = q.FreezeFanout(t.Context(), event, nil); err != nil {
		t.Fatal(err)
	}
	if err = q.CompleteFanout(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_user_id":%d,"before":%q,"limit":100}`, principal.UserID, time.Now().UTC().Format(time.RFC3339Nano))
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, "https://example.test/api/v1/admin/plugins/fanouts/archive", body, "", "https://example.test")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"fanouts":1`) || !strings.Contains(response.Body.String(), `"tasks":0`) {
		t.Fatal("site fanout archive", response.Code, response.Body.String())
	}
	state, err := backend.Read(t.Context())
	if err != nil || len(state.Fanouts) != 0 || len(state.ArchivedFanouts) != 1 || !state.ArchivedFanouts[event.Key].Receipt.Delivered {
		t.Fatal("fanout archive did not persist", err)
	}
}
