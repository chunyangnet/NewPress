package pluginjobs

import "encoding/json"

// TaskTransitionReserveBytes leaves room for lease credentials, the longest
// error/status strings, fractional UTC timestamps, and a 19-digit version.
// These mutable fields can grow by less than 256 encoded bytes from any valid
// state. Payloads and identity fields do not grow during task execution.
const TaskTransitionReserveBytes = 256

const fanoutCancellationReserveBytes = len(`,"cancelled":true`)

func admissionBytes(state State, encodedBytes int) int64 {
	required := int64(encodedBytes)
	for _, job := range state.Jobs {
		if job.Status != "succeeded" && job.Status != "cancelled" {
			// Failed tasks can be retried or cancelled after restoring a backup.
			required += TaskTransitionReserveBytes
		}
	}
	for _, receipt := range state.Fanouts {
		if !receipt.Delivered && !receipt.Cancelled {
			required += int64(len(`,"delivered":true`))
		}
		if !receipt.Cancelled {
			required += int64(fanoutCancellationReserveBytes)
		}
	}
	return required
}

func admitsEntries(before, after State) bool {
	for id := range after.Jobs {
		if _, exists := before.Jobs[id]; !exists {
			return true
		}
	}
	for key := range after.Fanouts {
		if _, exists := before.Fanouts[key]; !exists {
			return true
		}
	}
	return false
}

// Active work and durable history have independent budgets in one atomic file.
// Archive moves can free the active budget without sacrificing old identities.
// Never evict history to make an archive operation fit.
func checkStorageSize(state State, admission bool) error {
	active := state
	active.ArchivedJobs, active.ArchivedFanouts = nil, nil
	active.Quarantines = nil
	raw, err := json.Marshal(active)
	if err != nil {
		return err
	}
	required := int64(len(raw))
	if admission {
		required = admissionBytes(active, len(raw))
	}
	if required > MaxStateBytes {
		return ErrCapacity
	}
	history := struct {
		Jobs    map[string]ArchivedJob    `json:"archived_jobs,omitempty"`
		Fanouts map[string]ArchivedFanout `json:"archived_fanouts,omitempty"`
	}{state.ArchivedJobs, state.ArchivedFanouts}
	raw, err = json.Marshal(history)
	if err != nil {
		return err
	}
	required = int64(len(raw))
	for _, f := range state.ArchivedFanouts {
		if !f.Receipt.Cancelled {
			required += int64(fanoutCancellationReserveBytes)
		}
	}
	if required > MaxArchiveBytes {
		return ErrCapacity
	}
	if len(state.Quarantines) > 0 {
		required, err = QuarantineRequiredBytes(state.Quarantines)
		if err != nil {
			return err
		}
		if required > MaxQuarantineBytes {
			return ErrCapacity
		}
	}
	return nil
}
