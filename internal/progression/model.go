// Package progression implements non-transferable experience and timed
// memberships. Membership never changes an account's administrative roles.
package progression

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/checkin"
	"newpress.local/newpress/internal/virtualcurrency"
)

const MaxLevels = 50
const MaxPlans = 16
const MaxPlanBenefits = 8
const MaxPlanBenefitLength = 80
const MaxMembershipDays = 3650
const HistoryPage = 30

var (
	ErrInvalid        = errors.New("invalid progression input")
	ErrConflict       = errors.New("progression settings changed")
	ErrDisabled       = errors.New("progression module disabled")
	ErrIneligible     = errors.New("experience reward ineligible")
	ErrLimit          = errors.New("experience limit reached")
	ErrReused         = errors.New("progression request reused")
	ErrUnavailable    = errors.New("progression store unavailable")
	ErrMemberConflict = errors.New("membership changed")
	ErrPending        = errors.New("membership purchase pending")
	ErrUsedPlan       = errors.New("a used membership plan cannot be removed")
)

type ExperienceRules struct {
	Enabled              bool   `json:"enabled"`
	Name                 string `json:"name"`
	Maximum              int64  `json:"maximum,string"`
	DailyLimit           int64  `json:"daily_limit,string"`
	RequireVerifiedEmail bool   `json:"require_verified_email"`
}
type Level struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Threshold int64  `json:"threshold,string"`
	Color     string `json:"color"`
}
type LevelRules struct {
	Enabled      bool    `json:"enabled"`
	PublicBadges bool    `json:"public_badges"`
	Items        []Level `json:"items"`
}
type CheckInRules struct {
	Mode          string `json:"mode,omitempty"`
	Timezone      string `json:"timezone,omitempty"`
	Enabled       bool   `json:"enabled"`
	Amount        int64  `json:"amount,string"`
	IntervalHours int    `json:"interval_hours"`
}

func (c CheckInRules) Rule() checkin.Rule {
	return checkin.Rule{Mode: c.Mode, Timezone: c.Timezone, Hours: c.IntervalHours}.Defaults()
}

type RegistrationRules struct {
	Enabled bool      `json:"enabled"`
	Amount  int64     `json:"amount,string"`
	Since   time.Time `json:"since"`
}
type PublicationRules struct {
	Enabled    bool      `json:"enabled"`
	Amount     int64     `json:"amount,string"`
	MinLength  int       `json:"min_length"`
	DailyLimit int       `json:"daily_limit"`
	Since      time.Time `json:"since"`
}
type Plan struct {
	Tier              int      `json:"tier,omitempty"`
	Key               string   `json:"key"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Benefits          []string `json:"benefits,omitempty"`
	Color             string   `json:"color"`
	Enabled           bool     `json:"enabled"`
	Days              int      `json:"days"`
	ExperiencePercent int      `json:"experience_percent"`
	PurchaseEnabled   bool     `json:"purchase_enabled"`
	Currency          string   `json:"currency"`
	Price             string   `json:"price"`
}
type MembershipRules struct {
	Enabled      bool   `json:"enabled"`
	PublicBadges bool   `json:"public_badges"`
	Plans        []Plan `json:"plans"`
}
type Settings struct {
	Version      int64             `json:"version,string"`
	Experience   ExperienceRules   `json:"experience"`
	Levels       LevelRules        `json:"levels"`
	CheckIn      CheckInRules      `json:"check_in"`
	Registration RegistrationRules `json:"registration"`
	Publication  PublicationRules  `json:"publication"`
	Membership   MembershipRules   `json:"membership"`
}

func DefaultSettings() Settings {
	return Settings{
		Version:    1,
		Experience: ExperienceRules{Name: "经验值", Maximum: 1_000_000_000, DailyLimit: 1000, RequireVerifiedEmail: true},
		Levels: LevelRules{PublicBadges: true, Items: []Level{
			{Key: "beginner", Name: "初来乍到", Threshold: 0, Color: "#64748b"},
			{Key: "reader", Name: "常来常往", Threshold: 100, Color: "#287456"},
			{Key: "contributor", Name: "热心分享", Threshold: 500, Color: "#2563eb"},
			{Key: "creator", Name: "资深创作者", Threshold: 2000, Color: "#b57525"},
			{Key: "veteran", Name: "社区之星", Threshold: 5000, Color: "#9f4561"},
		}},
		CheckIn:      CheckInRules{Mode: checkin.Interval, Timezone: checkin.DefaultTimezone, Amount: 10, IntervalHours: 24},
		Registration: RegistrationRules{Amount: 100},
		Publication:  PublicationRules{Amount: 50, MinLength: 300, DailyLimit: 5},
		Membership: MembershipRules{PublicBadges: true, Plans: []Plan{
			{Key: "member", Tier: 1, Name: "标准会员", Description: "会员徽章与经验加成", Color: "#b57525", Enabled: true, Days: 30, ExperiencePercent: 125, Currency: "main", Price: "100"},
			{Key: "supporter", Tier: 2, Name: "年度会员", Description: "更长的会员有效期与经验加成", Color: "#287456", Enabled: true, Days: 365, ExperiencePercent: 150, Currency: "main", Price: "1000"},
		}},
	}
}

func textOK(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < minimum || utf8.RuneCountInString(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' {
			return false
		}
	}
	return true
}
func colorOK(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func ValidateSettings(s Settings) error {
	if len(validationIssues(s)) > 0 {
		return ErrInvalid
	}
	return nil
}

func CloneSettings(s Settings) Settings {
	rule := s.CheckIn.Rule()
	s.CheckIn.Mode, s.CheckIn.Timezone = rule.Mode, rule.Timezone
	s.Levels.Items = append([]Level{}, s.Levels.Items...)
	s.Membership.Plans = append([]Plan{}, s.Membership.Plans...)
	for i := range s.Membership.Plans {
		s.Membership.Plans[i].Benefits = append([]string(nil), s.Membership.Plans[i].Benefits...)
	}
	return s
}
func (s Settings) Plan(key string) (Plan, bool) {
	for _, plan := range s.Membership.Plans {
		if plan.Key == key {
			plan.Benefits = append([]string(nil), plan.Benefits...)
			return plan, true
		}
	}
	return Plan{}, false
}

type Membership struct {
	Version int64     `json:"version,string"`
	Plan    string    `json:"plan"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Revoked bool      `json:"revoked"`
}

func (m Membership) Active(now time.Time) bool {
	return m.Plan != "" && !m.Revoked && !now.Before(m.Since) && now.Before(m.Until)
}

// MembershipBenefits is the effective, non-administrative membership summary.
// Benefits contains display text only: modules must enforce their own machine
// rules rather than treating those descriptions as permission identifiers.
type MembershipBenefits struct {
	Active            bool       `json:"active"`
	Plan              string     `json:"plan,omitempty"`
	Name              string     `json:"name,omitempty"`
	Until             *time.Time `json:"until,omitempty"`
	ExperiencePercent int        `json:"experience_percent"`
	PublicBadge       bool       `json:"public_badge"`
	Benefits          []string   `json:"benefits,omitempty"`
}

// PublicBenefits computes benefits at use time; consumers must not use a saved
// summary as a lasting grant. Disabling a plan stops new purchases but preserves
// existing memberships until expiry, matching reward and renewal rules.
func (s Settings) PublicBenefits(m Membership, now time.Time) MembershipBenefits {
	result := MembershipBenefits{ExperiencePercent: 100}
	if !s.Membership.Enabled || !m.Active(now) {
		return result
	}
	plan, found := s.Plan(m.Plan)
	if !found {
		return result
	}
	until := m.Until
	result.Active, result.Plan, result.Name, result.Until = true, plan.Key, plan.Name, &until
	result.PublicBadge = s.Membership.PublicBadges
	result.Benefits = plan.Benefits
	if s.Experience.Enabled {
		result.ExperiencePercent = plan.ExperiencePercent
	}
	return result
}

type Account struct {
	UserID         int64      `json:"user_id,string"`
	Experience     int64      `json:"experience,string"`
	Membership     Membership `json:"membership"`
	PendingRequest string     `json:"pending_request"`
}
type Entry struct {
	ID              int64      `json:"id,string"`
	UserID          int64      `json:"user_id,string"`
	ActorID         int64      `json:"actor_id,string"`
	Kind            string     `json:"kind"`
	Delta           int64      `json:"delta,string"`
	Balance         int64      `json:"balance,string"`
	Membership      Membership `json:"membership"`
	Reason          string     `json:"reason"`
	Days            int        `json:"days"`
	SourceID        int64      `json:"source_id,string"`
	OrderRequest    string     `json:"order_request"`
	PaymentCurrency string     `json:"payment_currency"`
	PaymentEntry    int64      `json:"payment_entry,string"`
	CreatedAt       time.Time  `json:"created_at"`
}
type Receipt struct {
	ActorID     int64  `json:"actor_id,string"`
	RequestID   string `json:"request_id"`
	Fingerprint string `json:"fingerprint"`
	EntryID     int64  `json:"entry_id,string"`
}
type Order struct {
	UserID            int64     `json:"user_id,string"`
	RequestID         string    `json:"request_id"`
	Fingerprint       string    `json:"fingerprint"`
	TermsHash         string    `json:"terms_hash"`
	Status            string    `json:"status"`
	Mode              string    `json:"mode"`
	Plan              string    `json:"plan"`
	PlanName          string    `json:"plan_name"`
	Days              int       `json:"days"`
	ConfigVersion     int64     `json:"config_version,string"`
	MemberVersion     int64     `json:"member_version,string"`
	Currency          string    `json:"currency"`
	CurrencyVersion   int64     `json:"currency_version,string"`
	CurrencyPrecision int       `json:"currency_precision"`
	CurrencySymbol    string    `json:"currency_symbol"`
	Amount            int64     `json:"amount,string"`
	PaymentRequest    string    `json:"payment_request"`
	PaymentReason     string    `json:"payment_reason"`
	PaymentEntry      int64     `json:"payment_entry,string"`
	EntryID           int64     `json:"entry_id,string"`
	CreatedAt         time.Time `json:"created_at"`
	CompletedAt       time.Time `json:"completed_at"`
}

func (o Order) Payment() virtualcurrency.SpendInput {
	return virtualcurrency.SpendInput{UserID: o.UserID, Amount: o.Amount, ExpectedVersion: o.CurrencyVersion, RequestID: o.PaymentRequest, Reason: o.PaymentReason}
}

type Snapshot struct {
	Settings         Settings           `json:"settings"`
	Account          Account            `json:"account"`
	Level            *Level             `json:"level"`
	NextLevel        *Level             `json:"next_level"`
	MembershipActive bool               `json:"membership_active"`
	Benefits         MembershipBenefits `json:"benefits"`
	EarnedToday      int64              `json:"earned_today,string"`
	NextCheckIn      time.Time          `json:"next_check_in"`
	Entries          []Entry            `json:"entries"`
	NextBefore       int64              `json:"next_before,string"`
	PendingOrder     *Order             `json:"pending_order,omitempty"`
}

// Each write transaction serializes configuration, accounts and receipts. The
// backend never holds a transaction while another ledger is being charged.
type Transaction interface {
	Settings() (Settings, error)
	SetSettings(Settings) error
	Account(int64) (Account, error)
	PutAccount(Account) error
	Entry(int64) (Entry, error)
	Append(Entry) (Entry, error)
	History(int64, int64, int) ([]Entry, error)
	RewardsSince(int64, time.Time) ([]Entry, error)
	Receipt(int64, string) (Receipt, bool, error)
	PutReceipt(Receipt) error
	Order(int64, string) (Order, bool, error)
	Orders() ([]Order, error)
	PutOrder(Order) error
	MaximumExperience() (int64, error)
	PlanUsed(string) (bool, error)
}
type Backend interface {
	Ready(context.Context) error
	Read(context.Context, func(Transaction) error) error
	Write(context.Context, func(Transaction) error) error
}
