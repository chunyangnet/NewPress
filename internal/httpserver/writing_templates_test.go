package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/writingtemplate"
)

func TestWritingTemplateAuthorizationAndGenerationVersion(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "template-author", "template-author@example.test", "作者", "author")
	_, reader, _ := createAndLoginTestUser(t, manager, principal, "template-reader", "template-reader@example.test", "读者", "subscriber")
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken, "Idempotency-Key": "new-template-1"}
	body := `{"action":"save","definition":{"base_prompt":"rewrite","title":"团队润色","description":"核实后修改表达","instruction":"保留全部事实、数字与引用。","example":"整理当前草稿"}}`
	for _, item := range []struct {
		cookie *http.Cookie
		status int
	}{{nil, 401}, {reader, 403}, {author, 403}} {
		response := serveContentAPI(handler, item.cookie, "GET", "https://example.test/api/v1/admin/ai/templates", "", nil)
		if response.Code != item.status {
			t.Fatal("management read permission", response.Code)
		}
	}
	authorHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": authorPrincipal.CSRFToken, "Idempotency-Key": "author-template"}
	if response := serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/templates", body, authorHeaders); response.Code != 403 {
		t.Fatal("author modified team templates", response.Code)
	}
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", body, headers)
	var created writingtemplate.Result
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &created) != nil || created.Version != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", body, headers)
	if response.Code != 200 || response.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, author, "GET", "https://example.test/api/v1/admin/ai/prompts", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":"team:`+created.ID+`"`) || !strings.Contains(response.Body.String(), `"template_version":1`) || !strings.Contains(response.Body.String(), `"needs_draft":true`) || strings.Contains(response.Body.String(), `"actor"`) {
		t.Fatal("author catalog", response.Code, response.Body.String())
	}
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("<newpress-meta>{\"title\":\"草稿\"}</newpress-meta>\n保留事实。", "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		calls++
		payload, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(payload), "保留全部事实、数字与引用。") {
			t.Error("template instruction was not applied")
		}
	})
	var generation map[string]any
	if err := json.Unmarshal([]byte(aiGenerationJSON()), &generation); err != nil {
		t.Fatal(err)
	}
	generation["prompt_id"], generation["template_version"] = "team:"+created.ID, 1
	encoded, _ := json.Marshal(generation)
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", string(encoded), authorHeaders)
	if response.Code != 400 || calls != 0 {
		t.Fatal("team mode bypassed required draft", response.Code, calls, response.Body.String())
	}
	generation["draft"] = map[string]string{"title": "原稿", "content": "原有事实和引用。"}
	encoded, _ = json.Marshal(generation)
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", string(encoded), authorHeaders)
	if response.Code != 200 || calls != 1 || !strings.Contains(response.Body.String(), "event: done") {
		t.Fatal("team generation", response.Code, calls, response.Body.String())
	}
	headers["Idempotency-Key"] = "archive-template"
	archive, _ := json.Marshal(map[string]any{"action": "archive", "id": created.ID, "expected_version": 1})
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(archive), headers)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", string(encoded), authorHeaders)
	if response.Code != 409 || calls != 1 || !strings.Contains(response.Body.String(), "ai.template_archived") {
		t.Fatal("archived generation sent", response.Code, calls)
	}
	headers["Idempotency-Key"] = "activate-template"
	activate, _ := json.Marshal(map[string]any{"action": "activate", "id": created.ID, "expected_version": 2})
	if response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(activate), headers); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", string(encoded), authorHeaders)
	if response.Code != 409 || calls != 1 || !strings.Contains(response.Body.String(), "ai.template_conflict") {
		t.Fatal("stale generation sent", response.Code, calls)
	}
	generation["template_version"] = 3
	encoded, _ = json.Marshal(generation)
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", string(encoded), authorHeaders)
	if response.Code != 200 || calls != 2 {
		t.Fatal("current generation refused", response.Code, calls)
	}
}

func TestWritingTemplateSettingsOnlyManager(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	store, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	role := auth.Role{Slug: "template-manager", Name: "模板管理员", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage}}
	if err = store.EnsureRoles(t.Context(), append(auth.BuiltInRoles(), role)); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Template owner password 2026")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: store, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := manager.AuthenticateContext(t.Context(), "template-test", "owner", "Template owner password 2026")
	if err != nil {
		t.Fatal(err)
	}
	_, cookie, principal := createAndLoginTestUser(t, manager, owner, "template-manager", "template-manager@example.test", "模板管理员", "template-manager")
	s.auth = manager
	handler := s.routes()
	for _, target := range []string{"/api/v1/admin/ai/prompts", "/api/v1/admin/ai/templates"} {
		response := serveContentAPI(handler, cookie, "GET", "https://example.test"+target, "", nil)
		if response.Code != 200 {
			t.Fatal(target, response.Code, response.Body.String())
		}
	}
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken, "Idempotency-Key": "settings-only-template"}
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", `{"action":"save","definition":{"base_prompt":"article","title":"仅管理权限","instruction":"保留事实"}}`, headers)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	if response.Code != 403 {
		t.Fatal("settings-only manager gained generation permission", response.Code)
	}
}

func TestWritingTemplateImportRequiresPreviewDigestAndConfirmation(t *testing.T) {
	_, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken, "Idempotency-Key": "import-template"}
	doc := `{"schema":1,"kind":"newpress.writing-template","template_id":"11111111111111111111111111111111","version":7,"definition":{"base_prompt":"article","title":"导入范本","description":"来源版本七","instruction":"保留来源事实","example":"说明具体内容"}}`
	previewBody, _ := json.Marshal(map[string]string{"document": doc})
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/template-import-preview", string(previewBody), headers)
	var preview writingtemplate.ImportPreview
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &preview) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	request := map[string]any{"action": "import", "document": doc, "import_sha256": preview.SHA256}
	body, _ := json.Marshal(request)
	if response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(body), headers); response.Code != 400 {
		t.Fatal("unconfirmed import accepted", response.Code)
	}
	request["confirm_import"] = true
	request["import_sha256"] = strings.Repeat("0", 64)
	body, _ = json.Marshal(request)
	if response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(body), headers); response.Code != 400 {
		t.Fatal("wrong import digest", response.Code)
	}
	request["import_sha256"] = preview.SHA256
	body, _ = json.Marshal(request)
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(body), headers)
	var result writingtemplate.Result
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.ID == preview.Document.TemplateID || result.Version != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/templates/"+result.ID, "", nil)
	var item writingtemplate.Template
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &item) != nil || item.Revisions[0].Source.Version != 7 || item.Revisions[0].Source.SHA256 != preview.SHA256 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/templates/"+result.ID+"/export?version=1", "", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), `"actor"`) || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || response.Header().Get("Content-Disposition") == "" {
		t.Fatal("export", response.Code, response.Header())
	}
	for _, target := range []string{"templates?limit=0", "templates?unknown=1", "templates?offset=0&offset=1"} {
		if response = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/"+target, "", nil); response.Code != 400 {
			t.Fatal(target, response.Code)
		}
	}
	for _, extra := range []string{`,"actor":"forged"`, `,"definition":{},"definition":{}`} {
		bad := strings.TrimSuffix(string(body), "}") + extra + "}"
		if response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", bad, headers); response.Code != 400 {
			t.Fatal("ambiguous or forged input", response.Code, response.Body.String())
		}
	}
	delete(headers, "X-CSRF-Token")
	if response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/templates", string(body), headers); response.Code != 403 {
		t.Fatal("CSRF missing", response.Code)
	}
}
