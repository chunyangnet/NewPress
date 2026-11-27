package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

func newReviewedTaskHTTPTestServer(t *testing.T) (*Server, http.Handler, *http.Cookie, auth.Principal) {
	t.Helper()
	s, _, _, _, _ := newCommentHTTPTestServer(t)
	users, err := auth.NewFileUserStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	password := "Persistent task review password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.NewWithContext(t.Context(), auth.Config{
		Username: "reviewer", PasswordHash: hash, Users: users, BootstrapEmail: "reviewer@example.test",
		AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, principal, err := s.auth.AuthenticateContext(t.Context(), "task-review-tests", "reviewer", password)
	if err != nil || principal.UserID < 1 {
		t.Fatal("persistent reviewer login", err)
	}
	return s, s.routes(), &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}, principal
}

func TestPluginTaskHTTPReviewedReissueAndEpochChangeReplay(t *testing.T) {
	s, handler, cookie, principal := newReviewedTaskHTTPTestServer(t)
	installTaskFixture(t, s)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	now := time.Now().UTC().Add(-time.Hour)
	source, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-original-key", ActorID: principal.UserID, Payload: json.RawMessage(`{"private":"old task payload"}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Archive(t.Context(), pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	before, _ := backend.Read(t.Context())
	sourceVersion := before.ArchivedJobs[source.ID].Version
	base := "https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + source.ID
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base+"/review", "", "", "https://example.test")
	var details struct {
		pluginjobs.ReviewDetails
		CurrentUserID int64 `json:"current_user_id"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &details) != nil || !details.Reissuable || details.Task.ID != source.ID || details.Task.ArchivedAt.IsZero() || details.CurrentUserID != principal.UserID {
		t.Fatal("review archived source", response.Code, response.Body.String())
	}
	for _, private := range []string{"private-original-key", "old task payload", `"payload"`, `"epoch"`, `"actor_id"`, `"fingerprint"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("review exposed %s", private)
		}
	}
	body := fmt.Sprintf(`{"expected_user_id":%d,"source_version":%d,"kind":"task.execute.v1","key":"stable-reissue","payload":{"reviewed":"new parameters"},"reason":"checked prior business results","confirmed":true}`, principal.UserID, sourceVersion)
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/reissues", body, "", "https://example.test")
	var child pluginjobs.Summary
	if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &child) != nil || child.ID == source.ID || child.Reissue == nil || child.Reissue.SourceID != source.ID {
		t.Fatal("reissue response", response.Code, response.Body.String())
	}
	for _, private := range []string{"checked prior business results", "new parameters", `"reviewer_id"`, `"payload"`, `"actor_id"`, `"epoch"`, `"request_fingerprint"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("reissue summary exposed %s", private)
		}
	}
	state, err := backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(before.ArchivedJobs[source.ID], state.ArchivedJobs[source.ID]) || len(state.Jobs) != 1 {
		t.Fatal("reissue changed source", err)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, strings.Replace(base, source.ID, child.ID, 1)+"/review", "", "", "https://example.test")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &details) != nil || details.Provenance == nil || details.Provenance.ReviewerID != principal.UserID || details.Provenance.Reason != "checked prior business results" {
		t.Fatal("dedicated review lost provenance", response.Code, response.Body.String())
	}
	if err = s.plugins.SetEnabled("task-fixture", false); err != nil {
		t.Fatal(err)
	}
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/reissues", body, "", "https://example.test")
	var replay pluginjobs.Summary
	if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &replay) != nil || replay.ID != child.ID {
		t.Fatal("disabled epoch lost accepted command", response.Code, response.Body.String())
	}
	newBody := strings.Replace(body, "stable-reissue", "fresh-disabled-reissue", 1)
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/reissues", newBody, "", "https://example.test")
	if response.Code != 409 {
		t.Fatal("disabled plugin admitted new work", response.Code, response.Body.String())
	}
	state, _ = backend.Read(t.Context())
	if len(state.Jobs) != 1 {
		t.Fatal("replay or denied admission created extra work")
	}
}

func TestPluginTaskHTTPReissueRequiresIdentityAndExplicitReview(t *testing.T) {
	s, handler, cookie, principal := newReviewedTaskHTTPTestServer(t)
	installTaskFixture(t, s)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	now := time.Now().UTC().Add(-time.Hour)
	source, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "source", ActorID: principal.UserID, Payload: json.RawMessage(`{}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	state, _ := backend.Read(t.Context())
	version := state.Jobs[source.ID].Version
	url := "https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + source.ID + "/reissues"
	body := fmt.Sprintf(`{"expected_user_id":%d,"source_version":%d,"kind":"task.execute.v1","key":"new","payload":{},"reason":"checked results","confirmed":true}`, principal.UserID, version)
	for _, tc := range []struct {
		name, csrf, origin, body string
		want                     int
	}{
		{"csrf", "", "https://example.test", body, 403},
		{"origin", principal.CSRFToken, "https://other.test", body, 403},
		{"identity", principal.CSRFToken, "https://example.test", strings.Replace(body, fmt.Sprintf(`"expected_user_id":%d`, principal.UserID), `"expected_user_id":999999`, 1), 409},
		{"confirmation", principal.CSRFToken, "https://example.test", strings.Replace(body, `"confirmed":true`, `"confirmed":false`, 1), 422},
		{"missing payload", principal.CSRFToken, "https://example.test", strings.Replace(body, `"payload":{},`, "", 1), 422},
		{"reason", principal.CSRFToken, "https://example.test", strings.Replace(body, "checked results", "", 1), 422},
		{"stale version", principal.CSRFToken, "https://example.test", strings.Replace(body, fmt.Sprintf(`"source_version":%d`, version), `"source_version":1`, 1), 409},
		{"other hook", principal.CSRFToken, "https://example.test", strings.Replace(body, "task.execute.v1", "event.deliver.v1", 1), 409},
		{"unknown field", principal.CSRFToken, "https://example.test", strings.Replace(body, `"confirmed":true`, `"confirmed":true,"unexpected":1`, 1), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := serveAdminCommentAPI(handler, cookie, tc.csrf, http.MethodPost, url, tc.body, "", tc.origin)
			if response.Code != tc.want {
				t.Fatal("boundary", response.Code, response.Body.String())
			}
		})
	}
	for _, reviewURL := range []string{
		"https://example.test/api/v1/admin/plugins/other/tasks/" + source.ID + "/review",
		"https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + source.ID + "/review?unexpected=1",
	} {
		response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, reviewURL, "", "", "https://example.test")
		if response.Code < 400 || response.Code >= 500 {
			t.Fatal("invalid review scope accepted", response.Code)
		}
	}
	after, _ := backend.Read(t.Context())
	if !reflect.DeepEqual(state, after) {
		t.Fatal("rejected review or reissue modified tasks")
	}
	response := serveCommentRequest(handler, nil, http.MethodGet, strings.TrimSuffix(url, "/reissues")+"/review", "", "https://example.test", "127.0.0.1:54321")
	if response.Code != 401 {
		t.Fatal("anonymous review returned private metadata", response.Code)
	}
}

func TestPluginTaskHTTPReissueSourceVersionConflictIsAtomic(t *testing.T) {
	s, handler, cookie, principal := newReviewedTaskHTTPTestServer(t)
	installTaskFixture(t, s)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(q)
	now := time.Now().UTC().Add(-time.Hour)
	source, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "failed-source", ActorID: principal.UserID, Payload: json.RawMessage(`{}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.Update(t.Context(), func(s *pluginjobs.State) error {
		j := s.Jobs[source.ID]
		j.Status = "failed"
		s.Jobs[j.ID] = j
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := "https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + source.ID
	response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodGet, base+"/review", "", "", "https://example.test")
	if response.Code != 200 {
		t.Fatal("initial source review", response.Code)
	}
	if err = q.Change(t.Context(), source.Plugin, source.ID, source.Version, "retry", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	before, err := backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_user_id":%d,"source_version":%d,"kind":"task.execute.v1","key":"reviewed","payload":{},"reason":"checked current business","confirmed":true}`, principal.UserID, source.Version)
	response = serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, base+"/reissues", body, "", "https://example.test")
	if response.Code != 409 {
		t.Fatal("source version was silently replaced", response.Code, response.Body.String())
	}
	state, _ := backend.Read(t.Context())
	if !reflect.DeepEqual(state, before) || len(state.Jobs) != 1 || state.Jobs[source.ID].Status != "pending" {
		t.Fatal("stale review created work")
	}
}

func TestPluginTaskHTTPReviewRejectsBootstrapAndRevokedCapabilities(t *testing.T) {
	t.Run("bootstrap identity", func(t *testing.T) {
		_, handler, _, cookie, principal := newCommentHTTPTestServer(t)
		base := "https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + strings.Repeat("a", 64)
		for _, tc := range []struct{ method, suffix string }{{http.MethodGet, "/review"}, {http.MethodPost, "/reissues"}} {
			response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, tc.method, base+tc.suffix, "", "", "https://example.test")
			if response.Code != 403 || !strings.Contains(response.Body.String(), "plugins.reissue_account") {
				t.Fatal("bootstrap identity was not clearly rejected", response.Code, response.Body.String())
			}
		}
	})
	t.Run("capability refresh", func(t *testing.T) {
		s, handler, _, owner := newReviewedTaskHTTPTestServer(t)
		password := "Secondary task reviewer password 2026"
		user, err := s.auth.CreateUser(t.Context(), owner, auth.UserInput{
			Username: "secondary", Email: "secondary@example.test", Status: auth.UserStatusActive,
			Locale: "zh-CN", RoleSlugs: []string{"administrator"},
		}, password)
		if err != nil {
			t.Fatal(err)
		}
		token, principal, err := s.auth.AuthenticateContext(t.Context(), "secondary-review", user.Username, password)
		if err != nil || !principal.Can(auth.CapabilityPluginsManage) {
			t.Fatal("secondary administrator login", err)
		}
		if _, err = s.auth.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{
			Username: user.Username, Email: user.Email, Status: user.Status, Locale: user.Locale, RoleSlugs: []string{"editor"},
		}); err != nil {
			t.Fatal(err)
		}
		cookie := &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
		base := "https://example.test/api/v1/admin/plugins/task-fixture/tasks/" + strings.Repeat("a", 64)
		for _, tc := range []struct{ method, suffix string }{{http.MethodGet, "/review"}, {http.MethodPost, "/reissues"}} {
			response := serveAdminCommentAPI(handler, cookie, principal.CSRFToken, tc.method, base+tc.suffix, "", "", "https://example.test")
			if response.Code != 403 {
				t.Fatal("old session retained plugin management capability", response.Code, response.Body.String())
			}
		}
	})
}
