package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func (s *Server) SetAITelemetry(store *aieditor.TelemetryStore) {
	if store != nil {
		s.aiTelemetry = store
	}
}

type aiRequestObservation struct {
	server  *Server
	record  aieditor.RequestRecord
	started time.Time
	price   *aieditor.ModelPrice
}

func (s *Server) observeAI(provider string, c aieditor.Connection, kind string, user int64) *aiRequestObservation {
	now := time.Now()
	name, price := s.aiSettings.Accounting(provider, c.Model)
	if provider == "" {
		provider = "_temporary"
	}
	if !s.aiSettings.Operations().Costs {
		price = nil
	}
	return &aiRequestObservation{server: s, started: now, price: price, record: aieditor.RequestRecord{At: now, Day: now.In(s.siteLocation()).Format("2006-01-02"), ProviderID: provider, Provider: name, Model: c.Model, Protocol: c.Protocol, Kind: kind, UserID: user, Status: "error", ErrorCode: "interrupted"}}
}
func (o *aiRequestObservation) first() {
	if o.record.FirstTokenMS == nil {
		elapsed := time.Since(o.started).Milliseconds()
		o.record.FirstTokenMS = &elapsed
	}
}
func (o *aiRequestObservation) fail(err error) {
	o.record.Status = "error"
	var known *aieditor.Error
	if errors.As(err, &known) {
		o.record.ErrorCode = known.Code
		o.record.UpstreamStatus = known.Details.UpstreamStatus
	}
	if errors.Is(err, context.DeadlineExceeded) {
		o.record.Status, o.record.ErrorCode = "timeout", "timeout"
	} else if errors.Is(err, context.Canceled) {
		o.record.Status, o.record.ErrorCode = "cancelled", "cancelled"
	}
}
func (o *aiRequestObservation) finish(ctx context.Context) {
	if o.record.ErrorCode == "interrupted" && ctx.Err() != nil {
		o.fail(ctx.Err())
	}
	o.record.DurationMS = time.Since(o.started).Milliseconds()
	o.record.Cost = aieditor.EstimateCost(o.price, o.record.Usage, o.record.Kind, o.record.Status, o.record.Images)
	o.server.aiTelemetry.Record(o.record)
}
func (s *Server) adminAIUsagePage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	current, title := "ai-usage", "AI 消耗"
	if r.URL.Path == "/admin/analytics/ai/requests/" {
		current, title = "ai-requests", "AI 请求明细"
	}
	data := s.baseViewFor(w, title, "查看模型用量、费用估算与生成请求。", r.URL.Path)
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-ai-usage"
	data.Admin = s.adminView(current, p, readOnly)
	s.render(w, 200, "admin-ai-usage", data)
}
func (s *Server) adminAIUsage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"start": true, "end": true, "provider": true, "model": true, "kind": true, "status": true, "before": true, "limit": true})
	if !ok {
		return
	}
	location := s.siteLocation(r.Context())
	now := time.Now().In(location)
	end := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -29).Format("2006-01-02")
	if values.Get("start") != "" {
		start = values.Get("start")
	}
	if values.Get("end") != "" {
		end = values.Get("end")
	}
	a, ea := time.ParseInLocation("2006-01-02", start, location)
	b, eb := time.ParseInLocation("2006-01-02", end, location)
	invalid := func() {
		s.writeAPIError(w, r, 400, "ai.invalid_range", "请选择不超过 90 天的日期、有效筛选条件与分页参数", nil)
	}
	if ea != nil || eb != nil || a.After(b) || b.After(a.AddDate(0, 0, 89)) || end > now.Format("2006-01-02") {
		invalid()
		return
	}
	f := aieditor.UsageFilter{Start: start, End: end, ProviderID: values.Get("provider"), Model: values.Get("model"), Kind: values.Get("kind"), Status: values.Get("status"), Limit: 25}
	if len(f.ProviderID) > 64 || len(f.Model) > 200 || f.Kind != "" && f.Kind != "text" && f.Kind != "image" || f.Status != "" && f.Status != "success" && f.Status != "error" && f.Status != "cancelled" && f.Status != "timeout" {
		invalid()
		return
	}
	if values.Get("before") != "" {
		n, err := strconv.ParseUint(values.Get("before"), 10, 64)
		if err != nil {
			invalid()
			return
		}
		f.Before = n
	}
	if values.Get("limit") != "" {
		n, err := strconv.Atoi(values.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			invalid()
			return
		}
		f.Limit = n
	}
	report := s.aiTelemetry.Report(f)
	report.Timezone = location.String()
	report.Today = now.Format("2006-01-02")
	type providerOption struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Models  []string `json:"models"`
		Balance bool     `json:"balance"`
	}
	providers := []providerOption{}
	for _, p := range s.aiSettings.Snapshot().Providers {
		providers = append(providers, providerOption{p.ID, p.Name, p.Models, p.Balance.Enabled})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		aieditor.UsageReport
		Providers []providerOption `json:"providers"`
	}{report, providers})
}
func (s *Server) adminAIBalance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		ProviderID string `json:"provider_id"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	result, err := s.aiBalances.Query(r.Context(), s.aiSettings, input.ProviderID)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (s *Server) registerAIUsageRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/analytics/ai/{$}", s.adminAIUsagePage)
	mux.HandleFunc("GET /admin/analytics/ai/requests/{$}", s.adminAIUsagePage)
	mux.HandleFunc("GET /api/v1/admin/ai/usage", s.adminAIUsage)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/usage", s.adminAPIMethodNotAllowed("GET"))
	}
}
