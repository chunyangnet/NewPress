package pluginjobs

import (
	"context"
	"time"
)

// ValidateClaim compares private claim credentials, never a caller's task ID
// alone. It is a read check, not a transactional fence for external effects.
func ValidateClaim(current, claim Job, now time.Time) error {
	id, fingerprint, err := identity(Input{claim.Plugin, claim.Epoch, claim.Kind, claim.Key, claim.ActorID, claim.Payload})
	if err != nil || id != claim.ID || fingerprint != claim.Fingerprint || current.ID != claim.ID || current.Plugin != claim.Plugin || current.Epoch != claim.Epoch || current.ActorID != claim.ActorID || current.Kind != claim.Kind || current.Key != claim.Key || current.Fingerprint != claim.Fingerprint || current.Status != "running" || current.LeaseToken == "" || current.LeaseToken != claim.LeaseToken || current.Version != claim.Version || !current.LeaseUntil.Equal(claim.LeaseUntil) || !current.LeaseUntil.After(now) || now.Before(current.UpdatedAt) {
		return ErrAuthorization
	}
	return nil
}

func (q *Queue) CheckClaim(ctx context.Context, claim Job, now time.Time) error {
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return err
	}
	blocked, err := QuarantineWorkPolicy(state.Quarantines)
	if err != nil || blocked[claim.ID] {
		return ErrAuthorization
	}
	current, ok := state.Jobs[claim.ID]
	if !ok {
		return ErrAuthorization
	}
	return ValidateClaim(current, claim, now)
}
