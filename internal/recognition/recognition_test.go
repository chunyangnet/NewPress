package recognition

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/virtualcurrency"
)

type testUsers struct{}

func (testUsers) GetUser(_ context.Context, id int64) (auth.User, error) {
	return auth.User{ID: id, Status: auth.UserStatusActive}, nil
}

type testPosts struct {
	content.Repository
	post content.Post
}

func (p *testPosts) GetByID(_ context.Context, id int64) (content.Post, error) {
	if id == p.post.ID {
		return p.post, nil
	}
	return content.Post{}, content.ErrNotFound
}
func fixture(t *testing.T) (*Service, *virtualcurrency.FileStore, TipInput, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "recognition.json")
	b, err := NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	coin, err := virtualcurrency.NewFileStore(filepath.Join(dir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	// These scenarios explicitly use a whole-unit currency.
	cfg := virtualcurrency.DefaultSettings()
	cfg.Precision = 0
	cfg.Enabled = true
	cfg.AllowTransfers = true
	cfg, err = coin.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 2, Delta: 1000, ExpectedVersion: cfg.Version, RequestID: "recognition-fixture-credit", Reason: "测试积分"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Backend: b, Users: testUsers{}, Currencies: virtualcurrency.NewMemoryCatalog(coin), Content: &testPosts{post: content.Post{ID: 7, AuthorID: 1, Type: content.TypePost, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)}}}
	settings := InitialState().Settings
	settings.TipsEnabled = true
	settings.VerificationEnabled = true
	if err = s.SaveSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	return s, coin, TipInput{PostID: 7, RequestID: "recognition-tip-request", Currency: "main", Amount: "10", SettingsVersion: 2, CurrencyVersion: cfg.Version}, path
}
func TestTipsConcurrentReplayPrivacyAndRestart(t *testing.T) {
	s, coin, in, path := fixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { _, e := s.Tip(t.Context(), 2, in); errs <- e })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	from, _ := coin.Balance(t.Context(), 2)
	to, _ := coin.Balance(t.Context(), 1)
	if from != 990 || to != 10 {
		t.Fatal("duplicate debit", from, to)
	}
	bad := in
	bad.Amount = "20"
	if _, e := s.Tip(t.Context(), 2, bad); !errors.Is(e, virtualcurrency.ErrIdempotencyConflict) {
		t.Fatal(e)
	}
	st, _ := s.Backend.Read(t.Context())
	v := s.Supports(t.Context(), st, 7, false)
	if v[2].Label != "已打赏 10 积分" {
		t.Fatal(v)
	}
	if len(s.Supports(t.Context(), st, 8, false)) != 0 {
		t.Fatal("tip leaked to another article")
	}
	in.RequestID = "recognition-hidden-request"
	in.Hidden = true
	in.Amount = "30"
	if _, err := s.Tip(t.Context(), 2, in); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Backend.Read(t.Context())
	raw, _ := json.Marshal(s.Supports(t.Context(), st, 7, false))
	if strings.Contains(string(raw), "10") || strings.Contains(string(raw), "30") || strings.Contains(string(raw), "40") || strings.Contains(string(raw), "积分") {
		t.Fatal("private amount exposed", string(raw))
	}
	loaded, err := NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Backend = loaded
	if err = s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Tip(t.Context(), 2, in); err != nil {
		t.Fatal(err)
	}
	balance, _ := coin.Balance(t.Context(), 2)
	if balance != 960 {
		t.Fatal(balance)
	}
}
func TestTipEligibilityAndUnpaidReservations(t *testing.T) {
	s, coin, in, _ := fixture(t)
	if _, err := s.Tip(t.Context(), 1, in); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	in.Amount = "0.1"
	if _, err := s.Tip(t.Context(), 2, in); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	in.Amount = "2000"
	if _, err := s.Tip(t.Context(), 2, in); !errors.Is(err, virtualcurrency.ErrInsufficient) {
		t.Fatal(err)
	}
	st, _ := s.Backend.Read(t.Context())
	if len(s.Supports(t.Context(), st, 7, false)) > 0 {
		t.Fatal("failed tip awarded badge")
	}
	amount, _ := coin.Balance(t.Context(), 2)
	if amount != 1000 {
		t.Fatal(amount)
	}
	p := s.Content.(*testPosts)
	p.post.Status = content.StatusDraft
	in.Amount = "10"
	in.RequestID = "recognition-draft-request"
	if _, err := s.Tip(t.Context(), 2, in); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

type failCompletion struct {
	Backend
	fail bool
}

func (b *failCompletion) Update(ctx context.Context, fn func(*State) error) error {
	return b.Backend.Update(ctx, func(s *State) error {
		if e := fn(s); e != nil {
			return e
		}
		if b.fail {
			for _, t := range s.Tips {
				if t.EntryID > 0 {
					return ErrUnavailable
				}
			}
		}
		return nil
	})
}
func TestTipRecoversAfterLedgerCommit(t *testing.T) {
	s, coin, in, _ := fixture(t)
	base := s.Backend
	failure := &failCompletion{Backend: base, fail: true}
	s.Backend = failure
	if _, err := s.Tip(t.Context(), 2, in); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	st, _ := base.Read(t.Context())
	if st.Tips[TipKey(2, in.RequestID)].EntryID != 0 {
		t.Fatal("completion unexpectedly committed")
	}
	if !s.Supports(t.Context(), st, 7, false)[2].Supported {
		t.Fatal("committed ledger receipt not recognized")
	}
	failure.fail = false
	if tip, err := s.Tip(t.Context(), 2, in); err != nil || tip.EntryID == 0 {
		t.Fatal(tip, err)
	}
	amount, _ := coin.Balance(t.Context(), 2)
	if amount != 990 {
		t.Fatal("retry charged twice", amount)
	}
}
func TestAwardsVerificationPrivateEvidenceExpiryAndRevocation(t *testing.T) {
	s, _, _, path := fixture(t)
	a, err := s.UpdateIdentity(t.Context(), 2, false, IdentityInput{UserID: 2, Version: 1, Action: "apply", Key: "creator", Label: "摄影作者", Evidence: "private-evidence.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateIdentity(t.Context(), 2, false, IdentityInput{UserID: 2, Version: a.Version, Action: "approve", Key: "creator", Reason: "自行通过"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	a, err = s.UpdateIdentity(t.Context(), 1, true, IdentityInput{UserID: 2, Version: a.Version, Action: "approve", Key: "creator", Reason: "作品核验完成", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Backend.Read(t.Context())
	badges := st.PublicBadges(2, time.Now())
	raw, _ := json.Marshal(badges)
	if len(badges) != 1 || strings.Contains(string(raw), "private-evidence") {
		t.Fatal(string(raw))
	}
	if len(st.PublicBadges(2, time.Now().Add(48*time.Hour))) != 0 {
		t.Fatal("expired verification visible")
	}
	a, err = s.UpdateIdentity(t.Context(), 1, true, IdentityInput{UserID: 2, Version: a.Version, Action: "grant", Key: "original", Reason: "优秀原创", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.UpdateIdentity(t.Context(), 2, false, IdentityInput{UserID: 2, Version: a.Version, Action: "equip", Equipped: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.Backend.Read(t.Context())
	if len(st.PublicBadges(2, time.Now())) != 1 {
		t.Fatal("unequipped medal visible")
	}
	a, err = s.UpdateIdentity(t.Context(), 1, true, IdentityInput{UserID: 2, Version: a.Version, Action: "revoke", Key: "creator", Reason: "认证到期复核"})
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.Backend.Read(t.Context())
	if len(st.PublicBadges(2, time.Now())) != 0 || a.Application.Status != "revoked" {
		t.Fatal(st, a)
	}
	if _, err = s.UpdateIdentity(t.Context(), 2, false, IdentityInput{UserID: 2, Version: 1, Action: "equip"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = NewFileBackend(path); err != nil {
		t.Fatal(err)
	}
}
func TestSettingsRejectUnsafeDesignsAndUsedKindChanges(t *testing.T) {
	s, _, _, _ := fixture(t)
	st, _ := s.Backend.Read(t.Context())
	cfg := st.Settings
	for _, image := range []string{"https://external.test/track.png", "javascript:alert(1)", "/media/../secret", "/media/vector.svg", "/media/vector.%73vg", "/media/page.html", "/media/icon.png?redirect=1"} {
		cfg.Badges[0].Image = image
		if ValidateSettings(cfg) == nil {
			t.Fatal(image)
		}
	}
	cfg.Badges[0].Image = ""
	cfg.Badges[0].Color = "red; color: transparent"
	if ValidateSettings(cfg) == nil {
		t.Fatal("unsafe color")
	}
	_, err := s.UpdateIdentity(t.Context(), 1, true, IdentityInput{UserID: 2, Version: 1, Action: "grant", Key: "original", Reason: "优秀原创"})
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.Backend.Read(t.Context())
	st.Settings.Badges[1].Kind = "verified"
	if err = s.SaveSettings(t.Context(), st.Settings); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
