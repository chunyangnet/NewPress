// Package shop owns inventory, purchase schedules and fulfillment. Only verified
// payment receipts may advance a paid order; browser return URLs never do so.
package shop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
	"time"
)

var (
	ErrInvalid          = errors.New("invalid shop request")
	ErrConflict         = errors.New("shop catalog changed")
	ErrDisabled         = errors.New("shop disabled")
	ErrNotFound         = errors.New("shop item not found")
	ErrStock            = errors.New("shop item unavailable")
	ErrRaffleTotal      = errors.New("raffle ticket allocation exhausted")
	ErrRaffleUserLimit  = errors.New("raffle user allowance exceeded")
	ErrRaffleOrderLimit = errors.New("raffle order allowance exceeded")
	ErrRaffleClosed     = fmt.Errorf("raffle entry closed: %w", ErrStock)
	ErrReused           = errors.New("shop request reused")
	ErrUnavailable      = errors.New("shop unavailable")
	ErrState            = errors.New("shop order state does not allow this action")
	ErrIneligible       = errors.New("shop purchase is not eligible")
)

const MaxStateBytes = 64 << 20
const MaxAmount int64 = 1_000_000_000_000

type Variant struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Image string `json:"image,omitempty"`
	Price int64  `json:"price,string"`
	Stock int64  `json:"stock,string"`
}
type BundleItem struct {
	Product  string `json:"product"`
	Quantity int    `json:"quantity"`
}
type InstallmentPlan struct {
	Periods      []int `json:"periods"`
	IntervalDays int   `json:"interval_days"`
	InterestBPS  int   `json:"interest_bps"`
}
type GroupPlan struct {
	Size  int   `json:"size"`
	Price int64 `json:"price,string"`
	Hours int   `json:"hours"`
}
type RaffleTier struct {
	Prize   string `json:"prize"`
	WinBPS  int    `json:"win_bps,omitempty"`
	Winners int    `json:"winners,omitempty"`
}
type RaffleCampaign struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	TicketLimit int    `json:"ticket_limit"`
	UserLimit   int    `json:"user_limit"`
}
type RafflePlan struct {
	Mode        string       `json:"mode"`
	DrawAt      time.Time    `json:"draw_at"`
	Winners     int          `json:"winners"`
	WinBPS      int          `json:"win_bps"`
	Prize       string       `json:"prize"`
	Tiers       []RaffleTier `json:"tiers,omitempty"`
	Campaign    string       `json:"campaign,omitempty"`
	TicketLimit int          `json:"ticket_limit,omitempty"`
	UserLimit   int          `json:"user_limit,omitempty"`
	OrderLimit  int          `json:"order_limit,omitempty"`
}
type Product struct {
	Key                 string           `json:"key"`
	Title               string           `json:"title"`
	Summary             string           `json:"summary"`
	Image               string           `json:"image"`
	Images              []string         `json:"images,omitempty"`
	Description         string           `json:"description,omitempty"`
	DescriptionSource   string           `json:"description_source,omitempty"`
	DescriptionAuthorID int64            `json:"description_author_id,string,omitempty"`
	DetailImages        []string         `json:"detail_images,omitempty"`
	Services            *ServiceModules  `json:"services,omitempty"`
	Category            string           `json:"category"`
	Kind                string           `json:"kind"`
	Mode                string           `json:"mode"`
	Enabled             bool             `json:"enabled"`
	Currency            string           `json:"currency"`
	Precision           int              `json:"precision"`
	Price               int64            `json:"price,string"`
	CompareAt           int64            `json:"compare_at,string"`
	Stock               int64            `json:"stock,string"`
	ShippingFee         int64            `json:"shipping_fee,string"`
	Variants            []Variant        `json:"variants"`
	Delivery            string           `json:"delivery,omitempty"`
	Codes               []string         `json:"codes,omitempty"`
	Bundle              []BundleItem     `json:"bundle"`
	ReleaseAt           time.Time        `json:"release_at"`
	Deposit             int64            `json:"deposit,string"`
	BalanceDueAt        time.Time        `json:"balance_due_at"`
	MaxDonation         int64            `json:"max_donation,string"`
	Installment         *InstallmentPlan `json:"installment"`
	Group               *GroupPlan       `json:"group"`
	Raffle              *RafflePlan      `json:"raffle"`
}
type Catalog struct {
	Version         int64            `json:"version,string"`
	Enabled         bool             `json:"enabled"`
	OrderMinutes    int              `json:"order_minutes"`
	Terms           string           `json:"terms"`
	RefundPolicy    string           `json:"refund_policy"`
	Products        []Product        `json:"products"`
	RaffleCampaigns []RaffleCampaign `json:"raffle_campaigns,omitempty"`
}
type Address struct {
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	Region     string `json:"region"`
	Detail     string `json:"detail"`
	PostalCode string `json:"postal_code"`
}
type QuoteInput struct {
	Coupon         string `json:"coupon"`
	Product        string `json:"product"`
	Variant        string `json:"variant"`
	Quantity       int    `json:"quantity"`
	Periods        int    `json:"periods"`
	DonationAmount int64  `json:"donation_amount,string"`
	GroupID        string `json:"group_id"`
	Mode           string `json:"mode"`
}
type Coupon struct {
	Code            string    `json:"code"`
	Title           string    `json:"title"`
	Kind            string    `json:"kind"`
	Value           int64     `json:"value,string"`
	Minimum         int64     `json:"minimum,string"`
	MaximumDiscount int64     `json:"maximum_discount,string"`
	Currency        string    `json:"currency"`
	Kinds           []string  `json:"kinds"`
	Modes           []string  `json:"modes"`
	Products        []string  `json:"products"`
	Categories      []string  `json:"categories"`
	Levels          []string  `json:"levels"`
	Memberships     []string  `json:"memberships"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	ClaimLimit      int       `json:"claim_limit"`
	UseLimit        int       `json:"use_limit"`
	PerUserLimit    int       `json:"per_user_limit"`
	Home            bool      `json:"home"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
}
type CouponClaim struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	UserID    int64     `json:"user_id,string"`
	Status    string    `json:"status"`
	OrderID   string    `json:"order_id,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
	UsedAt    time.Time `json:"used_at"`
}
type Purchase struct {
	QuoteInput
	UserID    int64   `json:"user_id,string"`
	Version   int64   `json:"version,string"`
	RequestID string  `json:"request_id"`
	Accepted  bool    `json:"accepted"`
	Address   Address `json:"address"`
}
type Installment struct {
	Index       int       `json:"index"`
	Title       string    `json:"title"`
	Principal   int64     `json:"principal,string"`
	Interest    int64     `json:"interest,string"`
	Amount      int64     `json:"amount,string"`
	DueAt       time.Time `json:"due_at"`
	AvailableAt time.Time `json:"available_at"`
	PaidAt      time.Time `json:"paid_at"`
	PaymentRef  string    `json:"payment_ref,omitempty"`
}
type Quote struct {
	Coupon              string        `json:"coupon"`
	CouponTitle         string        `json:"coupon_title"`
	Discount            int64         `json:"discount,string"`
	Precision           int           `json:"precision"`
	OriginalSubtotal    int64         `json:"original_subtotal,string"`
	Version             int64         `json:"version,string"`
	Product             string        `json:"product"`
	Variant             string        `json:"variant"`
	Quantity            int           `json:"quantity"`
	Kind                string        `json:"kind"`
	Mode                string        `json:"mode"`
	Currency            string        `json:"currency"`
	UnitPrice           int64         `json:"unit_price,string"`
	Subtotal            int64         `json:"subtotal,string"`
	Shipping            int64         `json:"shipping,string"`
	Interest            int64         `json:"interest,string"`
	Total               int64         `json:"total,string"`
	PayNow              int64         `json:"pay_now,string"`
	RequiresAddress     bool          `json:"requires_address"`
	Schedule            []Installment `json:"schedule"`
	InterestDescription string        `json:"interest_description"`
}
type Reservation struct {
	Product  string   `json:"product"`
	Variant  string   `json:"variant"`
	Quantity int      `json:"quantity"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Delivery string   `json:"delivery"`
	Codes    []string `json:"codes"`
}
type Delivery struct {
	Product  string `json:"product"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Quantity int    `json:"quantity"`
}
type Ticket struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	Round       string    `json:"round"`
	Status      string    `json:"status"`
	Prize       string    `json:"prize"`
	DrawAt      time.Time `json:"draw_at"`
	DrawnAt     time.Time `json:"drawn_at"`
	Winners     int       `json:"winners"`
	Delivery    string    `json:"delivery,omitempty"`
	DeliveredAt time.Time `json:"delivered_at"`
}
type Order struct {
	ID          string `json:"id"`
	UserID      int64  `json:"user_id,string"`
	RequestID   string `json:"request_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Title       string `json:"title"`
	Quote
	Status               string        `json:"status"`
	PaidAmount           int64         `json:"paid_amount,string"`
	Address              Address       `json:"address"`
	GroupID              string        `json:"group_id"`
	Deliveries           []Delivery    `json:"deliveries"`
	Tickets              []Ticket      `json:"tickets"`
	Carrier              string        `json:"carrier"`
	Tracking             string        `json:"tracking"`
	RefundReference      string        `json:"refund_reference"`
	RefundNote           string        `json:"refund_note"`
	RefundPreviousStatus string        `json:"refund_previous_status,omitempty"`
	RefundReview         string        `json:"refund_review,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	ExpiresAt            time.Time     `json:"expires_at"`
	CompletedAt          time.Time     `json:"completed_at"`
	ShippedAt            time.Time     `json:"shipped_at"`
	Terms                string        `json:"terms"`
	RefundPolicy         string        `json:"refund_policy"`
	CatalogVersion       int64         `json:"catalog_version,string"`
	Snapshot             Product       `json:"snapshot,omitempty"`
	Reservations         []Reservation `json:"reservations,omitempty"`
	Released             bool          `json:"released"`
}
type Group struct {
	ID        string    `json:"id"`
	Product   string    `json:"product"`
	Variant   string    `json:"variant"`
	Version   int64     `json:"version,string"`
	Size      int       `json:"size"`
	Orders    []string  `json:"orders"`
	Paid      int       `json:"paid"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
}
type PaymentReceipt struct {
	OrderID   string `json:"order_id"`
	UserID    int64  `json:"user_id,string"`
	Index     int    `json:"index"`
	Reference string `json:"reference"`
	Amount    int64  `json:"amount,string"`
	Currency  string `json:"currency"`
}
type PaymentRequest struct {
	OrderID                  string
	Index                    int
	UserID                   int64
	Amount                   int64
	Currency, Title, Product string
	ExpiresAt                time.Time
}
type Shipment struct {
	OrderID  string `json:"order_id"`
	Carrier  string `json:"carrier"`
	Tracking string `json:"tracking"`
}
type Refund struct {
	OrderID   string `json:"order_id"`
	Reference string `json:"reference"`
	Note      string `json:"note"`
}
type State struct {
	Schema         int                       `json:"schema"`
	Catalog        Catalog                   `json:"catalog"`
	Orders         map[string]Order          `json:"orders"`
	Groups         map[string]Group          `json:"groups"`
	Receipts       map[string]PaymentReceipt `json:"receipts"`
	CouponVersion  int64                     `json:"coupon_version,string"`
	Coupons        []Coupon                  `json:"coupons"`
	Claims         map[string]CouponClaim    `json:"claims"`
	WalletPayments map[string]WalletPayment  `json:"wallet_payments"`
	Reviews        map[string]Review         `json:"reviews"`
}
type Backend interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}
type Service struct {
	Backend    Backend
	Now        func() time.Time
	Currencies virtualcurrency.Catalog
	Members    *progression.Store
	Users      virtualcurrency.Users
}

func InitialState() State {
	return State{Schema: 1, Catalog: Catalog{Version: 1, OrderMinutes: 30, Terms: "请确认商品、交付方式与购买计划后付款。", RefundPolicy: "退款请联系站点管理员核对，实际退款以支付渠道回执为准。", Products: []Product{}}, Orders: map[string]Order{}, Groups: map[string]Group{}, Receipts: map[string]PaymentReceipt{}, Coupons: []Coupon{}, Claims: map[string]CouponClaim{}, WalletPayments: map[string]WalletPayment{}, Reviews: map[string]Review{}}
}
func NormalizeState(s *State) {
	if s.Reviews == nil {
		s.Reviews = map[string]Review{}
	}
	if s.Orders == nil {
		s.Orders = map[string]Order{}
	}
	if s.Groups == nil {
		s.Groups = map[string]Group{}
	}
	if s.Receipts == nil {
		s.Receipts = map[string]PaymentReceipt{}
	}
	if s.Coupons == nil {
		s.Coupons = []Coupon{}
	}
	if s.Claims == nil {
		s.Claims = map[string]CouponClaim{}
	}
	if s.WalletPayments == nil {
		s.WalletPayments = map[string]WalletPayment{}
	}
}
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func newID(prefix string) string {
	var b [14]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func PublicCatalog(c Catalog) Catalog {
	c.Products = append([]Product(nil), c.Products...)
	// Show the effective stock without exposing card codes or delivery content.
	available := map[string]int64{}
	minimum := func(a, b int64) int64 {
		if a < 0 || b >= 0 && b < a {
			return b
		}
		return a
	}
	for _, p := range c.Products {
		n := p.Stock
		if !p.Enabled {
			n = 0
		}
		if p.Kind == "cdk" {
			n = minimum(n, int64(len(p.Codes)))
		}
		if len(p.Variants) > 0 {
			var total int64
			for _, v := range p.Variants {
				if v.Stock < 0 {
					total = -1
					break
				}
				total += v.Stock
			}
			n = minimum(n, total)
		}
		available[p.Key] = n
	}
	for i := range c.Products {
		p := &c.Products[i]
		p.Stock = available[p.Key]
		for _, item := range p.Bundle {
			if n := available[item.Product]; n >= 0 && item.Quantity > 0 {
				p.Stock = minimum(p.Stock, n/int64(item.Quantity))
			}
		}
		p.Variants = append([]Variant(nil), p.Variants...)
		for j := range p.Variants {
			p.Variants[j].Stock = minimum(p.Variants[j].Stock, p.Stock)
		}
		p.Codes = nil
		p.Delivery = ""
		p.DescriptionSource = ""
		p.DescriptionAuthorID = 0
		if p.Services != nil {
			config := *p.Services
			config.Items = append([]ServiceModule(nil), config.Items...)
			if config.Mode != "custom" {
				config.Items = nil
			}
			p.Services = &config
		}
	}
	return c
}
func PublicOrder(o Order) Order {
	o.Snapshot = Product{}
	o.Reservations = nil
	o.Fingerprint = ""
	o.RequestID = ""
	return o
}
