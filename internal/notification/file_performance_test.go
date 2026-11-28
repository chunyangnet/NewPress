package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func notificationFileBenchmark(b *testing.B, receipts int) (*FileStore, int64) {
	b.Helper()
	d := emptyDocument()
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	p := Defaults(2)
	p.Version, p.UpdatedAt, p.UnsubscribeKey = 1, at, strings.Repeat("a", 64)
	d.Preferences[2] = p
	for i := range receipts {
		key := fmt.Sprintf("event-%08d:%s", i, strings.Repeat("x", 140))
		d.Receipts[key] = at
	}
	if err := validateDocument(d); err != nil {
		b.Fatal(err)
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) >= maxFileBytes {
		b.Fatal("invalid benchmark document", len(raw), err)
	}
	path := filepath.Join(b.TempDir(), "notifications.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		b.Fatal(err)
	}
	s, err := NewFileStore(path)
	if err != nil {
		b.Fatal(err)
	}
	return s, int64(len(raw))
}

func BenchmarkNotificationFileOperations(b *testing.B) {
	for _, receipts := range []int{1000, 100000} {
		for _, operation := range []string{"ready", "idle-claim", "preferences-update"} {
			b.Run(fmt.Sprintf("%d/%s", receipts, operation), func(b *testing.B) {
				s, size := notificationFileBenchmark(b, receipts)
				ctx := context.Background()
				now := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
				p, err := s.Preferences(ctx, 2)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(size)
				b.ResetTimer()
				for b.Loop() {
					switch operation {
					case "ready":
						if err := s.Ready(ctx); err != nil {
							b.Fatal(err)
						}
					case "idle-claim":
						if _, found, err := s.ClaimMail(ctx, now); err != nil || found {
							b.Fatal(found, err)
						}
					case "preferences-update":
						p.Reply = !p.Reply
						p, err = s.SavePreferences(ctx, p, now)
						if err != nil {
							b.Fatal(err)
						}
					}
				}
				b.ReportMetric(float64(size), "file-bytes")
			})
		}
	}
}
