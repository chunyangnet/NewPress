package notification

import (
	"fmt"
	"strings"
	"time"
)

// Accept only the exact oversized identity emitted before JobKey introduced
// hashing. Preserve it for provider deduplication; never rename an existing job.
func validStoredJobKey(key string, job Job, receipts map[string]time.Time) bool {
	if key == "" {
		return false
	}
	if len(key) <= 160 {
		return true
	}
	suffix := fmt.Sprintf(":%s:%d", job.Kind, job.UserID)
	if !strings.HasSuffix(key, suffix) {
		return false
	}
	receipt := strings.TrimSuffix(key, suffix)
	if !validReceipt(receipt) {
		return false
	}
	_, exists := receipts[receipt]
	return exists && job.Kind != Weekly
}
