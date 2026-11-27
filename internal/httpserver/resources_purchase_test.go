package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

type unlockFixture struct {
	s       *Server
	h       http.Handler
	repo    content.Repository
	service *commerce.Service
	coin    *virtualcurrency.FileStore
	owner   auth.Principal
	reader  auth.Principal
	cookie  *http.Cookie
	post    content.Post
	action  string
}

func newUnlockFixture(t *testing.T, action string) *unlockFixture {
	t.Helper()
	s, h, accounts, repo, _, owner := newPersistentAdminTestServer(t)
	service, _, coin := commerceHTTPStore(t, s)
	_, cookie, reader := createAndLoginTestUser(t, accounts, owner, "unlock-reader", "unlock-reader@example.test", "资源读者", "subscriber")
	c := resources.Condition{ID: "price", Kind: "payment", Currency: "CNY", Amount: 990}
	if action == "points" {
		c.Kind, c.Currency, c.Amount = "points", "main", 100
	}
	cfg := resourceFixtureConfig()
	cfg.Items[0].Variants[0].Groups = []resources.Group{{Name: "购买", Conditions: []resources.Condition{c}}}
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "资源购买回归", Slug: "unlock-regression", Status: content.StatusPublish, Resources: &cfg})
	quote, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: reader.UserID, Delta: 1000, ExpectedVersion: quote.Version, RequestID: "unlock-resource-funding", Reason: "资源回归余额"}); err != nil {
		t.Fatal(err)
	}
	return &unlockFixture{s: s, h: h, repo: repo, service: service, coin: coin, owner: owner, reader: reader, cookie: cookie, post: p, action: action}
}

func (f *unlockFixture) item() resources.Resource { return resources.FromMeta(f.post.Meta).Items[0] }
func (f *unlockFixture) endpoint() string {
	return fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", f.post.ID)
}
func (f *unlockFixture) purchase() resourceActionInput {
	return resourceActionInput{Action: f.action, Variant: "complete", Condition: "price", Group: 0, Revision: resources.Hash(f.item()), Channel: "epay", Accepted: true}
}
func (f *unlockFixture) call(t *testing.T, in resourceActionInput) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return serveContentAPI(f.h, f.cookie, "POST", f.endpoint(), string(raw), authenticatedAPIHeaders(f.reader))
}
func (f *unlockFixture) change(t *testing.T, mutate func(*resources.Resource)) {
	t.Helper()
	in := inputFromPost(f.post)
	mutate(&in.Resources.Items[0])
	p, err := f.repo.Update(t.Context(), f.post.ID, content.Mutation{ExpectedVersion: f.post.Version}, in)
	if err != nil {
		t.Fatal(err)
	}
	f.post = p
}

type unlockVariant struct {
	ID, Blocked string
	Allowed     bool
	Groups      []resources.GroupStatus
}

func (f *unlockFixture) variant(t *testing.T, id string) unlockVariant {
	t.Helper()
	out := serveContentAPI(f.h, f.cookie, "GET", f.endpoint(), "", nil)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var state struct{ Variants []unlockVariant }
	if err := json.Unmarshal(out.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	for _, v := range state.Variants {
		if v.ID == id {
			return v
		}
	}
	t.Fatal("missing variant", id)
	return unlockVariant{}
}
func (f *unlockFixture) order(t *testing.T, out *httptest.ResponseRecorder) commerce.Order {
	t.Helper()
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var public struct{ ID string }
	if err := json.Unmarshal(out.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	o, err := f.service.Order(t.Context(), f.reader.UserID, public.ID)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func (f *unlockFixture) settle(t *testing.T, o commerce.Order) commerce.Order {
	t.Helper()
	if _, err := f.service.Notify(t.Context(), o.ID, http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, []byte(epayHTTPNotificationTrade(o.ID, virtualcurrency.FormatAmount(o.Amount, o.Precision), "fixture-merchant-secret", "repair-"+o.ID))); err != nil {
		t.Fatal(err)
	}
	next, err := f.service.Order(t.Context(), f.reader.UserID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestResourcePurchaseRepricingPreservesNewAndLegacyReceipts(t *testing.T) {
	for _, action := range []string{"points", "pay"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", action, legacy), func(t *testing.T) {
				f := newUnlockFixture(t, action)
				var original commerce.Order
				if legacy {
					item := f.item()
					v, c := item.Variants[0], item.Variants[0].Groups[0].Conditions[0]
					if action == "points" {
						quote, _ := f.coin.Settings(t.Context())
						if _, _, err := f.coin.Spend(t.Context(), resourceSpend(f.reader.UserID, f.post.ID, item, v, c, quote.Version)); err != nil {
							t.Fatal(err)
						}
					} else {
						var err error
						original, err = f.service.CreateResourcePayment(t.Context(), commerce.ResourcePayment{UserID: f.reader.UserID, Key: resources.ConditionKey(f.post.ID, item, v, c), Title: "旧资源订单", Currency: c.Currency, Amount: c.Amount, Channel: "epay", Accepted: true, URL: content.PublicPath(f.post) + "#resource-bundle"})
						if err != nil {
							t.Fatal(err)
						}
					}
				} else {
					out := f.call(t, f.purchase())
					if out.Code != 200 {
						t.Fatal(out.Code, out.Body.String())
					}
					if action == "pay" {
						original = f.order(t, out)
					}
				}
				if action == "pay" {
					if original = f.settle(t, original); original.Status != "completed" {
						t.Fatal(original.Status)
					}
				}
				if !f.variant(t, "complete").Allowed {
					t.Fatal("initial purchase did not unlock")
				}
				if action == "points" {
					if _, _, err := f.s.currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "bonus", Name: "奖励币", Symbol: "点", Precision: 1, ActorID: f.owner.UserID, RequestID: "unlock-new-currency"}); err != nil {
						t.Fatal(err)
					}
				}
				f.change(t, func(r *resources.Resource) {
					c := &r.Variants[0].Groups[0].Conditions[0]
					c.Amount *= 2
					if action == "points" {
						c.Currency = "bonus"
					} else {
						c.Currency = "USD"
					}
				})
				if !f.variant(t, "complete").Allowed {
					t.Fatal("repricing revoked an original receipt")
				}
				for range 2 {
					out := f.call(t, f.purchase())
					if out.Code != 200 {
						t.Fatal(out.Code, out.Body.String())
					}
					if action == "pay" && f.order(t, out).ID != original.ID {
						t.Fatal("repricing created a second payment")
					}
				}
				balance, err := f.coin.Balance(t.Context(), f.reader.UserID)
				if err != nil || action == "points" && balance != 900 {
					t.Fatal("second debit", balance, err)
				}
				if action == "pay" {
					if err := f.service.Backend.Update(t.Context(), func(st *commerce.State) error {
						o := st.Orders[original.ID]
						o.Status = "refunded"
						st.Orders[o.ID] = o
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if f.variant(t, "complete").Allowed {
						t.Fatal("refund retained access after repricing")
					}
				} else {
					f.change(t, func(r *resources.Resource) { r.Variants[0].Groups[0].Conditions[0].ID = "another-price" })
					if f.variant(t, "complete").Allowed {
						t.Fatal("purchase leaked to another condition")
					}
				}
			})
		}
	}
}

func TestResourceFirstSelectionGatesPurchaseAndDelivery(t *testing.T) {
	for _, action := range []string{"points", "pay"} {
		for _, shadowed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/shadowed=%t", action, shadowed), func(t *testing.T) {
				f := newUnlockFixture(t, action)
				f.change(t, func(r *resources.Resource) {
					r.Selection = "first"
					preview := resources.Variant{ID: "preview", Name: "公开版", Kind: "content", Body: "<p>PUBLIC</p>", Groups: []resources.Group{{Conditions: []resources.Condition{{ID: "public", Kind: "public"}}}}}
					if shadowed {
						r.Variants = append([]resources.Variant{preview}, r.Variants...)
					} else {
						r.Variants = append(r.Variants, preview)
					}
				})
				v := f.variant(t, "complete")
				out := f.call(t, f.purchase())
				if shadowed {
					if out.Code != 409 || v.Blocked != "selection" || v.Groups[0].Conditions[0].Purchasable {
						t.Fatal("shadowed purchase offered", out.Code, v)
					}
					balance, _ := f.coin.Balance(t.Context(), f.reader.UserID)
					st, _ := f.service.Backend.Read(t.Context())
					if balance != 1000 || len(st.Orders) != 0 {
						t.Fatal("shadowed purchase charged")
					}
				} else {
					if out.Code != 200 {
						t.Fatal(out.Code, out.Body.String())
					}
					if action == "pay" {
						f.settle(t, f.order(t, out))
					}
					if !f.variant(t, "complete").Allowed || f.variant(t, "preview").Allowed {
						t.Fatal("first selected version inconsistent")
					}
				}
				download := resourceActionInput{Action: "download", Variant: "complete", Revision: resources.Hash(f.item()), Link: "baidu", Request: "selection-download"}
				got := f.call(t, download)
				if shadowed && got.Code != 403 || !shadowed && got.Code != 200 {
					t.Fatal("delivery disagrees with selection", got.Code, got.Body.String())
				}
			})
		}
	}
}

func TestResourcePaymentDeadlineAndWithdrawalPreserveVerifiedMoney(t *testing.T) {
	for _, change := range []string{"deadline", "withdrawal", "selection", "condition"} {
		t.Run(change, func(t *testing.T) {
			f := newUnlockFixture(t, "pay")
			ends := time.Now().Add(time.Minute).UTC()
			f.change(t, func(r *resources.Resource) { r.EndsAt = &ends })
			o := f.order(t, f.call(t, f.purchase()))
			if !o.ExpiresAt.Equal(ends) || !o.ResourceDeadline.Equal(ends) || o.ResourceTerms == nil {
				t.Fatal("deadline snapshot missing", o.ExpiresAt, o.ResourceDeadline)
			}
			switch change {
			case "deadline":
				past := time.Now().Add(-time.Second).UTC()
				if err := f.service.Backend.Update(t.Context(), func(st *commerce.State) error {
					v := st.Orders[o.ID]
					v.CreatedAt = past.Add(-time.Minute)
					v.ExpiresAt = past
					v.ResourceDeadline = past
					v.ResourceTerms.EndsAt = &past
					st.Orders[v.ID] = v
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "withdrawal":
				if _, err := f.repo.ChangeStatus(t.Context(), f.post.ID, content.Mutation{ExpectedVersion: f.post.Version}, content.StatusPrivate); err != nil {
					t.Fatal(err)
				}
			case "selection":
				f.change(t, func(r *resources.Resource) {
					r.Selection = "first"
					r.Variants = append([]resources.Variant{{ID: "public", Name: "公开版", Kind: "content", Groups: []resources.Group{{Conditions: []resources.Condition{{ID: "public", Kind: "public"}}}}}}, r.Variants...)
				})
			case "condition":
				f.change(t, func(r *resources.Resource) { r.Variants[0].Groups[0].Conditions[0].ID = "new-condition" })
			}
			done := f.settle(t, o)
			if done.Status != "review" || done.TransactionID == "" || done.PaidAt.IsZero() || !done.CompletedAt.IsZero() || done.ErrorCode != "resource_delivery_requires_reconciliation" {
				t.Fatal("paid but undeliverable order falsely completed", done.Status, done.ErrorCode)
			}
			if f.settle(t, o).Status != "review" {
				t.Fatal("duplicate payment escaped reconciliation")
			}
		})
	}
}

type unlockCommitFailure struct {
	resources.Backend
	armed atomic.Bool
}

var errUnlockCommit = errors.New("resource projection commit interrupted")

func (b *unlockCommitFailure) Update(ctx context.Context, fn func(*resources.State) error) error {
	return b.Backend.Update(ctx, func(st *resources.State) error {
		if err := fn(st); err != nil {
			return err
		}
		for _, p := range st.Purchases {
			if p.Status == "completed" && b.armed.CompareAndSwap(true, false) {
				return errUnlockCommit
			}
		}
		return nil
	})
}

func TestResourcePointsRecoverInterruptedProjectionAfterRepricing(t *testing.T) {
	f := newUnlockFixture(t, "points")
	b := &unlockCommitFailure{Backend: f.s.resources.Backend}
	b.armed.Store(true)
	f.s.resources.Backend = b
	if got := f.call(t, f.purchase()); got.Code != 503 {
		t.Fatal("interruption was not reached", got.Code, got.Body.String())
	}
	if !f.variant(t, "complete").Allowed {
		t.Fatal("committed wallet receipt not recoverable")
	}
	f.change(t, func(r *resources.Resource) { r.Variants[0].Groups[0].Conditions[0].Amount = 200 })
	if got := f.call(t, f.purchase()); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	st, err := b.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Purchases {
		if p.Status != "completed" || p.Terms.Amount != 100 {
			t.Fatal("original debit snapshot lost", p)
		}
	}
	balance, _ := f.coin.Balance(t.Context(), f.reader.UserID)
	entries, _ := f.coin.PurchaseHistory(t.Context(), f.reader.UserID)
	if balance != 900 || len(entries) != 1 || len(st.Unlocks) != 1 {
		t.Fatal("recovery charged again", balance, len(entries), len(st.Unlocks))
	}
}

func TestResourcePointsConcurrentRequestsDebitOnce(t *testing.T) {
	f := newUnlockFixture(t, "points")
	in := f.purchase()
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			if out := f.call(t, in); out.Code != 200 {
				t.Error(out.Code, out.Body.String())
			}
		})
	}
	wg.Wait()
	balance, _ := f.coin.Balance(t.Context(), f.reader.UserID)
	entries, _ := f.coin.PurchaseHistory(t.Context(), f.reader.UserID)
	if balance != 900 || len(entries) != 1 {
		t.Fatal("concurrent duplicate debit", balance, len(entries))
	}
}

func TestResourcePointsSealOldReservationBeforeUsingNewPrice(t *testing.T) {
	f := newUnlockFixture(t, "points")
	r := f.item()
	v := r.Variants[0]
	quote, _ := f.coin.Settings(t.Context())
	p, err := f.s.resources.BeginPointPurchase(t.Context(), resources.PointPurchase{UserID: f.reader.UserID, Terms: resources.Terms(f.post.ID, r, v, v.Groups[0].Conditions[0]), CurrencyVersion: quote.Version, Precision: quote.Precision, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	f.change(t, func(r *resources.Resource) { r.Variants[0].Groups[0].Conditions[0].Amount = 200 })
	if got := f.call(t, f.purchase()); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	sealed, replay, err := f.coin.Spend(t.Context(), resourcePointSpend(p))
	if err != nil || !replay || sealed.Kind != virtualcurrency.PurchaseCanceled {
		t.Fatal("old debit still executable", sealed.Kind, replay, err)
	}
	balance, _ := f.coin.Balance(t.Context(), f.reader.UserID)
	if balance != 800 {
		t.Fatal("incorrect repriced debit", balance)
	}
}

func TestResourceHistoryCannotAuthorizePurchaseAndQuotaBlocksNewCharges(t *testing.T) {
	f := newUnlockFixture(t, "points")
	if err := f.s.resources.RecordUnlock(t.Context(), resources.Unlock{Actor: resourceActorID(f.reader.UserID), PostID: f.post.ID, Resource: "bundle", Variant: "complete", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if f.variant(t, "complete").Allowed {
		t.Fatal("history granted access without a financial receipt")
	}
	f.change(t, func(r *resources.Resource) { r.TotalLimit = 1 })
	if _, err := f.s.resources.Consume(t.Context(), resources.Delivery{Actor: resourceActorID(f.reader.UserID), PostID: f.post.ID, Resource: "bundle", Variant: "complete", Link: "baidu", Request: "previous-download", CreatedAt: time.Now().UTC()}, f.item()); err != nil {
		t.Fatal(err)
	}
	if f.variant(t, "complete").Groups[0].Conditions[0].Purchasable {
		t.Fatal("no-download purchase offered")
	}
	if out := f.call(t, f.purchase()); out.Code != 429 {
		t.Fatal(out.Code, out.Body.String())
	}
	balance, _ := f.coin.Balance(t.Context(), f.reader.UserID)
	if balance != 1000 {
		t.Fatal("charged with no delivery quota", balance)
	}
}

func TestResourcePaymentPolicyChangeRequiresFreshConfirmation(t *testing.T) {
	f := newUnlockFixture(t, "pay")
	cfg, err := f.service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in := f.purchase()
	in.PaymentVersion = cfg.Version
	cfg.RefundPolicy = "已更新的退款条款"
	if _, err = f.service.UpdateSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if out := f.call(t, in); out.Code != 409 {
		t.Fatal("stale policy accepted", out.Code, out.Body.String())
	}
	st, _ := f.service.Backend.Read(t.Context())
	if len(st.Orders) != 0 {
		t.Fatal("stale policy created a payment")
	}
}

type unlockDelayedSpend struct{ *virtualcurrency.FileStore }

func (s *unlockDelayedSpend) Spend(ctx context.Context, in virtualcurrency.SpendInput) (virtualcurrency.Entry, bool, error) {
	if _, ok := ctx.Deadline(); !ok {
		return virtualcurrency.Entry{}, false, errors.New("resource debit has no deadline")
	}
	<-ctx.Done()
	// The real wallet also checks context after obtaining its transaction lock.
	return s.FileStore.Spend(ctx, in)
}

type unlockSpendingCatalog struct {
	virtualcurrency.Catalog
	store virtualcurrency.Store
}

func (c *unlockSpendingCatalog) Store(ctx context.Context, key string) (virtualcurrency.Store, error) {
	if key == "main" {
		return c.store, nil
	}
	return c.Catalog.Store(ctx, key)
}

func TestResourcePointsDeadlineWhileWaitingDoesNotDebit(t *testing.T) {
	f := newUnlockFixture(t, "points")
	end := time.Now().Add(750 * time.Millisecond).UTC()
	f.change(t, func(r *resources.Resource) { r.EndsAt = &end })
	f.s.currencyCatalog = &unlockSpendingCatalog{Catalog: f.s.currencyCatalog, store: &unlockDelayedSpend{FileStore: f.coin}}
	out := f.call(t, f.purchase())
	if out.Code != 409 {
		t.Fatal("expired wallet wait did not stop purchase", out.Code, out.Body.String())
	}
	balance, err := f.coin.Balance(t.Context(), f.reader.UserID)
	if err != nil || balance != 1000 {
		t.Fatal("expired wait debited points", balance, err)
	}
	st, err := f.s.resources.Backend.Read(t.Context())
	if err != nil || len(st.Purchases) != 1 || len(st.Unlocks) != 0 {
		t.Fatal("expired reservation was not preserved", err, st)
	}
	for _, p := range st.Purchases {
		entry, found, err := f.coin.SpendReceipt(t.Context(), resourcePointSpend(p))
		if err != nil || !found || entry.Kind != virtualcurrency.PurchaseCanceled || p.Status != "cancelled" {
			t.Fatal("expired debit remained executable", entry, p, err)
		}
	}
}
