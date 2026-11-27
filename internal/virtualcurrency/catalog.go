package virtualcurrency

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const DefaultCurrencyKey = "main"
const MaxCurrencies = 32

var (
	ErrCurrencyNotFound = errors.New("currency not found")
	ErrCurrencyExists   = errors.New("currency identifier already exists")
	ErrCurrencyLimit    = errors.New("currency catalog limit reached")
)

// Catalog binds an immutable currency key to an independent ledger. Keys are
// never renamed or reused; disabling a currency is a settings change.
type Catalog interface {
	Ready(context.Context) error
	List(context.Context) ([]Currency, error)
	Store(context.Context, string) (Store, error)
	Create(context.Context, CreateCurrency) (Currency, bool, error)
}

type Currency struct {
	Key       string
	Settings  Settings
	CreatedAt time.Time
}

type CreateCurrency struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Symbol    string `json:"symbol"`
	Precision int    `json:"precision"`
	ActorID   int64  `json:"actor_id"`
	RequestID string `json:"request_id"`
}

func ValidCurrencyKey(key string) bool {
	if len(key) < 1 || len(key) > 32 || key[0] < 'a' || key[0] > 'z' || key[len(key)-1] == '-' {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// A new currency starts disabled with no issuance. Each amount remains an
// integer in its own smallest unit; changing precision never converts assets.
func NewCurrencySettings(in CreateCurrency) (Settings, error) {
	if !ValidCurrencyKey(in.Key) || in.ActorID < 1 || !ValidRequestID(in.RequestID) {
		return Settings{}, ErrInvalid
	}
	cfg := integerDefaults()
	cfg.Name, cfg.Symbol, cfg.Precision = in.Name, in.Symbol, in.Precision
	return NormalizeSettings(cfg)
}

func CreationFingerprint(in CreateCurrency, cfg Settings) string {
	return FingerprintForStore("currency-create-v1", in.Key, cfg.Name, cfg.Symbol, strconv.Itoa(cfg.Precision))
}
