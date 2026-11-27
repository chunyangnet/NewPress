package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

func TestPluginSharedMaintenanceWebhookHTTPProjection(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("a", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)}, Slot: "private-slot", URL: "https://example.com/private?token=secret", Body: json.RawMessage(`{"message":"private body"}`)}
	id, fp, body, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	i := plugindispatch.Intent{Schema: 1, ID: id, JobID: r.Authorization.Claim.ID, SourceFingerprint: strings.Repeat("d", 64), Plugin: "reader", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: 7, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: r.Slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: "pending", CreatedAt: at, UpdatedAt: at}
	raw, _ := json.Marshal(i)
	index, _ := json.Marshal(pluginshared.Index{JobID: i.JobID, Slot: i.Slot, Version: i.Version, State: i.State})
	record := pluginshared.Record{Type: pluginshared.WebhookIntent, Key: id, Data: raw, Index: index}
	if pluginshared.Diagnose(record).Fault != "" {
		t.Fatal("invalid fixture")
	}
	store.records = append(store.records, record)
	s.SetPluginTaskStore(store)
	response := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, "https://example.test/api/v1/admin/plugin-shared-records?type=webhook_intent&plugin=reader", "", "", "https://example.test")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var page pluginshared.Page
	if json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 1 || page.Items[0].Webhook == nil || page.Items[0].Webhook.OperationID != id || page.Items[0].Webhook.State != "pending" {
		t.Fatal(response.Body.String())
	}
	for _, secret := range []string{"private-slot", "private body", "example.com", `"url"`, `"fingerprint"`, `"epoch"`, `"actor_id"`, `"attempt_token"`} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("private request exposed", secret)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		response = serveAdminCommentAPI(h, c, p.CSRFToken, method, "https://example.test/api/v1/admin/plugin-shared-records?type=webhook_intent", `{}`, "", "https://example.test")
		if response.Code != 405 {
			t.Fatal("online maintenance mutation accepted", method, response.Code)
		}
	}
}

type sharedMaintenanceHTTPStore struct {
	*pluginjobs.Queue
	records     []pluginshared.Record
	quarantines []pluginshared.Quarantine
	err         error
	calls       int
}

func (s *sharedMaintenanceHTTPStore) InspectSharedRecords(_ context.Context, q pluginshared.Query) (pluginshared.Page, error) {
	s.calls++
	return pluginshared.Paginate(s.records, q), s.err
}
func (s *sharedMaintenanceHTTPStore) ListSharedQuarantines(_ context.Context, q pluginshared.Query) (pluginshared.QuarantinePage, error) {
	s.calls++
	return pluginshared.PaginateQuarantines(s.quarantines, q), s.err
}
func newSharedMaintenanceHTTPStore(t *testing.T) *sharedMaintenanceHTTPStore {
	t.Helper()
	b, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	job := strings.Repeat("a", 64)
	id, _ := plugineffects.OperationID(job, "private-slot")
	receipt := plugineffects.Receipt{Schema: 1, JobID: job, Plugin: "reader", Slot: "private-slot", ActorID: 7, Fingerprint: strings.Repeat("b", 64), ManifestDigest: strings.Repeat("c", 64), ModuleDigest: strings.Repeat("d", 64), Result: plugineffects.Result{OperationID: id, PostID: 12, PreviousVersion: 3, Version: 4, Changed: true, CommittedAt: time.Now().UTC()}}
	raw, _ := json.Marshal(receipt)
	index, _ := json.Marshal(pluginshared.Index{JobID: job, Slot: receipt.Slot})
	r := pluginshared.Record{Type: pluginshared.ContentEffect, Key: id, Data: raw, Index: index}
	bad := r
	bad.Key = "wrong-index"
	quarantine, err := pluginshared.Capture(bad, "index_mismatch", pluginshared.CaptureRequest{Type: bad.Type, Reference: pluginshared.Reference(bad), Key: "private-request-key", Operator: "offline-operator", Reason: "checked original index"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return &sharedMaintenanceHTTPStore{Queue: &pluginjobs.Queue{Backend: b}, records: []pluginshared.Record{r}, quarantines: []pluginshared.Quarantine{quarantine}}
}
func TestPluginSharedMaintenanceHTTPProjectionAndReadOnly(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	for _, endpoint := range []string{"plugin-shared-records", "plugin-shared-quarantines"} {
		url := "https://example.test/api/v1/admin/" + endpoint
		response := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, url+"?type=content_effect&plugin=reader&job="+strings.Repeat("a", 64)+"&limit=1", "", "", "https://example.test")
		if response.Code != 200 || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(response.Code, response.Body.String())
		}
		for _, secret := range []string{"private-slot", "private-request-key", `"actor_id"`, `"fingerprint"`, `"module_digest"`, `"manifest_digest"`, `"epoch"`, `"configuration"`, `"original"`, `"data"`, `"index"`} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("private original exposed", secret)
			}
		}
		if endpoint == "plugin-shared-records" {
			var page pluginshared.Page
			if json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 1 || page.Items[0].Result.PostID != 12 || page.Items[0].Result.Version != 4 {
				t.Fatal(response.Body.String())
			}
		}
		before := store.calls
		for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
			r := serveAdminCommentAPI(h, c, p.CSRFToken, method, url, `{}`, "", "https://example.test")
			if r.Code != 405 {
				t.Fatal("online maintenance mutation", method, r.Code)
			}
		}
		if store.calls != before {
			t.Fatal("write method reached reader")
		}
	}
}
func TestPluginSharedMaintenanceHTTPQueryAuthenticationAndUnavailable(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	for _, endpoint := range []string{"plugin-shared-records", "plugin-shared-quarantines"} {
		url := "https://example.test/api/v1/admin/" + endpoint
		anon := serveCommentRequest(h, nil, http.MethodGet, url, "", "https://example.test", "127.0.0.1:54321")
		if anon.Code != 401 && anon.Code != 403 {
			t.Fatal("anonymous read", anon.Code)
		}
		for _, query := range []string{"?type=task", "?job=bad", "?plugin=../bad", "?limit=0", "?limit=1001", "?limit=nan", "?offset=-1", "?offset=1000000", "?payload=true", "?type=authority&type=content_effect"} {
			r := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, url+query, "", "", "https://example.test")
			if r.Code != 400 && r.Code != 422 {
				t.Fatal(query, r.Code, r.Body.String())
			}
		}
	}
	if store.calls != 0 {
		t.Fatal("invalid request reached storage")
	}
	url := "https://example.test/api/v1/admin/plugin-shared-records"
	legacy, lh, _, lc, lp := newCommentHTTPTestServer(t)
	legacy.SetPluginTaskStore(store)
	if r := serveAdminCommentAPI(lh, lc, lp.CSRFToken, http.MethodGet, url, "", "", "https://example.test"); r.Code != 403 {
		t.Fatal("bootstrap read", r.Code)
	}
	store.err = errors.New("private database credentials")
	if r := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, url, "", "", "https://example.test"); r.Code != 503 || strings.Contains(r.Body.String(), "private database credentials") {
		t.Fatal(r.Code, r.Body.String())
	}
	s.SetPluginTaskStore(store.Queue)
	if r := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, url, "", "", "https://example.test"); r.Code != 503 {
		t.Fatal("file mode", r.Code)
	}
}
func TestPluginSharedMaintenanceHTTPRateAndRevokedAccount(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	url := "https://example.test/api/v1/admin/plugin-shared-records"
	for i := 0; i < 31; i++ {
		r := serveAdminCommentAPI(h, c, p.CSRFToken, http.MethodGet, url, "", "", "https://example.test")
		want := 200
		if i == 30 {
			want = 429
		}
		if r.Code != want {
			t.Fatal(i, r.Code, r.Body.String())
		}
	}
	password := "Secondary shared reviewer password 2026"
	u, err := s.auth.CreateUser(t.Context(), p, auth.UserInput{Username: "shared-reviewer", Email: "shared@example.test", Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"}}, password)
	if err != nil {
		t.Fatal(err)
	}
	token, other, err := s.auth.AuthenticateContext(t.Context(), "shared-review", u.Username, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.auth.UpdateUser(t.Context(), p, u.ID, u.Version, auth.UserInput{Username: u.Username, Email: u.Email, Status: u.Status, Locale: u.Locale, RoleSlugs: []string{"editor"}}); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	for _, endpoint := range []string{"plugin-shared-records", "plugin-shared-quarantines"} {
		r := serveAdminCommentAPI(h, cookie, other.CSRFToken, http.MethodGet, "https://example.test/api/v1/admin/"+endpoint, "", "", "https://example.test")
		if r.Code != 403 {
			t.Fatal("stale capability read", r.Code)
		}
	}
	if store.calls != 30 {
		t.Fatal("rejected request read storage", store.calls)
	}
}
