package progression

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMembershipBenefitValidation(t *testing.T) {
	for _, benefits := range [][]string{nil, {}, {"公开会员身份", strings.Repeat("好", MaxPlanBenefitLength)}} {
		cfg := DefaultSettings()
		cfg.Membership.Plans[0].Benefits = benefits
		if err := ValidateSettings(cfg); err != nil {
			t.Fatalf("valid benefits rejected: %q: %v", benefits, err)
		}
	}
	for name, benefits := range map[string][]string{
		"empty": {""}, "leading space": {" 权益"}, "trailing space": {"权益 "},
		"newline": {"权益\n第二行"}, "format control": {"会员\u200b权益"},
		"html": {"<b>权益</b>"}, "invalid utf8": {string([]byte{0xff})},
		"long": {strings.Repeat("好", MaxPlanBenefitLength+1)}, "duplicate": {"会员身份", "会员身份"},
		"too many": {"一", "二", "三", "四", "五", "六", "七", "八", "九"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultSettings()
			cfg.Membership.Plans[0].Benefits = benefits
			if !errors.Is(ValidateSettings(cfg), ErrInvalid) {
				t.Fatal("invalid benefits accepted")
			}
			issues := validationIssues(cfg)
			if len(issues) == 0 || !strings.HasPrefix(issues[0].Field, "membership.plans[0].benefits") {
				t.Fatalf("missing editable field location: %+v", issues)
			}
		})
	}
}

func TestMembershipBenefitCopiesAreIsolated(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Membership.Enabled, cfg.Experience.Enabled = true, true
	cfg.Membership.Plans[0].Benefits = []string{"原始权益"}
	copy := CloneSettings(cfg)
	copy.Membership.Plans[0].Benefits[0] = "克隆变更"
	plan, _ := cfg.Plan("member")
	plan.Benefits[0] = "查找变更"
	now := time.Now().UTC()
	member := Membership{Plan: "member", Since: now, Until: now.Add(time.Hour)}
	summary := cfg.PublicBenefits(member, now)
	summary.Benefits[0] = "公开摘要变更"
	*summary.Until = now
	if cfg.Membership.Plans[0].Benefits[0] != "原始权益" || !member.Until.Equal(now.Add(time.Hour)) {
		t.Fatal("caller mutated stored membership benefits")
	}
	if !reflect.DeepEqual(cfg.Membership.Plans[0].Benefits, []string{"原始权益"}) {
		t.Fatal(cfg.Membership.Plans[0].Benefits)
	}
}

func TestPublicBenefitsOnlyApplyDuringActiveMembership(t *testing.T) {
	now := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	base := DefaultSettings()
	base.Membership.Enabled, base.Experience.Enabled = true, true
	base.Membership.Plans[0].Benefits = []string{"会员权益说明"}
	member := Membership{Version: 1, Plan: "member", Since: now, Until: now.Add(time.Hour)}
	for _, test := range []struct {
		name    string
		edit    func(*Settings, *Membership)
		active  bool
		percent int
		badge   bool
	}{
		{"active", func(*Settings, *Membership) {}, true, 125, true},
		{"plan closed to new purchases", func(s *Settings, _ *Membership) { s.Membership.Plans[0].Enabled = false }, true, 125, true},
		{"automatic experience paused", func(s *Settings, _ *Membership) { s.Experience.Enabled = false }, true, 100, true},
		{"badge disabled", func(s *Settings, _ *Membership) { s.Membership.PublicBadges = false }, true, 125, false},
		{"expired boundary", func(_ *Settings, m *Membership) { m.Until = now }, false, 100, false},
		{"revoked", func(_ *Settings, m *Membership) { m.Revoked = true }, false, 100, false},
		{"future", func(_ *Settings, m *Membership) { m.Since = now.Add(time.Minute) }, false, 100, false},
		{"module paused", func(s *Settings, _ *Membership) { s.Membership.Enabled = false }, false, 100, false},
		{"unknown plan", func(_ *Settings, m *Membership) { m.Plan = "missing" }, false, 100, false},
		{"no membership", func(_ *Settings, m *Membership) { *m = Membership{} }, false, 100, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, m := CloneSettings(base), member
			test.edit(&cfg, &m)
			result := cfg.PublicBenefits(m, now)
			if result.Active != test.active || result.ExperiencePercent != test.percent || result.PublicBadge != test.badge {
				t.Fatalf("unexpected effective benefits: %+v", result)
			}
			if test.active {
				if result.Plan != "member" || result.Name != cfg.Membership.Plans[0].Name || result.Until == nil || !result.Until.Equal(m.Until) || !reflect.DeepEqual(result.Benefits, cfg.Membership.Plans[0].Benefits) {
					t.Fatal("missing active benefits", result)
				}
			} else if result.Plan != "" || result.Name != "" || result.Until != nil || len(result.Benefits) != 0 {
				t.Fatal("inactive membership exposed benefits", result)
			}
		})
	}
}

func TestMembershipBenefitsLegacyFileAndNewRoundTrip(t *testing.T) {
	s, _, cfg, _, path := fixture(t)
	legacy, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte(`"benefits"`)) {
		t.Fatal("legacy fixture contains new field")
	}
	if _, err := NewFileStore(path); err != nil {
		t.Fatal("legacy ledger cannot be opened", err)
	}
	cfg.Membership.Plans[0].Benefits = []string{"会员身份展示", "自动奖励经验加成"}
	saved, err := s.Store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Membership.Plans[0].Benefits[0], saved.Membership.Plans[0].Benefits[0] = "输入被修改", "输出被修改"
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Settings(t.Context())
	if err != nil || loaded.Membership.Plans[0].Benefits[0] != "会员身份展示" {
		t.Fatal("benefits did not round-trip independently", loaded, err)
	}
	loaded.Membership.Plans[0].Benefits[0] = "读取被修改"
	again, err := reopened.Settings(t.Context())
	if err != nil || again.Membership.Plans[0].Benefits[0] != "会员身份展示" {
		t.Fatal("read alias escaped", err)
	}
	// Optional fields do not weaken strict decoding of null and scalar values.
	var data map[string]any
	if err := json.Unmarshal(legacy, &data); err != nil {
		t.Fatal(err)
	}
	plan := data["settings"].(map[string]any)["membership"].(map[string]any)["plans"].([]any)[0].(map[string]any)
	for _, malformed := range []any{nil, "not an array", []any{"<script>"}} {
		plan["benefits"] = malformed
		body, _ := json.Marshal(data)
		target := filepath.Join(t.TempDir(), "malformed.json")
		if err := os.WriteFile(target, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(target); err == nil {
			t.Fatalf("malformed benefits accepted: %#v", malformed)
		}
	}
}

func TestSnapshotBenefitsExpireAtBoundary(t *testing.T) {
	s, _, cfg, now, _ := fixture(t)
	cfg.Membership.Plans[0].Benefits = []string{"有效期内展示"}
	cfg, err := s.Store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Users.(testUsers).items[2]
	granted, err := s.Store.ManageMembership(t.Context(), MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "grant", Plan: "member", Days: 1, RequestID: "benefits-expiry-grant", Reason: "权益有效期测试"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || !view.MembershipActive || !view.Benefits.Active || view.Benefits.ExperiencePercent != 125 || len(view.Benefits.Benefits) != 1 {
		t.Fatal("active benefits missing", view.Benefits, err)
	}
	*now = granted.Entry.Membership.Until
	view, err = s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.MembershipActive || view.Benefits.Active || view.Benefits.ExperiencePercent != 100 || len(view.Benefits.Benefits) != 0 {
		t.Fatal("benefits survived exact expiry", view.Benefits, err)
	}
	reward, err := s.CheckIn(t.Context(), 2, cfg.Version, "benefits-expiry-checkin")
	if err != nil || reward.Entry.Delta != cfg.CheckIn.Amount {
		t.Fatal("expired membership still received multiplier", reward, err)
	}
	if !reflect.DeepEqual(s.Users.(testUsers).items[2], before) {
		t.Fatal("membership changed user or administrative roles")
	}
}
