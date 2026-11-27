package database

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginTaskSerializationPreservesFingerprint(t *testing.T) {
	ctx := context.Background()
	q, err := taskQueue(nil)
	if err != nil {
		t.Fatal(err)
	}
	in := pluginjobs.Input{Plugin: "example", Epoch: strings.Repeat("c", 64), Kind: "task.execute", Key: "key", Payload: json.RawMessage(`{ "z": "中文 <value>", "a": 1 }`)}
	now := time.Now().UTC()
	original, err := q.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeTask(raw)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := taskQueue(&decoded)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restored.Enqueue(ctx, in, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Fingerprint != original.Fingerprint || replay.ID != original.ID {
		t.Fatal("serialization changed identity")
	}
	claim, ok, err := restored.Claim(ctx, now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	raw, err = json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := decodeTask(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !roundtrip.LeaseUntil.Equal(claim.LeaseUntil) || roundtrip.LeaseToken != claim.LeaseToken {
		t.Fatal("serialization lost lease")
	}
}
func TestPluginTaskDecoderRejectsUnknownAndCorruptState(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unexpected":true}`, `null`, `{} {}`, strings.Repeat(" ", 65537)} {
		if _, err := decodeTask([]byte(raw)); err == nil {
			t.Fatal("accepted invalid task")
		}
	}
}
