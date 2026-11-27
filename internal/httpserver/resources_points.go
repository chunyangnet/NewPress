package httpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

type resourcePointReceipt struct {
	purchase resources.PointPurchase
	entry    virtualcurrency.Entry
}

func resourcePointSpend(p resources.PointPurchase) virtualcurrency.SpendInput {
	in := p.Payment()
	return virtualcurrency.SpendInput{UserID: in.UserID, Amount: in.Amount, ExpectedVersion: in.ExpectedVersion, RequestID: in.RequestID, Reason: in.Reason}
}

func (s *Server) finishResourcePoints(ctx context.Context, p resources.PointPurchase, entry virtualcurrency.Entry) (resources.PointPurchase, error) {
	if !virtualcurrency.ValidateSpendReceipt(entry, resourcePointSpend(p)) {
		return p, resources.ErrInvalid
	}
	return s.resources.FinishPointPurchase(ctx, p, resources.PointReceipt{ID: entry.ID, UserID: entry.UserID, Amount: entry.PurchaseAmount,
		RequestID: p.Payment().RequestID, Paid: entry.Kind == virtualcurrency.PurchasePayment, CreatedAt: entry.CreatedAt})
}

func resourcePurchaseHistory(ctx context.Context, coin virtualcurrency.Store, user int64) ([]virtualcurrency.Entry, error) {
	if history, ok := coin.(virtualcurrency.PurchaseHistoryStore); ok {
		return history.PurchaseHistory(ctx, user)
	}
	entries := []virtualcurrency.Entry{}
	var before int64
	for {
		page, err := coin.History(ctx, user, before, virtualcurrency.MaxHistoryPage)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return entries, nil
		}
		for _, e := range page {
			if e.Kind == virtualcurrency.PurchasePayment {
				entries = append(entries, e)
			}
		}
		next := page[len(page)-1].ID
		if next < 1 || before != 0 && next >= before {
			return nil, resources.ErrUnavailable
		}
		before = next
	}
}

// A history row is only a candidate. Both the immutable operation fingerprint
// and its debit receipt must match before an older price can authorize access.
func (s *Server) resourcePointReceipt(ctx context.Context, p content.Post, r resources.Resource, v resources.Variant, c resources.Condition, f resourceFacts, histories map[string][]virtualcurrency.Entry) (resourcePointReceipt, error) {
	terms := resources.Terms(p.ID, r, v, c)
	key := resources.Hash(f.user, terms.Key())
	if s.currencyCatalog == nil && s.currency == nil && len(f.state.Purchases) == 0 {
		return resourcePointReceipt{}, nil
	}
	if snapshot, ok := f.state.Purchases[key]; ok {
		coin, err := s.resourceCoin(ctx, snapshot.Terms.Currency)
		if err != nil {
			return resourcePointReceipt{}, err
		}
		spender, ok := coin.(virtualcurrency.SpendingStore)
		if !ok {
			return resourcePointReceipt{}, resources.ErrUnavailable
		}
		entry, found, err := spender.SpendReceipt(ctx, resourcePointSpend(snapshot))
		if err != nil || snapshot.Status != "pending" && (!found || entry.ID != snapshot.PaymentEntry) {
			if err == nil {
				err = resources.ErrUnavailable
			}
			return resourcePointReceipt{}, err
		}
		if found && entry.Kind == virtualcurrency.PurchasePayment {
			return resourcePointReceipt{snapshot, entry}, nil
		}
		// A pending/cancelled projection can come from an older backup. Look for
		// a later paid receipt before permitting another attempt.
	}
	keys := []string{c.Currency}
	if s.currencyCatalog != nil {
		currencies, err := s.currencyCatalog.List(ctx)
		if err != nil {
			return resourcePointReceipt{}, err
		}
		for _, coin := range currencies {
			if coin.Key != c.Currency {
				keys = append(keys, coin.Key)
			}
		}
	}
	for _, currency := range keys {
		coin, err := s.resourceCoin(ctx, currency)
		if err != nil {
			if errors.Is(err, virtualcurrency.ErrCurrencyNotFound) || s.currencyCatalog == nil && currency != "main" {
				continue
			}
			return resourcePointReceipt{}, err
		}
		entries, loaded := histories[currency]
		if !loaded {
			entries, err = resourcePurchaseHistory(ctx, coin, f.user)
			if err != nil {
				return resourcePointReceipt{}, err
			}
			histories[currency] = entries
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Reason, "文章资源 ") || entry.PurchaseAmount < 1 {
				continue
			}
			original := terms
			original.Currency, original.Amount, original.EndsAt = currency, entry.PurchaseAmount, nil
			snapshot := resources.PointPurchase{UserID: f.user, Terms: original, Attempt: 1, Legacy: true, CurrencyVersion: 1,
				Status: "completed", PaymentEntry: entry.ID, CreatedAt: entry.CreatedAt, Title: r.Title}
			prefix := "文章资源 v2 " + terms.Key()[:48] + " "
			if strings.HasPrefix(entry.Reason, prefix) {
				attempt, err := strconv.Atoi(strings.TrimPrefix(entry.Reason, prefix))
				if err != nil || attempt < 1 || attempt > 1_000_000 {
					continue
				}
				snapshot.Legacy, snapshot.Attempt = false, attempt
			}
			payment := resourcePointSpend(snapshot)
			if entry.Reason != payment.Reason || !virtualcurrency.ValidateSpendReceipt(entry, payment) {
				continue
			}
			stored, fingerprint, err := coin.Receipt(ctx, f.user, payment.RequestID)
			if err != nil {
				return resourcePointReceipt{}, err
			}
			if len(stored) != 1 || stored[0].ID != entry.ID || fingerprint != virtualcurrency.SpendFingerprint(f.user, payment.Amount, payment.Reason) || !virtualcurrency.ValidateSpendReceipt(stored[0], payment) {
				return resourcePointReceipt{}, resources.ErrUnavailable
			}
			return resourcePointReceipt{snapshot, stored[0]}, nil
		}
	}
	return resourcePointReceipt{}, nil
}

func (s *Server) buyResourcePoints(ctx context.Context, p content.Post, r resources.Resource, v resources.Variant, c resources.Condition, f resourceFacts) (virtualcurrency.Entry, bool, error) {
	if receipt := f.points[resources.PurchaseKey(p.ID, r, v, c)]; receipt.entry.Kind == virtualcurrency.PurchasePayment {
		if snapshot, ok := f.state.Purchases[receipt.purchase.Key()]; ok && snapshot.Payment().RequestID == receipt.purchase.Payment().RequestID && snapshot.Status == "pending" {
			_, err := s.finishResourcePoints(ctx, snapshot, receipt.entry)
			return receipt.entry, true, err
		}
		return receipt.entry, true, nil
	}
	coin, err := s.resourceCoin(ctx, c.Currency)
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	quote, err := coin.Settings(ctx)
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	purchase, err := s.resources.BeginPointPurchase(ctx, resources.PointPurchase{UserID: f.user, Terms: resources.Terms(p.ID, r, v, c),
		CurrencyVersion: quote.Version, Precision: quote.Precision, Title: r.Title, CreatedAt: time.Now().UTC()})
	if err != nil {
		return virtualcurrency.Entry{}, false, err
	}
	// Repricing a pending reservation first seals its old debit. A racing debit
	// wins as a purchase; it can never be followed by a second wallet charge.
	for retry := 0; retry < 2; retry++ {
		original, err := s.resourceCoin(ctx, purchase.Terms.Currency)
		if err != nil {
			return virtualcurrency.Entry{}, false, err
		}
		spender, ok := original.(virtualcurrency.SpendingStore)
		if !ok {
			return virtualcurrency.Entry{}, false, resources.ErrUnavailable
		}
		entry, found, err := spender.SpendReceipt(ctx, resourcePointSpend(purchase))
		if err != nil {
			return entry, false, err
		}
		if found && entry.Kind == virtualcurrency.PurchasePayment {
			_, err = s.finishResourcePoints(ctx, purchase, entry)
			return entry, true, err
		}
		changed := purchase.Terms.Currency != c.Currency || purchase.Terms.Amount != c.Amount || purchase.Precision != quote.Precision || purchase.CurrencyVersion != quote.Version || resources.Hash(purchase.Terms.EndsAt) != resources.Hash(r.EndsAt)
		closed := !resources.Available(r, time.Now()) || purchase.Terms.EndsAt != nil && !time.Now().Before(*purchase.Terms.EndsAt)
		if changed || closed || found {
			if !found {
				entry, _, err = spender.CancelSpend(ctx, resourcePointSpend(purchase))
			}
			if err != nil {
				return entry, false, err
			}
			_, err = s.finishResourcePoints(ctx, purchase, entry)
			if err != nil || entry.Kind == virtualcurrency.PurchasePayment || closed {
				if closed && err == nil && entry.Kind != virtualcurrency.PurchasePayment {
					err = resources.ErrPurchaseChanged
				}
				return entry, entry.Kind == virtualcurrency.PurchasePayment, err
			}
			purchase, err = s.resources.BeginPointPurchase(ctx, resources.PointPurchase{UserID: f.user, Terms: resources.Terms(p.ID, r, v, c), CurrencyVersion: quote.Version, Precision: quote.Precision, Title: r.Title, CreatedAt: time.Now().UTC()})
			if err != nil {
				return entry, false, err
			}
			continue
		}
		spendCtx := ctx
		var cancel context.CancelFunc
		if purchase.Terms.EndsAt != nil {
			spendCtx, cancel = context.WithDeadline(ctx, *purchase.Terms.EndsAt)
		}
		entry, replay, err := spender.Spend(spendCtx, resourcePointSpend(purchase))
		if cancel != nil {
			cancel()
		}
		if err != nil {
			expired := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && purchase.Terms.EndsAt != nil && !time.Now().Before(*purchase.Terms.EndsAt)
			if !expired && !errors.Is(err, virtualcurrency.ErrInsufficient) && !errors.Is(err, virtualcurrency.ErrConflict) && !errors.Is(err, virtualcurrency.ErrDisabled) {
				return entry, replay, err
			}
			cause := err
			if expired {
				cause = resources.ErrPurchaseChanged
			}
			entry, replay, err = spender.CancelSpend(ctx, resourcePointSpend(purchase))
			if err == nil {
				_, err = s.finishResourcePoints(ctx, purchase, entry)
			}
			if err == nil && entry.Kind != virtualcurrency.PurchasePayment {
				err = cause
			}
			return entry, replay, err
		}
		_, err = s.finishResourcePoints(ctx, purchase, entry)
		return entry, replay, err
	}
	return virtualcurrency.Entry{}, false, resources.ErrPurchaseChanged
}
