package virtualcurrency

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testCurrency(t *testing.T, path string) (*FileStore, Settings) {
	t.Helper()
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture deliberately uses the original minor-unit rewards at precision 2.
	cfg := integerDefaults()
	cfg.Enabled = true
	cfg.AllowTransfers = true
	cfg.Precision = 2
	cfg.Name = "阅读币"
	cfg.Symbol = "R"
	cfg, err = s.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg
}
func seedCurrency(t *testing.T, s *FileStore, cfg Settings, user, amount int64) Entry {
	t.Helper()
	entry, err := s.Adjust(t.Context(), Adjustment{ActorID: 99, UserID: user, Delta: amount, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("seed-account-%020d", user), Reason: "测试发放"})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestAmountsRemainExact(t *testing.T) {
	for _, tt := range []struct {
		value     int64
		precision int
		text      string
	}{
		{1, 6, "0.000001"}, {-1, 2, "-0.01"}, {-125, 2, "-1.25"}, {math.MaxInt64, 6, "9223372036854.775807"},
		{math.MinInt64, 6, "-9223372036854.775808"}, {math.MaxInt64, 0, "9223372036854775807"}, {0, 2, "0.00"},
	} {
		if got := FormatAmount(tt.value, tt.precision); got != tt.text {
			t.Fatalf("format %d: %s", tt.value, got)
		}
		if tt.value > 0 {
			got, err := ParseAmount(tt.text, tt.precision)
			if err != nil || got != tt.value {
				t.Fatalf("parse %s: %d %v", tt.text, got, err)
			}
		}
	}
	for _, raw := range []string{"", "0", "0.00", "-1", "+1", " 1", "1 ", "1e2", "NaN", "1_000", "1,000", "01", ".1", "1.", "1.001", "１", "1.2.3", "9223372036854775808", "92233720368547758.08"} {
		if _, err := ParseAmount(raw, 2); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", raw, err)
		}
	}
	if got, err := ParseAmount("12.3", 2); err != nil || got != 1230 {
		t.Fatalf("short fraction: %d %v", got, err)
	}
}

func TestTransferPersistencePrivacyAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "currency.json")
	s, cfg := testCurrency(t, path)
	seedCurrency(t, s, cfg, 1, 1000)
	in := TransferInput{UserID: 1, RecipientID: 2, Amount: 125, ExpectedVersion: cfg.Version, RequestID: "transfer-test-request-1"}
	first, err := s.Transfer(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sent.Balance != 875 || first.Received.Balance != 125 || first.Sent.Delta+first.Received.Delta != 0 {
		t.Fatal(first)
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.Transfer(t.Context(), in)
	if err != nil || !retry.Replayed || retry.Sent.ID != first.Sent.ID {
		t.Fatal(retry, err)
	}
	cfg, err = s.Settings(t.Context())
	if err != nil || !cfg.PrecisionLocked {
		t.Fatal(cfg, err)
	}
	changed := cfg
	changed.Precision = 3
	if _, err = s.UpdateSettings(t.Context(), cfg.Version, changed); !errors.Is(err, ErrPrecisionLocked) {
		t.Fatal(err)
	}
	changed = cfg
	changed.Enabled = false
	changed, err = s.UpdateSettings(t.Context(), cfg.Version, changed)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err = s.Transfer(t.Context(), in); err != nil || !retry.Replayed {
		t.Fatal("replay after settings change", err)
	}
	in.Amount++
	if _, err = s.Transfer(t.Context(), in); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	in.RequestID = "transfer-test-request-2"
	if _, err = s.Transfer(t.Context(), in); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	in.ExpectedVersion = changed.Version
	if _, err = s.Transfer(t.Context(), in); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	history, err := s.History(t.Context(), 2, 0, 10)
	if err != nil || len(history) != 1 || history[0].UserID != 2 {
		t.Fatal(history, err)
	}
	page, err := s.History(t.Context(), 1, first.Sent.ID, 1)
	if err != nil || len(page) != 1 || page[0].Kind != "admin_credit" {
		t.Fatal(page, err)
	}
	if err = validateFileState(s.data); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentTransfersCannotOverspend(t *testing.T) {
	s, cfg := testCurrency(t, "")
	seedCurrency(t, s, cfg, 1, 100)
	var success atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			_, err := s.Transfer(t.Context(), TransferInput{UserID: 1, RecipientID: int64(i + 2), Amount: 10, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("concurrent-transfer-%020d", i)})
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrInsufficient) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 10 {
		t.Fatal("successful transfers", success.Load())
	}
	var total int64
	for _, balance := range s.data.Balances {
		total += balance
	}
	if total != 100 {
		t.Fatal("value was created or lost", total)
	}
	if err := validateFileState(s.data); err != nil {
		t.Fatal(err)
	}
}

func TestAdjustmentBoundsAndSettingsRace(t *testing.T) {
	s, cfg := testCurrency(t, "")
	cfg.MaxBalance = math.MaxInt64
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	seedCurrency(t, s, cfg, 1, math.MaxInt64)
	base := Adjustment{ActorID: 99, UserID: 1, Delta: 1, ExpectedVersion: cfg.Version, RequestID: "overflow-adjustment-1", Reason: "调整"}
	if _, err = s.Adjust(t.Context(), base); !errors.Is(err, ErrBalanceLimit) {
		t.Fatal(err)
	}
	base.Delta = math.MinInt64
	if _, err = s.Adjust(t.Context(), base); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	base.Delta = -math.MaxInt64
	if _, err = s.Adjust(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	base.RequestID = "negative-adjustment-2"
	base.Delta = -1
	if _, err = s.Adjust(t.Context(), base); !errors.Is(err, ErrInsufficient) {
		t.Fatal(err)
	}
	for _, reason := range []string{"", "  ", "<script>", "a\x7fb", "a\u202eb"} {
		base.Delta = 1
		base.Reason = reason
		if _, err = s.Adjust(t.Context(), base); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted reason", reason, err)
		}
	}
	stale := cfg
	stale.Precision = 0
	if _, err = s.UpdateSettings(t.Context(), cfg.Version, stale); !errors.Is(err, ErrPrecisionLocked) {
		t.Fatal(err)
	}
}

func TestFileStoreRejectsExternalEditsAndFailedWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "currency.json")
	s, cfg := testCurrency(t, path)
	stale, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	seedCurrency(t, s, cfg, 1, 50)
	in := Adjustment{ActorID: 99, UserID: 2, Delta: 1, ExpectedVersion: cfg.Version, RequestID: "unchanged-file-request", Reason: "调整"}
	if _, err = stale.Adjust(t.Context(), in); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("stale snapshot overwrote ledger", err)
	}
	if balance, _ := stale.Balance(t.Context(), 2); balance != 0 {
		t.Fatal("failed write changed memory")
	}
	if err = stale.Ready(t.Context()); err == nil {
		t.Fatal("stale store reported ready")
	}
	block := filepath.Join(dir, "blocked")
	if err = os.WriteFile(block, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	empty, _ := NewFileStore("")
	empty.path = filepath.Join(block, "ledger.json")
	in.ExpectedVersion = 1
	if _, err = empty.Adjust(t.Context(), in); err == nil {
		t.Fatal("write to a non-directory succeeded")
	}
	if len(empty.data.Entries) != 0 || empty.data.NextEntryID != 1 {
		t.Fatal("failed write consumed receipt")
	}
	empty.path = filepath.Join(dir, "recovered.json")
	if _, err = empty.Adjust(t.Context(), in); err != nil {
		t.Fatal("failed request could not retry", err)
	}
}

func TestFileRecoveryRejectsBrokenLedger(t *testing.T) {
	s, cfg := testCurrency(t, "")
	seedCurrency(t, s, cfg, 1, 100)
	if _, err := s.Transfer(t.Context(), TransferInput{UserID: 1, RecipientID: 2, Amount: 10, ExpectedVersion: cfg.Version, RequestID: "valid-transfer-receipt"}); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*fileState){
		"balance":                   func(d *fileState) { d.Balances["1"]++ },
		"missing zero account":      func(d *fileState) { d.Balances["3"] = 0 },
		"entry balance":             func(d *fileState) { d.Entries[1].Balance++ },
		"orphan operation":          func(d *fileState) { d.Entries[0].OperationID = 99 },
		"missing operation":         func(d *fileState) { d.Operations = d.Operations[:1]; d.NextOpID = 2 },
		"invalid digest":            func(d *fileState) { d.Operations[0].Fingerprint = strings.Repeat("0", 64) },
		"actor":                     func(d *fileState) { d.Entries[2].ActorID = 2 },
		"unbalanced transfer":       func(d *fileState) { d.Entries[2].Delta++; d.Entries[2].Balance++; d.Balances["2"]++ },
		"self transfer":             func(d *fileState) { d.Entries[1].PeerID = 1 },
		"wrong kind":                func(d *fileState) { d.Entries[2].Kind = "credit" },
		"missing reason":            func(d *fileState) { d.Entries[0].Reason = "" },
		"wrong next ID":             func(d *fileState) { d.NextEntryID++ },
		"duplicate entry reference": func(d *fileState) { d.Operations[1].EntryIDs = []int64{2, 2} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := cloneState(s.data)
			mutate(&data)
			body, _ := json.Marshal(data)
			path := filepath.Join(t.TempDir(), "ledger.json")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path); err == nil {
				t.Fatal("accepted corrupt ledger")
			}
		})
	}
	body, _ := json.Marshal(s.data)
	for _, bad := range []string{strings.Replace(string(body), `"schema":3`, `"schema":3,"Schema":3`, 1), strings.Replace(string(body), `"schema":3`, `"schema":3,"schema":3`, 1), `{"schema":3}`, string(body) + `{}`} {
		path := filepath.Join(t.TempDir(), "ledger.json")
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil {
			t.Fatal("accepted ambiguous JSON")
		}
	}
}

func TestFileStoreRejectsRetiredSchemas(t *testing.T) {
	s, cfg := testCurrency(t, "")
	seedCurrency(t, s, cfg, 1, 123)
	for _, schema := range []int{1, 2} {
		document := s.data
		document.Schema = schema
		body, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "ledger.json")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil {
			t.Fatalf("accepted retired schema %d", schema)
		}
	}
}

func TestCheckInAndAutomaticRewardsAreAtomic(t *testing.T) {
	s, cfg := testCurrency(t, "")
	now := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	cfg.CheckInEnabled = true
	cfg.RegistrationEnabled = true
	cfg.PublicationEnabled = true
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	var issued atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			r, err := s.Reward(t.Context(), RewardInput{UserID: 1, Kind: RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("check-in-%020d", i), VerifiedEmail: true})
			if err != nil {
				t.Error(err)
			} else if !r.Replayed {
				issued.Add(1)
			}
		})
	}
	wg.Wait()
	if issued.Load() != 1 {
		t.Fatal("check-in credits", issued.Load())
	}
	registration := RewardInput{UserID: 1, SourceID: 1, Kind: RewardRegistration, OccurredAt: now, VerifiedEmail: true}
	for range 3 {
		if _, err = s.Reward(t.Context(), registration); err != nil {
			t.Fatal(err)
		}
	}
	publication := RewardInput{UserID: 1, SourceID: 10, Kind: RewardPublication, OccurredAt: now, VerifiedEmail: true, ContentLength: 300}
	for range 3 {
		if _, err = s.Reward(t.Context(), publication); err != nil {
			t.Fatal(err)
		}
	}
	publication.UserID = 2
	if _, err = s.Reward(t.Context(), publication); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("changed author received reward", err)
	}
	wallet, err := s.Wallet(t.Context(), 1, 0, 30)
	if err != nil || wallet.Balance != 160 || len(wallet.Entries) != 3 || wallet.NextCheckInAt.IsZero() {
		t.Fatal(wallet, err)
	}
	registration.UserID = 2
	registration.SourceID = 2
	registration.OccurredAt = cfg.RegistrationSince.Add(-time.Second)
	if _, err = s.Reward(t.Context(), registration); !errors.Is(err, ErrRewardIneligible) {
		t.Fatal("old account eligible", err)
	}
	publication.SourceID = 11
	publication.VerifiedEmail = false
	if _, err = s.Reward(t.Context(), publication); !errors.Is(err, ErrRewardIneligible) {
		t.Fatal("unverified publication eligible", err)
	}
	now = now.Add(24 * time.Hour)
	if r, err := s.Reward(t.Context(), RewardInput{UserID: 1, Kind: RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: "check-in-next-day-request", VerifiedEmail: true}); err != nil || r.Replayed {
		t.Fatal(r, err)
	}
	if err = validateFileState(s.data); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalRewardBudgetAndPublicationCount(t *testing.T) {
	s, cfg := testCurrency(t, "")
	cfg.CheckInEnabled = true
	cfg.DailyRewardLimit = 20
	cfg.DailyIssuanceLimit = 30
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var issued atomic.Int64
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Go(func() {
			_, err := s.Reward(t.Context(), RewardInput{UserID: int64(i), Kind: RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: fmt.Sprintf("budget-check-%020d", i), VerifiedEmail: true})
			if err == nil {
				issued.Add(1)
			} else if !errors.Is(err, ErrRewardLimit) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if issued.Load() != 3 {
		t.Fatal("global reward budget exceeded", issued.Load())
	}
	cfg.PublicationEnabled = true
	cfg.PublicationReward = 1
	cfg.PublicationMinLength = 1
	cfg.PublicationDailyLimit = 1
	cfg.DailyIssuanceLimit = 100
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := RewardInput{UserID: 30, SourceID: 30, Kind: RewardPublication, OccurredAt: time.Now(), VerifiedEmail: true, ContentLength: 1}
	if _, err = s.Reward(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in.SourceID++
	if _, err = s.Reward(t.Context(), in); !errors.Is(err, ErrRewardLimit) {
		t.Fatal("publication count exceeded", err)
	}
	if err = validateFileState(s.data); err != nil {
		t.Fatal(err)
	}
}
