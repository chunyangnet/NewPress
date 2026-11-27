package aieditor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func opInt(n int) *int           { return &n }
func opFloat(n float64) *float64 { return &n }
func TestOperationPricesPreserveUnknownAndCacheAccounting(t *testing.T) {
	price := &ModelPrice{Model: "writer", Enabled: true, Currency: "USD", Mode: "tokens", Input: opFloat(2), Output: opFloat(8), CacheRead: opFloat(.2), CacheWrite: opFloat(2.5), Multiplier: 1}
	usage := Usage{PromptTokens: opInt(1000000), CompletionTokens: opInt(500000), CachedTokens: opInt(300000), CacheCreationTokens: opInt(100000), ReasoningTokens: opInt(400000)}
	result := EstimateCost(price, usage, "text", "success", 0)
	if result.Amount == nil || math.Abs(*result.Amount-5.51) > 1e-10 || !result.Complete {
		t.Fatalf("cache/reasoning double count: %+v", result)
	}
	usage.CompletionTokens = nil
	result = EstimateCost(price, usage, "text", "error", 0)
	if result.Amount == nil || math.Abs(*result.Amount-1.51) > 1e-10 || result.Complete {
		t.Fatalf("missing output: %+v", result)
	}
	result = EstimateCost(price, Usage{}, "text", "success", 0)
	if result.Amount != nil || result.Complete {
		t.Fatal("unknown became free")
	}
	usage = Usage{PromptTokens: opInt(0), CompletionTokens: opInt(0)}
	result = EstimateCost(price, usage, "text", "success", 0)
	if result.Amount == nil || *result.Amount != 0 || !result.Complete {
		t.Fatal("real zero lost")
	}
	usage.CachedTokens = opInt(1)
	if EstimateCost(price, usage, "text", "success", 0).Amount != nil {
		t.Fatal("inconsistent cache was priced")
	}
	price.Mode, price.Flat = "image", opFloat(.15)
	result = EstimateCost(price, Usage{}, "image", "error", 2)
	if result.Amount == nil || *result.Amount != .3 || result.Complete {
		t.Fatal("generated but not saved images lost", result)
	}
	price.Mode = "request"
	if EstimateCost(price, Usage{}, "text", "error", 0).Amount != nil {
		t.Fatal("failed request treated as known charged request")
	}
}

func TestOperationsSettingsRoundTripRedactionAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	s, _ := NewSettingsStore(path)
	config := DefaultTelemetryConfig()
	config.Balances = true
	input := Settings{Revision: s.Snapshot().Revision, DefaultProvider: "p", Telemetry: &config, Providers: []Provider{{ID: "p", Name: "Test", BaseURL: "https://api.example.test/v1", APIKey: "private-generation-key", Models: []string{"m"}, Pricing: []ModelPrice{{Model: "m", Enabled: true, Input: opFloat(2), Output: opFloat(3)}}, Balance: BalanceConfig{Enabled: true, Format: "newapi", Token: "private-query-token", UserID: "17"}}}}
	saved, err := s.Save(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "private-") || !saved.Providers[0].Balance.HasToken {
		t.Fatal("token leaked or missing key state")
	}
	*saved.Providers[0].Pricing[0].Input = 999
	name, price := s.Accounting("p", "m")
	if name != "Test" || *price.Input != 2 {
		t.Fatal("snapshot aliases internal price")
	}
	saved = s.Snapshot()
	saved.Telemetry = nil
	if _, err = s.Save(saved); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := reopened.balanceProvider("p")
	if err != nil || provider.Balance.Token != "private-query-token" {
		t.Fatal("secret or feature flag lost")
	}
	for _, badPath := range []string{"https://attacker.test/balance", "//attacker.test/balance", "/../balance", "/balance?key=secret", "/x%2f..", "/x\\evil"} {
		input = reopened.Snapshot()
		input.Providers[0].Balance.Path = badPath
		if _, err = reopened.Save(input); err == nil {
			t.Fatalf("unsafe path accepted: %s", badPath)
		}
	}
	input = reopened.Snapshot()
	input.Providers[0].BaseURL = "https://different.example.test"
	input.Providers[0].APIKey = "new-key"
	if _, err = reopened.Save(input); err == nil {
		t.Fatal("balance token followed changed origin")
	}
	input = reopened.Snapshot()
	input.Providers[0].Pricing[0].Input = opFloat(-1)
	if _, err = reopened.Save(input); err == nil {
		t.Fatal("negative price saved")
	}
	input = reopened.Snapshot()
	input.Telemetry.RetentionDays = 365
	if _, err = reopened.Save(input); err == nil {
		t.Fatal("unbounded retention saved")
	}
}

func TestBalanceFormatsAndUntrustedResponses(t *testing.T) {
	tests := []struct {
		format, body, currency string
		remaining              float64
	}{
		{"deepseek", `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"2.5"},{"currency":"CNY","total_balance":"8.5"}]}`, "CNY", 8.5},
		{"siliconflow", `{"data":{"totalBalance":"0"}}`, "CNY", 0},
		{"openrouter", `{"data":{"total_credits":10,"total_usage":1.5}}`, "USD", 8.5},
		{"newapi", `{"success":true,"data":{"quota":1500000,"used_quota":500000}}`, "USD", 3},
		{"sub2api", `{"isValid":true,"remaining":12,"unit":"USD","quota":{"limit":100,"used":88}}`, "USD", 12},
		{"custom", `{"data":[{"left":"-2.5"}]}`, "USD", -2.5},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			b := BalanceConfig{Format: test.format, Currency: test.currency, RemainingPath: "data.0.left"}
			result, err := parseBalance([]byte(test.body), b)
			if err != nil || result.Remaining == nil || *result.Remaining != test.remaining || result.Currency != test.currency {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"isValid":false,"remaining":10}`, `{"remaining":"NaN"}`, `{"remaining":null}`, `{"remaining":4} {}`, `{"remaining":"<script>"}`} {
		if _, err := parseBalance([]byte(body), BalanceConfig{Format: "sub2api", Currency: "USD"}); err == nil {
			t.Fatal("invalid data accepted", body)
		}
	}
}

func TestImageUsageAdaptersAndFailedDownloadPreserveReportedUsage(t *testing.T) {
	_, encoded := imageFixture(t)
	for _, test := range []struct{ protocol, body string }{
		{ProtocolOpenAIImages, `{"usage":{"input_tokens":10,"output_tokens":30,"total_tokens":40},"data":[{"b64_json":"` + encoded + `"}]}`},
		{ProtocolGemini, `{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":25,"thoughtsTokenCount":5,"totalTokenCount":40},"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}`},
		{ProtocolGeminiInteractions, `{"usage":{"total_input_tokens":10,"total_output_tokens":30,"total_tokens":40},"outputs":[{"type":"image","data":"` + encoded + `","mime_type":"image/png"}]}`},
		{ProtocolSeedream, `{"usage":{"prompt_tokens":10,"completion_tokens":30,"total_tokens":40},"data":[{"b64_json":"` + encoded + `"}]}`},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			var usage Usage
			var images int
			client := &ImageClient{HTTP: &http.Client{Transport: imageTransport(func(*http.Request) (*http.Response, error) { return imageResponse(test.body), nil })}}
			_, err := client.GenerateObserved(t.Context(), Connection{BaseURL: "https://images.example.test", APIKey: "test", Model: "image-model", Protocol: test.protocol}, ImageGeneration{Prompt: "测试画面", AspectRatio: "1:1"}, func(u Usage, n int) { usage, images = u, n })
			if err != nil || images != 1 || usage.TotalTokens == nil || *usage.TotalTokens != 40 || usage.CompletionTokens == nil || *usage.CompletionTokens != 30 {
				t.Fatal("image usage missing", usage, images, err)
			}
		})
	}
	client := &ImageClient{HTTP: &http.Client{Transport: imageTransport(func(*http.Request) (*http.Response, error) {
		return imageResponse(`{"usage":{"input_tokens":10,"output_tokens":30,"total_tokens":40},"data":[{"url":"http://127.0.0.1/private"}]}`), nil
	})}}
	var usage Usage
	var images int
	_, err := client.GenerateObserved(t.Context(), Connection{BaseURL: "https://images.example.test", APIKey: "test", Model: "image-model", Protocol: ProtocolOpenAIImages}, ImageGeneration{Prompt: "测试画面", AspectRatio: "1:1"}, func(u Usage, n int) { usage, images = u, n })
	if err == nil || images != 1 || usage.TotalTokens == nil || *usage.TotalTokens != 40 {
		t.Fatal("reported image cost lost on download failure")
	}
}

func TestBalanceRedirectAndRawErrorNotExposed(t *testing.T) {
	service := NewBalanceService()
	for _, status := range []int{302, 401, 500} {
		service.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://unrelated.test/"}}, Body: io.NopCloser(strings.NewReader("private-credential"))}, nil
		})}
		_, err := service.fetch(t.Context(), Provider{BaseURL: "https://api.example.test/v1", APIKey: "private-credential", Balance: BalanceConfig{Format: "sub2api", Currency: "USD"}})
		if err == nil || strings.Contains(err.Error(), "private-credential") {
			t.Fatal("rejected response exposed")
		}
	}
}
func TestBalanceConcurrentQueriesCacheAndConfigInvalidation(t *testing.T) {
	settings, _ := NewSettingsStore("")
	config := DefaultTelemetryConfig()
	config.Balances = true
	_, err := settings.Save(Settings{Revision: settings.Snapshot().Revision, DefaultProvider: "p", Telemetry: &config, Providers: []Provider{{ID: "p", Name: "Test", BaseURL: "https://api.example.test/v1", APIKey: "synthetic", Models: []string{"m"}, Balance: BalanceConfig{Enabled: true, Format: "sub2api", CacheSeconds: 60}}}})
	if err != nil {
		t.Fatal(err)
	}
	service := NewBalanceService()
	var calls atomic.Int32
	service.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://api.example.test/v1/usage" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("wrong target/auth")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":20,"unit":"USD"}`))}, nil
	})}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			result, err := service.Query(context.Background(), settings, "p")
			if err != nil || result.Remaining == nil || *result.Remaining != 20 {
				t.Error("query failed", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate probes", calls.Load())
	}
	saved := settings.Snapshot()
	saved.Providers[0].Balance.Currency = "CNY"
	settings.Save(saved)
	service.Query(context.Background(), settings, "p")
	if calls.Load() != 2 {
		t.Fatal("stale cache after configuration change")
	}
	saved = settings.Snapshot()
	saved.Telemetry.Balances = false
	settings.Save(saved)
	if _, err = service.Query(context.Background(), settings, "p"); err == nil || calls.Load() != 2 {
		t.Fatal("disabled query sent")
	}
}

func TestTelemetryRetentionPaginationPersistenceAndToggles(t *testing.T) {
	cfg := DefaultTelemetryConfig()
	cfg.MaxRecords = 100
	store, err := NewTelemetryStore(filepath.Join(t.TempDir(), "ai-usage.json"), func() TelemetryConfig { return cfg })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	price := &ModelPrice{Enabled: true, Model: "m", Mode: "tokens", Currency: "USD", Input: opFloat(1), Output: opFloat(2), Multiplier: 1}
	record := RequestRecord{At: now, Day: day, Model: "m", ProviderID: "p", Provider: "Test", Kind: "text", Status: "success", Usage: Usage{PromptTokens: opInt(10), CompletionTokens: opInt(20), TotalTokens: opInt(30)}, DurationMS: 1000}
	record.Cost = EstimateCost(price, record.Usage, "text", "success", 0)
	for range 150 {
		store.Record(record)
	}
	report := store.Report(UsageFilter{Start: day, End: day, Limit: 25})
	if report.Totals.Requests != 150 || report.Retained != 100 || len(report.Records) != 25 || report.Records[0].Sequence != 150 || report.Next != 126 {
		t.Fatalf("bad report %+v", report)
	}
	page := store.Report(UsageFilter{Start: day, End: day, Limit: 25, Before: report.Next})
	if page.Records[0].Sequence != 125 {
		t.Fatal("unstable cursor")
	}
	cfg.Details = false
	cfg.Costs = false
	store.Record(record)
	report = store.Report(UsageFilter{Start: day, End: day})
	if report.Retained != 100 || report.Totals.Requests != 151 || report.Totals.CostKnown != 150 {
		t.Fatal("toggles ignored")
	}
	cfg.Enabled = false
	store.Record(record)
	if store.Report(UsageFilter{Start: day, End: day}).Totals.Requests != 151 {
		t.Fatal("paused collection recorded")
	}
	if err = store.Flush(now); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewTelemetryStore(store.path, func() TelemetryConfig { return cfg })
	if err != nil {
		t.Fatal(err)
	}
	report = reopened.Report(UsageFilter{Start: day, End: day})
	if report.Totals.Requests != 151 || report.Records[0].Sequence != 150 {
		t.Fatal("restart changed data")
	}
	cfg.Enabled = true
	cfg.Details = true
	cfg.MaxRecords = 100
	record.Day = now.AddDate(0, 0, -95).Format("2006-01-02")
	reopened.Record(record)
	reopened.Flush(now)
	if reopened.Report(UsageFilter{Start: "2000-01-01", End: day}).Totals.Requests != 151 {
		t.Fatal("retention did not remove expired rows")
	}
}

func TestTelemetryParallelRecordAndCardinality(t *testing.T) {
	cfg := DefaultTelemetryConfig()
	cfg.MaxRecords = 100
	store, _ := NewTelemetryStore("", func() TelemetryConfig { return cfg })
	day := time.Now().Format("2006-01-02")
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for n := range 3500 {
				store.Record(RequestRecord{Day: day, Model: fmt.Sprintf("m-%d-%d", worker, n), Kind: "text", Status: "success"})
			}
		})
	}
	wg.Wait()
	result := store.Report(UsageFilter{Start: day, End: day})
	if result.Totals.Requests != 14000 || result.Retained != 100 || result.Collapsed == 0 || len(store.buckets) > maxAIBuckets+8 {
		t.Fatal("bounded collection lost totals", result.Totals.Requests, len(store.buckets))
	}
}

func BenchmarkTelemetryRecord(b *testing.B) {
	s, _ := NewTelemetryStore("", DefaultTelemetryConfig)
	r := RequestRecord{Day: "2026-11-16", Kind: "text", Model: "m", Status: "success"}
	b.ResetTimer()
	for range b.N {
		s.Record(r)
	}
}
func BenchmarkTelemetryReport10000(b *testing.B) {
	cfg := DefaultTelemetryConfig()
	cfg.MaxRecords = 10000
	s, _ := NewTelemetryStore("", func() TelemetryConfig { return cfg })
	for range 10000 {
		s.Record(RequestRecord{Day: "2026-11-16", Kind: "text", Model: "m", Status: "success"})
	}
	b.ResetTimer()
	for range b.N {
		s.Report(UsageFilter{Start: "2026-11-16", End: "2026-11-16"})
	}
}
