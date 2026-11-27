package pluginjobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// Subscription is captured by the host from an enabled installation, never
// supplied by an event payload. Since is the beginning of its current epoch.
type Subscription struct {
	Plugin  string
	Epoch   string
	Since   time.Time
	ActorID int64
}

// Event contains only identifiers and a version, not content or user data.
// Key is the stable source-outbox key; At must be the original commit time.
type Event struct {
	Key      string
	Kind     string
	ObjectID int64
	Version  int64
	At       time.Time
}

// Fanout only durably enqueues. The caller may acknowledge its source event
// after success; plugin execution is independent. Partial failure is replayed
// against the same subscriptions and stable keys, not treated as a rollback.
// The caller must freeze recipients durably before calling this function so
// enabling a plugin between retries cannot subscribe it to an old event.
func Fanout(ctx context.Context, store Store, event Event, subscriptions []Subscription, now time.Time) error {
	if store == nil || event.Key == "" || len(event.Key) > 200 || !utf8.ValidString(event.Key) || !name.MatchString(event.Kind) || event.ObjectID <= 0 || event.Version <= 0 || event.At.IsZero() || now.IsZero() {
		return ErrInvalid
	}
	payload, err := json.Marshal(struct {
		Schema   int    `json:"schema"`
		Kind     string `json:"kind"`
		ObjectID int64  `json:"object_id"`
		Version  int64  `json:"version"`
	}{1, event.Kind, event.ObjectID, event.Version})
	if err != nil {
		return err
	}
	// Validate the complete recipient set before admitting any work.
	for _, s := range subscriptions {
		if s.Since.IsZero() {
			return ErrInvalid
		}
		if _, _, err = identity(Input{Plugin: s.Plugin, Epoch: s.Epoch, Kind: "event.deliver.v1", Key: "event:" + event.Key, ActorID: s.ActorID, Payload: payload}); err != nil {
			return err
		}
	}
	var failures []error
	for _, s := range subscriptions {
		if err = ctx.Err(); err != nil {
			return err
		}
		if event.At.Before(s.Since) {
			continue
		}
		_, err = store.Enqueue(ctx, Input{Plugin: s.Plugin, Epoch: s.Epoch, Kind: "event.deliver.v1", Key: "event:" + event.Key, ActorID: s.ActorID, Payload: payload}, now)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
