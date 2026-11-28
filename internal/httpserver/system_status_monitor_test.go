package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/systeminfo"
)

func waitStatusPending(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.systemStatus.mu.Lock()
		p := s.systemStatus.pending
		ok := want < 0 && p == nil || want >= 0 && p != nil && p.waiters == want
		s.systemStatus.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("shared status waiter state not reached", want)
}

func TestSystemStatusSharedInitiatorCancellation(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var worker context.Context
	var calls atomic.Int32
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(ctx context.Context) error { calls.Add(1); worker = ctx; close(entered); <-release; return nil }}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan error, 1), make(chan systemStatusView, 1)
	go func() { _, err := s.systemStatusReportChecked(ctx); first <- err }()
	<-entered
	go func() {
		report, err := s.systemStatusReportChecked(t.Context())
		if err != nil {
			t.Error(err)
		}
		second <- report
	}()
	waitStatusPending(t, s, 2)
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initiator cancellation blocked")
	}
	if worker.Err() != nil {
		t.Fatal("initiator cancelled another caller's probe")
	}
	once.Do(func() { close(release) })
	select {
	case report := <-second:
		if report.CollectedAt.IsZero() {
			t.Fatal("remaining caller lost result")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shared result not delivered")
	}
	waitStatusPending(t, s, -1)
	if calls.Load() != 1 {
		t.Fatal("duplicate collection", calls.Load())
	}
}

func TestSystemStatusLastWaiterAndStuckWorkerBound(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ctx.Err()
		}
		return nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.systemStatusReportChecked(ctx); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("last waiter left probe running")
	}
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		report, err := s.systemStatusReportChecked(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !report.CollectedAt.IsZero() {
			t.Fatal("stuck worker wait returned data", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unbounded replacement workers", calls.Load())
	}
	once.Do(func() { close(release) })
	waitStatusPending(t, s, -1)
	s.systemStatus.mu.Lock()
	cached := s.systemStatus.cached
	s.systemStatus.mu.Unlock()
	if !cached.CollectedAt.IsZero() {
		t.Fatal("abandoned worker cached cancelled result")
	}
	report, err := s.systemStatusReportChecked(t.Context())
	if err != nil || report.CollectedAt.IsZero() || calls.Load() != 2 {
		t.Fatal("fresh retry failed", err, calls.Load())
	}
}

func TestSystemStatusSharedSnapshotScopes(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	type privateKey struct{}
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(ctx context.Context) error {
		if ctx.Value(privateKey{}) != nil {
			t.Error("worker retained private request values")
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}}
	snapshot := s.siteSnapshot(t.Context())
	firstSnapshot, secondSnapshot := snapshot, snapshot
	firstSnapshot.Settings.Identity.Timezone = "UTC"
	secondSnapshot.Settings.Identity.Timezone = "America/New_York"
	ctx := context.WithValue(t.Context(), privateKey{}, "private request")
	firstCtx := context.WithValue(ctx, siteRequestKey{}, siteRequestState{snapshot: firstSnapshot, location: site.TimeLocation(firstSnapshot.Settings)})
	secondCtx := context.WithValue(ctx, siteRequestKey{}, siteRequestState{snapshot: secondSnapshot, location: site.TimeLocation(secondSnapshot.Settings)})
	first, second := make(chan systemStatusView, 1), make(chan systemStatusView, 1)
	go func() {
		v, e := s.systemStatusReportChecked(firstCtx)
		if e != nil {
			t.Error(e)
		}
		first <- v
	}()
	<-entered
	go func() {
		v, e := s.systemStatusReportChecked(secondCtx)
		if e != nil {
			t.Error(e)
		}
		second <- v
	}()
	waitStatusPending(t, s, 2)
	once.Do(func() { close(release) })
	a, b := <-first, <-second
	if a.Timezone != "UTC" || b.Timezone != "America/New_York" || calls.Load() != 2 {
		t.Fatal("request snapshot mixed", a.Timezone, b.Timezone, calls.Load())
	}
	if next, e := s.systemStatusReportChecked(secondCtx); e != nil || !next.CollectedAt.Equal(b.CollectedAt) || calls.Load() != 2 {
		t.Fatal("matching request lost cache", e, calls.Load())
	}
	secondSnapshot.Revision += "changed"
	updated := context.WithValue(ctx, siteRequestKey{}, siteRequestState{snapshot: secondSnapshot, location: site.TimeLocation(secondSnapshot.Settings)})
	if _, e := s.systemStatusReportChecked(updated); e != nil || calls.Load() != 3 {
		t.Fatal("cache reused different settings", e, calls.Load())
	}
}

func TestSystemStatusCloneAllMutableFields(t *testing.T) {
	v := 1.0
	n := uint64(100)
	original := systemStatusView{Metrics: []systemStatusMetric{{Label: "metric"}}, Checks: []systemStatusCheck{{LatencyMS: &v}}, Groups: []systemStatusGroup{{Rows: []systemStatusRow{{Value: "row"}}}}, Resources: systeminfo.Snapshot{CPUPercent: &v, UptimeSeconds: &v, Memory: &systeminfo.Memory{TotalBytes: n}, Disk: &systeminfo.Disk{TotalBytes: n}, ProcessRSSBytes: &n}, Process: systemProcessSnapshot{LastGCPauseMS: &v}}
	want, _ := json.Marshal(original)
	copy := cloneSystemStatus(original)
	copy.Metrics[0].Label = "mutated"
	*copy.Checks[0].LatencyMS = 2
	copy.Groups[0].Rows[0].Value = "mutated"
	*copy.Resources.CPUPercent = 2
	*copy.Resources.UptimeSeconds = 2
	copy.Resources.Memory.TotalBytes = 2
	copy.Resources.Disk.TotalBytes = 2
	*copy.Resources.ProcessRSSBytes = 2
	*copy.Process.LastGCPauseMS = 2
	got, _ := json.Marshal(original)
	if string(got) != string(want) {
		t.Fatal("clone shares mutable status fields")
	}
	if !reflect.DeepEqual(cloneSystemStatus(systemStatusView{}), systemStatusView{}) {
		t.Fatal("clone changes unavailable measurements")
	}
}

func TestSystemStatusCacheConcurrentReaders(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	original, err := s.systemStatusReportChecked(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				report, err := s.systemStatusReportChecked(t.Context())
				if err != nil {
					t.Error(err)
					return
				}
				if report.Checks[0].Detail == "mutated" {
					t.Error("shared check")
				}
				report.Checks[0].Detail = "mutated"
				report.Groups[0].Rows[0].Value = "mutated"
				if report.Resources.Memory != nil {
					report.Resources.Memory.TotalBytes = 1
				}
			}
		})
	}
	wg.Wait()
	again, err := s.systemStatusReportChecked(t.Context())
	if err != nil || again.Groups[0].Rows[0].Value != original.Groups[0].Rows[0].Value {
		t.Fatal("concurrent caller corrupted cache", err)
	}
}

func TestSystemStatusWorkerFailureAndRetry(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	themes := s.themes
	s.themes = nil
	report, err := s.systemStatusReportChecked(t.Context())
	if !errors.Is(err, errSystemStatusCollection) || !report.CollectedAt.IsZero() {
		t.Fatal("failed worker returned data", err)
	}
	waitStatusPending(t, s, -1)
	s.themes = themes
	if report, err := s.systemStatusReportChecked(t.Context()); err != nil || report.CollectedAt.IsZero() {
		t.Fatal("failed worker blocked retry", err)
	}
}

func TestSystemStatusProbePanicIsRedacted(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { panic("PRIVATE-PROBE-FAILURE") }}
	report, err := s.systemStatusReportChecked(t.Context())
	if err != nil || report.Checks[0].Status != "error" || report.Status != "error" {
		t.Fatal("panicking probe reported healthy", err)
	}
	encoded, _ := json.Marshal(report)
	if string(encoded) == "" || strings.Contains(string(encoded), "PRIVATE-PROBE-FAILURE") {
		t.Fatal("probe diagnostics leaked")
	}
}
