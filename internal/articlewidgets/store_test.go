package articlewidgets

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"newpress.local/newpress/internal/virtualcurrency"
)

func TestVotesPersistAndOnlyOneConcurrentVote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "widgets")
	s := NewStore(path)
	c := example("poll")
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for range 16 {
		wg.Go(func() {
			_, err := s.Vote(t.Context(), 1, 1, c, "u:2", 1)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if !errors.Is(err, ErrAlready) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success != 1 {
		t.Fatal(success)
	}
	st, err := NewStore(path).State(t.Context(), 1, 1, c, "u:2")
	if err != nil || st.Total != 1 || st.Counts[1] != 1 || !st.Participated {
		t.Fatal(st, err)
	}
	c.Tone = "amber"
	c.Width = "compact"
	st, err = s.State(t.Context(), 1, 1, c, "u:2")
	if err != nil || st.Total != 1 {
		t.Fatal("appearance edit reset votes", st, err)
	}
	c.Items[0].Title = "Changed"
	st, err = s.State(t.Context(), 1, 1, c, "u:2")
	if err != nil || st.Total != 0 {
		t.Fatal("changed configuration reused results", st, err)
	}
}

type recordingLedger struct {
	virtualcurrency.Store
	mu        sync.Mutex
	transfers map[string]virtualcurrency.TransferResult
	calls     int
	timeout   bool
}

func (l *recordingLedger) Settings(context.Context) (virtualcurrency.Settings, error) {
	return virtualcurrency.Settings{Version: 1, Enabled: true, AllowTransfers: true, MinTransfer: 1, MaxTransfer: 100, MaxBalance: 10000}, nil
}
func (l *recordingLedger) Balance(context.Context, int64) (int64, error) { return 10000, nil }
func (l *recordingLedger) Transfer(_ context.Context, in virtualcurrency.TransferInput) (virtualcurrency.TransferResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.transfers == nil {
		l.transfers = map[string]virtualcurrency.TransferResult{}
	}
	if prior, ok := l.transfers[in.RequestID]; ok {
		prior.Replayed = true
		return prior, nil
	}
	l.calls++
	result := virtualcurrency.TransferResult{Sent: virtualcurrency.Entry{UserID: in.UserID, Delta: -in.Amount}, Received: virtualcurrency.Entry{UserID: in.RecipientID, Delta: in.Amount}}
	l.transfers[in.RequestID] = result
	if l.timeout {
		l.timeout = false
		return virtualcurrency.TransferResult{}, context.DeadlineExceeded
	}
	return result, nil
}
func TestPacketDurableIntentReplaysAfterTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "widgets")
	s := NewStore(path)
	c := example("redpacket")
	c.Amount = "2"
	c.Count = 1
	ledger := &recordingLedger{timeout: true}
	if _, err := s.Claim(t.Context(), 1, 1, 2, c, ledger); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := s.Activate(t.Context(), 1, 1, c, ledger, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), 1, 1, 2, c, ledger); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if st, err := s.State(t.Context(), 1, 1, c, "u:2"); err != nil || !st.PendingSelf || st.Pending != 1 {
		t.Fatal("pending claimant must be able to retry", st, err)
	}
	if st, err := s.State(t.Context(), 1, 1, c, "u:3"); err != nil || st.PendingSelf {
		t.Fatal("pending marker leaked to another claimant", st, err)
	}
	if _, err := s.Claim(t.Context(), 1, 1, 3, c, ledger); !errors.Is(err, ErrClosed) {
		t.Fatal("pending slot overspent", err)
	}
	s = NewStore(path)
	st, err := s.Claim(t.Context(), 1, 1, 2, c, ledger)
	if err != nil || st.Claimed != 1 || st.Pending != 0 || !st.Participated || ledger.calls != 1 {
		t.Fatal(st, err, ledger.calls)
	}
	_, err = s.Claim(t.Context(), 1, 1, 2, c, ledger)
	if err != nil || ledger.calls != 1 {
		t.Fatal("duplicate debit", err)
	}
	_, err = s.Claim(t.Context(), 1, 1, 1, c, ledger)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("self claim")
	}
	st, err = s.State(t.Context(), 1, 9, c, "u:2")
	if err != nil || st.Active {
		t.Fatal("author change reused spending authorization", st, err)
	}
}
func TestRatingRejectsOutOfRangeAndAggregates(t *testing.T) {
	s := NewStore("")
	c := example("rating")
	for _, v := range []int{-1, 0, 6} {
		if _, err := s.Vote(t.Context(), 1, 1, c, "u:1", v); !errors.Is(err, ErrInvalid) {
			t.Fatal(v, err)
		}
	}
	_, _ = s.Vote(t.Context(), 1, 1, c, "u:1", 5)
	st, err := s.Vote(t.Context(), 1, 1, c, "g:123", 3)
	if err != nil || st.Total != 2 || st.Average != 4 {
		t.Fatal(st, err)
	}
}
