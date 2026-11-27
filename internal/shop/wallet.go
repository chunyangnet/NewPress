package shop

import (
	"context"
	"errors"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
	"strconv"
	"strings"
	"time"
)

type CurrencyInfo struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Precision int    `json:"precision"`
	Enabled   bool   `json:"enabled"`
	Balance   *int64 `json:"balance,string,omitempty"`
}
type WalletPayment struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	UserID      int64     `json:"user_id,string"`
	Index       int       `json:"index"`
	Currency    string    `json:"currency"`
	Amount      int64     `json:"amount,string"`
	Version     int64     `json:"version,string"`
	Precision   int       `json:"precision"`
	Status      string    `json:"status"`
	Entry       int64     `json:"entry,string"`
	RefundEntry int64     `json:"refund_entry,string"`
	CreatedAt   time.Time `json:"created_at"`
}

func walletKey(currency string) string { return strings.TrimPrefix(currency, "wallet:") }
func walletCurrency(currency string) bool {
	return strings.HasPrefix(currency, "wallet:") && virtualcurrency.ValidCurrencyKey(walletKey(currency))
}
func currencyOK(currency string) bool { return currency == "CNY" || walletCurrency(currency) }
func (p WalletPayment) spend() virtualcurrency.SpendInput {
	return virtualcurrency.SpendInput{UserID: p.UserID, Amount: p.Amount, ExpectedVersion: p.Version, RequestID: p.ID, Reason: "商城订单 " + p.OrderID + " 第 " + strconv.Itoa(p.Index+1) + " 笔付款"}
}
func (s *Service) CurrencyList(ctx context.Context, user int64) ([]CurrencyInfo, error) {
	out := []CurrencyInfo{{Key: "CNY", Name: "人民币", Symbol: "¥", Precision: 2, Enabled: true}}
	if s.Currencies == nil {
		return out, nil
	}
	currencies, err := s.Currencies.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range currencies {
		item := CurrencyInfo{Key: "wallet:" + c.Key, Name: c.Settings.Name, Symbol: c.Settings.Symbol, Precision: c.Settings.Precision, Enabled: c.Settings.Enabled}
		if user > 0 {
			store, e := s.Currencies.Store(ctx, c.Key)
			if e != nil {
				return nil, e
			}
			balance, e := store.Balance(ctx, user)
			if e != nil {
				return nil, e
			}
			item.Balance = &balance
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Service) walletStore(ctx context.Context, currency string) (virtualcurrency.Store, virtualcurrency.SpendingStore, error) {
	if s.Currencies == nil || !walletCurrency(currency) {
		return nil, nil, ErrUnavailable
	}
	store, err := s.Currencies.Store(ctx, walletKey(currency))
	if err != nil {
		return nil, nil, err
	}
	spend, ok := store.(virtualcurrency.SpendingStore)
	if !ok {
		return nil, nil, ErrUnavailable
	}
	return store, spend, nil
}
func (s *Service) validateCurrencies(ctx context.Context, c *Catalog) error {
	for i := range c.Products {
		p := &c.Products[i]
		if p.Currency == "CNY" {
			p.Precision = 2
			continue
		}
		store, _, err := s.walletStore(ctx, p.Currency)
		if err != nil {
			return err
		}
		cfg, err := store.Settings(ctx)
		if err != nil {
			return err
		}
		if cfg.Precision != p.Precision || p.Enabled && !cfg.Enabled {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) PayWallet(ctx context.Context, user int64, id string, index int) (Order, error) {
	o, err := s.Order(ctx, user, id)
	if err != nil {
		return Order{}, err
	}
	store, _, err := s.walletStore(ctx, o.Currency)
	if err != nil {
		return Order{}, err
	}
	cfg, err := store.Settings(ctx)
	if err != nil {
		return Order{}, err
	}
	if cfg.Precision != o.Precision {
		return Order{}, ErrConflict
	}
	if s.Users != nil {
		u, e := s.Users.GetUser(ctx, user)
		if e != nil {
			return Order{}, e
		}
		if u.Status != auth.UserStatusActive || cfg.RequireVerifiedEmail && u.EmailVerifiedAt == nil {
			return Order{}, ErrIneligible
		}
	}
	var payment WalletPayment
	err = s.Backend.Update(ctx, func(st *State) error {
		if st.WalletPayments == nil {
			st.WalletPayments = map[string]WalletPayment{}
		}
		for _, p := range st.WalletPayments {
			if p.OrderID == id && p.Index == index && p.Status != "cancelled" {
				payment = p
				return nil
			}
		}
		current := st.Orders[id]
		if current.UserID != user {
			return ErrNotFound
		}
		if _, e := paymentRequest(current, index, s.now()); e != nil {
			return e
		}
		payment = WalletPayment{ID: newID("sw_"), OrderID: id, UserID: user, Index: index, Currency: o.Currency, Amount: current.Schedule[index].Amount, Version: cfg.Version, Precision: cfg.Precision, Status: "pending", CreatedAt: s.now()}
		st.WalletPayments[payment.ID] = payment
		return nil
	})
	if err != nil {
		return Order{}, err
	}
	if err = s.finishWallet(ctx, payment, true); err != nil {
		return Order{}, err
	}
	return s.Order(ctx, user, id)
}

// Recovery only inspects/fences persisted intents. It never initiates a debit
// that the ledger has not already recorded after an explicit payment request.
func (s *Service) finishWallet(ctx context.Context, p WalletPayment, allowDebit bool) error {
	store, spend, err := s.walletStore(ctx, p.Currency)
	if err != nil {
		return err
	}
	if p.Status == "cancelled" {
		return nil
	}
	entry, found, err := spend.SpendReceipt(ctx, p.spend())
	if err != nil {
		return err
	}
	var cause error
	if !found {
		o, e := s.Order(ctx, p.UserID, p.OrderID)
		if e != nil {
			return e
		}
		cfg, e := store.Settings(ctx)
		if e != nil {
			return e
		}
		_, valid := paymentRequest(o, p.Index, s.now())
		if allowDebit && valid == nil && cfg.Precision == p.Precision {
			entry, _, err = spend.Spend(ctx, p.spend())
			if errors.Is(err, virtualcurrency.ErrInsufficient) || errors.Is(err, virtualcurrency.ErrConflict) || errors.Is(err, virtualcurrency.ErrDisabled) {
				cause = err
				entry, _, err = spend.CancelSpend(ctx, p.spend())
			}
		} else {
			entry, _, err = spend.CancelSpend(ctx, p.spend())
		}
		if err != nil {
			return err
		}
	}
	if !virtualcurrency.ValidateSpendReceipt(entry, p.spend()) {
		return ErrUnavailable
	}
	err = s.Backend.Update(ctx, func(st *State) error {
		v := st.WalletPayments[p.ID]
		v.Entry = entry.ID
		v.Status = "paid"
		if entry.Kind == virtualcurrency.PurchaseCanceled {
			v.Status = "cancelled"
		}
		st.WalletPayments[p.ID] = v
		return nil
	})
	if err != nil {
		return err
	}
	if entry.Kind == virtualcurrency.PurchaseCanceled {
		if cause != nil {
			return cause
		}
		return ErrState
	}
	_, err = s.MarkPaid(ctx, PaymentReceipt{OrderID: p.OrderID, UserID: p.UserID, Index: p.Index, Reference: p.ID, Amount: p.Amount, Currency: p.Currency})
	return err
}
func (s *Service) RecoverWallet(ctx context.Context) error {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	for _, p := range st.WalletPayments {
		if err = ctx.Err(); err != nil {
			return err
		}
		if p.Status == "pending" || p.Status == "paid" {
			if _, ok := st.Receipts[p.ID]; !ok {
				if e := s.finishWallet(ctx, p, false); e != nil && !errors.Is(e, ErrState) {
					return e
				}
			}
		}
	}
	st, err = s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	// Summarize receipts once; scanning the entire ledger for every refunded
	// order would make the recurring recovery grow quadratically.
	refunded, incomplete := map[string]int64{}, map[string]bool{}
	for _, p := range st.WalletPayments {
		if p.Status == "cancelled" {
			continue
		}
		if !refundedWalletPayment(st, st.Orders[p.OrderID], p) {
			incomplete[p.OrderID] = true
		} else {
			refunded[p.OrderID] += p.Amount
		}
	}
	for _, o := range st.Orders {
		if walletCurrency(o.Currency) && (o.Status == "refund_required" || o.Status == "refunded" && (incomplete[o.ID] || refunded[o.ID] != o.PaidAmount || o.RefundReference != walletRefundReference(o.ID))) {
			if err = s.refundWallet(ctx, o); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) refundWallet(ctx context.Context, o Order) error {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	var ok bool
	if o, ok = st.Orders[o.ID]; !ok {
		return ErrNotFound
	}
	if !walletCurrency(o.Currency) || !member(o.Status, "refund_required", "refunded") {
		return ErrState
	}
	// Fence every persisted intent before issuing credits. A payment that
	// raced with refund approval either has a debit receipt or is cancelled;
	// recovery never initiates a new debit.
	for _, p := range st.WalletPayments {
		if p.OrderID == o.ID && member(p.Status, "pending", "paid") {
			if _, recorded := st.Receipts[p.ID]; !recorded {
				if e := s.finishWallet(ctx, p, false); e != nil && !errors.Is(e, ErrState) {
					return e
				}
			}
		}
	}
	st, err = s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	o = st.Orders[o.ID]
	if !member(o.Status, "refund_required", "refunded") {
		return ErrState
	}
	store, spend, err := s.walletStore(ctx, o.Currency)
	if err != nil {
		return err
	}
	for _, p := range st.WalletPayments {
		if p.OrderID != o.ID || p.Status == "cancelled" {
			continue
		}
		if p.Status == "pending" {
			return ErrState
		}
		debit, found, e := spend.SpendReceipt(ctx, p.spend())
		if e != nil {
			return e
		}
		if !found || !virtualcurrency.ValidateSpendReceipt(debit, p.spend()) || debit.Kind != virtualcurrency.PurchasePayment || debit.ID != p.Entry {
			return ErrUnavailable
		}
		if p.RefundEntry > 0 {
			receipt, e := walletRefundReceipt(ctx, store, p)
			if e != nil {
				return e
			}
			if receipt.ID != p.RefundEntry {
				return ErrUnavailable
			}
		}
		cfg, e := store.Settings(ctx)
		if e != nil {
			return e
		}
		if cfg.Precision != p.Precision {
			return ErrConflict
		}
		entry, e := store.Credit(ctx, virtualcurrency.CreditInput{UserID: o.UserID, Amount: p.Amount, ExpectedVersion: cfg.Version, RequestID: "refund-" + p.ID, Source: "商城退款 " + o.ID})
		if e != nil {
			return e
		}
		receipt, e := walletRefundReceipt(ctx, store, p)
		if e != nil {
			return e
		}
		if receipt.ID != entry.ID || p.RefundEntry > 0 && p.RefundEntry != receipt.ID {
			return ErrUnavailable
		}
		if e = s.Backend.Update(ctx, func(state *State) error {
			v := state.WalletPayments[p.ID]
			if !member(state.Orders[o.ID].Status, "refund_required", "refunded") || v.Status != "paid" || v.Entry != p.Entry {
				return ErrState
			}
			v.RefundEntry = receipt.ID
			state.WalletPayments[p.ID] = v
			return nil
		}); e != nil {
			return e
		}
	}
	_, err = s.reconcileRefund(ctx, Refund{OrderID: o.ID, Reference: walletRefundReference(o.ID), Note: "已原路退回站内账户"})
	return err
}

func walletRefundReference(id string) string { return "wallet-refund-" + id }

func walletRefundComplete(st State, o Order) bool {
	var refunded int64
	for _, p := range st.WalletPayments {
		if p.OrderID != o.ID || p.Status == "cancelled" {
			continue
		}
		if !refundedWalletPayment(st, o, p) || refunded > o.PaidAmount-p.Amount {
			return false
		}
		refunded += p.Amount
	}
	return refunded == o.PaidAmount
}

func refundedWalletPayment(st State, o Order, p WalletPayment) bool {
	return p.Status == "paid" && p.RefundEntry > 0 && st.Receipts[p.ID] == (PaymentReceipt{OrderID: o.ID, UserID: o.UserID, Index: p.Index, Reference: p.ID, Amount: p.Amount, Currency: o.Currency})
}

func walletRefundReceipt(ctx context.Context, store virtualcurrency.Store, p WalletPayment) (virtualcurrency.Entry, error) {
	entries, fp, err := store.Receipt(ctx, 0, "refund-"+p.ID)
	if err != nil {
		return virtualcurrency.Entry{}, err
	}
	source := "商城退款 " + p.OrderID
	if fp != virtualcurrency.CreditFingerprint(p.UserID, p.Amount, source) || len(entries) != 1 {
		return virtualcurrency.Entry{}, ErrUnavailable
	}
	e := entries[0]
	if e.ID < 1 || e.OperationID < 1 || e.UserID != p.UserID || e.ActorID != 0 || e.PeerID != 0 || e.SourceID != 0 || e.PurchaseAmount != 0 || e.Kind != "credit" || e.Delta != p.Amount || e.Reason != source {
		return virtualcurrency.Entry{}, ErrUnavailable
	}
	return e, nil
}
func (s *Service) ValidateWallet(ctx context.Context) error {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	for _, p := range st.WalletPayments {
		store, spend, e := s.walletStore(ctx, p.Currency)
		if e != nil {
			return e
		}
		entry, found, e := spend.SpendReceipt(ctx, p.spend())
		if e != nil {
			return e
		}
		if found && !virtualcurrency.ValidateSpendReceipt(entry, p.spend()) || p.Entry > 0 && (!found || entry.ID != p.Entry || p.Status == "paid" && entry.Kind != virtualcurrency.PurchasePayment || p.Status == "cancelled" && entry.Kind != virtualcurrency.PurchaseCanceled) {
			return ErrUnavailable
		}
		if p.RefundEntry > 0 {
			entry, e := walletRefundReceipt(ctx, store, p)
			if e != nil {
				return e
			}
			if entry.ID != p.RefundEntry || p.Status != "paid" {
				return ErrUnavailable
			}
		}
	}
	return nil
}
