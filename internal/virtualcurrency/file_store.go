package virtualcurrency

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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const maxFileBytes = 64 << 20

type fileState struct {
	Schema      int              `json:"schema"`
	Settings    Settings         `json:"settings"`
	Balances    map[string]int64 `json:"balances"`
	Entries     []Entry          `json:"entries"`
	Operations  []Operation      `json:"operations"`
	NextEntryID int64            `json:"next_entry_id"`
	NextOpID    int64            `json:"next_operation_id"`
}

type FileStore struct {
	mu     sync.RWMutex
	path   string
	data   fileState
	digest [32]byte
	exists bool
	now    func() time.Time
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, now: time.Now, data: fileState{Schema: 3, Settings: DefaultSettings(), Balances: map[string]int64{}, Entries: []Entry{}, Operations: []Operation{}, NextEntryID: 1, NextOpID: 1}}
	if path == "" {
		return s, nil
	}
	body, exists, err := readCurrencyFile(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return s, nil
	}
	if !utf8.Valid(body) || rejectDuplicateJSON(body) != nil {
		return nil, ErrInvalid
	}
	var data fileState
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&data); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrInvalid
	}

	if err = validateFileState(data); err != nil {
		return nil, err
	}
	s.data, s.digest, s.exists = data, sha256.Sum256(body), true
	return s, nil
}

func readCurrencyFile(path string) ([]byte, bool, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if info.Size() > maxFileBytes {
		return nil, false, ErrStoreUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(body) > maxFileBytes {
		return nil, false, ErrStoreUnavailable
	}
	return body, true, err
}

func (s *FileStore) unchanged() error {
	if s.path == "" {
		return nil
	}
	body, exists, err := readCurrencyFile(s.path)
	if err != nil {
		return err
	}
	if exists != s.exists || exists && sha256.Sum256(body) != s.digest {
		return ErrStoreUnavailable
	}
	return nil
}

func (s *FileStore) Ready(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.unchanged()
}

func (s *FileStore) settings() Settings {
	value := s.data.Settings
	rule := value.CheckInRule()
	value.CheckInMode, value.CheckInTimezone = rule.Mode, rule.Timezone
	value.PrecisionLocked = len(s.data.Entries) > 0
	return value
}

func (s *FileStore) Settings(ctx context.Context) (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	return s.settings(), nil
}

func (s *FileStore) UpdateSettings(ctx context.Context, expected int64, value Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Settings{}, err
	}
	value, err := PrepareSettings(s.data.Settings, value, expected, len(s.data.Entries) > 0, s.now())
	if err != nil {
		return Settings{}, err
	}
	for _, balance := range s.data.Balances {
		if balance > value.MaxBalance {
			return Settings{}, ErrBalanceLimit
		}
	}
	d := cloneState(s.data)
	d.Settings = value
	if err = s.persist(ctx, d); err != nil {
		return Settings{}, err
	}
	return value, nil
}

func (s *FileStore) Balance(ctx context.Context, userID int64) (int64, error) {
	if userID < 1 {
		return 0, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return s.data.Balances[strconv.FormatInt(userID, 10)], nil
}

func currencyHistory(d fileState, userID, before int64, limit int) []Entry {
	result := make([]Entry, 0, limit)
	for i := len(d.Entries) - 1; i >= 0 && len(result) < limit; i-- {
		e := d.Entries[i]
		if e.UserID == userID && (before == 0 || e.ID < before) {
			result = append(result, e)
		}
	}
	return result
}

func (s *FileStore) History(ctx context.Context, userID, before int64, limit int) ([]Entry, error) {
	if userID < 1 || before < 0 || limit < 1 || limit > MaxHistoryPage {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return currencyHistory(s.data, userID, before, limit), nil
}

func saturatingAdd(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

func (s *FileStore) Wallet(ctx context.Context, userID, before int64, limit int) (Wallet, error) {
	if userID < 1 || before < 0 || limit < 1 || limit > MaxHistoryPage {
		return Wallet{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Wallet{}, err
	}
	value := Wallet{Settings: s.settings(), Balance: s.data.Balances[strconv.FormatInt(userID, 10)], Entries: currencyHistory(s.data, userID, before, limit)}
	cutoff := s.now().Add(-24 * time.Hour)
	for _, e := range s.data.Entries {
		if e.UserID != userID {
			continue
		}
		if e.Kind == RewardCheckIn {
			next := value.Settings.CheckInRule().Next(e.CreatedAt)
			if next.After(value.NextCheckInAt) {
				value.NextCheckInAt = next
			}
		}
		if !e.CreatedAt.After(cutoff) {
			continue
		}
		if e.Kind == "transfer_out" {
			value.TransferredToday = saturatingAdd(value.TransferredToday, -e.Delta)
		}
		if IsReward(e.Kind) {
			value.RewardedToday = saturatingAdd(value.RewardedToday, e.Delta)
		}
	}
	return value, nil
}

func (s *FileStore) Adjust(ctx context.Context, in Adjustment) (Entry, error) {
	if in.ActorID < 1 || in.UserID < 1 || in.Delta == 0 || in.Delta == math.MinInt64 || in.ExpectedVersion < 1 || !ValidRequestID(in.RequestID) || !ValidReason(in.Reason) {
		return Entry{}, ErrInvalid
	}
	kind := "admin_credit"
	if in.Delta < 0 {
		kind = "admin_debit"
	}
	return s.applySingle(ctx, in.ActorID, in.UserID, in.Delta, in.ExpectedVersion, in.RequestID, strings.TrimSpace(in.Reason), kind, AdjustmentFingerprint(in.UserID, in.Delta, in.Reason), false)
}

func (s *FileStore) Credit(ctx context.Context, in CreditInput) (Entry, error) {
	if in.UserID < 1 || in.Amount < 1 || in.ExpectedVersion < 1 || !ValidRequestID(in.RequestID) || !ValidReason(in.Source) {
		return Entry{}, ErrInvalid
	}
	return s.applySingle(ctx, 0, in.UserID, in.Amount, in.ExpectedVersion, in.RequestID, strings.TrimSpace(in.Source), "credit", CreditFingerprint(in.UserID, in.Amount, in.Source), true)
}

func (s *FileStore) applySingle(ctx context.Context, actor, user, delta, version int64, requestID, reason, kind, fp string, requireEnabled bool) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if prior, ok := findOperation(s.data.Operations, actor, requestID); ok {
		if prior.Fingerprint != fp {
			return Entry{}, ErrIdempotencyConflict
		}
		if len(prior.EntryIDs) != 1 {
			return Entry{}, ErrStoreUnavailable
		}
		return findEntry(s.data.Entries, prior.EntryIDs[0]), nil
	}
	if version != s.data.Settings.Version {
		return Entry{}, ErrConflict
	}
	if requireEnabled && !s.data.Settings.Enabled {
		return Entry{}, ErrDisabled
	}
	return s.appendSingle(ctx, actor, user, 0, delta, requestID, reason, kind, fp)
}

func (s *FileStore) appendSingle(ctx context.Context, actor, user, source, delta int64, requestID, reason, kind, fp string) (Entry, error) {
	current := s.data.Balances[strconv.FormatInt(user, 10)]
	if delta < 0 && current < -delta {
		return Entry{}, ErrInsufficient
	}
	if delta > 0 && current > s.data.Settings.MaxBalance-delta {
		return Entry{}, ErrBalanceLimit
	}
	if s.data.NextEntryID >= math.MaxInt64 || s.data.NextOpID >= math.MaxInt64 {
		return Entry{}, ErrStoreUnavailable
	}
	d := cloneState(s.data)
	e := Entry{ID: d.NextEntryID, OperationID: d.NextOpID, UserID: user, ActorID: actor, SourceID: source, Delta: delta, Balance: current + delta, Kind: kind, Reason: reason, CreatedAt: s.now().UTC()}
	d.NextEntryID++
	d.NextOpID++
	d.Balances[strconv.FormatInt(user, 10)] = e.Balance
	d.Entries = append(d.Entries, e)
	d.Operations = append(d.Operations, Operation{ID: e.OperationID, ActorID: actor, RequestID: requestID, Fingerprint: fp, EntryIDs: []int64{e.ID}})
	if err := s.persist(ctx, d); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *FileStore) Transfer(ctx context.Context, in TransferInput) (TransferResult, error) {
	if in.UserID < 1 || in.RecipientID < 1 || in.UserID == in.RecipientID || in.Amount < 1 || in.ExpectedVersion < 1 || !ValidRequestID(in.RequestID) {
		return TransferResult{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TransferResult{}, err
	}
	fp := TransferFingerprint(in.RecipientID, in.Amount)
	if prior, ok := findOperation(s.data.Operations, in.UserID, in.RequestID); ok {
		if prior.Fingerprint != fp {
			return TransferResult{}, ErrIdempotencyConflict
		}
		if len(prior.EntryIDs) != 2 {
			return TransferResult{}, ErrStoreUnavailable
		}
		return TransferResult{Sent: findEntry(s.data.Entries, prior.EntryIDs[0]), Received: findEntry(s.data.Entries, prior.EntryIDs[1]), Replayed: true}, nil
	}
	c := s.data.Settings
	if in.ExpectedVersion != c.Version {
		return TransferResult{}, ErrConflict
	}
	if !c.Enabled {
		return TransferResult{}, ErrDisabled
	}
	if !c.AllowTransfers {
		return TransferResult{}, ErrTransferDisabled
	}
	if in.Amount < c.MinTransfer || in.Amount > c.MaxTransfer {
		return TransferResult{}, ErrInvalid
	}
	fromKey, toKey := strconv.FormatInt(in.UserID, 10), strconv.FormatInt(in.RecipientID, 10)
	from, to := s.data.Balances[fromKey], s.data.Balances[toKey]
	if from < in.Amount {
		return TransferResult{}, ErrInsufficient
	}
	if to > c.MaxBalance-in.Amount {
		return TransferResult{}, ErrBalanceLimit
	}
	var spent int64
	now := s.now().UTC()
	cutoff := now.Add(-24 * time.Hour)
	for _, e := range s.data.Entries {
		if e.UserID == in.UserID && e.Kind == "transfer_out" && e.CreatedAt.After(cutoff) {
			spent = saturatingAdd(spent, -e.Delta)
		}
	}
	if spent > c.DailyTransferLimit-in.Amount {
		return TransferResult{}, ErrRateLimit
	}
	if s.data.NextEntryID > math.MaxInt64-2 || s.data.NextOpID >= math.MaxInt64 {
		return TransferResult{}, ErrStoreUnavailable
	}
	d := cloneState(s.data)
	sent := Entry{ID: d.NextEntryID, OperationID: d.NextOpID, UserID: in.UserID, PeerID: in.RecipientID, ActorID: in.UserID, Delta: -in.Amount, Balance: from - in.Amount, Kind: "transfer_out", CreatedAt: now}
	received := Entry{ID: d.NextEntryID + 1, OperationID: d.NextOpID, UserID: in.RecipientID, PeerID: in.UserID, ActorID: in.UserID, Delta: in.Amount, Balance: to + in.Amount, Kind: "transfer_in", CreatedAt: now}
	d.NextEntryID += 2
	d.NextOpID++
	d.Balances[fromKey], d.Balances[toKey] = sent.Balance, received.Balance
	d.Entries = append(d.Entries, sent, received)
	d.Operations = append(d.Operations, Operation{ID: sent.OperationID, ActorID: in.UserID, RequestID: in.RequestID, Fingerprint: fp, EntryIDs: []int64{sent.ID, received.ID}})
	if err := s.persist(ctx, d); err != nil {
		return TransferResult{}, err
	}
	return TransferResult{Sent: sent, Received: received}, nil
}

func (s *FileStore) Reward(ctx context.Context, in RewardInput) (RewardResult, error) {
	actor, requestID, fp, err := RewardIdentity(in)
	if err != nil {
		return RewardResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return RewardResult{}, err
	}
	if prior, ok := findOperation(s.data.Operations, actor, requestID); ok {
		if prior.Fingerprint != fp {
			return RewardResult{}, ErrIdempotencyConflict
		}
		if len(prior.EntryIDs) != 1 {
			return RewardResult{}, ErrStoreUnavailable
		}
		return RewardResult{Entry: findEntry(s.data.Entries, prior.EntryIDs[0]), Replayed: true}, nil
	}
	now := s.now().UTC()
	cfg := s.data.Settings
	amount, err := RewardAmount(cfg, in, now)
	if err != nil {
		return RewardResult{}, err
	}
	var userTotal, globalTotal int64
	publicationCount := 0
	for i := len(s.data.Entries) - 1; i >= 0; i-- {
		e := s.data.Entries[i]
		if in.Kind == RewardCheckIn && e.UserID == in.UserID && e.Kind == RewardCheckIn && now.Before(cfg.CheckInRule().Next(e.CreatedAt)) {
			return RewardResult{Entry: e, Replayed: true}, nil
		}
		if !IsReward(e.Kind) || !e.CreatedAt.After(now.Add(-24*time.Hour)) {
			continue
		}
		globalTotal = saturatingAdd(globalTotal, e.Delta)
		if e.UserID == in.UserID {
			userTotal = saturatingAdd(userTotal, e.Delta)
			if e.Kind == RewardPublication {
				publicationCount++
			}
		}
	}
	if userTotal > cfg.DailyRewardLimit-amount || globalTotal > cfg.DailyIssuanceLimit-amount || in.Kind == RewardPublication && publicationCount >= cfg.PublicationDailyLimit {
		return RewardResult{}, ErrRewardLimit
	}
	entry, err := s.appendSingle(ctx, actor, in.UserID, in.SourceID, amount, requestID, RewardReason(in.Kind), in.Kind, fp)
	return RewardResult{Entry: entry}, err
}

func findOperation(ops []Operation, actor int64, request string) (Operation, bool) {
	for _, op := range ops {
		if op.ActorID == actor && op.RequestID == request {
			return op, true
		}
	}
	return Operation{}, false
}

func (s *FileStore) Receipt(ctx context.Context, actor int64, request string) ([]Entry, string, error) {
	if actor < 0 || !ValidRequestID(request) {
		return nil, "", ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	op, ok := findOperation(s.data.Operations, actor, request)
	if !ok {
		return nil, "", nil
	}
	entries := make([]Entry, 0, len(op.EntryIDs))
	for _, id := range op.EntryIDs {
		entries = append(entries, findEntry(s.data.Entries, id))
	}
	return entries, op.Fingerprint, nil
}
func findEntry(entries []Entry, id int64) Entry {
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	return Entry{}
}
func cloneState(s fileState) fileState {
	d := s
	d.Balances = make(map[string]int64, len(s.Balances))
	for k, v := range s.Balances {
		d.Balances[k] = v
	}
	d.Entries = append([]Entry{}, s.Entries...)
	d.Operations = append([]Operation{}, s.Operations...)
	// Existing operations and their entry ID slices are immutable.
	return d
}

func (s *FileStore) persist(ctx context.Context, d fileState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path != "" {
		if err := s.unchanged(); err != nil {
			return err
		}
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(body) > maxFileBytes {
			return ErrStoreUnavailable
		}
		if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return err
		}
		if _, err = packagefs.LstatNoSymlink(filepath.Dir(s.path)); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".currency-*")
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
		if err = s.unchanged(); err != nil {
			return err
		}
		if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path); err != nil {
			return err
		}
		s.digest, s.exists = sha256.Sum256(body), true
	}
	s.data = d
	return nil
}
