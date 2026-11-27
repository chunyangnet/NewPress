package plugindispatch

import "time"

// SuppressForMaintenance retains provable request and attempt facts. Repair is
// never permission to send: pending is cancelled and sending remains uncertain.
// Terminal records are returned unchanged, including their original timestamps.
func SuppressForMaintenance(i Intent, at time.Time) (Intent, error) {
	if Validate(i) != nil || at.IsZero() || at.Before(i.UpdatedAt) {
		return Intent{}, ErrInvalid
	}
	switch i.State {
	case "pending":
		i.State, i.Version = "cancelled", 2
		i.UpdatedAt, i.Outcome = at.UTC(), Outcome{Code: "maintenance"}
	case "sending":
		i.State, i.Version = "uncertain", 3
		i.UpdatedAt, i.ResultAt, i.Outcome = at.UTC(), at.UTC(), Outcome{Code: "maintenance"}
	}
	return i, Validate(i)
}
