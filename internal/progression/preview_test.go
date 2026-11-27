package progression

import (
	"testing"
)

func previewCode(issues []SettingsIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestSettingsPreviewExplainsNumbersWithoutMutation(t *testing.T) {
	s, _, cfg, _, _ := fixture(t)
	draft := CloneSettings(cfg)
	draft.Experience.DailyLimit = 2_000
	draft.CheckIn.Amount = 20
	before, err := s.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewSettings(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Store.Settings(t.Context())
	if err != nil || after.Version != before.Version || after.Experience.DailyLimit != before.Experience.DailyLimit {
		t.Fatal("preview mutated settings", before, after, err)
	}
	if !preview.Valid || len(preview.Issues) != 0 || len(preview.Metrics) != 8 || len(preview.Plans) != len(draft.Membership.Plans) || len(preview.Levels) != len(draft.Levels.Items) {
		t.Fatal(preview)
	}
	if preview.Plans[0].Amount != "1025" || preview.Plans[0].Price != "10.25" || preview.Plans[0].Currency != "main" || preview.Plans[0].CurrencyName != "积分" {
		t.Fatal(preview.Plans[0])
	}
	if preview.Metrics[7].Value != "2 / 1" {
		t.Fatal("plan counts", preview.Metrics[7])
	}
}

func TestSettingsPreviewDetectsStaleLimitsAndUsedPlans(t *testing.T) {
	s, _, cfg, _, _ := fixture(t)
	if _, err := s.Store.Adjust(t.Context(), Adjustment{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Delta: 500, RequestID: "preview-account-experience", Reason: "预检测试"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.ManageMembership(t.Context(), MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "grant", Plan: "member", Days: 30, RequestID: "preview-used-plan", Reason: "预检测试"}); err != nil {
		t.Fatal(err)
	}
	draft := CloneSettings(cfg)
	draft.Version++
	draft.Experience.Maximum = 499
	draft.Membership.Plans = draft.Membership.Plans[1:]
	draft.CheckIn.Amount = 900
	preview, err := s.PreviewSettings(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid || !previewCode(preview.Issues, "settings.version_changed") || !previewCode(preview.Issues, "experience.maximum_below_existing") || !previewCode(preview.Issues, "membership.plan_used") || !previewCode(preview.Issues, "rewards.exceeds_daily_limit") {
		t.Fatal(preview)
	}
}

func TestSettingsPreviewExplainsCurrencyPrecisionAndWarnings(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Experience.Enabled = false
	cfg.CheckIn.Enabled = true
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "10.255"
	cfg.Membership.Plans[0].Currency = "gold"
	cfg.Membership.Plans[1].Enabled = false
	preview := AnalyzeSettings(cfg, PreviewContext{Current: cfg, Currencies: []CurrencyFacts{
		{Key: "main", Name: "积分", Symbol: "分", Precision: 0, Enabled: true, MaxBalance: 1_000_000},
		{Key: "gold", Name: "金币", Symbol: "G", Precision: 2, Enabled: false, MaxBalance: 1_000},
	}})
	if preview.Valid || !previewCode(preview.Issues, "membership.price_precision_mismatch") || !previewCode(preview.Issues, "membership.currency_disabled") || !previewCode(preview.Issues, "experience.rewards_paused") {
		t.Fatal(preview)
	}
	if preview.Plans[0].Status != "error" || preview.Plans[0].CurrencyName != "金币" {
		t.Fatal(preview.Plans[0])
	}
}

func TestSettingsPreviewPriceMustFitCurrencyBalance(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "1000.01"
	cfg.Membership.Plans[0].Currency = "gold"
	preview := AnalyzeSettings(cfg, PreviewContext{Current: cfg, Currencies: []CurrencyFacts{
		{Key: "gold", Name: "金币", Symbol: "G", Precision: 2, Enabled: true, MaxBalance: 1000_00},
	}})
	if preview.Valid || !previewCode(preview.Issues, "membership.price_exceeds_balance") || preview.Plans[0].Amount != "100001" {
		t.Fatal(preview)
	}
}

func TestValidationIssuesMatchesAuthoritativeRules(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Version = 1
	if issues := validationIssues(cfg); len(issues) != 0 || ValidateSettings(cfg) != nil {
		t.Fatal(issues)
	}
	bad := CloneSettings(cfg)
	bad.CheckIn.IntervalHours = 0
	bad.Levels.Items[0].Threshold = 1
	bad.Membership.Plans[0].Color = "red"
	bad.Membership.Plans[0].Price = "0"
	issues := validationIssues(bad)
	for _, code := range []string{"check_in.interval_invalid", "levels.first_threshold", "membership.color_invalid", "membership.price_invalid"} {
		if !previewCode(issues, code) {
			t.Fatal("missing", code, issues)
		}
	}
	if ValidateSettings(bad) == nil {
		t.Fatal("authoritative validation accepted invalid settings")
	}
}
