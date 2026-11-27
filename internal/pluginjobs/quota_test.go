package pluginjobs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPluginQuotaPreservesReplayAndOtherPlugins(t *testing.T) {
	b, err := NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{Backend: b}
	now := time.Now().UTC()
	in := Input{Plugin: "quota", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "0", Payload: json.RawMessage(`{}`)}
	// Seed validated receipts in one transaction rather than quadratic test I/O.
	err = b.Update(t.Context(), func(s *State) error {
		for i := range MaxPluginJobs {
			item := in
			item.Key = fmt.Sprint(i)
			id, fp, e := identity(item)
			if e != nil {
				return e
			}
			s.Jobs[id] = Job{ID: id, Plugin: item.Plugin, Epoch: item.Epoch, Kind: item.Kind, Key: item.Key, Fingerprint: fp, Payload: item.Payload, Status: "pending", Version: 1, CreatedAt: now, UpdatedAt: now, AvailableAt: now}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Enqueue(t.Context(), in, now); err != nil {
		t.Fatal("replay blocked at quota", err)
	}
	in.Key = "new"
	if _, err = q.Enqueue(t.Context(), in, now); err != ErrCapacity {
		t.Fatalf("quota=%v", err)
	}
	in.Plugin = "other"
	if _, err = q.Enqueue(t.Context(), in, now); err != nil {
		t.Fatal("other plugin blocked", err)
	}
}
