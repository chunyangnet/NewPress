package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
	"time"
)

func (s *Server) socialReferrals(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, r.Method == "POST")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	policy := s.siteSettings.Snapshot().Settings.RegistrationPolicy().Referrals()
	if r.Method == "POST" {
		var input struct {
			Action  string `json:"action"`
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
			return
		}
		var err error
		switch input.Action {
		case "generate":
			_, err = s.auth.GenerateReferral(r.Context(), p.UserID, policy)
		case "toggle":
			err = s.auth.SetReferralCode(r.Context(), p.UserID, input.ID, input.Enabled)
		case "claim":
			err = s.claimReferralRewards(r.Context(), p.UserID)
		default:
			s.writeAPIError(w, r, 422, "referral.invalid", "请选择有效操作", nil)
			return
		}
		if err != nil {
			s.accessError(w, r, err)
			return
		}
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	invitees, err := s.auth.ReferralUsers(r.Context(), p.UserID)
	if err != nil {
		s.accessError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, c := range u.Referral.Codes {
		items = append(items, map[string]any{"id": c.ID, "code": c.Code, "enabled": c.Enabled, "status": auth.RegistrationCodeStatus(c, time.Now()), "used": c.Used, "max_uses": c.MaxUses, "expires_at": c.ExpiresAt, "url": s.config.PublicURL + "/account/access/?invite=" + url.QueryEscape(c.Code)})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": policy.Enabled, "items": items, "invited": len(invitees), "max_codes": policy.MaxCodes, "daily_codes": policy.DailyCodes, "min_age_days": policy.MinAgeDays, "verified_email": policy.VerifiedEmail, "verified_phone": policy.VerifiedPhone})
}

func (s *Server) claimReferralRewards(ctx context.Context, id int64) error {
	u, err := s.auth.GetUser(ctx, id)
	if err != nil {
		return err
	}
	users, err := s.auth.ReferralUsers(ctx, id)
	if err != nil {
		return err
	}
	users = append(users, u)
	var failures []error
	for _, invitee := range users {
		f := invitee.Referral
		if f.InviterID == 0 || f.Currency == "" || f.InviterReward == 0 && f.InviteeReward == 0 || invitee.Status != auth.UserStatusActive || f.RequireVerifiedEmail && invitee.EmailVerifiedAt == nil {
			continue
		}
		owner, err := s.auth.GetUser(ctx, f.InviterID)
		if err != nil {
			return err
		}
		if owner.Status != auth.UserStatusActive {
			continue
		}
		if s.currencyCatalog == nil {
			return fmt.Errorf("奖励币种服务暂不可用")
		}
		store, err := s.currencyCatalog.Store(ctx, f.Currency)
		if err != nil {
			return err
		}
		cfg, err := store.Settings(ctx)
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			failures = append(failures, fmt.Errorf("奖励币种暂未启用，请稍后再次领取"))
			continue
		}
		for _, reward := range []struct {
			id, amount int64
			side       string
		}{{owner.ID, f.InviterReward, "owner"}, {invitee.ID, f.InviteeReward, "new"}} {
			if reward.amount == 0 {
				continue
			}
			_, err = store.Credit(ctx, virtualcurrency.CreditInput{UserID: reward.id, Amount: reward.amount, ExpectedVersion: cfg.Version, Source: "邀请注册奖励", RequestID: fmt.Sprintf("referral_%s_%020d", reward.side, invitee.ID)})
			if err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
