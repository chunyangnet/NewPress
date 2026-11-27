package recognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

type Service struct {
	Backend    Backend
	Users      virtualcurrency.Users
	Content    content.Repository
	Currencies virtualcurrency.Catalog
}

func (s *Service) active(ctx context.Context, id int64) error {
	if s.Users == nil {
		return ErrUnavailable
	}
	u, err := s.Users.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Status != auth.UserStatusActive {
		return ErrForbidden
	}
	return nil
}
func (s *Service) SaveSettings(ctx context.Context, cfg Settings) error {
	if ValidateSettings(cfg) != nil {
		return ErrInvalid
	}
	if cfg.TipsEnabled {
		if s.Currencies == nil {
			return ErrUnavailable
		}
		for _, c := range cfg.Currencies {
			store, err := s.Currencies.Store(ctx, c.Key)
			if err != nil {
				return err
			}
			rules, err := store.Settings(ctx)
			if err != nil {
				return err
			}
			if !rules.Enabled || !rules.AllowTransfers {
				return virtualcurrency.ErrTransferDisabled
			}
			for _, raw := range c.Presets {
				amount, e := virtualcurrency.ParseAmount(raw, rules.Precision)
				if e != nil || amount < rules.MinTransfer || amount > rules.MaxTransfer {
					return ErrInvalid
				}
			}
		}
	}
	return s.Backend.Update(ctx, func(st *State) error {
		if cfg.Version != st.Settings.Version {
			return ErrConflict
		}
		// Existing awards/applications retain stable meaning, even after disabling a design.
		for _, a := range st.Identities {
			keys := []string{}
			for _, g := range a.Grants {
				keys = append(keys, g.Key)
			}
			if a.Application != nil {
				keys = append(keys, a.Application.Key)
			}
			for _, key := range keys {
				old, _ := st.Settings.Badge(key)
				next, ok := cfg.Badge(key)
				if !ok || old.Kind != next.Kind {
					return ErrConflict
				}
			}
		}
		cfg.Version++
		st.Settings = cfg
		st.PrivacyUnified = true
		return nil
	})
}

type IdentityInput struct {
	UserID   int64    `json:"user_id"`
	Version  int64    `json:"version"`
	Action   string   `json:"action"`
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Evidence string   `json:"evidence"`
	Reason   string   `json:"reason"`
	Days     int      `json:"days"`
	Equipped []string `json:"equipped,omitempty"`
}

func (s *Service) UpdateIdentity(ctx context.Context, actor int64, admin bool, in IdentityInput) (Identity, error) {
	var out Identity
	if actor < 1 || in.UserID < 1 || !admin && actor != in.UserID || in.Version < 1 || !TextOK(in.Label, 0, 30) || !TextOK(in.Evidence, 0, 1000) || !TextOK(in.Reason, 0, 200) || in.Days < 0 || in.Days > 3650 {
		return out, ErrInvalid
	}
	if err := s.active(ctx, in.UserID); err != nil {
		return out, err
	}
	err := s.Backend.Update(ctx, func(st *State) error {
		a := st.Identity(in.UserID)
		if a.Version != in.Version {
			return ErrConflict
		}
		now := time.Now().UTC()
		b, exists := st.Settings.Badge(in.Key)
		switch in.Action {
		case "apply":
			if admin || actor != in.UserID || !st.Settings.VerificationEnabled {
				return ErrDisabled
			}
			if !exists || !b.Enabled || b.Kind != "verified" || !TextOK(in.Label, 1, 30) || !TextOK(in.Evidence, 1, 1000) {
				return ErrInvalid
			}
			if a.Application != nil && a.Application.Status == "pending" {
				return ErrPending
			}
			for _, g := range a.Grants {
				badge, _ := st.Settings.Badge(g.Key)
				if badge.Kind == "verified" && g.Active(now) {
					return ErrConflict
				}
			}
			a.Application = &Application{Key: in.Key, Label: in.Label, Evidence: in.Evidence, Status: "pending", UpdatedAt: now}
		case "equip":
			if len(in.Equipped) > st.Settings.MaxMedals {
				return ErrInvalid
			}
			seen := map[string]bool{}
			for _, key := range in.Equipped {
				badge, ok := st.Settings.Badge(key)
				found := false
				for _, g := range a.Grants {
					if g.Key == key && g.Active(now) {
						found = true
					}
				}
				if !ok || !badge.Enabled || badge.Kind != "medal" || !found || seen[key] {
					return ErrInvalid
				}
				seen[key] = true
			}
			a.Equipped = slices.Clone(in.Equipped)
		case "grant", "revoke", "approve", "reject":
			if !admin {
				return ErrForbidden
			}
			if !TextOK(in.Reason, 1, 200) || !exists {
				return ErrInvalid
			}
			if in.Action == "approve" || in.Action == "reject" {
				if a.Application == nil || a.Application.Status != "pending" || a.Application.Key != in.Key {
					return ErrConflict
				}
				a.Application.Status, a.Application.Reason, a.Application.UpdatedAt = "rejected", in.Reason, now
				if in.Action == "approve" {
					a.Application.Status = "approved"
					in.Label = a.Application.Label
				}
			}
			if in.Action == "grant" || in.Action == "approve" {
				if !b.Enabled || b.Kind == "verified" && !st.Settings.VerificationEnabled {
					return ErrDisabled
				}
				// A user has one selected title and one current certification.
				a.Grants = slices.DeleteFunc(a.Grants, func(g Grant) bool {
					old, _ := st.Settings.Badge(g.Key)
					return g.Key == in.Key || b.Kind != "medal" && old.Kind == b.Kind
				})
				g := Grant{Key: in.Key, Label: in.Label, ActorID: actor, GrantedAt: now}
				if in.Days > 0 {
					g.ExpiresAt = now.AddDate(0, 0, in.Days)
				}
				a.Grants = append(a.Grants, g)
				if b.Kind == "medal" && !slices.Contains(a.Equipped, in.Key) && len(a.Equipped) < st.Settings.MaxMedals {
					a.Equipped = append(a.Equipped, in.Key)
				}
			}
			if in.Action == "revoke" {
				a.Grants = slices.DeleteFunc(a.Grants, func(g Grant) bool { return g.Key == in.Key })
				a.Equipped = slices.DeleteFunc(a.Equipped, func(key string) bool { return key == in.Key })
				if b.Kind == "verified" && a.Application != nil && a.Application.Key == in.Key && a.Application.Status == "approved" {
					a.Application.Status, a.Application.Reason, a.Application.UpdatedAt = "revoked", in.Reason, now
				}
			}
		default:
			return ErrInvalid
		}
		a.Version++
		a.History = append(a.History, Event{Action: in.Action, Key: in.Key, Reason: in.Reason, ActorID: actor, CreatedAt: now})
		if len(a.History) > 100 {
			a.History = a.History[len(a.History)-100:]
		}
		st.Identities[strconv.FormatInt(in.UserID, 10)] = a
		out = a
		return nil
	})
	return out, err
}

type TipInput struct {
	PostID          int64  `json:"post_id"`
	RequestID       string `json:"request_id"`
	Currency        string `json:"currency"`
	Amount          string `json:"amount"`
	Hidden          bool   `json:"hidden"`
	SettingsVersion int64  `json:"settings_version"`
	CurrencyVersion int64  `json:"currency_version"`
}

func (t Tip) PaymentRequest() string {
	digest := sha256.Sum256([]byte(TipKey(t.UserID, t.RequestID)))
	return "article-tip-" + hex.EncodeToString(digest[:])
}
func (s *Service) tipReceipt(ctx context.Context, t Tip) (int64, error) {
	if s.Currencies == nil {
		return 0, ErrUnavailable
	}
	coin, err := s.Currencies.Store(ctx, t.Currency)
	if err != nil {
		return 0, err
	}
	entries, fp, err := coin.Receipt(ctx, t.UserID, t.PaymentRequest())
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		if t.EntryID > 0 {
			return 0, ErrUnavailable
		}
		return 0, nil
	}
	if len(entries) != 2 || fp != virtualcurrency.TransferFingerprint(t.AuthorID, t.Amount) {
		return 0, ErrUnavailable
	}
	a, b := entries[0], entries[1]
	if a.Kind != "transfer_out" || a.UserID != t.UserID || a.PeerID != t.AuthorID || a.Delta != -t.Amount || b.Kind != "transfer_in" || b.UserID != t.AuthorID || b.PeerID != t.UserID || b.Delta != t.Amount || t.EntryID > 0 && t.EntryID != a.ID {
		return 0, ErrUnavailable
	}
	return a.ID, nil
}
func (s *Service) Ready(ctx context.Context) error {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	for _, tip := range st.Tips {
		if _, err = s.tipReceipt(ctx, tip); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Tip(ctx context.Context, user int64, in TipInput) (Tip, error) {
	var out Tip
	if user < 1 || in.PostID < 1 || !virtualcurrency.ValidRequestID(in.RequestID) || !virtualcurrency.ValidCurrencyKey(in.Currency) {
		return out, ErrInvalid
	}
	if err := s.active(ctx, user); err != nil {
		return out, err
	}
	if s.Currencies == nil {
		return out, ErrUnavailable
	}
	coin, err := s.Currencies.Store(ctx, in.Currency)
	if err != nil {
		return out, err
	}
	rules, err := coin.Settings(ctx)
	if err != nil {
		return out, err
	}
	amount, err := virtualcurrency.ParseAmount(in.Amount, rules.Precision)
	if err != nil || amount <= 0 {
		return out, ErrInvalid
	}
	key := TipKey(user, in.RequestID)
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return out, err
	}
	if prior, ok := st.Tips[key]; ok {
		if prior.PostID != in.PostID || prior.Currency != in.Currency || prior.Amount != amount || prior.Hidden != in.Hidden {
			return out, virtualcurrency.ErrIdempotencyConflict
		}
		id, err := s.tipReceipt(ctx, prior)
		if err != nil {
			return out, err
		}
		if id > 0 {
			prior.EntryID = id
			if st.Tips[key].EntryID == 0 {
				err = s.Backend.Update(ctx, func(state *State) error { state.Tips[key] = prior; return nil })
			}
			return prior, err
		}
	}
	post, err := s.Content.GetByID(ctx, in.PostID)
	if err != nil {
		return out, err
	}
	if post.Type != content.TypePost || !content.PublicAt(post, time.Now()) || post.AuthorID < 1 || post.AuthorID == user {
		return out, ErrForbidden
	}
	if err = s.active(ctx, post.AuthorID); err != nil {
		return out, err
	}
	if !rules.Enabled || !rules.AllowTransfers {
		return out, virtualcurrency.ErrTransferDisabled
	}
	if rules.Version != in.CurrencyVersion {
		return out, ErrConflict
	}
	if amount < rules.MinTransfer || amount > rules.MaxTransfer {
		return out, ErrInvalid
	}
	err = s.Backend.Update(ctx, func(st *State) error {
		cfg := st.Settings
		if !cfg.TipsEnabled {
			return ErrDisabled
		}
		if cfg.Version != in.SettingsVersion {
			return ErrConflict
		}
		if !slices.ContainsFunc(cfg.Currencies, func(c TipCurrency) bool { return c.Key == in.Currency }) || in.Hidden && !cfg.AllowHideAmount && !cfg.HideAmounts {
			return ErrInvalid
		}
		if prior, ok := st.Tips[key]; ok {
			if prior.PostID != in.PostID || prior.Currency != in.Currency || prior.Amount != amount || prior.Hidden != in.Hidden || prior.AuthorID != post.AuthorID {
				return virtualcurrency.ErrIdempotencyConflict
			}
			out = prior
			// An unpaid reservation can adopt the freshly confirmed currency
			// version; its recipient, currency, amount and request remain fixed.
			if out.EntryID == 0 {
				out.CurrencyVersion = in.CurrencyVersion
				st.Tips[key] = out
			}
			return nil
		}
		out = Tip{UserID: user, AuthorID: post.AuthorID, PostID: post.ID, RequestID: in.RequestID, Currency: in.Currency, Amount: amount, Precision: rules.Precision, Symbol: rules.Symbol, CurrencyVersion: in.CurrencyVersion, Hidden: in.Hidden, CreatedAt: time.Now().UTC()}
		st.Tips[key] = out
		return nil
	})
	if err != nil {
		return out, err
	}
	result, err := coin.Transfer(ctx, virtualcurrency.TransferInput{UserID: user, RecipientID: out.AuthorID, Amount: out.Amount, ExpectedVersion: out.CurrencyVersion, RequestID: out.PaymentRequest()})
	if err != nil {
		return out, err
	}
	out.EntryID = result.Sent.ID
	err = s.Backend.Update(ctx, func(st *State) error { st.Tips[key] = out; return nil })
	return out, err
}

type Support struct {
	Supported bool   `json:"supported"`
	Label     string `json:"label,omitempty"`
}

// Supports contains no hidden amount, count, receipt, or currency metadata.
func (s *Service) Supports(ctx context.Context, st State, post int64, hide bool) map[int64]Support {
	result := map[int64]Support{}
	totals := map[int64]map[string]int64{}
	symbols := map[string]Tip{}
	hidden := map[int64]bool{}
	for _, t := range st.Tips {
		if t.PostID != post {
			continue
		}
		if t.EntryID == 0 {
			id, e := s.tipReceipt(ctx, t)
			if e != nil || id == 0 {
				continue
			}
		}
		result[t.UserID] = Support{Supported: true, Label: "已打赏"}
		hidden[t.UserID] = hidden[t.UserID] || t.Hidden || st.Settings.HideAmounts || hide
		if totals[t.UserID] == nil {
			totals[t.UserID] = map[string]int64{}
		}
		old := totals[t.UserID][t.Currency]
		if old > (1<<63-1)-t.Amount {
			hidden[t.UserID] = true
			continue
		}
		totals[t.UserID][t.Currency] += t.Amount
		symbols[t.Currency] = t
	}
	for user, currencies := range totals {
		if hidden[user] {
			continue
		}
		keys := make([]string, 0, len(currencies))
		for k := range currencies {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		label := "已打赏 "
		for i, key := range keys {
			if i > 0 {
				label += " · "
			}
			t := symbols[key]
			label += fmt.Sprintf("%s %s", virtualcurrency.FormatAmount(currencies[key], t.Precision), t.Symbol)
		}
		result[user] = Support{Supported: true, Label: label}
	}
	return result
}
