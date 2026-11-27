package pluginjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nestedTaskPayload(depth int) json.RawMessage {
	return json.RawMessage(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
}

func TestTaskRejectsEncodingThatCannotReopen(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"malformed key", "invalid-\xff", json.RawMessage(`{}`)},
		{"key grows beyond byte limit", strings.Repeat("\xff", 86), json.RawMessage(`{}`)},
	}
	// A persisted payload has three enclosing objects: state, jobs, and job.
	// These values fit the standalone JSON decoder and payload byte limit but
	// exceed the decoder's nesting limit once inserted into that document.
	for _, depth := range []int{9998, 9999, 10000} {
		payload := nestedTaskPayload(depth)
		if !json.Valid(payload) || len(payload) > MaxPayloadBytes {
			t.Fatalf("bad nesting fixture: depth=%d bytes=%d", depth, len(payload))
		}
		cases = append(cases, struct {
			name    string
			key     string
			payload json.RawMessage
		}{"depth " + strconv.Itoa(depth), "deep", payload})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.json")
			queue := testQueue(t, path)
			now := time.Now().UTC()
			original, err := queue.Enqueue(t.Context(), testInput(), now)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			in := testInput()
			in.Key, in.Payload = test.key, test.payload
			if job, err := queue.Enqueue(t.Context(), in, now); !errors.Is(err, ErrInvalid) || job.ID != "" {
				t.Errorf("unreadable encoding was committed: id=%q err=%v", job.ID, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("rejected encoding changed the existing durable queue", err)
			}
			queue = testQueue(t, path)
			state, err := queue.Backend.Read(t.Context())
			if err != nil || len(state.Jobs) != 1 || state.Jobs[original.ID].ID != original.ID {
				t.Fatal("original job was not preserved", err)
			}
		})
	}
}

func TestTaskEncodingBoundariesSurviveRestartAndCompletion(t *testing.T) {
	for _, test := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"nesting", "depth-boundary", nestedTaskPayload(9997)},
		{"Unicode key", strings.Repeat("\ufffd", 85) + "x", json.RawMessage(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.json")
			queue := testQueue(t, path)
			in := testInput()
			in.Key, in.Payload = test.key, test.payload
			now := time.Now().UTC()
			original, err := queue.Enqueue(t.Context(), in, now)
			if err != nil {
				t.Fatal("readable boundary was rejected", err)
			}
			queue = testQueue(t, path)
			replay, err := queue.Enqueue(t.Context(), in, now)
			if err != nil || replay.ID != original.ID || replay.Key != in.Key || replay.Fingerprint != original.Fingerprint {
				t.Fatal("restart changed identity or payload", err)
			}
			claim, ok, err := queue.Claim(t.Context(), now)
			if err != nil || !ok || !bytes.Equal(claim.Payload, in.Payload) {
				t.Fatal("boundary task cannot execute after restart", err)
			}
			if err := queue.Finish(t.Context(), claim, "", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			queue = testQueue(t, path)
			state, err := queue.Backend.Read(t.Context())
			if err != nil || state.Jobs[original.ID].Status != "succeeded" {
				t.Fatal("completion did not survive restart", err)
			}
		})
	}
}

func TestFanoutRejectsMalformedEventKey(t *testing.T) {
	for _, recipients := range []int{0, 1} {
		t.Run(strconv.Itoa(recipients), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.json")
			queue := testQueue(t, path)
			now := time.Now().UTC()
			event := Event{Key: "original", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
			if _, err := queue.FreezeFanout(t.Context(), event, nil); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var subscriptions []Subscription
			if recipients != 0 {
				subscriptions = []Subscription{{Plugin: "example", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}}
			}
			event.Key = strings.Repeat("\xff", 67)
			if receipt, err := queue.FreezeFanout(t.Context(), event, subscriptions); !errors.Is(err, ErrInvalid) || receipt.Event.Key != "" {
				t.Errorf("invalid event key was frozen: err=%v", err)
			}
			if err := Fanout(t.Context(), queue, event, subscriptions, now); !errors.Is(err, ErrInvalid) {
				t.Errorf("invalid event key was delivered: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("invalid event key altered durable receipts", err)
			}
			queue = testQueue(t, path)
			event.Key = strings.Repeat("\ufffd", 66) + "xx"
			if err := DeliverFrozen(t.Context(), queue, queue, event, subscriptions, now); err != nil {
				t.Fatal("valid Unicode event key was rejected", err)
			}
			queue = testQueue(t, path)
			if err := DeliverFrozen(t.Context(), queue, queue, event, nil, now); err != nil {
				t.Fatal("valid event key changed on restart", err)
			}
			state, err := queue.Backend.Read(t.Context())
			if err != nil || len(state.Fanouts) != 2 || len(state.Jobs) != recipients {
				t.Fatal("fanout replay lost receipts or duplicated tasks", err)
			}
		})
	}
}
