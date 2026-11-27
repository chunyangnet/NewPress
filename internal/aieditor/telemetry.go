package aieditor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const maxAIBuckets = 12000
const maxTelemetryBytes = 32 << 20

// No prompts, generated content, reasoning, URLs, headers or credentials belong
// in request records. ErrorCode is an application-owned code, never upstream text.
type RequestRecord struct {
	Sequence       uint64       `json:"sequence"`
	ID             string       `json:"id"`
	At             time.Time    `json:"at"`
	Day            string       `json:"day"`
	ProviderID     string       `json:"provider_id"`
	Provider       string       `json:"provider"`
	Model          string       `json:"model"`
	Protocol       string       `json:"protocol"`
	Kind           string       `json:"kind"`
	Purpose        string       `json:"purpose,omitempty"`
	UserID         int64        `json:"user_id"`
	Status         string       `json:"status"`
	ErrorCode      string       `json:"error_code,omitempty"`
	UpstreamStatus int          `json:"upstream_status,omitempty"`
	DurationMS     int64        `json:"duration_ms"`
	FirstTokenMS   *int64       `json:"first_token_ms,omitempty"`
	Usage          Usage        `json:"usage"`
	Images         int          `json:"images"`
	Cost           CostEstimate `json:"cost"`
}

type Totals struct {
	InputKnown       int64              `json:"input_known"`
	OutputKnown      int64              `json:"output_known"`
	SpeedOutput      int64              `json:"speed_output"`
	Saturated        bool               `json:"saturated"`
	Requests         int64              `json:"requests"`
	Success          int64              `json:"success"`
	Errors           int64              `json:"errors"`
	Cancelled        int64              `json:"cancelled"`
	Images           int64              `json:"images"`
	Input            int64              `json:"input"`
	Output           int64              `json:"output"`
	Tokens           int64              `json:"tokens"`
	Cached           int64              `json:"cached"`
	CacheWrite       int64              `json:"cache_write"`
	Reasoning        int64              `json:"reasoning"`
	UsageKnown       int64              `json:"usage_known"`
	CostKnown        int64              `json:"cost_known"`
	CostPartial      int64              `json:"cost_partial"`
	DurationMS       int64              `json:"duration_ms"`
	FirstTokenMS     int64              `json:"first_token_ms"`
	FirstTokenKnown  int64              `json:"first_token_known"`
	OutputDurationMS int64              `json:"output_duration_ms"`
	Costs            map[string]float64 `json:"costs"`
}

func (a *Totals) merge(b Totals) {
	a.Requests += b.Requests
	a.Success += b.Success
	a.Errors += b.Errors
	a.Cancelled += b.Cancelled
	a.Images += b.Images
	a.InputKnown += b.InputKnown
	a.OutputKnown += b.OutputKnown
	a.Saturated = a.Saturated || b.Saturated
	for _, pair := range []struct {
		dst   *int64
		value int64
	}{{&a.Input, b.Input}, {&a.Output, b.Output}, {&a.Tokens, b.Tokens}, {&a.Cached, b.Cached}, {&a.CacheWrite, b.CacheWrite}, {&a.Reasoning, b.Reasoning}, {&a.SpeedOutput, b.SpeedOutput}} {
		if *pair.dst > maxSafeTokens-pair.value {
			*pair.dst = maxSafeTokens
			a.Saturated = true
		} else {
			*pair.dst += pair.value
		}
	}
	a.UsageKnown += b.UsageKnown
	a.CostKnown += b.CostKnown
	a.CostPartial += b.CostPartial
	a.DurationMS += b.DurationMS
	a.FirstTokenMS += b.FirstTokenMS
	a.FirstTokenKnown += b.FirstTokenKnown
	a.OutputDurationMS += b.OutputDurationMS
	if a.Costs == nil {
		a.Costs = map[string]float64{}
	}
	for currency, amount := range b.Costs {
		a.Costs[currency] += amount
	}
}
func recordTotals(r RequestRecord) Totals {
	t := Totals{Requests: 1, Images: int64(r.Images), DurationMS: r.DurationMS, Costs: map[string]float64{}}
	switch r.Status {
	case "success":
		t.Success = 1
	case "cancelled":
		t.Cancelled = 1
	default:
		t.Errors = 1
	}
	for _, item := range []struct {
		from *int
		to   *int64
	}{{r.Usage.PromptTokens, &t.Input}, {r.Usage.CompletionTokens, &t.Output}, {r.Usage.TotalTokens, &t.Tokens}, {r.Usage.CachedTokens, &t.Cached}, {r.Usage.CacheCreationTokens, &t.CacheWrite}, {r.Usage.ReasoningTokens, &t.Reasoning}} {
		if item.from != nil {
			*item.to = int64(*item.from)
		}
	}
	if r.Usage.PromptTokens != nil {
		t.InputKnown = 1
	}
	if r.Usage.CompletionTokens != nil {
		t.OutputKnown = 1
	}
	if r.Usage.TotalTokens != nil {
		t.UsageKnown = 1
	}
	if r.Usage.CompletionTokens != nil && r.Kind == "text" {
		t.OutputDurationMS = r.DurationMS
		t.SpeedOutput = int64(*r.Usage.CompletionTokens)
	}
	if r.FirstTokenMS != nil {
		t.FirstTokenKnown = 1
		t.FirstTokenMS = *r.FirstTokenMS
	}
	if r.Cost.Amount != nil {
		t.Costs[r.Cost.Currency] = *r.Cost.Amount
		t.CostKnown = 1
		if !r.Cost.Complete {
			t.CostPartial = 1
		}
	}
	return t
}

type UsageBucket struct {
	Day        string `json:"day"`
	ProviderID string `json:"provider_id"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Totals
}
type telemetryState struct {
	Version   int             `json:"version"`
	Sequence  uint64          `json:"sequence"`
	Buckets   []UsageBucket   `json:"buckets"`
	Records   []RequestRecord `json:"records"`
	Collapsed int64           `json:"collapsed"`
}

// Requests update bounded memory in O(1). A single background writer snapshots
// dirty state every 15 seconds; no disk I/O is performed in the generation path.
type TelemetryStore struct {
	mu           sync.RWMutex
	flushMu      sync.Mutex
	path         string
	buckets      map[string]UsageBucket
	records      []RequestRecord
	head         int
	sequence     uint64
	revision     uint64
	flushed      uint64
	collapsed    int64
	persistError bool
	persistedAt  time.Time
	config       func() TelemetryConfig
}

func NewTelemetryStore(path string, config func() TelemetryConfig) (*TelemetryStore, error) {
	if config == nil {
		config = DefaultTelemetryConfig
	}
	s := &TelemetryStore{path: path, buckets: map[string]UsageBucket{}, config: config}
	if path == "" {
		return s, nil
	}
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("无法读取 AI 消耗统计")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTelemetryBytes+1))
	if err != nil || len(data) > maxTelemetryBytes {
		return nil, errors.New("AI 消耗统计文件过大或无法读取")
	}
	var state telemetryState
	if json.Unmarshal(data, &state) != nil || state.Version != 1 || len(state.Records) > 10000 || len(state.Buckets) > maxAIBuckets+800 {
		return nil, errors.New("AI 消耗统计文件格式无效")
	}
	for _, b := range state.Buckets {
		s.buckets[bucketKey(b)] = b
	}
	s.records = state.Records
	s.sequence = state.Sequence
	s.collapsed = state.Collapsed
	s.pruneLocked(time.Now(), config())
	return s, nil
}
func bucketKey(b UsageBucket) string {
	return b.Day + "\x00" + b.ProviderID + "\x00" + b.Model + "\x00" + b.Kind + "\x00" + b.Status
}
func (s *TelemetryStore) Record(r RequestRecord) {
	if s == nil {
		return
	}
	cfg := s.config()
	if !cfg.Enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	r.Sequence = s.sequence
	if r.ID == "" {
		r.ID = newRevision()
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	if r.Day == "" {
		r.Day = r.At.Format("2006-01-02")
	}
	if !cfg.Costs {
		r.Cost = CostEstimate{}
	}
	b := UsageBucket{Day: r.Day, ProviderID: r.ProviderID, Provider: r.Provider, Model: r.Model, Kind: r.Kind, Status: r.Status}
	key := bucketKey(b)
	if _, exists := s.buckets[key]; !exists && len(s.buckets) >= maxAIBuckets {
		// Keep totals when dimension cardinality is exhausted. This bucket is
		// visible as "其他模型" rather than silently losing requests.
		b.ProviderID, b.Provider, b.Model = "_other", "其他接口", "其他模型"
		key = bucketKey(b)
		s.collapsed++
	}
	if current, exists := s.buckets[key]; exists {
		b = current
	}
	b.Totals.merge(recordTotals(r))
	s.buckets[key] = b
	if cfg.Details {
		if len(s.records) < cfg.MaxRecords {
			if s.head != 0 {
				s.linearizeLocked()
			}
			s.records = append(s.records, r)
		} else {
			if len(s.records) > cfg.MaxRecords {
				s.pruneLocked(time.Now(), cfg)
			}
			if len(s.records) < cfg.MaxRecords {
				s.records = append(s.records, r)
			} else {
				s.records[s.head] = r
				s.head = (s.head + 1) % len(s.records)
			}
		}
	}
	s.revision++
}
func (s *TelemetryStore) linearizeLocked() {
	if s.head == 0 {
		return
	}
	out := append([]RequestRecord{}, s.records[s.head:]...)
	out = append(out, s.records[:s.head]...)
	s.records = out
	s.head = 0
}
func (s *TelemetryStore) pruneLocked(now time.Time, cfg TelemetryConfig) {
	// UTC gives persisted data a stable retention clock when the site timezone
	// changes; reporting still uses the site's calendar captured on each request.
	cutoff := now.UTC().AddDate(0, 0, -cfg.RetentionDays).Format("2006-01-02")
	changed := false
	for key, b := range s.buckets {
		if b.Day < cutoff {
			delete(s.buckets, key)
			changed = true
		}
	}
	s.linearizeLocked()
	keep := s.records[:0]
	for _, r := range s.records {
		if r.Day >= cutoff {
			keep = append(keep, r)
		} else {
			changed = true
		}
	}
	s.records = keep
	if len(s.records) > cfg.MaxRecords {
		s.records = append([]RequestRecord{}, s.records[len(s.records)-cfg.MaxRecords:]...)
		changed = true
	}
	if changed {
		s.revision++
	}
}
func (s *TelemetryStore) Flush(now time.Time) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	cfg := s.config()
	s.mu.Lock()
	s.pruneLocked(now, cfg)
	if s.revision == s.flushed || s.path == "" {
		s.mu.Unlock()
		return nil
	}
	revision := s.revision
	state := telemetryState{Version: 1, Sequence: s.sequence, Collapsed: s.collapsed, Records: append([]RequestRecord{}, s.records...)}
	for _, b := range s.buckets {
		copy := b
		copy.Costs = map[string]float64{}
		for k, v := range b.Costs {
			copy.Costs[k] = v
		}
		state.Buckets = append(state.Buckets, copy)
	}
	s.mu.Unlock()
	data, err := json.Marshal(state)
	if err == nil && len(data) > maxTelemetryBytes {
		err = errors.New("AI telemetry capacity exceeded")
	}
	if err == nil {
		err = writeTelemetryFile(s.path, data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistError = err != nil
	if err == nil {
		s.flushed = revision
		s.persistedAt = now
	}
	return err
}
func writeTelemetryFile(path string, data []byte) error {
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ai-usage-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return packagefs.ReplaceRegularFileNoSymlink(f.Name(), path)
}
func (s *TelemetryStore) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := s.Flush(time.Now()); err != nil {
				slog.Warn("AI usage final flush failed")
			}
			return
		case now := <-ticker.C:
			if err := s.Flush(now); err != nil {
				slog.Warn("AI usage persistence unavailable")
			}
		}
	}
}

type UsageFilter struct {
	Start, End, ProviderID, Model, Kind, Status string
	Before                                      uint64
	Limit                                       int
}

func (f UsageFilter) match(day, provider, model, kind, status string) bool {
	return day >= f.Start && day <= f.End && (f.ProviderID == "" || provider == f.ProviderID) && (f.Model == "" || model == f.Model) && (f.Kind == "" || kind == f.Kind) && (f.Status == "" || status == f.Status)
}

type UsageReport struct {
	Today        string          `json:"today"`
	Config       TelemetryConfig `json:"config"`
	Totals       Totals          `json:"totals"`
	Trend        []UsageBucket   `json:"trend"`
	Models       []UsageBucket   `json:"models"`
	Records      []RequestRecord `json:"records"`
	Next         uint64          `json:"next"`
	Retained     int             `json:"retained"`
	Collapsed    int64           `json:"collapsed"`
	PersistError bool            `json:"persist_error"`
	PersistedAt  time.Time       `json:"persisted_at"`
	Start        string          `json:"start"`
	End          string          `json:"end"`
	Timezone     string          `json:"timezone"`
}

func (s *TelemetryStore) Report(f UsageFilter) UsageReport {
	report := UsageReport{Config: s.config(), Trend: []UsageBucket{}, Models: []UsageBucket{}, Records: []RequestRecord{}, Start: f.Start, End: f.End}
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 25
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	report.PersistError, report.PersistedAt, report.Collapsed = s.persistError, s.persistedAt, s.collapsed
	days, models := map[string]UsageBucket{}, map[string]UsageBucket{}
	for _, b := range s.buckets {
		if !f.match(b.Day, b.ProviderID, b.Model, b.Kind, b.Status) {
			continue
		}
		report.Totals.merge(b.Totals)
		d := days[b.Day]
		d.Day = b.Day
		d.Totals.merge(b.Totals)
		days[b.Day] = d
		key := b.ProviderID + "\x00" + b.Model
		m := models[key]
		m.ProviderID, m.Provider, m.Model = b.ProviderID, b.Provider, b.Model
		m.Totals.merge(b.Totals)
		models[key] = m
	}
	for _, d := range days {
		report.Trend = append(report.Trend, d)
	}
	sort.Slice(report.Trend, func(i, j int) bool { return report.Trend[i].Day < report.Trend[j].Day })
	for _, m := range models {
		report.Models = append(report.Models, m)
	}
	sort.Slice(report.Models, func(i, j int) bool {
		if report.Models[i].Requests == report.Models[j].Requests {
			return report.Models[i].ProviderID+report.Models[i].Model < report.Models[j].ProviderID+report.Models[j].Model
		}
		return report.Models[i].Requests > report.Models[j].Requests
	})
	if len(report.Models) > 50 {
		report.Models = report.Models[:50]
	}
	for i := len(s.records) - 1; i >= 0; i-- {
		r := s.records[(s.head+i)%len(s.records)]
		if !f.match(r.Day, r.ProviderID, r.Model, r.Kind, r.Status) {
			continue
		}
		report.Retained++
		if f.Before != 0 && r.Sequence >= f.Before {
			continue
		}
		if len(report.Records) < f.Limit {
			report.Records = append(report.Records, r)
		} else if report.Next == 0 {
			report.Next = report.Records[len(report.Records)-1].Sequence
		}
	}
	return report
}
