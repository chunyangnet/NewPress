package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
)

func waitAIRecords(t *testing.T, s *Server, n int) aieditor.UsageReport {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		report := s.aiTelemetry.Report(aieditor.UsageFilter{Start: "2000-01-01", End: "2100-01-01"})
		if report.Totals.Requests >= int64(n) {
			return report
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("AI worker did not record request")
	return aieditor.UsageReport{}
}
func TestAIUsageAuthorizationLifecycleAndPrivacy(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "usage-author", "usage-author@example.test", "作者", "author")
	for _, item := range []struct {
		cookie *http.Cookie
		status int
	}{{nil, 401}, {author, 403}, {cookie, 200}} {
		response := serveContentAPI(handler, item.cookie, "GET", "https://example.test/api/v1/admin/ai/usage", "", nil)
		if response.Code != item.status {
			t.Fatal("usage authorization", response.Code)
		}
	}
	for _, route := range []string{"/admin/analytics/ai/", "/admin/analytics/ai/requests/"} {
		response := serveCookieRequest(handler, "GET", "https://example.test"+route, cookie, nil)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "chunnel-ai-usage.js") || !strings.Contains(response.Body.String(), "data-ai-usage") {
			t.Fatal("missing usage page", route, response.Code)
		}
	}
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": authorPrincipal.CSRFToken}
	setAIResponse(s, 200, "text/event-stream", aiFrame("# 标题\n\n只属于正文的私密内容。", "stop")+"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140}}\n\ndata: [DONE]\n\n", nil)
	response := serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	if !strings.Contains(response.Body.String(), "event: done") {
		t.Fatal("generation failed", response.Body.String())
	}
	report := waitAIRecords(t, s, 1)
	if report.Totals.Tokens != 140 || report.Records[0].Status != "success" || report.Records[0].FirstTokenMS == nil {
		t.Fatal("usage not captured", report)
	}
	setAIResponse(s, 429, "application/json", `{"error":{"message":"synthetic-test-key","code":"insufficient_quota"}}`, nil)
	serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/generate", aiGenerationJSON(), headers)
	report = waitAIRecords(t, s, 2)
	if report.Totals.Requests != 2 || report.Records[0].Status != "error" || report.Records[0].UpstreamStatus != 429 {
		t.Fatal("provider rejection was lost", report.Records)
	}
	body, _ := json.Marshal(report)
	for _, secret := range []string{"synthetic-test-key", "只属于正文的私密内容", "写一篇完整教程", "writer.example.test"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("sensitive data in usage", secret)
		}
	}
	for _, q := range []string{"start=2025-01-01&end=2026-01-01", "limit=100000", "before=-1", "kind=unknown", "status=oops", "start=wrong", "unknown=1"} {
		r := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/usage?"+q, "", nil)
		if r.Code != 400 {
			t.Fatal("bad query accepted", q, r.Code)
		}
	}
	for _, method := range []string{"POST", "DELETE", "PUT"} {
		r := serveContentAPI(handler, cookie, method, "https://example.test/api/v1/admin/ai/usage", "{}", headers)
		if r.Code != 405 {
			t.Fatal("bad method accepted", r.Code)
		}
	}
	response = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/balance", `{"provider_id":"p"}`, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if response.Code != 403 {
		t.Fatal("balance probe without CSRF")
	}
	response = serveContentAPI(handler, author, "POST", "https://example.test/api/v1/admin/ai/balance", `{"provider_id":"p"}`, headers)
	if response.Code != 403 {
		t.Fatal("author could probe balance")
	}
}
func TestAIUsageCancellationRecordedOnce(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	observation := s.observeAI("", aieditor.Connection{Model: "m"}, "text", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observation.finish(ctx)
	report := waitAIRecords(t, s, 1)
	if report.Totals.Cancelled != 1 || report.Records[0].Status != "cancelled" {
		t.Fatal("cancellation lost")
	}
}

func TestAIUsageStreamCancellationKeepsPartialUsageAndReleasesSlot(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			io.WriteString(writer, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105}}\n\n")
			io.WriteString(writer, aiFrame("尚未结束的正文", ""))
			<-r.Context().Done()
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	})}}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/admin/ai/generate", strings.NewReader(aiGenerationJSON()))
	request.Host = "example.test"
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", principal.CSRFToken)
	request.AddCookie(cookie)
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	var received strings.Builder
	for !strings.Contains(received.String(), "尚未结束的正文") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		received.WriteString(line)
	}
	cancel()
	response.Body.Close()
	report := waitAIRecords(t, s, 1)
	if report.Totals.Requests != 1 || report.Records[0].Status != "cancelled" || report.Totals.Tokens != 105 || report.Records[0].FirstTokenMS == nil {
		t.Fatal("partial cancelled usage lost", report)
	}
	s.aiState.mu.Lock()
	active := s.aiState.active[principal.UserID]
	s.aiState.mu.Unlock()
	if active {
		t.Fatal("cancelled worker retained concurrency slot")
	}
}

func seedAIUsageBrowser(t *testing.T, s *Server) {
	t.Helper()
	config := s.aiSettings.Snapshot()
	ops := aieditor.DefaultTelemetryConfig()
	ops.Balances = true
	config.Telemetry = &ops
	config.DefaultProvider = "writer"
	input, output, cache, flat := 2.0, 8.0, .2, .1
	config.Providers = []aieditor.Provider{
		{ID: "writer", Name: "主力写作", BaseURL: "https://fixture.example.test/v1", APIKey: "synthetic-key", Models: []string{"writer-thinking", "writer-natural"}, Pricing: []aieditor.ModelPrice{{Model: "writer-thinking", Enabled: true, Currency: "USD", Mode: "tokens", Input: &input, Output: &output, CacheRead: &cache, Multiplier: 1}}, Balance: aieditor.BalanceConfig{Enabled: true, Format: "sub2api", Currency: "USD"}},
		{ID: "image", Name: "文章配图", BaseURL: "https://fixture.example.test/v1", Protocol: aieditor.ProtocolOpenAIImages, APIKey: "synthetic-key", Models: []string{"image-fast"}, Pricing: []aieditor.ModelPrice{{Model: "image-fast", Enabled: true, Currency: "CNY", Mode: "image", Flat: &flat, Multiplier: 1}}},
	}
	if _, err := s.aiSettings.Save(config); err != nil {
		t.Fatal(err)
	}
	store, err := aieditor.NewTelemetryStore(filepath.Join(t.TempDir(), "ai-usage.json"), s.aiSettings.Operations)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAITelemetry(store)
	for day := 29; day >= 0; day-- {
		for n := 0; n < (day*7%13)+5; n++ {
			at := time.Now().AddDate(0, 0, -day).Add(-time.Duration(n) * time.Minute)
			input, output, cached, total := 1200+day*200, 400+n*120, 600, 1600+day*200+n*120
			first := int64(650 + n*70)
			r := aieditor.RequestRecord{At: at, Day: at.In(s.siteLocation()).Format("2006-01-02"), ProviderID: "writer", Provider: "主力写作", Model: "writer-thinking", Kind: "text", Status: "success", UserID: 1, DurationMS: int64(3000 + n*400), FirstTokenMS: &first, Usage: aieditor.Usage{PromptTokens: &input, CompletionTokens: &output, CachedTokens: &cached, TotalTokens: &total, TotalSource: "reported"}}
			if n%5 == 0 {
				r.ProviderID, r.Provider, r.Model, r.Kind = "image", "文章配图", "image-fast", "image"
				r.Images = 1
				r.Usage = aieditor.Usage{}
			} else if n%7 == 0 {
				r.Status, r.ErrorCode = "error", "provider_rate_limit"
				r.UpstreamStatus = 429
				r.Usage = aieditor.Usage{}
			}
			_, price := s.aiSettings.Accounting(r.ProviderID, r.Model)
			r.Cost = aieditor.EstimateCost(price, r.Usage, r.Kind, r.Status, r.Images)
			store.Record(r)
		}
	}
	s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":28.56,"unit":"USD","quota":{"limit":100,"used":71.44}}`))}, nil
	})}
	if err = store.Flush(time.Now()); err != nil {
		t.Fatal(err)
	}
}
