package notification

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStoredLegacyJobKeyRequiresExactReceiptAndScope(t *testing.T) {
	receipt := strings.Repeat("r", 160)
	receipts := map[string]time.Time{receipt: time.Now().UTC()}
	job := Job{Delivery: Delivery{UserID: 2, Kind: Reply}}
	key := fmt.Sprintf("%s:%s:%d", receipt, job.Kind, job.UserID)
	if !validStoredJobKey(key, job, receipts) {
		t.Fatal("valid legacy key rejected")
	}
	for _, invalid := range []string{"", key + "x", receipt + ":mention:2", receipt + ":reply:3", "x" + key} {
		if validStoredJobKey(invalid, job, receipts) {
			t.Fatalf("invalid key accepted: %q", invalid)
		}
	}
	if validStoredJobKey(key, job, nil) {
		t.Fatal("key without durable receipt accepted")
	}
	job.Kind = Weekly
	if validStoredJobKey(receipt+":weekly:2", job, receipts) {
		t.Fatal("weekly key accepted as legacy event")
	}
}
