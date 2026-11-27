package articlewidgets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/virtualcurrency"
)

var ErrAlready = errors.New("你已经参与过这个组件")
var ErrClosed = errors.New("红包尚未启用、已经暂停或份数已领完")
var ErrCapacity = errors.New("组件参与人数已达到上限")

type Claim struct {
	RequestID string `json:"request_id"`
	Done      bool   `json:"done"`
}
type Record struct {
	Version         int              `json:"version"`
	Fingerprint     string           `json:"fingerprint"`
	Author          int64            `json:"author"`
	Votes           map[string]int   `json:"votes"`
	Claims          map[string]Claim `json:"claims"`
	Active          bool             `json:"active"`
	Amount          int64            `json:"amount"`
	SettingsVersion int64            `json:"settings_version"`
	Precision       int              `json:"precision"`
}
type State struct {
	Counts       []int   `json:"counts"`
	Total        int     `json:"total"`
	Average      float64 `json:"average"`
	Participated bool    `json:"participated"`
	Choice       int     `json:"choice"`
	Active       bool    `json:"active"`
	Claimed      int     `json:"claimed"`
	Pending      int     `json:"pending"`
	PendingSelf  bool    `json:"pending_self"`
}
type Store struct {
	mu     sync.Mutex
	path   string
	memory map[string]Record
}

func NewStore(path string) *Store { return &Store{path: path, memory: map[string]Record{}} }
func digest(value string) string  { s := sha256.Sum256([]byte(value)); return hex.EncodeToString(s[:]) }
func key(post, author int64, c Config) string {
	return digest(fmt.Sprintf("%d:%d:%s:%s", post, author, c.ID, c.InteractionFingerprint()))
}

type scopeKey struct{}

// WithScope isolates non-article content without changing existing article keys.
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func scopedKey(ctx context.Context, post, author int64, c Config) string {
	k := key(post, author, c)
	if scope, _ := ctx.Value(scopeKey{}).(string); scope != "" {
		return digest(scope + ":" + k)
	}
	return k
}

// Transactions use an OS directory lock and reload each time. This coordinates
// all processes, including SQL deployments that share the configured data dir.
// Files are sharded per component so reads/writes never scan all site votes.
func (s *Store) transact(ctx context.Context, post, author int64, c Config, fn func(*Record, func() error) error) error {
	if post < 1 || author < 1 || c.Validate() != nil {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	k := scopedKey(ctx, post, author, c)
	d := Record{Version: 1, Fingerprint: c.InteractionFingerprint(), Author: author, Votes: map[string]int{}, Claims: map[string]Claim{}}
	path := ""
	if s.path != "" {
		lock, err := dirlock.Acquire(s.path)
		if err != nil {
			return err
		}
		defer lock.Close()
		path = filepath.Join(s.path, k+".json")
		info, err := packagefs.LstatNoSymlink(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() > 8<<20 {
				return ErrInvalid
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			opened, err := f.Stat()
			if err != nil || !os.SameFile(info, opened) {
				return ErrInvalid
			}
			// Existing files must not inherit a fresh record's defaults.
			d = Record{}
			if packagefs.DecodeJSONInto(f, 8<<20, "article widget record", &d) != nil {
				return ErrInvalid
			}
			if err := f.Close(); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if prior, ok := s.memory[k]; ok {
		raw, _ := json.Marshal(prior)
		_ = json.Unmarshal(raw, &d)
	}
	if d.Version != 1 || d.Fingerprint != c.InteractionFingerprint() || d.Author != author || d.Votes == nil || d.Claims == nil || len(d.Votes) > 50000 || len(d.Claims) > 10000 {
		return ErrInvalid
	}
	persist := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(body) > 8<<20 {
			return ErrCapacity
		}
		if path != "" {
			f, err := os.CreateTemp(s.path, ".widget-*")
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
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), path); err != nil {
				return err
			}
		} else {
			var clone Record
			_ = json.Unmarshal(body, &clone)
			s.memory[k] = clone
		}
		return nil
	}
	return fn(&d, persist)
}

func snapshot(d *Record, c Config, actor string) State {
	n := len(c.Items)
	if c.Kind == "rating" {
		n = int(c.Max) + 1
	}
	st := State{Counts: make([]int, n), Active: d.Active, Choice: -1}
	var sum int
	for who, v := range d.Votes {
		if v >= 0 && v < n {
			st.Counts[v]++
			st.Total++
			sum += v
		}
		if who == actor {
			st.Participated = true
			st.Choice = v
		}
	}
	if st.Total > 0 {
		st.Average = float64(sum) / float64(st.Total)
	}
	for who, cl := range d.Claims {
		if cl.Done {
			st.Claimed++
		} else {
			st.Pending++
		}
		if who == actor && cl.Done {
			st.Participated = true
		}
		if who == actor && !cl.Done {
			st.PendingSelf = true
		}
	}
	return st
}
func (s *Store) State(ctx context.Context, post, author int64, c Config, actor string) (State, error) {
	var st State
	err := s.transact(ctx, post, author, c, func(d *Record, _ func() error) error { st = snapshot(d, c, actor); return nil })
	return st, err
}
func (s *Store) Vote(ctx context.Context, post, author int64, c Config, actor string, value int) (State, error) {
	var st State
	if len(actor) < 3 || len(actor) > 100 || c.Kind != "poll" && c.Kind != "rating" || c.Kind == "poll" && (value < 0 || value >= len(c.Items)) || c.Kind == "rating" && (value < 1 || value > int(c.Max)) {
		return st, ErrInvalid
	}
	err := s.transact(ctx, post, author, c, func(d *Record, persist func() error) error {
		if _, ok := d.Votes[actor]; ok {
			return ErrAlready
		}
		if len(d.Votes) >= 50000 {
			return ErrCapacity
		}
		d.Votes[actor] = value
		if err := persist(); err != nil {
			return err
		}
		st = snapshot(d, c, actor)
		return nil
	})
	return st, err
}

func (s *Store) Activate(ctx context.Context, post, author int64, c Config, ledger virtualcurrency.Store, active bool) error {
	if c.Kind != "redpacket" || ledger == nil {
		return ErrInvalid
	}
	return s.transact(ctx, post, author, c, func(d *Record, persist func() error) error {
		if active {
			cfg, err := ledger.Settings(ctx)
			if err != nil {
				return err
			}
			amount, err := virtualcurrency.ParseAmount(c.Amount, cfg.Precision)
			if err != nil {
				return err
			}
			if !cfg.Enabled || !cfg.AllowTransfers {
				return virtualcurrency.ErrTransferDisabled
			}
			if amount < cfg.MinTransfer || amount > cfg.MaxTransfer || amount > cfg.MaxBalance/int64(c.Count) {
				return virtualcurrency.ErrInvalid
			}
			if d.Amount != 0 && (d.Amount != amount || d.Precision != cfg.Precision) {
				return virtualcurrency.ErrConflict
			}
			balance, err := ledger.Balance(ctx, author)
			if err != nil {
				return err
			}
			remaining := c.Count - len(d.Claims)
			if remaining > 0 && balance < amount*int64(remaining) {
				return virtualcurrency.ErrInsufficient
			}
			d.Amount, d.Precision, d.SettingsVersion = amount, cfg.Precision, cfg.Version
		}
		d.Active = active
		return persist()
	})
}

func (s *Store) Claim(ctx context.Context, post, author, user int64, c Config, ledger virtualcurrency.Store) (State, error) {
	var st State
	if user < 1 || user == author || c.Kind != "redpacket" || ledger == nil {
		return st, ErrInvalid
	}
	actor := fmt.Sprintf("u:%d", user)
	err := s.transact(ctx, post, author, c, func(d *Record, persist func() error) error {
		cl, exists := d.Claims[actor]
		if cl.Done {
			st = snapshot(d, c, actor)
			return nil
		}
		if !d.Active || d.Amount < 1 || !exists && len(d.Claims) >= c.Count {
			return ErrClosed
		}
		if !exists {
			cl = Claim{RequestID: "npw_" + digest(scopedKey(ctx, post, author, c)+":"+actor)}
			d.Claims[actor] = cl
			if err := persist(); err != nil {
				return err
			}
		}
		// The durable intent reserves a slot BEFORE the atomic ledger transfer.
		// The same request ID replays its receipt after a timeout or crash.
		result, err := ledger.Transfer(ctx, virtualcurrency.TransferInput{UserID: author, RecipientID: user, Amount: d.Amount, ExpectedVersion: d.SettingsVersion, RequestID: cl.RequestID})
		if err != nil {
			if errors.Is(err, virtualcurrency.ErrConflict) || errors.Is(err, virtualcurrency.ErrInsufficient) || errors.Is(err, virtualcurrency.ErrDisabled) || errors.Is(err, virtualcurrency.ErrTransferDisabled) || errors.Is(err, virtualcurrency.ErrBalanceLimit) || errors.Is(err, virtualcurrency.ErrRateLimit) || errors.Is(err, virtualcurrency.ErrInvalid) {
				delete(d.Claims, actor)
				if e := persist(); e != nil {
					return e
				}
			}
			return err
		}
		if result.Sent.UserID != author || result.Received.UserID != user || result.Sent.Delta != -d.Amount || result.Received.Delta != d.Amount {
			return ErrInvalid
		}
		cl.Done = true
		d.Claims[actor] = cl
		if err := persist(); err != nil {
			return err
		}
		st = snapshot(d, c, actor)
		return nil
	})
	return st, err
}
