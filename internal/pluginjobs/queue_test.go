package pluginjobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testInput() Input {
	return Input{Plugin: "example", Epoch: strings.Repeat("a", 64), Kind: "task.execute", Key: "event:1", Payload: json.RawMessage(`{"id":1}`)}
}
func testQueue(t *testing.T, path string) *Queue {
	t.Helper()
	b, e := NewFileBackend(path)
	if e != nil {
		t.Fatal(e)
	}
	return &Queue{Backend: b}
}
func TestEnqueueReplayAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.json")
	q := testQueue(t, path)
	now := time.Now().UTC()
	in := testInput()
	first, e := q.Enqueue(ctx, in, now)
	if e != nil {
		t.Fatal(e)
	}
	first.Payload[0] = 'x' // returned data cannot mutate storage
	q = testQueue(t, path)
	again, e := q.Enqueue(ctx, in, now.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if again.ID != first.ID || again.Version != 1 || !again.CreatedAt.Equal(now) {
		t.Fatal("replay changed task")
	}
	in.Payload = json.RawMessage(`{"id":2}`)
	if _, e = q.Enqueue(ctx, in, now); !errors.Is(e, ErrConflict) {
		t.Fatalf("different replay: %v", e)
	}
}
func TestLeaseFencingAndRetry(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	if _, e := q.Enqueue(ctx, testInput(), now); e != nil {
		t.Fatal(e)
	}
	first, ok, e := q.Claim(ctx, now)
	if e != nil || !ok {
		t.Fatalf("claim: %v %v", ok, e)
	}
	if _, ok, e = q.Claim(ctx, now.Add(time.Second)); ok || e != nil {
		t.Fatalf("duplicate claim: %v %v", ok, e)
	}
	later := now.Add(LeaseDuration)
	second, ok, e := q.Claim(ctx, later)
	if e != nil || !ok || second.LeaseToken == first.LeaseToken {
		t.Fatalf("reclaim: %v %v", ok, e)
	}
	if e = q.Finish(ctx, first, "", later); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale finish: %v", e)
	}
	if e = q.Finish(ctx, second, "execution_failed", later); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = q.Claim(ctx, later); ok || e != nil {
		t.Fatalf("ignored backoff: %v %v", ok, e)
	}
	third, ok, e := q.Claim(ctx, later.Add(4*time.Second))
	if e != nil || !ok {
		t.Fatalf("retry: %v %v", ok, e)
	}
	if e = q.Finish(ctx, third, "", later.Add(4*time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, ok, e = q.Claim(ctx, later.Add(time.Hour)); ok || e != nil {
		t.Fatalf("completed reclaimed: %v %v", ok, e)
	}
}
func TestConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	if _, e := q.Enqueue(ctx, testInput(), now); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 32)
	for range 32 {
		wg.Go(func() {
			_, ok, e := q.Claim(ctx, now)
			if e != nil {
				t.Error(e)
			}
			results <- ok
		})
	}
	wg.Wait()
	close(results)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claim winners = %d", count)
	}
}
func TestExpiredAttemptsAndManualRetry(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	original, e := q.Enqueue(ctx, testInput(), now)
	if e != nil {
		t.Fatal(e)
	}
	for range MaxAttempts {
		if _, ok, e := q.Claim(ctx, now); e != nil || !ok {
			t.Fatalf("claim: %v %v", ok, e)
		}
		now = now.Add(LeaseDuration)
	}
	if _, ok, e := q.Claim(ctx, now); e != nil || ok {
		t.Fatalf("exhausted: %v %v", ok, e)
	}
	s, e := q.Backend.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	j := s.Jobs[original.ID]
	if j.Status != "failed" || j.ErrorCode != "lease_expired" {
		t.Fatalf("unexpected terminal state: %s %s", j.Status, j.ErrorCode)
	}
	if e = q.Change(ctx, "other", j.ID, j.Version, "retry", now); !errors.Is(e, ErrConflict) {
		t.Fatalf("cross-plugin retry: %v", e)
	}
	if e = q.Change(ctx, j.Plugin, j.ID, j.Version, "retry", now); e != nil {
		t.Fatal(e)
	}
	claim, ok, e := q.Claim(ctx, now)
	if e != nil || !ok {
		t.Fatalf("manual retry: %v %v", ok, e)
	}
	if e = q.Change(ctx, claim.Plugin, claim.ID, claim.Version, "cancel", now); e != nil {
		t.Fatal(e)
	}
	if e = q.Finish(ctx, claim, "", now); !errors.Is(e, ErrConflict) {
		t.Fatalf("cancel fence: %v", e)
	}
}
func TestFileRollbackAndExternalChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.json")
	q := testQueue(t, path)
	now := time.Now().UTC()
	j, e := q.Enqueue(ctx, testInput(), now)
	if e != nil {
		t.Fatal(e)
	}
	sentinel := errors.New("rollback")
	if e = q.Backend.Update(ctx, func(s *State) error { delete(s.Jobs, j.ID); return sentinel }); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	s, e := q.Backend.Read(ctx)
	if e != nil || len(s.Jobs) != 1 {
		t.Fatalf("rollback: %v", e)
	}
	s.Jobs[j.ID] = Job{}
	s, e = q.Backend.Read(ctx)
	if e != nil || s.Jobs[j.ID].ID != j.ID {
		t.Fatal("read alias")
	}
	if e = os.WriteFile(path, []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = q.Backend.Read(ctx); !errors.Is(e, ErrConflict) {
		t.Fatalf("external change: %v", e)
	}
	if _, e = NewFileBackend(path); e == nil {
		t.Fatal("accepted invalid state")
	}
}
func TestInvalidAndCancelledRequests(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := q.Enqueue(ctx, testInput(), now); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	in := testInput()
	in.Payload = json.RawMessage(`invalid`)
	if _, e := q.Enqueue(context.Background(), in, now); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
