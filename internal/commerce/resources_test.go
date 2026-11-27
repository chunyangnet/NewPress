package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/resources"
)

func resourcePaymentInput() ResourcePayment {
	terms := resources.PurchaseTerms{PostID: 1, Resource: "bundle", Variant: "full", Condition: "payment", Kind: "payment", Currency: "CNY", Amount: 990}
	return ResourcePayment{UserID: 2, Key: terms.LegacyKey(), Terms: &terms, Title: "完整资源", Currency: "CNY", Amount: 990, Channel: "gateway", URL: "/posts/resource#resource-bundle", Accepted: true}
}

func createResourceOrder(t *testing.T, f commerceFixture, in ResourcePayment) Order {
	t.Helper()
	o, err := f.service.CreateResourcePayment(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func expireResourceOrder(t *testing.T, s *Service, id string) Order {
	t.Helper()
	if err := s.Backend.Update(t.Context(), func(st *State) error {
		o := st.Orders[id]
		o.CreatedAt = time.Now().Add(-time.Hour).UTC()
		o.ExpiresAt = time.Now().Add(-time.Second).UTC()
		st.Orders[id] = o
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	o, err := s.Order(t.Context(), 2, id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestResourceRetryRequiresConfirmedProviderClosure(t *testing.T) {
	for _, scenario := range []string{"pending", "failed-attempt", "closed", "local-cancel", "query-error", "never-created"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCommerceFixture(t)
			in := resourcePaymentInput()
			old := expireResourceOrder(t, f.service, createResourceOrder(t, f, in).ID)
			n := paidNotification(old)
			n.Status, n.TransactionID = payment.Pending, ""
			if scenario == "closed" {
				n.Status, n.Closed = payment.Failed, true
			}
			if scenario == "failed-attempt" {
				n.Status = payment.Failed
			}
			if scenario == "local-cancel" || scenario == "never-created" {
				if err := f.service.Backend.Update(t.Context(), func(st *State) error {
					o := st.Orders[old.ID]
					o.Status = "cancelled"
					if scenario == "never-created" {
						o.Checkout = payment.Checkout{}
						o.CheckoutAttempts = 0
					}
					st.Orders[o.ID] = o
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "query-error" {
				f.provider.latest[old.ID] = n
			}
			in.Amount = 1990
			in.Terms.Amount = 1990
			in.Key = in.Terms.LegacyKey()
			next, err := f.service.CreateResourcePayment(t.Context(), in)
			canRetry := scenario == "closed" || scenario == "never-created"
			if canRetry {
				if err != nil || next.ID == old.ID || next.Amount != 1990 || next.Checkout.URL == "" || next.RequestID == old.RequestID {
					t.Fatal("confirmed closure did not reopen safely", next, err)
				}
				sealed, _ := f.service.Order(t.Context(), 2, old.ID)
				if sealed.Status != "cancelled" || sealed.PaymentClosedAt.IsZero() || sealed.Amount != 990 {
					t.Fatal("old receipt modified", sealed)
				}
			} else if !errors.Is(err, ErrPending) || next.ID != old.ID || f.provider.creates.Load() != 1 {
				t.Fatal("uncertain provider order was replaced", next.ID, err, f.provider.creates.Load())
			}
		})
	}
}

func TestResourceConcurrentReopeningAndLateOriginalPayment(t *testing.T) {
	f := newCommerceFixture(t)
	in := resourcePaymentInput()
	old := createResourceOrder(t, f, in)
	closed := paidNotification(old)
	closed.Status, closed.TransactionID, closed.Closed = payment.Failed, "", true
	if err := notify(t, f.service, old, closed); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	for range 24 {
		wg.Go(func() {
			o, err := f.service.CreateResourcePayment(context.Background(), in)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- o.ID
		})
	}
	wg.Wait()
	close(ids)
	var id string
	for next := range ids {
		if id == "" {
			id = next
		}
		if next != id || next == old.ID {
			t.Fatal("multiple live retry orders", next, id)
		}
	}
	if f.provider.creates.Load() != 2 {
		t.Fatal("multiple provider creations", f.provider.creates.Load())
	}
	if err := notify(t, f.service, old, paidNotification(old)); err != nil {
		t.Fatal(err)
	}
	late, _ := f.service.Order(t.Context(), 2, old.ID)
	if late.Status != "review" || late.ErrorCode != "resource_duplicate_payment" || late.TransactionID == "" || late.PaidAt.IsZero() {
		t.Fatal("late payment was lost or fulfilled twice", late)
	}
	if err := notify(t, f.service, old, paidNotification(old)); err != nil {
		t.Fatal(err)
	}
	late, _ = f.service.Order(t.Context(), 2, old.ID)
	if late.Status != "review" {
		t.Fatal("replayed payment escaped review")
	}
	current, _ := f.service.Order(t.Context(), 2, id)
	if current.Status != "pending" {
		t.Fatal("old payment changed new order", current.Status)
	}
}

func TestResourceVerifiedDeadlineFailureIsDurableAndSticky(t *testing.T) {
	f := newCommerceFixture(t)
	in := resourcePaymentInput()
	end := time.Now().Add(time.Minute).UTC()
	in.Terms.EndsAt = &end
	o := createResourceOrder(t, f, in)
	if err := f.service.Backend.Update(t.Context(), func(st *State) error {
		v := st.Orders[o.ID]
		past := time.Now().Add(-time.Second).UTC()
		v.CreatedAt = past.Add(-time.Minute)
		v.ExpiresAt = past
		v.ResourceDeadline = past
		v.ResourceTerms.EndsAt = &past
		st.Orders[v.ID] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := notify(t, f.service, o, paidNotification(o)); err != nil {
			t.Fatal("verified payment not acknowledged", err)
		}
	}
	done, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || done.Status != "review" || done.PaidAt.IsZero() || done.TransactionID == "" || !done.CompletedAt.IsZero() || done.ErrorCode != "resource_delivery_requires_reconciliation" {
		t.Fatal(done, err)
	}
	if public := PublicOrder(done); public["checkout"].(payment.Checkout).URL != "" {
		t.Fatal("review order still payable")
	}
	restarted := f.restart(t)
	done, err = restarted.Order(t.Context(), 2, o.ID)
	if err != nil || done.Status != "review" || done.TransactionID == "" {
		t.Fatal("reconciliation facts lost on restart", done, err)
	}
}

func TestResourceTransientFulfillmentAndInterruptedCommitRecover(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(stringID(boolID(interrupt)), func(t *testing.T) {
			f := newCommerceFixture(t)
			o := createResourceOrder(t, f, resourcePaymentInput())
			if interrupt {
				b := &interruptCommit{Backend: f.service.Backend}
				b.armed.Store(true)
				f.service.Backend = b
			} else {
				f.service.ValidateResource = func(context.Context, Order) error { return ErrUnavailable }
			}
			if err := notify(t, f.service, o, paidNotification(o)); err == nil {
				t.Fatal("expected recoverable failure")
			}
			pending, _ := f.service.Order(t.Context(), 2, o.ID)
			if pending.Status != "paid" || pending.TransactionID == "" {
				t.Fatal("verified money lost", pending.Status)
			}
			f.service.ValidateResource = func(context.Context, Order) error { return nil }
			if err := f.service.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			done, _ := f.service.Order(t.Context(), 2, o.ID)
			if done.Status != "completed" || done.TransactionID != pending.TransactionID || f.provider.creates.Load() != 1 {
				t.Fatal("recovery created a second payment", done.Status)
			}
		})
	}
}

func boolID(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func TestResourceRefundRacingFulfillmentKeepsReview(t *testing.T) {
	f := newCommerceFixture(t)
	o := createResourceOrder(t, f, resourcePaymentInput())
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.ValidateResource = func(context.Context, Order) error { close(entered); <-release; return nil }
	finished := make(chan error, 1)
	go func() { finished <- notify(t, f.service, o, paidNotification(o)) }()
	<-entered
	refunded := paidNotification(o)
	refunded.Status = payment.Refunded
	if err := notify(t, f.service, o, refunded); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	done, _ := f.service.Order(t.Context(), 2, o.ID)
	if done.Status != "review" || done.ErrorCode != "refund_requires_reconciliation" || done.TransactionID == "" || !done.CompletedAt.IsZero() {
		t.Fatal("refund overwritten by fulfillment", done)
	}
}

type resourceInspectProvider struct {
	*fakeProvider
	inspects atomic.Int32
}

func (p *resourceInspectProvider) Inspect(_ context.Context, _ string, id string) (payment.Notification, error) {
	p.inspects.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest[id], nil
}

func TestResourceClosedAndUndeliverableSyncUsesReadOnlyInspection(t *testing.T) {
	for _, expired := range []bool{true, false} {
		t.Run(stringID(boolID(expired)), func(t *testing.T) {
			f := newCommerceFixture(t)
			spy := &resourceInspectProvider{fakeProvider: f.provider}
			f.service.ProviderFactory = func(payment.Config) (payment.Provider, error) { return spy, nil }
			o := createResourceOrder(t, f, resourcePaymentInput())
			if expired {
				o = expireResourceOrder(t, f.service, o.ID)
			} else {
				f.service.ValidateResource = func(context.Context, Order) error { return ErrResourceDelivery }
			}
			n := paidNotification(o)
			n.Status, n.TransactionID = payment.Pending, ""
			spy.latest[o.ID] = n
			if _, err := f.service.Sync(t.Context(), 2, o.ID); err != nil {
				t.Fatal(err)
			}
			if spy.inspects.Load() != 1 || spy.queries.Load() != 0 {
				t.Fatal("resource reconciliation attempted a capture", spy.inspects.Load(), spy.queries.Load())
			}
		})
	}
}

func TestResourceLegacyIdentityAndCorruptSnapshotValidation(t *testing.T) {
	f := newCommerceFixture(t)
	in := resourcePaymentInput()
	o := createResourceOrder(t, f, in)
	legacy := o
	legacy.ResourceTerms = nil
	legacy.ResourceAttempt = 0
	newPrice := *in.Terms
	newPrice.Amount, newPrice.Currency = 1990, "USD"
	if !ResourceMatches(legacy, newPrice) || !ResourceMatches(o, newPrice) {
		t.Fatal("legacy receipt revoked after repricing")
	}
	newPrice.Condition = "another"
	if ResourceMatches(legacy, newPrice) || ResourceMatches(o, newPrice) {
		t.Fatal("receipt escaped its condition")
	}
	st, err := f.service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bad := st.Orders[o.ID]
	bad.ResourceTerms.Amount++
	st.Orders[o.ID] = bad
	if ValidateState(st) == nil {
		t.Fatal("corrupt purchase terms accepted")
	}
}

type resourcePaymentTransport func(*http.Request) (*http.Response, error)

func (f resourcePaymentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResourcePayPalApprovalRequiresCurrentDelivery(t *testing.T) {
	for _, scenario := range []string{"active", "expired", "cancelled", "undeliverable", "terms-mismatch", "cancel-during-query", "withdraw-during-oauth"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCommerceFixture(t)
			config := payment.Config{Provider: "paypal", AppID: "app", Secret: "secret", WebhookID: "hook", MerchantID: "seller", Sandbox: true}
			sealed, err := f.service.Vault.Seal(config)
			if err != nil {
				t.Fatal(err)
			}
			cfg := f.cfg
			cfg.Channels[0].Provider, cfg.Channels[0].Currency, cfg.Channels[0].SealedConfig = "paypal", "USD", sealed
			cfg.Products = nil
			if _, err = f.service.UpdateSettings(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			in := resourcePaymentInput()
			in.Currency, in.Terms.Currency = "USD", "USD"
			in.Key = in.Terms.LegacyKey()
			o := createResourceOrder(t, f, in)
			if scenario == "expired" {
				o = expireResourceOrder(t, f.service, o.ID)
			}
			var deliverable atomic.Bool
			deliverable.Store(scenario != "undeliverable")
			f.service.ValidateResource = func(context.Context, Order) error {
				if !deliverable.Load() {
					return ErrResourceDelivery
				}
				return nil
			}
			cancelOrder := func() {
				if err := f.service.Backend.Update(t.Context(), func(st *State) error {
					v := st.Orders[o.ID]
					v.Status = "cancelled"
					st.Orders[o.ID] = v
					return nil
				}); err != nil {
					t.Error(err)
				}
			}
			if scenario == "cancelled" {
				cancelOrder()
			}
			var captures, reads atomic.Int32
			data := func() map[string]any {
				value, _ := payment.FormatAmount(o.Amount, o.Currency)
				if scenario == "terms-mismatch" {
					value = "19.90"
				}
				unit := map[string]any{"custom_id": o.ID, "payee": map[string]string{"merchant_id": "seller"}, "amount": map[string]string{"currency_code": o.Currency, "value": value}}
				state := "APPROVED"
				if captures.Load() > 0 {
					state = "COMPLETED"
					unit["payments"] = map[string]any{"captures": []any{map[string]any{"id": "CAP_RESOURCE", "status": "COMPLETED", "final_capture": true, "amount": unit["amount"]}}}
				}
				return map[string]any{"id": o.Checkout.ProviderOrderID, "intent": "CAPTURE", "status": state, "purchase_units": []any{unit}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/oauth2/token":
					if scenario == "withdraw-during-oauth" && reads.Load() == 2 {
						deliverable.Store(false)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "token", "token_type": "Bearer"})
				case "/v1/notifications/verify-webhook-signature":
					_ = json.NewEncoder(w).Encode(map[string]string{"verification_status": "SUCCESS"})
				case "/v2/checkout/orders/" + o.Checkout.ProviderOrderID:
					if reads.Add(1) == 2 && scenario == "cancel-during-query" {
						cancelOrder()
					}
					_ = json.NewEncoder(w).Encode(data())
				case "/v2/checkout/orders/" + o.Checkout.ProviderOrderID + "/capture":
					captures.Add(1)
					_ = json.NewEncoder(w).Encode(data())
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			t.Cleanup(server.Close)
			target, _ := url.Parse(server.URL)
			client := &http.Client{Timeout: 3 * time.Second, Transport: resourcePaymentTransport(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				u := *r.URL
				u.Scheme, u.Host = target.Scheme, target.Host
				copy.URL = &u
				return http.DefaultTransport.RoundTrip(copy)
			})}
			provider, err := payment.New(config, client)
			if err != nil {
				t.Fatal(err)
			}
			f.service.ProviderFactory = func(payment.Config) (payment.Provider, error) { return provider, nil }
			body, _ := json.Marshal(map[string]any{"id": "EV_RESOURCE", "event_type": "CHECKOUT.ORDER.APPROVED", "resource": data()})
			h := http.Header{"Paypal-Auth-Algo": {"SHA256withRSA"}, "Paypal-Cert-Url": {"https://api-m.sandbox.paypal.com/cert"}, "Paypal-Transmission-Id": {"transmission"}, "Paypal-Transmission-Sig": {"signature"}, "Paypal-Transmission-Time": {"2026-11-25T00:00:00Z"}}
			_, err = f.service.Notify(t.Context(), o.ID, h, body)
			if scenario == "terms-mismatch" {
				if !errors.Is(err, ErrUnverified) {
					t.Fatal("wrong amount approved", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			final, err := f.service.Order(t.Context(), 2, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "active" {
				if captures.Load() != 1 || final.Status != "completed" {
					t.Fatal("valid approval did not complete", captures.Load(), final)
				}
			} else if captures.Load() != 0 || !final.PaidAt.IsZero() || final.Status == "completed" {
				t.Fatal("unavailable resource was captured", captures.Load(), final)
			}
		})
	}
}

type resourceDeadlineProvider struct{ *fakeProvider }

func (p *resourceDeadlineProvider) Query(ctx context.Context, _, _ string) (payment.Notification, error) {
	p.queries.Add(1)
	if _, ok := ctx.Deadline(); !ok {
		return payment.Notification{}, errors.New("resource query has no deadline")
	}
	<-ctx.Done()
	return payment.Notification{}, ctx.Err()
}

func TestResourceQueryCannotOutliveItsPaymentDeadline(t *testing.T) {
	f := newCommerceFixture(t)
	o := createResourceOrder(t, f, resourcePaymentInput())
	if err := f.service.Backend.Update(t.Context(), func(st *State) error {
		v := st.Orders[o.ID]
		v.ExpiresAt = time.Now().Add(200 * time.Millisecond).UTC()
		st.Orders[o.ID] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := &resourceDeadlineProvider{fakeProvider: f.provider}
	f.service.ProviderFactory = func(payment.Config) (payment.Provider, error) { return provider, nil }
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := f.service.Sync(ctx, 2, o.ID); !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatal("provider query crossed the resource deadline", err, ctx.Err())
	}
	current, err := f.service.Order(t.Context(), 2, o.ID)
	if err != nil || current.Status != "pending" || !current.PaidAt.IsZero() {
		t.Fatal("timed out query marked resource paid", current, err)
	}
}

func TestResourceLegacyOpaqueKeyCannotChangePurchaseTerms(t *testing.T) {
	f := newCommerceFixture(t)
	in := resourcePaymentInput()
	in.Terms = nil
	o := createResourceOrder(t, f, in)
	in.Amount++
	if got, err := f.service.CreateResourcePayment(t.Context(), in); !errors.Is(err, ErrReused) || got.ID != o.ID {
		t.Fatal("opaque legacy request accepted a different price", got, err)
	}
	if f.provider.creates.Load() != 1 {
		t.Fatal("legacy conflict created another checkout")
	}
}
