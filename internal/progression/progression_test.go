package progression

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

type testUsers struct{ items map[int64]auth.User }

func (u testUsers) GetUser(_ context.Context, id int64) (auth.User, error) {
	v, ok := u.items[id]
	if !ok {
		return v, auth.ErrUserNotFound
	}
	return v, nil
}

type testPosts struct {
	content.Repository
	post content.Post
}

func (p *testPosts) GetByID(_ context.Context, id int64) (content.Post, error) {
	if p.post.ID != id {
		return content.Post{}, content.ErrNotFound
	}
	return p.post, nil
}
func fixture(t *testing.T) (*Service, *virtualcurrency.FileStore, Settings, *time.Time, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "progression.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	store.now = func() time.Time { return now.Add(-time.Hour) }
	cfg := DefaultSettings()
	cfg.Experience.Enabled = true
	cfg.Levels.Enabled = true
	cfg.CheckIn.Enabled = true
	cfg.Registration.Enabled = true
	cfg.Publication.Enabled = true
	cfg.Publication.MinLength = 4
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "10.25"
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }
	coin, err := virtualcurrency.NewFileStore(filepath.Join(dir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := virtualcurrency.DefaultSettings()
	c.Enabled = true
	c.Precision = 2
	c, err = coin.UpdateSettings(t.Context(), 1, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 2, Delta: 100_000, ExpectedVersion: c.Version, RequestID: "progression-fixture-fund", Reason: "测试余额"}); err != nil {
		t.Fatal(err)
	}
	users := testUsers{items: map[int64]auth.User{1: {ID: 1, Status: auth.UserStatusActive, CreatedAt: now, EmailVerifiedAt: &now}, 2: {ID: 2, Status: auth.UserStatusActive, CreatedAt: now, EmailVerifiedAt: &now}}}
	posts := &testPosts{post: content.Post{ID: 10, AuthorID: 2, Type: content.TypePost, Status: content.StatusPublish, CreatedAt: now.Add(-time.Minute), PublishedAt: now.Add(-time.Minute), Version: 1, ContentHTML: "<p>合格的正文</p>"}}
	return &Service{Store: store, Users: users, Content: posts, Currencies: virtualcurrency.NewMemoryCatalog(coin)}, coin, cfg, &now, path
}
func purchaseInput(cfg Settings) PurchaseInput {
	return PurchaseInput{UserID: 2, ExpectedVersion: cfg.Version, Plan: "member", Mode: "grant", RequestID: "progression-purchase-one"}
}
func TestExperienceLevelsRewardsMembershipExpiry(t *testing.T) {
	s, _, cfg, now, path := fixture(t)
	grant := MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "grant", Plan: "member", Days: 30, RequestID: "admin-membership-grant", Reason: "活动开通"}
	r, err := s.Store.ManageMembership(t.Context(), grant)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Store.ManageMembership(t.Context(), grant)
	if err != nil || !replay.Replayed || replay.Entry.ID != r.Entry.ID {
		t.Fatal(replay, err)
	}
	if _, err = s.Registration(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	check, err := s.CheckIn(t.Context(), 2, cfg.Version, "experience-check-in-one")
	if err != nil || check.Entry.Delta != 12 {
		t.Fatal(check, err)
	}
	for _, request := range []string{"experience-check-in-one", "experience-check-in-two"} {
		if r, err := s.CheckIn(t.Context(), 2, cfg.Version, request); err != nil || !r.Replayed {
			t.Fatal(r, err)
		}
	}
	event := activity.New(activity.Publication, 10, 10, 2, 1, content.StatusPublish, now.Add(-time.Minute))
	for range 2 {
		if err = s.Observe(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Experience != 199 || view.Level.Key != "reader" || view.NextLevel.Threshold != 500 || !view.MembershipActive {
		t.Fatal(view, err)
	}
	*now = now.Add(31 * 24 * time.Hour)
	r, err = s.CheckIn(t.Context(), 2, cfg.Version, "experience-after-expiry")
	if err != nil || r.Entry.Delta != 10 {
		t.Fatal(r, err)
	}
	view, err = s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.MembershipActive {
		t.Fatal(view, err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.Store.now
	view, err = reopened.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Experience != 209 {
		t.Fatal(view, err)
	}
	if r, err = reopened.Reward(t.Context(), RewardInput{UserID: 2, Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: "experience-check-in-two", VerifiedEmail: true}); err != nil || r.Entry.ID != check.Entry.ID {
		t.Fatal("old check-in request issued again", r, err)
	}
}
func TestExperienceLimitsAndSettingsConflicts(t *testing.T) {
	s, _, cfg, _, _ := fixture(t)
	in := Adjustment{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Delta: cfg.Experience.Maximum, RequestID: "experience-adjust-max", Reason: "边界测试"}
	if _, err := s.Store.Adjust(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{1, math.MaxInt64, math.MinInt64, -cfg.Experience.Maximum - 1} {
		in.RequestID = fmt.Sprintf("experience-limit-%d", delta)
		in.Delta = delta
		if _, err := s.Store.Adjust(t.Context(), in); err == nil {
			t.Fatal(delta)
		}
	}
	lower := CloneSettings(cfg)
	lower.Experience.Maximum--
	if _, err := s.Store.UpdateSettings(t.Context(), cfg.Version, lower); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	bad := CloneSettings(cfg)
	bad.Levels.Items[1].Threshold = 0
	if _, err := s.Store.UpdateSettings(t.Context(), cfg.Version, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	bad = CloneSettings(cfg)
	bad.Membership.Plans[0].Color = "red;display:none"
	if ValidateSettings(bad) == nil {
		t.Fatal("CSS accepted")
	}
	if _, err := s.Store.UpdateSettings(t.Context(), 1, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestConcurrentPurchaseAndRenewalReplays(t *testing.T) {
	s, coin, cfg, _, path := fixture(t)
	in := purchaseInput(cfg)
	var wg sync.WaitGroup
	var orders sync.Map
	for range 16 {
		wg.Go(func() {
			o, err := s.Purchase(t.Context(), in)
			if err != nil {
				t.Error(err)
			}
			orders.Store(o.PaymentEntry, true)
		})
	}
	wg.Wait()
	n := 0
	orders.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatal("multiple payments", n)
	}
	balance, _ := coin.Balance(t.Context(), 2)
	if balance != 100_000-1025 {
		t.Fatal(balance)
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Membership.Version != 1 || view.PendingOrder != nil {
		t.Fatal(view, err)
	}
	oldUntil := view.Account.Membership.Until
	in.Mode = "renew"
	in.MemberVersion = 1
	in.RequestID = "progression-renewal-one"
	o, err := s.Purchase(t.Context(), in)
	if err != nil || o.Status != "completed" {
		t.Fatal(o, err)
	}
	view, _ = s.Store.Snapshot(t.Context(), 2, 0)
	if !view.Account.Membership.Until.Equal(oldUntil.Add(30 * 24 * time.Hour)) {
		t.Fatal(view)
	}
	if _, err = NewFileStore(path); err != nil {
		t.Fatal(err)
	}
	if _, err = virtualcurrency.NewFileStore(filepath.Join(filepath.Dir(path), "currency.json")); err != nil {
		t.Fatal("payment ledger restore", err)
	}
	in.Plan = "supporter"
	if _, err = s.Purchase(t.Context(), in); !errors.Is(err, ErrReused) {
		t.Fatal(err)
	}
}
func reserve(t *testing.T, s *Service, cfg Settings, in PurchaseInput) Order {
	t.Helper()
	coin, _ := s.Currencies.Store(t.Context(), "main")
	c, _ := coin.Settings(t.Context())
	o, err := s.Store.BeginPurchase(t.Context(), in, CurrencyQuote{Key: "main", Symbol: c.Symbol, Precision: c.Precision, Version: c.Version, Enabled: c.Enabled})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func TestPurchaseCancellationRaceAndNoAutomaticCharge(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			s, coin, cfg, _, _ := fixture(t)
			in := purchaseInput(cfg)
			o := reserve(t, s, cfg, in)
			if _, done, err := s.Recover(t.Context(), 2); err != nil || done {
				t.Fatal(done, err)
			}
			if balance, _ := coin.Balance(t.Context(), 2); balance != 100_000 {
				t.Fatal("recover charged", balance)
			}
			admin := MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "grant", Plan: "member", Days: 30, Reason: "冲突测试", RequestID: "pending-admin-membership"}
			if _, err := s.Store.ManageMembership(t.Context(), admin); !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			if cancelFirst {
				if _, _, err := coin.CancelSpend(t.Context(), o.Payment()); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, _, err := coin.Spend(t.Context(), o.Payment()); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if _, err := s.Cancel(t.Context(), 2, in.RequestID); err != nil {
						t.Error(err)
					}
				})
				wg.Go(func() {
					if _, err := s.Purchase(t.Context(), in); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			view, err := s.Store.Snapshot(t.Context(), 2, 0)
			if err != nil || view.PendingOrder != nil {
				t.Fatal(view, err)
			}
			balance, _ := coin.Balance(t.Context(), 2)
			if cancelFirst {
				if balance != 100_000 || view.MembershipActive {
					t.Fatal(balance, view)
				}
			} else {
				if balance != 100_000-1025 || !view.MembershipActive {
					t.Fatal(balance, view)
				}
			}
		})
	}
}

type failingBackend struct {
	Backend
	fail atomic.Bool
}
type failingTx struct {
	Transaction
	owner *failingBackend
}

func (t failingTx) Append(e Entry) (Entry, error) {
	if e.Kind == "membership_purchase" && t.owner.fail.Load() {
		return Entry{}, errors.New("injected commit failure")
	}
	return t.Transaction.Append(e)
}
func (b *failingBackend) Write(ctx context.Context, fn func(Transaction) error) error {
	return b.Backend.Write(ctx, func(tx Transaction) error { return fn(failingTx{tx, b}) })
}
func TestPaidOrderRecoveryKeepsTermsAfterConfigurationChanges(t *testing.T) {
	s, coin, cfg, _, path := fixture(t)
	failure := &failingBackend{Backend: s.Store.backend}
	s.Store.backend = failure
	failure.fail.Store(true)
	in := purchaseInput(cfg)
	if _, err := s.Purchase(t.Context(), in); err == nil {
		t.Fatal("failure hidden")
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.PendingOrder == nil || view.MembershipActive {
		t.Fatal(view, err)
	}
	cfg.Membership.Enabled = false
	cfg.Membership.Plans[0].Price = "999"
	cfg.Membership.Plans[0].Days = 365
	if _, err = s.Store.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	failure.fail.Store(false)
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = reopened
	o, done, err := s.Recover(t.Context(), 2)
	if err != nil || !done || o.Status != "completed" || o.Days != 30 || o.Amount != 1025 {
		t.Fatal(o, done, err)
	}
	if _, err = s.Purchase(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	balance, _ := coin.Balance(t.Context(), 2)
	if balance != 100_000-1025 {
		t.Fatal("double charge", balance)
	}
	if _, err = NewFileStore(path); err != nil {
		t.Fatal(err)
	}
}
func TestInsufficientPaymentSealsCancellation(t *testing.T) {
	s, coin, cfg, _, _ := fixture(t)
	c, _ := coin.Settings(t.Context())
	if _, err := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 2, Delta: -100_000, ExpectedVersion: c.Version, RequestID: "drain-before-membership", Reason: "不足测试"}); err != nil {
		t.Fatal(err)
	}
	in := purchaseInput(cfg)
	o, err := s.Purchase(t.Context(), in)
	if !errors.Is(err, virtualcurrency.ErrInsufficient) || o.Status != "cancelled" {
		t.Fatal(o, err)
	}
	if _, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 2, Delta: 100_000, ExpectedVersion: c.Version, RequestID: "refund-test-membership", Reason: "充值测试"}); err != nil {
		t.Fatal(err)
	}
	o, err = s.Purchase(t.Context(), in)
	if err != nil || o.Status != "cancelled" {
		t.Fatal(o, err)
	}
	b, _ := coin.Balance(t.Context(), 2)
	if b != 100_000 {
		t.Fatal("cancelled order later charged", b)
	}
}

func TestAdministrativeRecoveryOnlySettlesRequestedOrder(t *testing.T) {
	s, coin, cfg, _, _ := fixture(t)
	first := purchaseInput(cfg)
	completed, err := s.Purchase(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := PurchaseInput{UserID: 2, ExpectedVersion: cfg.Version, MemberVersion: 1, Plan: "member", Mode: "renew", RequestID: "recovery-newer-purchase"}
	pending := reserve(t, s, cfg, second)
	if _, _, err = coin.Spend(t.Context(), pending.Payment()); err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{first.RequestID, "recovery-missing-order"} {
		o, done, err := s.RecoverRequest(t.Context(), 2, request)
		if request == first.RequestID {
			if err != nil || done || o.RequestID != completed.RequestID || o.Status != "completed" {
				t.Fatal(o, done, err)
			}
		} else if !errors.Is(err, ErrInvalid) || done {
			t.Fatal(o, done, err)
		}
		view, err := s.Store.Snapshot(t.Context(), 2, 0)
		if err != nil || view.Account.Membership.Version != 1 || view.PendingOrder == nil || view.PendingOrder.RequestID != second.RequestID {
			t.Fatal("old recovery changed the newer order", view, err)
		}
	}
	o, done, err := s.RecoverRequest(t.Context(), 2, second.RequestID)
	if err != nil || !done || o.Status != "completed" || o.RequestID != second.RequestID {
		t.Fatal(o, done, err)
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.Account.Membership.Version != 2 || view.PendingOrder != nil {
		t.Fatal(view, err)
	}
	if balance, err := coin.Balance(t.Context(), 2); err != nil || balance != 100_000-2*1025 {
		t.Fatal("recovery changed paid amount", balance, err)
	}
}

func TestConcurrentDistinctPurchasesOnlyChargeOnce(t *testing.T) {
	s, coin, cfg, _, _ := fixture(t)
	var successful atomic.Int64
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			in := purchaseInput(cfg)
			in.RequestID = fmt.Sprintf("distinct-purchase-%d", i)
			o, err := s.Purchase(t.Context(), in)
			if err == nil && o.Status == "completed" {
				successful.Add(1)
			} else if !errors.Is(err, ErrPending) && !errors.Is(err, ErrMemberConflict) {
				t.Error(o, err)
			}
		})
	}
	wg.Wait()
	if successful.Load() != 1 {
		t.Fatal("multiple distinct purchases completed", successful.Load())
	}
	if balance, err := coin.Balance(t.Context(), 2); err != nil || balance != 100_000-1025 {
		t.Fatal(balance, err)
	}
}

func TestMembershipDurationAndUsedPlanRestrictions(t *testing.T) {
	s, _, cfg, now, path := fixture(t)
	grant := MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "grant", Plan: "member", Days: MaxMembershipDays, RequestID: "maximum-membership-grant", Reason: "期限边界测试"}
	r, err := s.Store.ManageMembership(t.Context(), grant)
	if err != nil || !r.Entry.Membership.Until.Equal(now.Add(MaxMembershipDays*24*time.Hour)) {
		t.Fatal(r, err)
	}
	renew := grant
	renew.Action, renew.Days, renew.MemberVersion, renew.RequestID = "renew", 1, 1, "beyond-membership-limit"
	if _, err := s.Store.ManageMembership(t.Context(), renew); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	removed := CloneSettings(cfg)
	removed.Membership.Plans = removed.Membership.Plans[1:]
	if _, err := s.Store.UpdateSettings(t.Context(), cfg.Version, removed); !errors.Is(err, ErrUsedPlan) {
		t.Fatal("removed used plan", err)
	}
	cfg.Membership.Plans[0].Enabled = false
	cfg, err = s.Store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || !view.MembershipActive {
		t.Fatal("disabled plan revoked existing member", view, err)
	}
	cfg.Membership.Enabled = false
	cfg, err = s.Store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || view.MembershipActive || view.Account.Membership.Revoked {
		t.Fatal("module switch changed stored membership", view, err)
	}
	revoke := MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, MemberVersion: 1, Action: "revoke", RequestID: "disabled-module-revoke", Reason: "管理员撤销"}
	if _, err := s.Store.ManageMembership(t.Context(), revoke); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err != nil {
		t.Fatal(err)
	}
}

func TestProgressionFileIntegrityAndExternalChanges(t *testing.T) {
	s, _, cfg, _, path := fixture(t)
	if _, err := s.Purchase(t.Context(), purchaseInput(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckIn(t.Context(), 2, cfg.Version, "integrity-check-in-one"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(map[string]any){
		func(d map[string]any) { d["accounts"].(map[string]any)["2"].(map[string]any)["experience"] = "999" },
		func(d map[string]any) { d["receipts"] = map[string]any{} },
		func(d map[string]any) { d["entries"].([]any)[0].(map[string]any)["days"] = 31 },
		func(d map[string]any) {
			for _, o := range d["orders"].(map[string]any) {
				o.(map[string]any)["amount"] = "1"
			}
		},
		func(d map[string]any) { d["Accounts"] = d["accounts"]; delete(d, "accounts") },
		func(d map[string]any) { d["orders"] = nil },
	} {
		var d map[string]any
		if err = json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		mutation(d)
		bad, _ := json.Marshal(d)
		target := filepath.Join(t.TempDir(), "corrupt.json")
		if err = os.WriteFile(target, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = NewFileStore(target); err == nil {
			t.Fatal("corrupt history accepted", string(bad))
		}
	}
	bad := strings.Replace(string(body), `"schema":1`, `"schema":1,"schema":1`, 1)
	if err = os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if s.Store.Ready(t.Context()) == nil {
		t.Fatal("external change ignored")
	}
	if _, err = s.CheckIn(t.Context(), 2, cfg.Version, "integrity-check-in-two"); err == nil {
		t.Fatal("external change overwritten")
	}
}
func TestPublicationEligibilityAndAccountStatus(t *testing.T) {
	for _, variant := range []string{"short", "private", "author", "old", "unverified", "inactive"} {
		t.Run(variant, func(t *testing.T) {
			s, _, _, now, _ := fixture(t)
			posts := s.Content.(*testPosts)
			users := s.Users.(testUsers)
			u := users.items[2]
			switch variant {
			case "short":
				posts.post.ContentHTML = "<p>短</p><script>不可见</script>"
			case "private":
				posts.post.Status = content.StatusPrivate
			case "author":
				posts.post.AuthorID = 1
			case "old":
				posts.post.CreatedAt = now.Add(-24 * time.Hour)
			case "unverified":
				u.EmailVerifiedAt = nil
			case "inactive":
				u.Status = auth.UserStatusSuspended
			}
			users.items[2] = u
			e := activity.New(activity.Publication, 10, 10, 2, 1, content.StatusPublish, now.Add(-time.Minute))
			if err := s.Observe(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			v, _ := s.Store.Snapshot(t.Context(), 2, 0)
			if v.Account.Experience != 0 {
				t.Fatal(v)
			}
		})
	}
}
