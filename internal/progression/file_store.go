package progression

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const maxFileBytes = 64 << 20

type fileState struct {
	Schema      int                `json:"schema"`
	Settings    Settings           `json:"settings"`
	Accounts    map[string]Account `json:"accounts"`
	Entries     []Entry            `json:"entries"`
	Receipts    map[string]Receipt `json:"receipts"`
	Orders      map[string]Order   `json:"orders"`
	NextEntryID int64              `json:"next_entry_id,string"`
}
type fileBackend struct {
	mu     sync.RWMutex
	path   string
	data   fileState
	digest [32]byte
	exists bool
}

// An empty path is an isolated, transactional in-memory store for tests.
func NewFileStore(path string) (*Store, error) {
	b := &fileBackend{path: path, data: fileState{Schema: 1, Settings: DefaultSettings(), Accounts: map[string]Account{}, Entries: []Entry{}, Receipts: map[string]Receipt{}, Orders: map[string]Order{}, NextEntryID: 1}}
	if path != "" {
		body, exists, err := readFile(path)
		if err != nil {
			return nil, err
		}
		if exists {
			data, err := packagefs.DecodeJSON[fileState](bytes.NewReader(body), maxFileBytes, "progression")
			if err != nil || !utf8.Valid(body) {
				return nil, ErrInvalid
			}
			// Go accepts null, case aliases and missing fields. Persisted ledgers must
			// round-trip exactly in addition to passing history validation.
			canonical, err := json.Marshal(data)
			if err != nil {
				return nil, err
			}
			var original, encoded any
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if err = d.Decode(&original); err != nil {
				return nil, ErrInvalid
			}
			d = json.NewDecoder(bytes.NewReader(canonical))
			d.UseNumber()
			if err = d.Decode(&encoded); err != nil || !reflect.DeepEqual(original, encoded) {
				return nil, ErrInvalid
			}
			if err = validateState(data); err != nil {
				return nil, err
			}
			b.data, b.digest, b.exists = data, sha256.Sum256(body), true
		}
	}
	return NewStore(b), nil
}
func readFile(path string) ([]byte, bool, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if info.Size() > maxFileBytes {
		return nil, false, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(body) > maxFileBytes {
		return nil, false, ErrUnavailable
	}
	return body, true, err
}
func (b *fileBackend) unchanged() error {
	if b.path == "" {
		return nil
	}
	body, exists, err := readFile(b.path)
	if err != nil {
		return err
	}
	if exists != b.exists || exists && sha256.Sum256(body) != b.digest {
		return ErrUnavailable
	}
	return nil
}
func (b *fileBackend) Ready(ctx context.Context) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.unchanged()
}
func cloneState(s fileState) fileState {
	d := s
	d.Settings = CloneSettings(s.Settings)
	d.Entries = append([]Entry{}, s.Entries...)
	d.Accounts = make(map[string]Account, len(s.Accounts))
	for k, v := range s.Accounts {
		d.Accounts[k] = v
	}
	d.Receipts = make(map[string]Receipt, len(s.Receipts))
	for k, v := range s.Receipts {
		d.Receipts[k] = v
	}
	d.Orders = make(map[string]Order, len(s.Orders))
	for k, v := range s.Orders {
		d.Orders[k] = v
	}
	return d
}
func (b *fileBackend) Read(ctx context.Context, fn func(Transaction) error) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.unchanged(); err != nil {
		return err
	}
	d := cloneState(b.data)
	return fn(&fileTransaction{data: &d})
}
func (b *fileBackend) Write(ctx context.Context, fn func(Transaction) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.unchanged(); err != nil {
		return err
	}
	d := cloneState(b.data)
	if err := fn(&fileTransaction{data: &d, write: true}); err != nil {
		return err
	}
	if err := validateState(d); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.path != "" {
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(body) > maxFileBytes {
			return ErrUnavailable
		}
		if err = os.MkdirAll(filepath.Dir(b.path), 0700); err != nil {
			return err
		}
		if _, err = packagefs.LstatNoSymlink(filepath.Dir(b.path)); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(b.path), ".progression-*")
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
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = b.unchanged(); err != nil {
			return err
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), b.path); err != nil {
			return err
		}
		b.digest, b.exists = sha256.Sum256(body), true
	}
	b.data = d
	return nil
}

type fileTransaction struct {
	data  *fileState
	write bool
}

func (t *fileTransaction) Settings() (Settings, error) { return CloneSettings(t.data.Settings), nil }
func (t *fileTransaction) SetSettings(s Settings) error {
	if !t.write {
		return ErrUnavailable
	}
	t.data.Settings = CloneSettings(s)
	return nil
}
func (t *fileTransaction) Account(id int64) (Account, error) {
	if id < 1 {
		return Account{}, ErrInvalid
	}
	a, ok := t.data.Accounts[number(id)]
	if !ok {
		a.UserID = id
	}
	return a, nil
}
func (t *fileTransaction) PutAccount(a Account) error {
	if !t.write {
		return ErrUnavailable
	}
	t.data.Accounts[number(a.UserID)] = a
	return nil
}
func (t *fileTransaction) Entry(id int64) (Entry, error) {
	if id < 1 || id >= t.data.NextEntryID {
		return Entry{}, ErrUnavailable
	}
	return t.data.Entries[id-1], nil
}
func (t *fileTransaction) Append(e Entry) (Entry, error) {
	if !t.write || t.data.NextEntryID == math.MaxInt64 {
		return Entry{}, ErrUnavailable
	}
	e.ID = t.data.NextEntryID
	t.data.NextEntryID++
	t.data.Entries = append(t.data.Entries, e)
	return e, nil
}
func (t *fileTransaction) History(user, before int64, limit int) ([]Entry, error) {
	entries := []Entry{}
	for i := len(t.data.Entries) - 1; i >= 0 && len(entries) < limit; i-- {
		e := t.data.Entries[i]
		if e.UserID == user && (before == 0 || e.ID < before) {
			entries = append(entries, e)
		}
	}
	return entries, nil
}
func (t *fileTransaction) RewardsSince(user int64, since time.Time) ([]Entry, error) {
	entries := []Entry{}
	for _, e := range t.data.Entries {
		if e.UserID == user && isReward(e.Kind) && e.CreatedAt.After(since) {
			entries = append(entries, e)
		}
	}
	return entries, nil
}
func (t *fileTransaction) Receipt(actor int64, request string) (Receipt, bool, error) {
	r, ok := t.data.Receipts[key(actor, request)]
	return r, ok, nil
}
func (t *fileTransaction) PutReceipt(r Receipt) error {
	if !t.write {
		return ErrUnavailable
	}
	k := key(r.ActorID, r.RequestID)
	if _, found := t.data.Receipts[k]; found {
		return ErrReused
	}
	t.data.Receipts[k] = r
	return nil
}
func (t *fileTransaction) Order(user int64, request string) (Order, bool, error) {
	o, ok := t.data.Orders[key(user, request)]
	return o, ok, nil
}
func (t *fileTransaction) Orders() ([]Order, error) {
	orders := make([]Order, 0, len(t.data.Orders))
	for _, o := range t.data.Orders {
		orders = append(orders, o)
	}
	return orders, nil
}
func (t *fileTransaction) PutOrder(o Order) error {
	if !t.write {
		return ErrUnavailable
	}
	k := key(o.UserID, o.RequestID)
	if old, exists := t.data.Orders[k]; exists {
		if old.Status != "pending" || !SameOrderTerms(old, o) || o.Status == "pending" {
			return ErrReused
		}
	}
	t.data.Orders[k] = o
	return nil
}
func (t *fileTransaction) MaximumExperience() (int64, error) {
	var n int64
	for _, a := range t.data.Accounts {
		n = max(n, a.Experience)
	}
	return n, nil
}
func (t *fileTransaction) PlanUsed(plan string) (bool, error) {
	for _, e := range t.data.Entries {
		if e.Membership.Plan == plan {
			return true, nil
		}
	}
	for _, o := range t.data.Orders {
		if o.Plan == plan {
			return true, nil
		}
	}
	return false, nil
}

// SameOrderTerms is also enforced by the PostgreSQL trigger. Only settlement
// fields may change, exactly once, after an order has been reserved.
func SameOrderTerms(a, b Order) bool {
	a.Status = ""
	a.PaymentEntry = 0
	a.EntryID = 0
	a.CompletedAt = time.Time{}
	b.Status = ""
	b.PaymentEntry = 0
	b.EntryID = 0
	b.CompletedAt = time.Time{}
	return a == b
}
