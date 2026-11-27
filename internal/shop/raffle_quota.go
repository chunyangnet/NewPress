package shop

import (
	"context"
	"time"
)

// A configured cap of zero is unlimited. Remaining counts use -1 for unlimited
// so an exhausted allocation can still be expressed as zero.
func validTicketCap(n int) bool { return n >= 0 && n <= 1_000_000_000 }
func ticketsLeft(cap, used int) int {
	if cap == 0 {
		return -1
	}
	return max(0, cap-used)
}

type RaffleQuota struct {
	ProductLeft      int `json:"product_left"`
	CampaignLeft     int `json:"campaign_left"`
	UserProductLeft  int `json:"user_product_left"`
	UserCampaignLeft int `json:"user_campaign_left"`
}

type raffleUsage struct{ total, personal int }
type raffleScope struct{ product, campaign string }

// Count existing orders once. A campaign is shared by every product attached
// to its key at checkout, including products that have since been taken down.
func raffleQuotas(st State, user int64, productKey string, now time.Time) map[string]RaffleQuota {
	quotas := map[string]RaffleQuota{}
	if !st.Catalog.Enabled {
		return quotas
	}
	campaigns := map[string]RaffleCampaign{}
	for _, c := range st.Catalog.RaffleCampaigns {
		campaigns[c.Key] = c
	}
	plans := map[raffleScope]*RafflePlan{}
	for _, p := range st.Catalog.Products {
		if !p.Enabled || p.Kind != "raffle" || (productKey != "" && productKey != p.Key) {
			continue
		}
		r := p.Raffle
		if r.Campaign != "" || r.TicketLimit != 0 || r.UserLimit != 0 {
			plans[raffleScope{p.Key, r.Campaign}] = r
		}
	}
	if len(plans) == 0 {
		return quotas
	}
	products := map[raffleScope]raffleUsage{}
	groups := map[string]raffleUsage{}
	for _, o := range st.Orders {
		if o.Kind != "raffle" || o.Snapshot.Raffle == nil {
			continue
		}
		qty := len(o.Tickets)
		if qty == 0 {
			if o.Released || o.Status != "pending" || !now.Before(o.ExpiresAt) {
				continue
			}
			qty = o.Quantity
		}
		personal := 0
		if user > 0 && o.UserID == user {
			personal = qty
		}
		campaign := o.Snapshot.Raffle.Campaign
		key := raffleScope{o.Product, campaign}
		if plans[key] != nil {
			u := products[key]
			products[key] = raffleUsage{u.total + qty, u.personal + personal}
		}
		if campaign != "" {
			u := groups[campaign]
			groups[campaign] = raffleUsage{u.total + qty, u.personal + personal}
		}
	}
	for key, r := range plans {
		group := campaigns[key.campaign]
		p, c := products[key], groups[key.campaign]
		quotas[key.product] = RaffleQuota{ProductLeft: ticketsLeft(r.TicketLimit, p.total), CampaignLeft: ticketsLeft(group.TicketLimit, c.total), UserProductLeft: ticketsLeft(r.UserLimit, p.personal), UserCampaignLeft: ticketsLeft(group.UserLimit, c.personal)}
	}
	return quotas
}

func (s *Service) PersonalRaffleQuotas(ctx context.Context, user int64) (map[string]RaffleQuota, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return nil, err
	}
	return raffleQuotas(st, user, "", s.now()), nil
}
