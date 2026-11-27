// Package virtualcurrency provides an integer-only, append-only account ledger.
package virtualcurrency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/checkin"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxNameRunes       = 40
	MaxSymbolRunes     = 12
	MaxReasonRunes     = 200
	MaxHistoryPage     = 100
	RewardCheckIn      = "reward_check_in"
	RewardRegistration = "reward_registration"
	RewardPublication  = "reward_publication"
)

var (
	ErrInvalid             = errors.New("invalid currency operation")
	ErrDisabled            = errors.New("virtual currency is disabled")
	ErrConflict            = errors.New("currency settings changed")
	ErrInsufficient        = errors.New("insufficient balance")
	ErrBalanceLimit        = errors.New("balance limit reached")
	ErrTransferDisabled    = errors.New("transfers are disabled")
	ErrRateLimit           = errors.New("daily transfer limit reached")
	ErrRewardLimit         = errors.New("daily reward limit reached")
	ErrRewardIneligible    = errors.New("not eligible for this reward")
	ErrIdempotencyConflict = errors.New("request id was already used for a different operation")
	ErrStoreUnavailable    = errors.New("currency store unavailable")
	ErrPrecisionLocked     = errors.New("currency precision cannot change after ledger activity")
)

// Amounts are integers in the smallest display unit. Precision is immutable
// after the first entry; activation dates and PrecisionLocked are server-owned.
type Settings struct {
	Version               int64     `json:"version"`
	Enabled               bool      `json:"enabled"`
	Name                  string    `json:"name"`
	Symbol                string    `json:"symbol"`
	Precision             int       `json:"precision"`
	MaxBalance            int64     `json:"max_balance"`
	AllowTransfers        bool      `json:"allow_transfers"`
	MinTransfer           int64     `json:"min_transfer"`
	MaxTransfer           int64     `json:"max_transfer"`
	DailyTransferLimit    int64     `json:"daily_transfer_limit"`
	PrecisionLocked       bool      `json:"precision_locked"`
	CheckInEnabled        bool      `json:"check_in_enabled"`
	CheckInReward         int64     `json:"check_in_reward"`
	CheckInMode           string    `json:"check_in_mode"`
	CheckInTimezone       string    `json:"check_in_timezone"`
	CheckInIntervalHours  int       `json:"check_in_interval_hours"`
	RegistrationEnabled   bool      `json:"registration_enabled"`
	RegistrationReward    int64     `json:"registration_reward"`
	RegistrationSince     time.Time `json:"registration_since"`
	PublicationEnabled    bool      `json:"publication_enabled"`
	PublicationReward     int64     `json:"publication_reward"`
	PublicationSince      time.Time `json:"publication_since"`
	PublicationMinLength  int       `json:"publication_min_length"`
	PublicationDailyLimit int       `json:"publication_daily_limit"`
	RequireVerifiedEmail  bool      `json:"require_verified_email"`
	DailyRewardLimit      int64     `json:"daily_reward_limit"`
	DailyIssuanceLimit    int64     `json:"daily_issuance_limit"`
}

func DefaultSettings() Settings {
	s := integerDefaults()
	s.Precision = 1
	s.MaxBalance = 10_000_000_000
	s.MaxTransfer = 1_000_000
	s.DailyTransferLimit = 2_000_000
	s.CheckInReward = 100
	s.RegistrationReward = 1_000
	s.PublicationReward = 500
	s.DailyRewardLimit = 10_000
	s.DailyIssuanceLimit = 1_000_000
	return s
}

// Custom currencies start with whole-unit defaults.
func integerDefaults() Settings {
	return Settings{Version: 1, Enabled: false, Name: "积分", Symbol: "积分", Precision: 0, MaxBalance: 1_000_000_000,
		AllowTransfers: false, MinTransfer: 1, MaxTransfer: 100_000, DailyTransferLimit: 200_000,
		CheckInMode: checkin.Interval, CheckInTimezone: checkin.DefaultTimezone, CheckInReward: 10, CheckInIntervalHours: 24, RegistrationReward: 100,
		PublicationReward: 50, PublicationMinLength: 300, PublicationDailyLimit: 5,
		RequireVerifiedEmail: true, DailyRewardLimit: 1_000, DailyIssuanceLimit: 100_000}
}

func (s Settings) CheckInRule() checkin.Rule {
	return checkin.Rule{Mode: s.CheckInMode, Timezone: s.CheckInTimezone, Hours: s.CheckInIntervalHours}.Defaults()
}

func NormalizeSettings(s Settings) (Settings, error) {
	rule := s.CheckInRule()
	if rule.Validate() != nil {
		return Settings{}, ErrInvalid
	}
	s.CheckInMode, s.CheckInTimezone = rule.Mode, rule.Timezone
	s.Name, s.Symbol = strings.TrimSpace(s.Name), strings.TrimSpace(s.Symbol)
	if s.Version < 0 || !safeText(s.Name, 1, MaxNameRunes) || !safeText(s.Symbol, 1, MaxSymbolRunes) ||
		s.Precision < 0 || s.Precision > 6 || s.MaxBalance < 1 ||
		s.MinTransfer < 1 || s.MaxTransfer < s.MinTransfer || s.MaxTransfer > s.MaxBalance ||
		s.DailyTransferLimit < s.MaxTransfer || s.DailyTransferLimit > s.MaxBalance ||
		s.CheckInIntervalHours < 1 || s.CheckInIntervalHours > 720 ||
		s.PublicationMinLength < 1 || s.PublicationMinLength > 100_000 ||
		s.PublicationDailyLimit < 1 || s.PublicationDailyLimit > 1_000 ||
		s.DailyRewardLimit < 1 || s.DailyRewardLimit > s.MaxBalance || s.DailyIssuanceLimit < s.DailyRewardLimit {
		return Settings{}, ErrInvalid
	}
	for _, rule := range []struct {
		enabled bool
		amount  int64
	}{
		{s.CheckInEnabled, s.CheckInReward}, {s.RegistrationEnabled, s.RegistrationReward}, {s.PublicationEnabled, s.PublicationReward},
	} {
		if rule.amount < 1 || rule.enabled && rule.amount > s.DailyRewardLimit {
			return Settings{}, ErrInvalid
		}
	}
	return s, nil
}

func safeText(value string, minRunes, maxRunes int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < minRunes || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' {
			return false
		}
	}
	return true
}

type Entry struct {
	ID             int64     `json:"id"`
	OperationID    int64     `json:"operation_id"`
	UserID         int64     `json:"user_id"`
	PeerID         int64     `json:"peer_id,omitempty"`
	ActorID        int64     `json:"actor_id,omitempty"`
	SourceID       int64     `json:"source_id,omitempty"`
	PurchaseAmount int64     `json:"purchase_amount,omitempty"`
	Delta          int64     `json:"delta"`
	Balance        int64     `json:"balance"`
	Kind           string    `json:"kind"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type TransferResult struct {
	Sent     Entry `json:"sent"`
	Received Entry `json:"received"`
	Replayed bool  `json:"replayed,omitempty"`
}

type Store interface {
	Ready(context.Context) error
	Settings(context.Context) (Settings, error)
	UpdateSettings(context.Context, int64, Settings) (Settings, error)
	Balance(context.Context, int64) (int64, error)
	History(context.Context, int64, int64, int) ([]Entry, error)
	Receipt(context.Context, int64, string) ([]Entry, string, error)
	Wallet(context.Context, int64, int64, int) (Wallet, error)
	Adjust(context.Context, Adjustment) (Entry, error)
	Transfer(context.Context, TransferInput) (TransferResult, error)
	Credit(context.Context, CreditInput) (Entry, error)
	Reward(context.Context, RewardInput) (RewardResult, error)
}

type RewardResult struct {
	Entry    Entry
	Replayed bool
}
type Wallet struct {
	Settings         Settings
	Balance          int64
	Entries          []Entry
	TransferredToday int64
	RewardedToday    int64
	NextCheckInAt    time.Time
}
type Adjustment struct {
	ActorID, UserID, Delta, ExpectedVersion int64
	RequestID, Reason                       string
}
type TransferInput struct {
	UserID, RecipientID, Amount, ExpectedVersion int64
	RequestID                                    string
}
type CreditInput struct {
	UserID, Amount, ExpectedVersion int64
	Source, RequestID               string
}

// RewardInput comes from trusted account/content facts, not a public JSON body.
// Amounts and stable business-event identities are always chosen by the store.
type RewardInput struct {
	UserID, SourceID, ExpectedVersion int64
	Kind, RequestID                   string
	OccurredAt                        time.Time
	VerifiedEmail                     bool
	ContentLength                     int
}

func ValidRequestID(id string) bool  { return validRequestID(id) }
func ValidReason(reason string) bool { return validReason(reason) }

func validRequestID(id string) bool {
	if len(id) < 16 || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validReason(reason string) bool { return safeText(strings.TrimSpace(reason), 1, MaxReasonRunes) }

func FingerprintForStore(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

type Operation struct {
	ID          int64   `json:"id"`
	ActorID     int64   `json:"actor_id"`
	RequestID   string  `json:"request_id"`
	Fingerprint string  `json:"fingerprint"`
	EntryIDs    []int64 `json:"entry_ids"`
}

func validateOperation(actor int64, requestID string) error {
	if actor < 0 || !validRequestID(requestID) {
		return ErrInvalid
	}
	return nil
}

func opKey(actor int64, requestID string) string { return fmt.Sprintf("%d:%s", actor, requestID) }
