package pluginjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskPayloadRejectsJSONExpansionBeforeCommit(t *testing.T) {
	for _, text := range []string{"<", ">", "&", "\u2028", "\u2029"} {
		t.Run(text, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
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
			in.Key = "expanded"
			in.Payload = json.RawMessage(`{"text":"` + strings.Repeat(text, 6000) + `"}`)
			encoded, err := json.Marshal(in.Payload)
			if err != nil || len(in.Payload) > MaxPayloadBytes || len(encoded) <= MaxPayloadBytes {
				t.Fatalf("invalid boundary fixture: input=%d encoded=%d err=%v", len(in.Payload), len(encoded), err)
			}
			if job, err := queue.Enqueue(t.Context(), in, now); !errors.Is(err, ErrInvalid) || job.ID != "" {
				t.Errorf("accepted unreloadable task: id=%q err=%v", job.ID, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Error("rejected payload changed durable task storage", err)
			}
			reopened, err := NewFileBackend(path)
			if err != nil {
				t.Fatalf("task storage cannot reopen after rejected payload: %v", err)
			}
			state, err := reopened.Read(t.Context())
			if err != nil || len(state.Jobs) != 1 || state.Jobs[original.ID].ID != original.ID {
				t.Fatal("original task was lost", err)
			}
		})
	}
}

func TestTaskPayloadDurableLimitRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	queue := testQueue(t, path)
	in := testInput()
	in.Payload = json.RawMessage(`{"text":"` + strings.Repeat("<", (MaxPayloadBytes-11)/6) + strings.Repeat("x", (MaxPayloadBytes-11)%6) + `"}`)
	encoded, err := json.Marshal(in.Payload)
	if err != nil || len(encoded) != MaxPayloadBytes {
		t.Fatalf("bad exact boundary: %d %v", len(encoded), err)
	}
	now := time.Now().UTC()
	original, err := queue.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	queue = testQueue(t, path)
	replay, err := queue.Enqueue(t.Context(), in, now.Add(time.Second))
	if err != nil || replay.ID != original.ID || replay.Fingerprint != original.Fingerprint {
		t.Fatal("restart changed boundary task identity", err)
	}
	claim, ok, err := queue.Claim(t.Context(), now.Add(time.Second))
	if err != nil || !ok || !bytes.Equal(claim.Payload, encoded) {
		t.Fatal("boundary task could not execute after restart", err)
	}
	if err = queue.Finish(t.Context(), claim, "", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	queue = testQueue(t, path)
	state, err := queue.Backend.Read(t.Context())
	if err != nil || state.Jobs[original.ID].Status != "succeeded" {
		t.Fatal("boundary result did not survive restart", err)
	}
}

func TestTaskPayloadRejectsJSONThatRestartCannotRead(t *testing.T) {
	for _, raw := range []string{
		`{"id":1,"id":2}`,
		`{"outer":[{"id":1,"id":2}]}`,
		`{"text":"\ud800"}`,
		`{"text":"\udc00"}`,
		"{\"text\":\"\xff\"}",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			queue := testQueue(t, path)
			in := testInput()
			in.Payload = json.RawMessage(raw)
			if job, err := queue.Enqueue(t.Context(), in, time.Now().UTC()); !errors.Is(err, ErrInvalid) || job.ID != "" {
				t.Errorf("accepted payload rejected by restart decoder: id=%q err=%v", job.ID, err)
			}
			if _, err := NewFileBackend(path); err != nil {
				t.Errorf("rejected task broke restart: %v", err)
			}
		})
	}
}
