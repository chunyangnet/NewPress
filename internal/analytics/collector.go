package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Collector uses a bounded nonblocking queue. A crash can lose unflushed
// observations; committed batches are idempotent across retry and restart.
type Collector struct {
	// Calendar configuration is independent of the flush lock. Callbacks are
	// always invoked without either lock held.
	locationMu       sync.RWMutex
	locationFunc     func() *time.Location
	store            Store
	queue            chan Event
	writer           string
	mu               sync.Mutex
	sequence         int64
	inflight         *Batch
	inflightLocation *time.Location
	dropped          atomic.Int64
	lastMaintenance  string
}

func NewCollector(store Store) *Collector {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return &Collector{store: store, queue: make(chan Event, 2048), writer: hex.EncodeToString(id[:])}
}

// SetLocationFunc binds the collector and stores supporting the same setter to
// the configured site IANA timezone. Already assembled batches keep their zone.
func (c *Collector) SetLocationFunc(location func() *time.Location) {
	if c == nil {
		return
	}
	c.locationMu.Lock()
	c.locationFunc = location
	c.locationMu.Unlock()
	if store, ok := c.store.(interface{ SetLocationFunc(func() *time.Location) }); ok {
		// Bind a stable accessor, so concurrent replacements cannot leave the
		// store using an older callback. Never call into the store under a lock.
		store.SetLocationFunc(c.zone)
	}
}

func (c *Collector) zone() *time.Location {
	c.locationMu.RLock()
	fn := c.locationFunc
	c.locationMu.RUnlock()
	if fn != nil {
		if location := fn(); location != nil {
			return location
		}
	}
	return time.UTC
}

func (c *Collector) Record(event Event) {
	if c == nil {
		return
	}
	select {
	case c.queue <- event:
	default:
		c.dropped.Add(1)
	}
}
func (c *Collector) Dropped() int64 { return c.dropped.Load() }
func (c *Collector) Flush(ctx context.Context, now time.Time) error {
	location := operationLocation(ctx, c.zone)
	ctx = WithLocation(ctx, location)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight != nil {
		if err := c.store.Apply(WithLocation(ctx, c.inflightLocation), *c.inflight, now); err != nil {
			return err
		}
		c.inflight = nil
		c.inflightLocation = nil
	}
	days := map[string]Day{}
	// Drain only a bounded snapshot; continuous traffic cannot starve shutdown.
	n := len(c.queue)
	for range n {
		event := <-c.queue
		if DateIn(event.At, location) < cutoffIn(now, location) || DateIn(event.At, location) > DateIn(now, location) {
			c.dropped.Add(1)
			continue
		}
		AddEventIn(days, event, location)
	}
	// An empty daily heartbeat still expires old persisted data.
	if len(days) == 0 {
		if c.lastMaintenance == DateIn(now, location) {
			return nil
		}
		state, err := c.store.Snapshot(ctx, now)
		if err != nil {
			return err
		}
		if DateIn(state.UpdatedAt, location) == DateIn(now, location) {
			c.lastMaintenance = DateIn(now, location)
			return nil
		}
	}
	c.sequence++
	batch := Batch{Writer: c.writer, Sequence: c.sequence, Days: days}
	c.inflight = &batch
	c.inflightLocation = location
	if err := c.store.Apply(ctx, batch, now); err != nil {
		return err
	}
	c.inflight = nil
	c.inflightLocation = nil
	c.lastMaintenance = DateIn(now, location)
	return nil
}
func (c *Collector) Snapshot(ctx context.Context, now time.Time) (State, error) {
	ctx = WithLocation(ctx, operationLocation(ctx, c.zone))
	if err := c.Flush(ctx, now); err != nil {
		return State{}, err
	}
	return c.store.Snapshot(ctx, now)
}
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := c.Flush(writeCtx, now)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("analytics aggregates could not be saved", "error", err)
			}
		}
	}
}
