package commerce

import (
	"context"
	"sort"
	"time"
)

// Recover retries already verified entitlements and interrupted internal
// exchanges. It never creates or captures a new external payment.
func (s *Service) Recover(ctx context.Context) error {
	state, err := s.Backend.Read(ctx)
	if err != nil {
		return err
	}
	pending := []Order{}
	for _, o := range state.Orders {
		if o.Status == "paid" || o.Kind == "exchange" && o.Status == "pending" || o.Kind == "shop" && o.Status == "review" {
			pending = append(pending, o)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].UpdatedAt.Before(pending[j].UpdatedAt) })
	if len(pending) > 50 {
		pending = pending[:50]
	}
	for _, o := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.Kind == "shop" && o.Status == "review" && s.ReviewShop != nil {
			_ = s.ReviewShop(ctx, shopSettlement(o))
		} else if o.Kind == "exchange" {
			_, _ = s.finishExchange(ctx, o)
		} else {
			_, _ = s.Fulfill(ctx, o.ID)
		}
	}
	return nil
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		task, cancel := context.WithTimeout(ctx, 25*time.Second)
		_ = s.Recover(task)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
