package articlewidgets

import (
	"path/filepath"
	"testing"
)

func TestProductScopesIsolateVotesAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "components")
	s := NewStore(path)
	c := example("poll")
	ctx := WithScope(t.Context(), "shop:camera")
	if _, err := s.Vote(ctx, 1, 1, c, "u:2", 1); err != nil {
		t.Fatal(err)
	}
	s = NewStore(path)
	for scope, total := range map[string]int{"shop:camera": 1, "shop:book": 0, "": 0} {
		state, err := s.State(WithScope(t.Context(), scope), 1, 1, c, "u:2")
		if err != nil || state.Total != total {
			t.Fatal(scope, state, err)
		}
	}
}

func TestProductScopesIsolatePacketTransfers(t *testing.T) {
	s, ledger := NewStore(""), &recordingLedger{}
	c := example("redpacket")
	c.Amount, c.Count = "2", 1
	for _, scope := range []string{"shop:camera", "shop:book", ""} {
		ctx := WithScope(t.Context(), scope)
		if err := s.Activate(ctx, 1, 1, c, ledger, true); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := s.Claim(ctx, 1, 1, 2, c, ledger); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ledger.calls != 3 {
		t.Fatal("transfers collided between content owners", ledger.calls)
	}
}
