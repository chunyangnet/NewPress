package shop

import (
	"context"
	"time"
)

// The round identity excludes purchase limits that may change during a draw.
func (r *RafflePlan) roundID(product string) string {
	// Capacity edits affect future purchases without splitting an existing draw.
	plan := *r
	plan.TicketLimit, plan.UserLimit, plan.OrderLimit = 0, 0, 0
	return fingerprint(struct {
		Key  string
		Plan RafflePlan
	}{product, plan})
}

func awaitingRaffleDraw(o Order) bool {
	return o.PaidAmount == o.Total && !o.Released && (o.Status == "awaiting_draw" || o.Status == "refund_requested" && o.RefundPreviousStatus == "awaiting_draw")
}

type RaffleRoundStats struct {
	Entries int            `json:"entries"`
	Winners int            `json:"winners"`
	Drawn   bool           `json:"drawn"`
	ByPrize map[string]int `json:"by_prize,omitempty"`
}

type StorefrontOverview struct {
	Catalog      Catalog
	ProductStats map[string]ProductStats
	RaffleQuotas map[string]RaffleQuota
	RaffleRounds map[string]*RaffleRoundStats
	ServerTime   time.Time
}

// Read prices, rules and published counts from the same version of the store.
func (s *Service) StorefrontOverview(ctx context.Context) (StorefrontOverview, error) {
	st, err := s.Backend.Read(ctx)
	if err != nil {
		return StorefrontOverview{}, err
	}
	catalog := PublicCatalog(st.Catalog)
	visible := make([]Product, 0, len(catalog.Products))
	rounds := make(map[string]*RaffleRoundStats)
	roundIDs := make(map[string]string)
	for _, p := range catalog.Products {
		if !catalog.Enabled || !p.Enabled {
			continue
		}
		visible = append(visible, p)
		if p.Kind == "raffle" && p.Raffle.Mode == "scheduled" {
			counts := &RaffleRoundStats{ByPrize: make(map[string]int)}
			rounds[p.Key] = counts
			roundIDs[p.Key] = p.Raffle.roundID(p.Key)
		}
	}
	catalog.Products = visible
	if len(rounds) > 0 {
		for _, o := range st.Orders {
			counts := rounds[o.Product]
			if counts == nil {
				continue
			}
			eligible := awaitingRaffleDraw(o)
			for _, ticket := range o.Tickets {
				if ticket.Round != roundIDs[o.Product] || ticket.Status == "pending" && !eligible {
					continue
				}
				// Published tickets stay in the result even after a later refund.
				counts.Entries++
				if ticket.Status != "pending" {
					counts.Drawn = true
				}
				if ticket.Status == "won" {
					counts.Winners++
					counts.ByPrize[ticket.Prize]++
				}
			}
		}
	}
	now := s.now()
	// Campaign metadata in the storefront is limited to published products.
	visibleCampaigns := make(map[string]bool)
	for _, p := range visible {
		if p.Kind == "raffle" {
			visibleCampaigns[p.Raffle.Campaign] = true
		}
	}
	catalog.RaffleCampaigns = nil
	for _, campaign := range st.Catalog.RaffleCampaigns {
		if visibleCampaigns[campaign.Key] {
			catalog.RaffleCampaigns = append(catalog.RaffleCampaigns, campaign)
		}
	}
	return StorefrontOverview{Catalog: catalog, ProductStats: summarizeReviews(st), RaffleRounds: rounds, RaffleQuotas: raffleQuotas(st, 0, "", now), ServerTime: now}, nil
}
