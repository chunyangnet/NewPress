package httpserver

import (
	"context"
	"errors"
	"slices"
	"time"

	"newpress.local/newpress/internal/site"
)

const systemStatusCollectionTimeout = 5 * time.Second

var errSystemStatusCollection = errors.New("system status collection failed")

type systemStatusCacheKey struct{ revision, timezone string }
type systemStatusPending struct {
	key     systemStatusCacheKey
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	report  systemStatusView
	err     error
}

// One worker owns native collection and readiness probes. Caller cancellation
// never waits for a probe to obey its context, and never cancels other callers.
func (s *Server) systemStatusReportChecked(parent context.Context) (systemStatusView, error) {
	ctx, cancel := context.WithTimeout(parent, systemStatusCollectionTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return systemStatusView{}, err
	}
	snapshot := s.siteSnapshot(ctx)
	location := site.TimeLocation(snapshot.Settings)
	key := systemStatusCacheKey{snapshot.Revision, location.String()}
	m := &s.systemStatus
	for {
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return systemStatusView{}, err
		}
		if m.cachedKey == key && !m.cached.CollectedAt.IsZero() && time.Since(m.cached.CollectedAt) < 2*time.Second {
			report := m.cached
			m.mu.Unlock()
			report = cloneSystemStatus(report)
			if err := ctx.Err(); err != nil {
				return systemStatusView{}, err
			}
			return report, nil
		}
		pending := m.pending
		if pending == nil {
			// Keep only the immutable site snapshot, not the initiating session,
			// request values or cancellation. Every waiter checks its own access.
			state := siteRequestState{snapshot: snapshot, location: location}
			worker, stop := context.WithTimeout(context.WithValue(context.Background(), siteRequestKey{}, state), systemStatusCollectionTimeout)
			pending = &systemStatusPending{key: key, ctx: worker, cancel: stop, done: make(chan struct{})}
			m.pending = pending
			go s.collectSharedSystemStatus(pending)
		}
		pending.waiters++
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-pending.done:
		}
		m.mu.Lock()
		pending.waiters--
		if err := ctx.Err(); err != nil {
			if pending.waiters == 0 {
				pending.cancel()
			}
			m.mu.Unlock()
			return systemStatusView{}, err
		}
		report, err := pending.report, pending.err
		m.mu.Unlock()
		if pending.key != key || errors.Is(err, context.Canceled) {
			continue
		}
		if err != nil {
			return systemStatusView{}, err
		}
		report = cloneSystemStatus(report)
		if err := ctx.Err(); err != nil {
			return systemStatusView{}, err
		}
		return report, nil
	}
}

func (s *Server) collectSharedSystemStatus(pending *systemStatusPending) {
	defer pending.cancel()
	var report systemStatusView
	defer func() {
		// Collection previously ran within the HTTP recovery boundary. Keep an
		// unexpected collector failure from stranding waiters or the worker slot.
		failed := recover() != nil
		m := &s.systemStatus
		m.mu.Lock()
		defer m.mu.Unlock()
		pending.err = pending.ctx.Err()
		if failed && pending.err == nil {
			pending.err = errSystemStatusCollection
		}
		if pending.err == nil {
			pending.report = report
			m.cached, m.cachedKey = report, pending.key
		}
		// Retain the occupied slot until the worker truly exits, even when all
		// waiters leave. A stuck native probe cannot cause unbounded replacements.
		m.pending = nil
		close(pending.done)
	}()
	report = s.collectSystemStatus(pending.ctx)
}

func statusCopyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneSystemStatus(value systemStatusView) systemStatusView {
	value.Metrics = slices.Clone(value.Metrics)
	value.Checks = slices.Clone(value.Checks)
	for i := range value.Checks {
		value.Checks[i].LatencyMS = statusCopyPointer(value.Checks[i].LatencyMS)
	}
	value.Groups = slices.Clone(value.Groups)
	for i := range value.Groups {
		value.Groups[i].Rows = slices.Clone(value.Groups[i].Rows)
	}
	value.Resources.CPUPercent = statusCopyPointer(value.Resources.CPUPercent)
	value.Resources.UptimeSeconds = statusCopyPointer(value.Resources.UptimeSeconds)
	value.Resources.Memory = statusCopyPointer(value.Resources.Memory)
	value.Resources.Disk = statusCopyPointer(value.Resources.Disk)
	value.Resources.ProcessRSSBytes = statusCopyPointer(value.Resources.ProcessRSSBytes)
	value.Process.LastGCPauseMS = statusCopyPointer(value.Process.LastGCPauseMS)
	return value
}
