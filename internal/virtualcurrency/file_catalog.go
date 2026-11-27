package virtualcurrency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const maxCatalogBytes = 128 << 10

type currencyCreation struct {
	CreateCurrency
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

type currencyCatalogState struct {
	Schema     int                `json:"schema"`
	Currencies []currencyCreation `json:"currencies"`
}

type FileCatalog struct {
	mu     sync.RWMutex
	root   string
	path   string
	data   currencyCatalogState
	stores map[string]Store
	digest [32]byte
	exists bool
}

// NewMemoryCatalog also adapts a pre-existing single-currency store. Keeping
// main as the default preserves callers that do not yet select a currency.
func NewMemoryCatalog(primary Store) *FileCatalog {
	if primary == nil {
		primary, _ = NewFileStore("")
	}
	return &FileCatalog{data: currencyCatalogState{Schema: 1, Currencies: []currencyCreation{{CreateCurrency: CreateCurrency{Key: DefaultCurrencyKey}}}}, stores: map[string]Store{DefaultCurrencyKey: primary}}
}

func NewFileCatalog(dataDir string) (*FileCatalog, error) {
	if dataDir == "" {
		return NewMemoryCatalog(nil), nil
	}
	primary, err := NewFileStore(filepath.Join(dataDir, "currency.json"))
	if err != nil {
		return nil, err
	}
	c := NewMemoryCatalog(primary)
	c.root, c.path = dataDir, filepath.Join(dataDir, "currency-catalog.json")
	body, exists, err := readCatalogFile(c.path)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err = c.validateUnpublishedLedgers(); err != nil {
			return nil, err
		}
		return c, nil
	}
	if !primary.exists || !utf8.Valid(body) || rejectDuplicateJSON(body) != nil {
		return nil, ErrStoreUnavailable
	}
	var state currencyCatalogState
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&state) != nil || d.Decode(new(any)) != io.EOF || state.Schema != 1 || len(state.Currencies) < 1 || len(state.Currencies) > MaxCurrencies {
		return nil, ErrInvalid
	}
	seen, receipts := map[string]bool{}, map[string]bool{}
	for i, record := range state.Currencies {
		if !ValidCurrencyKey(record.Key) || seen[record.Key] {
			return nil, ErrInvalid
		}
		seen[record.Key] = true
		if i == 0 {
			if record != c.data.Currencies[0] {
				return nil, ErrInvalid
			}
			continue
		}
		cfg, err := NewCurrencySettings(record.CreateCurrency)
		if err != nil || record.Key == DefaultCurrencyKey || record.Name != cfg.Name || record.Symbol != cfg.Symbol || record.CreatedAt.IsZero() || record.Fingerprint != CreationFingerprint(record.CreateCurrency, cfg) {
			return nil, ErrInvalid
		}
		key := opKey(record.ActorID, record.RequestID)
		if receipts[key] {
			return nil, ErrInvalid
		}
		receipts[key] = true
		store, err := NewFileStore(c.ledgerPath(record.Key))
		if err != nil || !store.exists {
			if err != nil {
				return nil, err
			}
			return nil, ErrStoreUnavailable
		}
		c.stores[record.Key] = store
	}
	c.data, c.digest, c.exists = state, sha256.Sum256(body), true
	if err = c.validateUnpublishedLedgers(); err != nil {
		return nil, err
	}
	return c, nil
}

// A missing or older catalog must not hide an already-used currency. Empty,
// untouched ledgers can be left by a crash before the first catalog commit.
func (c *FileCatalog) validateUnpublishedLedgers() error {
	dir, err := packagefs.OpenDirectoryNoSymlink(filepath.Join(c.root, "currencies"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxCurrencies + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > MaxCurrencies {
		return ErrStoreUnavailable
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "currency-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "currency-"), ".json")
		if !ValidCurrencyKey(key) || key == DefaultCurrencyKey {
			return ErrStoreUnavailable
		}
		if _, ok := c.stores[key]; ok {
			continue
		}
		store, err := NewFileStore(c.ledgerPath(key))
		if err != nil {
			return err
		}
		if !store.exists || len(store.data.Balances) != 0 || len(store.data.Entries) != 0 || len(store.data.Operations) != 0 {
			return ErrStoreUnavailable
		}
		expected := integerDefaults()
		expected.Name, expected.Symbol, expected.Precision = store.data.Settings.Name, store.data.Settings.Symbol, store.data.Settings.Precision
		if store.data.Settings != expected {
			return ErrStoreUnavailable
		}
	}
	return nil
}

func (c *FileCatalog) ledgerPath(key string) string {
	if c.root == "" {
		return ""
	}
	// A fixed prefix also avoids Windows reserved device filenames.
	return filepath.Join(c.root, "currencies", "currency-"+key+".json")
}

func readCatalogFile(path string) ([]byte, bool, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if info.Size() > maxCatalogBytes {
		return nil, false, ErrStoreUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(f, maxCatalogBytes+1))
	if len(body) > maxCatalogBytes {
		return nil, false, ErrStoreUnavailable
	}
	return body, true, err
}

func (c *FileCatalog) unchanged(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.path == "" {
		return nil
	}
	body, exists, err := readCatalogFile(c.path)
	if err != nil {
		return err
	}
	if exists != c.exists || exists && sha256.Sum256(body) != c.digest {
		return ErrStoreUnavailable
	}
	return nil
}

func (c *FileCatalog) Ready(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.unchanged(ctx); err != nil {
		return err
	}
	for _, store := range c.stores {
		if err := store.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *FileCatalog) Store(ctx context.Context, key string) (Store, error) {
	if !ValidCurrencyKey(key) {
		return nil, ErrInvalid
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.unchanged(ctx); err != nil {
		return nil, err
	}
	store, ok := c.stores[key]
	if !ok {
		return nil, ErrCurrencyNotFound
	}
	return store, nil
}

func (c *FileCatalog) List(ctx context.Context) ([]Currency, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.unchanged(ctx); err != nil {
		return nil, err
	}
	result := make([]Currency, 0, len(c.data.Currencies))
	for _, record := range c.data.Currencies {
		settings, err := c.stores[record.Key].Settings(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, Currency{Key: record.Key, Settings: settings, CreatedAt: record.CreatedAt})
	}
	return result, nil
}

func (c *FileCatalog) Create(ctx context.Context, in CreateCurrency) (Currency, bool, error) {
	cfg, err := NewCurrencySettings(in)
	if err != nil {
		return Currency{}, false, err
	}
	in.Name, in.Symbol = cfg.Name, cfg.Symbol
	fp := CreationFingerprint(in, cfg)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.unchanged(ctx); err != nil {
		return Currency{}, false, err
	}
	for _, record := range c.data.Currencies {
		if record.ActorID == in.ActorID && record.RequestID == in.RequestID {
			if record.Fingerprint != fp {
				return Currency{}, false, ErrIdempotencyConflict
			}
			settings, err := c.stores[record.Key].Settings(ctx)
			return Currency{Key: record.Key, Settings: settings, CreatedAt: record.CreatedAt}, true, err
		}
	}
	if _, exists := c.stores[in.Key]; exists {
		return Currency{}, false, ErrCurrencyExists
	}
	if len(c.data.Currencies) >= MaxCurrencies {
		return Currency{}, false, ErrCurrencyLimit
	}
	store, err := NewFileStore(c.ledgerPath(in.Key))
	if err != nil {
		return Currency{}, false, err
	}
	// An interrupted catalog commit can leave an unpublished empty ledger.
	// Only an exact empty creation may be adopted; never reset existing funds.
	if store.exists && (len(store.data.Balances) != 0 || len(store.data.Entries) != 0 || len(store.data.Operations) != 0 || store.data.Settings != cfg) {
		return Currency{}, false, ErrCurrencyExists
	}
	if !store.exists {
		state := cloneState(store.data)
		state.Settings = cfg
		if err = store.persist(ctx, state); err != nil {
			return Currency{}, false, err
		}
	}
	// Once a catalog exists, all referenced ledgers, including main, must exist.
	if primary, ok := c.stores[DefaultCurrencyKey].(*FileStore); ok && c.path != "" {
		primary.mu.Lock()
		if !primary.exists {
			err = primary.persist(ctx, cloneState(primary.data))
		}
		primary.mu.Unlock()
		if err != nil {
			return Currency{}, false, err
		}
	}
	record := currencyCreation{CreateCurrency: in, Fingerprint: fp, CreatedAt: time.Now().UTC()}
	state := currencyCatalogState{Schema: 1, Currencies: append(append([]currencyCreation{}, c.data.Currencies...), record)}
	if err = c.persist(ctx, state); err != nil {
		return Currency{}, false, err
	}
	c.stores[in.Key] = store
	return Currency{Key: in.Key, Settings: cfg, CreatedAt: record.CreatedAt}, false, nil
}

func (c *FileCatalog) persist(ctx context.Context, state currencyCatalogState) error {
	body, err := json.Marshal(state)
	if err != nil || len(body) > maxCatalogBytes {
		return ErrStoreUnavailable
	}
	if err = c.unchanged(ctx); err != nil {
		return err
	}
	if c.path != "" {
		if err = os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
			return err
		}
		if _, err = packagefs.LstatNoSymlink(filepath.Dir(c.path)); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(c.path), ".currency-catalog-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(body)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = c.unchanged(ctx); err != nil {
			return err
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), c.path); err != nil {
			return err
		}
		c.digest, c.exists = sha256.Sum256(body), true
	}
	c.data = state
	return nil
}

func ValidCreationFingerprint(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && value == hex.EncodeToString(b)
}
