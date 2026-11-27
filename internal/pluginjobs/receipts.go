package pluginjobs

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"
)

const MaxFanouts = 10000

// FanoutReceipt freezes recipients before the first enqueue. Empty recipient
// sets are receipts too: a later installation must not receive an old event.
type FanoutReceipt struct {
	Event         Event          `json:"event"`
	Subscriptions []Subscription `json:"subscriptions"`
	Cancelled     bool           `json:"cancelled,omitempty"`
	Delivered     bool           `json:"delivered,omitempty"`
}

type FanoutStore interface {
	FreezeFanout(context.Context, Event, []Subscription) (FanoutReceipt, error)
}

// FanoutReader lets retries bypass discovery without creating an empty receipt.
// FreezeFanout remains the authoritative arbiter when concurrent readers miss.
type FanoutReader interface {
	FindFanout(context.Context, Event) (FanoutReceipt, bool, error)
}

func (q *Queue) FindFanout(ctx context.Context, event Event) (FanoutReceipt, bool, error) {
	if err := ValidateFanout(FanoutReceipt{Event: event}); err != nil {
		return FanoutReceipt{}, false, err
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return FanoutReceipt{}, false, err
	}
	receipt, found := state.Fanouts[event.Key]
	if !found {
		if archived, exists := state.ArchivedFanouts[event.Key]; exists {
			receipt, found = archived.Receipt, true
		}
	}
	if found && !SameEvent(receipt.Event, event) {
		return FanoutReceipt{}, false, ErrConflict
	}
	if found && !receipt.Cancelled && !receipt.Delivered {
		if err := CheckQuarantineFanout(state.Quarantines, event.Key); err != nil {
			return FanoutReceipt{}, false, err
		}
	}
	receipt.Subscriptions = append([]Subscription{}, receipt.Subscriptions...)
	return receipt, found, nil
}

func ValidateFanout(r FanoutReceipt) error {
	e := r.Event
	if e.Key == "" || len(e.Key) > 200 || !utf8.ValidString(e.Key) || !name.MatchString(e.Kind) || e.ObjectID <= 0 || e.Version <= 0 || e.At.IsZero() || len(r.Subscriptions) > 1000 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range r.Subscriptions {
		if s.Since.IsZero() || seen[s.Plugin] {
			return ErrInvalid
		}
		seen[s.Plugin] = true
		if _, _, err := identity(Input{Plugin: s.Plugin, Epoch: s.Epoch, Kind: "event.deliver.v1", Key: "event:" + e.Key, ActorID: s.ActorID, Payload: json.RawMessage(`{}`)}); err != nil {
			return err
		}
	}
	return nil
}

func SameEvent(a, b Event) bool {
	return a.Key == b.Key && a.Kind == b.Kind && a.ObjectID == b.ObjectID && a.Version == b.Version && a.At.Equal(b.At)
}

func (q *Queue) FreezeFanout(ctx context.Context, event Event, subscriptions []Subscription) (FanoutReceipt, error) {
	candidate := FanoutReceipt{Event: event, Subscriptions: append([]Subscription{}, subscriptions...)}
	if err := ValidateFanout(candidate); err != nil {
		return FanoutReceipt{}, err
	}
	var out FanoutReceipt
	err := q.Backend.Update(ctx, func(s *State) error {
		if old, ok := s.Fanouts[event.Key]; ok {
			if !SameEvent(old.Event, event) {
				return ErrConflict
			}
			if !old.Cancelled && !old.Delivered {
				if err := CheckQuarantineFanout(s.Quarantines, event.Key); err != nil {
					return err
				}
			}
			out = old
			return nil
		}
		if archived, ok := s.ArchivedFanouts[event.Key]; ok {
			if !SameEvent(archived.Receipt.Event, event) {
				return ErrConflict
			}
			out = archived.Receipt
			return nil
		}
		if err := QuarantineAdmission(s.Quarantines, false); err != nil {
			return err
		}
		if len(s.Fanouts) >= MaxFanouts {
			return ErrCapacity
		}
		if s.Fanouts == nil {
			s.Fanouts = map[string]FanoutReceipt{}
		}
		s.Fanouts[event.Key] = candidate
		out = candidate
		return nil
	})
	if err != nil {
		return FanoutReceipt{}, err
	}
	out.Subscriptions = append([]Subscription{}, out.Subscriptions...)
	return out, nil
}

// FanoutCompleter records enqueue completion, never guest execution success.
type FanoutCompleter interface {
	CompleteFanout(context.Context, Event) error
}

func (q *Queue) CompleteFanout(ctx context.Context, event Event) error {
	if err := ValidateFanout(FanoutReceipt{Event: event}); err != nil {
		return err
	}
	return q.Backend.Update(ctx, func(s *State) error {
		r, ok := s.Fanouts[event.Key]
		if !ok {
			if archived, exists := s.ArchivedFanouts[event.Key]; exists && SameEvent(archived.Receipt.Event, event) {
				return nil
			}
		}
		if !ok || !SameEvent(r.Event, event) {
			return ErrConflict
		}
		if !r.Cancelled && !r.Delivered {
			if err := CheckQuarantineFanout(s.Quarantines, event.Key); err != nil {
				return err
			}
		}
		if !r.Cancelled {
			r.Delivered = true
		}
		s.Fanouts[event.Key] = r
		return nil
	})
}

// DeliverFrozen is safe to retry after partial enqueue or a lost source ACK.
// Never acknowledge the source if freezing or any recipient enqueue fails.
func DeliverFrozen(ctx context.Context, receipts FanoutStore, jobs Store, event Event, subscriptions []Subscription, now time.Time) error {
	if receipts == nil || jobs == nil || now.IsZero() {
		return ErrInvalid
	}
	frozen, err := receipts.FreezeFanout(ctx, event, subscriptions)
	if err != nil {
		return err
	}
	if frozen.Cancelled || frozen.Delivered {
		return nil
	}
	if err := Fanout(ctx, jobs, frozen.Event, frozen.Subscriptions, now); err != nil {
		return err
	}
	if completer, ok := receipts.(FanoutCompleter); ok {
		return completer.CompleteFanout(ctx, frozen.Event)
	}
	return nil
}
