package notification

// Mutable records need room to finish after admission. Notice timestamps can
// add fewer than 128 JSON bytes. Preference versions, flags and timestamps can
// grow by fewer than 128 bytes. A mail lease, attempts, status and timestamps
// can grow by fewer than 256 bytes; its delivery and creation time stay fixed.
const (
	noticeTransitionReserveBytes     = 128
	preferenceTransitionReserveBytes = 128
	mailTransitionReserveBytes       = 256
)

func notificationAdmissionBytes(d document, encodedBytes int) int64 {
	required := int64(encodedBytes) + int64(len(d.Notices))*noticeTransitionReserveBytes + int64(len(d.Preferences))*preferenceTransitionReserveBytes
	for _, job := range d.Jobs {
		if job.State == "pending" || job.State == "sending" || job.State == "failed" {
			// Failed mail can be manually retried. Sent, cancelled and uncertain
			// mail will never be claimed again and needs no execution reserve.
			required += mailTransitionReserveBytes
		}
	}
	return required
}

func admitsNotificationEntries(before, after document) bool {
	// Existing operations only append records; clearing an inbox preserves its
	// entries and receipts. Replays and state transitions do not increase counts.
	return len(after.Receipts) > len(before.Receipts) || len(after.Notices) > len(before.Notices) || len(after.Jobs) > len(before.Jobs) || len(after.Preferences) > len(before.Preferences)
}
