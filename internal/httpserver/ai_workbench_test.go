package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/content"
)

func seedWorkbench(t *testing.T, s *Server) {
	t.Helper()
	settings := s.aiSettings.Snapshot()
	settings.DefaultProvider = "workbench"
	settings.Providers = []aieditor.Provider{{ID: "workbench", Name: "报告接口", BaseURL: "https://fixture.example.test/v1", APIKey: "workbench-private-secret", Models: []string{"report-model", "report-slow"}}, {ID: "image-only", Name: "生图", BaseURL: "https://fixture.example.test/v1", APIKey: "image-private-secret", Protocol: aieditor.ProtocolOpenAIImages, Models: []string{"image-fixture"}}}
	if _, err := s.aiSettings.Save(settings); err != nil {
		t.Fatal(err)
	}
	store, err := aiworkbench.NewStore(filepath.Join(t.TempDir(), "ai-reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetAIReports(store)
}

func workbenchHeaders(token string) map[string]string {
	return map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": token}
}
func workbenchTestPreview(t *testing.T, handler http.Handler, cookie *http.Cookie, token, body string) aiworkbench.Preview {
	t.Helper()
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/preview", body, workbenchHeaders(token))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var preview aiworkbench.Preview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}
func workbenchGeneration(token string) string {
	body, _ := json.Marshal(map[string]string{"token": token, "provider_id": "workbench", "model": "report-model"})
	return string(body)
}

func TestAIWorkbenchAuthorizationPreviewAndReadOnlyFacts(t *testing.T) {
	s, handler, manager, repository, cookie, principal := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "workbench-author", "wa@example.test", "作者", "author")
	for _, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		status       int
	}{{nil, "", "https://example.test", 401}, {author, authorPrincipal.CSRFToken, "https://example.test", 403}, {cookie, "", "https://example.test", 403}, {cookie, principal.CSRFToken, "https://other.test", 403}} {
		headers := workbenchHeaders(tc.csrf)
		headers["Origin"] = tc.origin
		response := serveContentAPI(handler, tc.cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/preview", `{"task":"operations","days":7}`, headers)
		if response.Code != tc.status {
			t.Fatal("authorization", response.Code, tc.status)
		}
	}
	response := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/workbench", "", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-secret") || strings.Contains(response.Body.String(), "base_url") || strings.Contains(response.Body.String(), "image-only") {
		t.Fatal("connection projection", response.Body.String())
	}
	page := serveCookieRequest(handler, "GET", "https://example.test/admin/ai/", cookie, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-ai-workbench") || !strings.Contains(page.Body.String(), "chunnel-ai-workbench.js") {
		t.Fatal("workbench page", page.Code)
	}
	for _, task := range []string{"operations", "briefing", "seo", "planning", "costs"} {
		preview := workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"`+task+`","days":7}`)
		for _, secret := range []string{"workbench-private-secret", "api_key", "database_url", "hostname", "csrf_token", "owner@example.test", "\"records\""} {
			if strings.Contains(preview.Context, secret) {
				t.Errorf("%s leaks %s", task, secret)
			}
		}
		if len(preview.Context) > aiworkbench.MaxContextBytes || preview.Start == "" || preview.Timezone == "" {
			t.Fatal("unbounded/undated preview")
		}
	}
	private := createOwnedTestPost(t, repository, principal.UserID, "owner", "private-ai", "私有标题")
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/preview", `{"task":"summary","days":7,"post_id":`+jsonNumber(private.ID)+`}`, workbenchHeaders(principal.CSRFToken))
	if response.Code != 400 || strings.Contains(response.Body.String(), "私有标题") {
		t.Fatal("private article exposed")
	}
	for _, body := range []string{`{"task":"execute","days":7}`, `{"task":"operations","days":7,"api_key":"x"}`, `{"task":"summary","days":7}`, `{"task":"operations","days":0}`} {
		if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/preview", body, workbenchHeaders(principal.CSRFToken)); r.Code != 400 {
			t.Fatal("invalid body accepted", body, r.Code)
		}
	}
	if r := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/workbench?unknown=1", "", nil); r.Code != 400 {
		t.Fatal("unknown query accepted")
	}
}

func jsonNumber(id int64) string { b, _ := json.Marshal(id); return string(b) }

func TestAIWorkbenchGenerationPersistenceIsolationAndReplay(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	_, other, otherPrincipal := createAndLoginTestUser(t, manager, principal, "workbench-editor", "we@example.test", "编辑", "editor")
	preview := workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"summary","days":7,"material":"原始材料 ONLY_SNAPSHOT","focus":"提炼重点"}`)
	calls := 0
	setAIResponse(s, 200, "text/event-stream", aiFrame("## 总结\n\n原始材料的关键事实。", "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "ONLY_SNAPSHOT") || !strings.Contains(string(raw), "不可信") {
			t.Error("wrong source or system boundary")
		}
	})
	response := serveContentAPI(handler, other, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), workbenchHeaders(otherPrincipal.CSRFToken))
	if response.Code != 409 || calls != 0 {
		t.Fatal("other account consumed preview")
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), workbenchHeaders(principal.CSRFToken))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if aiDone(t, response.Body.String())["saved"] != true {
		t.Fatal("report not saved")
	}
	report := waitAIRecords(t, s, 1)
	if report.Records[0].Purpose != "summary" || report.Records[0].Kind != "text" {
		t.Fatal("usage purpose missing")
	}
	history := s.aiReports.List(principal.UserID, time.Now())
	if len(history) != 1 {
		t.Fatal("missing report")
	}
	id := history[0].ID
	for _, method := range []string{"GET", "DELETE"} {
		response = serveContentAPI(handler, other, method, "https://example.test/api/v1/admin/ai/workbench/reports/"+id, "", workbenchHeaders(otherPrincipal.CSRFToken))
		if response.Code != 404 {
			t.Fatal("cross account report access", method, response.Code)
		}
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), workbenchHeaders(principal.CSRFToken))
	if response.Code != 409 || calls != 1 {
		t.Fatal("preview replay")
	}
	response = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/workbench/reports/"+id, "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "ONLY_SNAPSHOT") || strings.Contains(response.Body.String(), preview.Token) {
		t.Fatal("report evidence missing or token retained")
	}
	response = serveContentAPI(handler, cookie, "DELETE", "https://example.test/api/v1/admin/ai/workbench/reports/"+id, "", workbenchHeaders(principal.CSRFToken))
	if response.Code != 200 || len(s.aiReports.List(principal.UserID, time.Now())) != 0 {
		t.Fatal("delete failed")
	}
}

func TestAIWorkbenchUpstreamFailureAndCancellation(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	preview := workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"summary","days":7,"material":"材料"}`)
	setAIResponse(s, 429, "application/json", `{"error":"secret upstream details"}`, nil)
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), workbenchHeaders(principal.CSRFToken))
	if !strings.Contains(response.Body.String(), "event: error") || strings.Contains(response.Body.String(), "secret upstream details") || len(s.aiReports.List(principal.UserID, time.Now())) != 0 {
		t.Fatal("failure leaked or saved", response.Body.String())
	}
	waitAIRecords(t, s, 1)
	preview = workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"summary","days":7,"material":"材料"}`)
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			_, _ = io.WriteString(writer, aiFrame("部分报告", ""))
			<-r.Context().Done()
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	})}}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/admin/ai/workbench/generate", strings.NewReader(workbenchGeneration(preview.Token)))
	r.Host = "example.test"
	for k, v := range workbenchHeaders(principal.CSRFToken) {
		r.Header.Set(k, v)
	}
	r.AddCookie(cookie)
	client := server.Client()
	client.Timeout = 5 * time.Second
	stream, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stream.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "部分报告") {
			break
		}
	}
	cancel()
	stream.Body.Close()
	report := waitAIRecords(t, s, 2)
	if report.Records[0].Status != "cancelled" || len(s.aiReports.List(principal.UserID, time.Now())) != 0 {
		t.Fatal("cancellation not honored")
	}
	s.aiState.mu.Lock()
	active := s.aiState.active[principal.UserID]
	s.aiState.mu.Unlock()
	if active {
		t.Fatal("generation slot leaked")
	}
}

func TestAIWorkbenchIncompleteBusySaveFailureAndModuleGate(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	headers := workbenchHeaders(principal.CSRFToken)
	preview := workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"summary","days":7,"material":"材料"}`)
	if !s.aiState.acquire(principal.UserID) {
		t.Fatal("could not acquire test slot")
	}
	r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), headers)
	if r.Code != 429 {
		t.Fatal("busy request was accepted")
	}
	s.aiState.release(principal.UserID)
	setAIResponse(s, 200, "text/event-stream", aiFrame("未完成的报告", "length")+"data: [DONE]\n\n", nil)
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), headers)
	if !strings.Contains(r.Body.String(), "event: error") || len(s.aiReports.List(principal.UserID, time.Now())) != 0 {
		t.Fatal("incomplete output saved")
	}
	waitAIRecords(t, s, 1)
	path := filepath.Join(t.TempDir(), "ai-reports.json")
	brokenStore, err := aiworkbench.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s.SetAIReports(brokenStore)
	preview = workbenchTestPreview(t, handler, cookie, principal.CSRFToken, `{"task":"summary","days":7,"material":"材料"}`)
	setAIResponse(s, 200, "text/event-stream", aiFrame("完整的报告", "stop")+"data: [DONE]\n\n", nil)
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/workbench/generate", workbenchGeneration(preview.Token), headers)
	done := aiDone(t, r.Body.String())
	if done["saved"] != false || done["warning"] == nil || len(brokenStore.List(principal.UserID, time.Now())) != 0 {
		t.Fatal("save failure reported as saved")
	}
	snapshot := s.siteSettings.Snapshot()
	body, _ := json.Marshal(map[string]any{"revision": snapshot.Revision, "modules": map[string]bool{"ai": false}})
	r = serveContentAPI(handler, cookie, "PATCH", "https://example.test/api/v1/admin/modules", string(body), headers)
	if r.Code != 200 {
		t.Fatal("disable module", r.Code, r.Body.String())
	}
	for _, path := range []string{"/admin/ai/", "/api/v1/admin/ai/workbench", "/api/v1/admin/ai/workbench/reports"} {
		r = serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if path == "/admin/ai/" {
			if r.Code != 200 || !strings.Contains(r.Body.String(), "data-feature-status") || strings.Contains(r.Body.String(), "data-ai-workbench") {
				t.Fatal("disabled workbench must show only authorized recovery", r.Code)
			}
			continue
		}
		if r.Code != 404 {
			t.Fatal("disabled workbench still exposed", path, r.Code)
		}
	}
}

func TestAIWorkbenchBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_AI_WORKBENCH_BROWSER") != "1" {
		t.Skip("opt-in synthetic browser fixture")
	}
	s, _, _, repository, _, principal := newPersistentAdminTestServer(t)
	seedAIUsageBrowser(t, s)
	reports, reportErr := aiworkbench.NewStore(filepath.Join(t.TempDir(), "ai-reports.json"))
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	s.SetAIReports(reports)
	s.config.PublicURL = "http://127.0.0.1:19428"
	s.config.AnalyticsEnabled = true
	settings := s.aiSettings.Snapshot()
	settings.Providers[0].Models = append(settings.Providers[0].Models, "report-slow")
	if _, err := s.aiSettings.Save(settings); err != nil {
		t.Fatal(err)
	}
	_, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "如何维护一份可靠的站点备份", Slug: "reliable-backups", Excerpt: "建立备份、验证恢复过程，并记录每次演练。", ContentSource: "## 备份范围\n保存文章、图片及部署配置。定期在独立目录恢复，核对内容与附件。", AuthorID: principal.UserID, AuthorName: "测试编辑", PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Model    string             `json:"model"`
			Messages []aieditor.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		text := "## 本次结论\n\n站点核心服务可读取。下面的建议基于本次快照，建议先检查备份恢复记录，再处理内容与访问数据中的缺口。\n\n## 优先处理\n\n1. **核对服务状态**：在系统状态页检查未通过的服务，并查看对应日志。\n2. **验证数据恢复**：现有资料不含备份结果，不能据此判断备份是否可用。\n3. **持续观察趋势**：当前资源指标为单次采样，结合后续记录确认是否存在持续负载。\n\n## 验证方式\n\n记录处理前后的就绪结果，重复执行相关检查。\n\n<img src=x onerror=alert(1)> 这段模拟文本只应显示为文字。"
		text += "\n\n## 分类统计\n\n| 分类 | 数量 | 文章 ID |\n|---|---:|---|\n| 项目日志 | 3 | ID1、ID4、ID6 |\n| 主题设计 | 3 | ID2、ID5、ID8 |\n| 扩展架构 | 3 | ID3、ID7、ID9 |\n\n> **样本说明**：*仅包含公开文章*，~~旧口径~~以本次快照为准。\n\n---\n\n- [x] 核对分类\n- [ ] 补充材料\n\n[管理后台](/admin/) · [不安全链接](javascript:alert(1)) · ![外部图片](https://tracker.invalid/pixel)\n\n~~~text\n| 原始代码 | 不渲染为表格 |\n~~~"
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			runes := []rune(text)
			for i := 0; i < len(runes); i += 12 {
				delay := 50 * time.Millisecond
				if request.Model == "report-slow" {
					delay = 500 * time.Millisecond
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
				if _, err := io.WriteString(writer, aiFrame(string(runes[i:min(i+12, len(runes))]), "")); err != nil {
					return
				}
			}
			_, _ = io.WriteString(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":820,\"completion_tokens\":360,\"total_tokens\":1180}}\n\n"+aiFrame("", "stop")+"data: [DONE]\n\n")
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	})}}
	listener, err := net.Listen("tcp", "127.0.0.1:19428")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Log("Synthetic workbench ready at http://127.0.0.1:19428; owner / Persistent owner password 2026")
	select {
	case <-t.Context().Done():
	case <-time.After(25 * time.Minute):
	}
}
