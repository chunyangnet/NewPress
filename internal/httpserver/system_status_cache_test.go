package httpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSystemStatusCacheResultIsolation(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	first := s.systemStatusReport(t.Context())
	want, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	first.Metrics[0].Label = "mutated metric"
	first.Groups[0].Rows[0].Value = "mutated host"
	first.Checks[0].Detail = "mutated check"
	if first.Checks[0].LatencyMS != nil {
		*first.Checks[0].LatencyMS = 9999
	}
	if first.Resources.Memory != nil {
		first.Resources.Memory.TotalBytes = 1
	}
	if first.Resources.Disk != nil {
		first.Resources.Disk.TotalBytes = 1
	}
	if first.Resources.ProcessRSSBytes != nil {
		*first.Resources.ProcessRSSBytes = 1
	}
	if first.Resources.UptimeSeconds != nil {
		*first.Resources.UptimeSeconds = 1
	}
	if first.Process.LastGCPauseMS != nil {
		*first.Process.LastGCPauseMS = 9999
	}
	next := s.systemStatusReport(t.Context())
	got, _ := json.Marshal(next)
	if string(want) != string(got) {
		t.Fatal("caller mutated shared status snapshot")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if report := s.systemStatusReport(ctx); !report.CollectedAt.IsZero() || len(report.Groups) > 0 {
		t.Fatal("cancelled caller read cached host details")
	}
}

func TestSystemStatusCanceledWaiter(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	entered, release, firstDone, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.repository = &statusAuthorityRepository{Repository: s.repository, ready: func(context.Context) error { close(entered); <-release; return nil }}
	defer func() { close(release); <-firstDone; <-secondDone }()
	go func() { defer close(firstDone); s.systemStatusReport(t.Context()) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	go func() { defer close(secondDone); s.systemStatusReport(ctx) }()
	cancel()
	select {
	case <-secondDone:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelled waiter blocked on shared report")
	}
}
