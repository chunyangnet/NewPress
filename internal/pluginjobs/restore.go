package pluginjobs

import (
	"context"
	"time"
)

// CancelRestored prevents active work from resuming as a side effect of a file
// backup restore. Terminal receipts remain intact: replaying an old enqueue key
// returns its cancelled receipt, never a fresh side effect. Administrators must
// deliberately submit new work with a new key after reviewing the restored site.
func (q *Queue) CancelRestored(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	return q.Backend.Update(ctx, func(state *State) error {
		for key, archived := range state.ArchivedFanouts {
			archived.Receipt.Cancelled = true
			state.ArchivedFanouts[key] = archived
		}
		for key, receipt := range state.Fanouts {
			receipt.Cancelled = true
			state.Fanouts[key] = receipt
		}
		for id, job := range state.Jobs {
			switch job.Status {
			case "pending", "running", "retry", "failed":
				job.Status = "cancelled"
				job.ErrorCode = "authorization_changed"
				job.Version++
				if !now.Before(job.UpdatedAt) {
					job.UpdatedAt = now.UTC()
				}
				job.LeaseToken = ""
				job.LeaseUntil = time.Time{}
				state.Jobs[id] = job
			}
		}
		return nil
	})
}
