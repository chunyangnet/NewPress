package pluginjobs

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

const (
	MaxArchivedJobs    = 100000
	MaxArchivedFanouts = 100000
	MaxArchiveBytes    = 64 << 20
	MaxSnapshotBytes   = MaxStateBytes + MaxArchiveBytes + MaxQuarantineBytes
	MaxArchiveBatch    = 1000
)

// ArchivedJob retains the terminal receipt and request fingerprint, but releases
// the private payload, actor, epoch and key. A replay supplies those fields and
// must match both the original ID and fingerprint. It never returns to Jobs.
type ArchivedJob struct {
	Summary
	Fingerprint string             `json:"fingerprint"`
	Review      *ReissueProvenance `json:"review,omitempty"`
}

// ArchivedFanout keeps the entire frozen cohort, including an empty cohort.
// Delivered means enqueue completion; it does not mean source acknowledgement.
type ArchivedFanout struct {
	Receipt    FanoutReceipt `json:"receipt"`
	ArchivedAt time.Time     `json:"archived_at"`
}

type ArchiveQuery struct {
	Plugin  string
	Before  time.Time
	Limit   int
	Tasks   bool
	Fanouts bool
}

type ArchiveResult struct {
	Tasks   int `json:"tasks"`
	Fanouts int `json:"fanouts"`
}

type ArchiveStore interface {
	Archive(context.Context, ArchiveQuery, time.Time) (ArchiveResult, error)
}

// The limit applies independently to each selected class. Fanouts belong to the
// site, so a plugin-scoped operation cannot move a shared fanout receipt.
func NormalizeArchiveQuery(q ArchiveQuery, now time.Time) (ArchiveQuery, error) {
	if now.IsZero() || q.Before.IsZero() || q.Before.After(now) || (!q.Tasks && !q.Fanouts) ||
		(q.Plugin != "" && (!name.MatchString(q.Plugin) || q.Fanouts)) {
		return ArchiveQuery{}, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > MaxArchiveBatch {
		return ArchiveQuery{}, ErrInvalid
	}
	return q, nil
}

func ValidateArchivedJob(j ArchivedJob) error {
	if err := ValidateReview(j.Summary, j.Fingerprint, j.Review); err != nil {
		return err
	}
	if !digest.MatchString(j.ID) || !digest.MatchString(j.Fingerprint) || !name.MatchString(j.Plugin) || !name.MatchString(j.Kind) ||
		(j.Status != "succeeded" && j.Status != "cancelled") || j.Version < 1 || j.Attempts < 0 || j.Attempts > MaxAttempts ||
		j.CreatedAt.IsZero() || j.UpdatedAt.Before(j.CreatedAt) || j.AvailableAt.IsZero() || j.ArchivedAt.IsZero() || j.ArchivedAt.Before(j.UpdatedAt) {
		return ErrInvalid
	}
	if j.ErrorCode != "" && j.ErrorCode != "execution_failed" && j.ErrorCode != "lease_expired" && j.ErrorCode != "authorization_changed" {
		return ErrInvalid
	}
	return nil
}

func ValidateArchivedFanout(f ArchivedFanout) error {
	if err := ValidateFanout(f.Receipt); err != nil {
		return err
	}
	if (!f.Receipt.Delivered && !f.Receipt.Cancelled) || f.ArchivedAt.IsZero() || f.ArchivedAt.Before(f.Receipt.Event.At) {
		return ErrInvalid
	}
	return nil
}

func validateArchives(s State) error {
	if len(s.ArchivedJobs) > MaxArchivedJobs || len(s.ArchivedFanouts) > MaxArchivedFanouts {
		return ErrInvalid
	}
	for id, j := range s.ArchivedJobs {
		if id != j.ID {
			return ErrInvalid
		}
		if _, duplicate := s.Jobs[id]; duplicate {
			return ErrInvalid
		}
		if err := ValidateArchivedJob(j); err != nil {
			return err
		}
	}
	for key, f := range s.ArchivedFanouts {
		if key != f.Receipt.Event.Key {
			return ErrInvalid
		}
		if _, duplicate := s.Fanouts[key]; duplicate {
			return ErrInvalid
		}
		if err := ValidateArchivedFanout(f); err != nil {
			return err
		}
	}
	return nil
}

func (j ArchivedJob) Replay(in Input) (Job, error) {
	if err := ValidateArchivedJob(j); err != nil {
		return Job{}, err
	}
	id, fp, err := identity(in)
	if err != nil {
		return Job{}, err
	}
	if id != j.ID || fp != j.Fingerprint || in.Plugin != j.Plugin || in.Kind != j.Kind {
		return Job{}, ErrConflict
	}
	return Job{ID: j.ID, Plugin: j.Plugin, Epoch: in.Epoch, Kind: j.Kind, Key: in.Key,
		Fingerprint: j.Fingerprint, ActorID: in.ActorID, Payload: append(json.RawMessage(nil), in.Payload...),
		Status: j.Status, Attempts: j.Attempts, Version: j.Version, CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt, AvailableAt: j.AvailableAt, ErrorCode: j.ErrorCode, ArchivedAt: j.ArchivedAt, Review: CloneReview(j.Review)}, nil
}

func ArchiveJob(j Job, now time.Time) (ArchivedJob, error) {
	out := ArchivedJob{Summary: Summarize(j), Fingerprint: j.Fingerprint, Review: CloneReview(j.Review)}
	out.ArchivedAt = now.UTC()
	return out, ValidateArchivedJob(out)
}

func (q *Queue) Archive(ctx context.Context, query ArchiveQuery, now time.Time) (ArchiveResult, error) {
	query, err := NormalizeArchiveQuery(query, now)
	if err != nil {
		return ArchiveResult{}, err
	}
	var out ArchiveResult
	err = q.Backend.Update(ctx, func(s *State) error {
		ids := []string{}
		if query.Tasks {
			for id, j := range s.Jobs {
				if (query.Plugin == "" || j.Plugin == query.Plugin) && (j.Status == "succeeded" || j.Status == "cancelled") && !j.UpdatedAt.After(query.Before) {
					ids = append(ids, id)
				}
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := s.Jobs[ids[i]], s.Jobs[ids[j]]
			if a.UpdatedAt.Equal(b.UpdatedAt) {
				return ids[i] < ids[j]
			}
			return a.UpdatedAt.Before(b.UpdatedAt)
		})
		ids = ids[:min(len(ids), query.Limit)]
		keys := []string{}
		if query.Fanouts {
			for key, f := range s.Fanouts {
				if (f.Delivered || f.Cancelled) && !f.Event.At.After(query.Before) {
					keys = append(keys, key)
				}
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := s.Fanouts[keys[i]].Event, s.Fanouts[keys[j]].Event
			if a.At.Equal(b.At) {
				return keys[i] < keys[j]
			}
			return a.At.Before(b.At)
		})
		keys = keys[:min(len(keys), query.Limit)]
		if len(s.ArchivedJobs)+len(ids) > MaxArchivedJobs || len(s.ArchivedFanouts)+len(keys) > MaxArchivedFanouts {
			return ErrCapacity
		}
		if len(ids) > 0 && s.ArchivedJobs == nil {
			s.ArchivedJobs = map[string]ArchivedJob{}
		}
		if len(keys) > 0 && s.ArchivedFanouts == nil {
			s.ArchivedFanouts = map[string]ArchivedFanout{}
		}
		for _, id := range ids {
			j, err := ArchiveJob(s.Jobs[id], now)
			if err != nil {
				return err
			}
			s.ArchivedJobs[id] = j
			delete(s.Jobs, id)
		}
		for _, key := range keys {
			s.ArchivedFanouts[key] = ArchivedFanout{Receipt: s.Fanouts[key], ArchivedAt: now.UTC()}
			delete(s.Fanouts, key)
		}
		if len(ids)+len(keys) > 0 {
			s.Schema = max(s.Schema, 2)
		}
		out = ArchiveResult{Tasks: len(ids), Fanouts: len(keys)}
		return nil
	})
	if err != nil {
		return ArchiveResult{}, err
	}
	return out, nil
}
