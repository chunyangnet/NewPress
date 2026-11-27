// Package pluginjobs owns durable, at-least-once extension work. Payloads are
// private storage data; HTTP handlers must use explicit safe projections.
package pluginjobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxJobs         = 10000
	MaxPluginJobs   = 1000
	MaxStateBytes   = 32 << 20
	MaxPayloadBytes = 32 << 10
	MaxAttempts     = 5
	LeaseDuration   = 30 * time.Second
)

var (
	ErrInvalid  = errors.New("invalid extension task")
	ErrConflict = errors.New("extension task changed")
	ErrCapacity = errors.New("extension task capacity reached")
	name        = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,95}$`)
	digest      = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Job struct {
	ID          string             `json:"id"`
	Plugin      string             `json:"plugin"`
	Epoch       string             `json:"epoch"`
	Kind        string             `json:"kind"`
	Key         string             `json:"key"`
	Fingerprint string             `json:"fingerprint"`
	ActorID     int64              `json:"actor_id"`
	Payload     json.RawMessage    `json:"payload"`
	Status      string             `json:"status"`
	Attempts    int                `json:"attempts"`
	Version     int64              `json:"version"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	AvailableAt time.Time          `json:"available_at"`
	LeaseUntil  time.Time          `json:"lease_until,omitempty"`
	LeaseToken  string             `json:"lease_token,omitempty"`
	ErrorCode   string             `json:"error_code,omitempty"`
	ArchivedAt  time.Time          `json:"archived_at,omitzero"`
	Review      *ReissueProvenance `json:"review,omitempty"`
}
type State struct {
	Schema          int                          `json:"schema"`
	Kind            string                       `json:"kind"`
	Jobs            map[string]Job               `json:"jobs"`
	Fanouts         map[string]FanoutReceipt     `json:"fanouts,omitempty"`
	ArchivedJobs    map[string]ArchivedJob       `json:"archived_jobs,omitempty"`
	ArchivedFanouts map[string]ArchivedFanout    `json:"archived_fanouts,omitempty"`
	Quarantines     map[string]QuarantinedRecord `json:"quarantines,omitempty"`
}
type Backend interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}
type Queue struct{ Backend Backend }
type Input struct {
	Plugin, Epoch, Kind, Key string
	ActorID                  int64
	Payload                  json.RawMessage
}

func InitialState() State {
	return State{Schema: CurrentSchema, Kind: "newpress.pluginjobs", Jobs: map[string]Job{}}
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func identity(in Input) (string, string, error) {
	if !digest.MatchString(in.Epoch) {
		return "", "", ErrInvalid
	}
	fp, err := inputFingerprint(in)
	if err != nil {
		return "", "", err
	}
	key, _ := json.Marshal([]string{in.Plugin, in.Epoch, in.Key})
	return hash(key), fp, nil
}

func inputFingerprint(in Input) (string, error) {
	if !name.MatchString(in.Plugin) || !name.MatchString(in.Kind) || len(in.Key) == 0 || len(in.Key) > 256 || !utf8.ValidString(in.Key) || in.ActorID < 0 || len(in.Payload) > MaxPayloadBytes || packagefs.ValidateUniqueJSON(in.Payload) != nil {
		return "", ErrInvalid
	}
	// RawMessage is compacted and HTML-escaped when a job is persisted. Bound
	// that representation too: restart validation reads these encoded bytes,
	// and PostgreSQL's task decoder also has a fixed per-row size limit.
	payload, err := json.Marshal(in.Payload)
	if err != nil || len(payload) > MaxPayloadBytes {
		return "", ErrInvalid
	}
	// JSON nesting is limited for the whole document. File storage wraps a
	// payload in the state, jobs map and job objects; validate that depth before
	// either backend accepts it. Marshal alone only checks each RawMessage.
	document := append([]byte(`{"jobs":{"task":{"payload":`), payload...)
	document = append(document, '}', '}', '}')
	if !json.Valid(document) {
		return "", ErrInvalid
	}
	body, _ := json.Marshal(struct {
		Kind    string
		Actor   int64
		Payload json.RawMessage
	}{in.Kind, in.ActorID, in.Payload})
	return hash(body), nil
}

func newJob(in Input, id, fp string, now time.Time) Job {
	return Job{ID: id, Plugin: in.Plugin, Epoch: in.Epoch, Kind: in.Kind, Key: in.Key, Fingerprint: fp, ActorID: in.ActorID, Payload: append(json.RawMessage(nil), in.Payload...), Status: "pending", Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), AvailableAt: now.UTC()}
}
func ValidateState(s State) error {
	if s.Schema < 1 || s.Schema > CurrentSchema || s.Kind != "newpress.pluginjobs" || s.Jobs == nil || len(s.Jobs) > MaxJobs {
		return ErrInvalid
	}
	if s.Schema == 1 && (len(s.ArchivedJobs) != 0 || len(s.ArchivedFanouts) != 0) {
		return ErrInvalid
	}
	if err := validateArchives(s); err != nil {
		return err
	}
	if err := validateQuarantines(s); err != nil {
		return err
	}
	if len(s.Fanouts) > MaxFanouts {
		return ErrInvalid
	}
	reissues := map[string]bool{}
	for _, job := range s.ArchivedJobs {
		if job.Review != nil {
			if s.Schema < ReviewSchema || reissues[job.Review.RequestID] {
				return ErrInvalid
			}
			reissues[job.Review.RequestID] = true
		}
	}
	for key, receipt := range s.Fanouts {
		if key != receipt.Event.Key {
			return ErrInvalid
		}
		if err := ValidateFanout(receipt); err != nil {
			return err
		}
	}
	for id, j := range s.Jobs {
		if err := ValidateReview(Summarize(j), j.Fingerprint, j.Review); err != nil {
			return err
		}
		if j.Review != nil {
			if s.Schema < ReviewSchema || reissues[j.Review.RequestID] ||
				j.Review.RequestID != reissueRequestID(j.Plugin, j.Review.SourceID, j.Key) || j.Review.ReviewerID != j.ActorID {
				return ErrInvalid
			}
			reissues[j.Review.RequestID] = true
		}
		want, fp, err := identity(Input{j.Plugin, j.Epoch, j.Kind, j.Key, j.ActorID, j.Payload})
		if err != nil || id != want || j.ID != id || j.Fingerprint != fp || j.Version < 1 || j.Attempts < 0 || j.Attempts > MaxAttempts || j.CreatedAt.IsZero() || j.UpdatedAt.Before(j.CreatedAt) || j.AvailableAt.IsZero() || !j.ArchivedAt.IsZero() {
			return ErrInvalid
		}
		switch j.Status {
		case "pending", "retry", "succeeded", "failed", "cancelled":
			if j.LeaseToken != "" || !j.LeaseUntil.IsZero() {
				return ErrInvalid
			}
		case "running":
			if !digest.MatchString(j.LeaseToken) || !j.LeaseUntil.After(j.UpdatedAt) || j.Attempts < 1 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if j.ErrorCode != "" && j.ErrorCode != "execution_failed" && j.ErrorCode != "lease_expired" && j.ErrorCode != "authorization_changed" {
			return ErrInvalid
		}
	}
	return nil
}
func (q *Queue) Enqueue(ctx context.Context, in Input, now time.Time) (Job, error) {
	id, fp, err := identity(in)
	if err != nil || now.IsZero() {
		return Job{}, ErrInvalid
	}
	var out Job
	err = q.Backend.Update(ctx, func(s *State) error {
		if old, ok := s.Jobs[id]; ok {
			if old.Fingerprint != fp {
				return ErrConflict
			}
			out = old
			return nil
		}
		if old, ok := s.ArchivedJobs[id]; ok {
			var err error
			out, err = old.Replay(in)
			return err
		}
		if err := QuarantineAdmission(s.Quarantines, true); err != nil {
			return err
		}
		pluginCount := 0
		for _, existing := range s.Jobs {
			if existing.Plugin == in.Plugin {
				pluginCount++
			}
		}
		if len(s.Jobs) >= MaxJobs || pluginCount >= MaxPluginJobs {
			return ErrCapacity
		}
		out = newJob(in, id, fp, now)
		s.Jobs[id] = out
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	out.Payload = append(json.RawMessage(nil), out.Payload...)
	return out, nil
}
func (q *Queue) Claim(ctx context.Context, now time.Time) (Job, bool, error) {
	if now.IsZero() {
		return Job{}, false, ErrInvalid
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Job{}, false, err
	}
	var out Job
	found := false
	err := q.Backend.Update(ctx, func(s *State) error {
		blocked, err := QuarantineWorkPolicy(s.Quarantines)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(s.Jobs))
		for id := range s.Jobs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := s.Jobs[ids[i]], s.Jobs[ids[j]]
			if a.AvailableAt.Equal(b.AvailableAt) {
				return a.ID < b.ID
			}
			return a.AvailableAt.Before(b.AvailableAt)
		})
		for _, id := range ids {
			if blocked[id] {
				continue
			}
			j := s.Jobs[id]
			expired := j.Status == "running" && !j.LeaseUntil.After(now)
			if !expired && ((j.Status != "pending" && j.Status != "retry") || j.AvailableAt.After(now)) {
				continue
			}
			if now.Before(j.UpdatedAt) {
				continue
			}
			if j.Attempts >= MaxAttempts {
				j.Status = "failed"
				j.ErrorCode = "lease_expired"
				j.LeaseToken = ""
				j.LeaseUntil = time.Time{}
				j.Version++
				j.UpdatedAt = now.UTC()
				s.Jobs[id] = j
				continue
			}
			j.Status = "running"
			j.Attempts++
			j.Version++
			j.UpdatedAt = now.UTC()
			j.LeaseUntil = now.Add(LeaseDuration).UTC()
			j.LeaseToken = hex.EncodeToString(token[:])
			s.Jobs[id] = j
			out = j
			found = true
			break
		}
		return nil
	})
	if err != nil {
		return Job{}, false, err
	}
	out.Payload = append(json.RawMessage(nil), out.Payload...)
	return out, found, nil
}
func (q *Queue) Finish(ctx context.Context, claim Job, code string, now time.Time) error {
	if code != "" && code != "execution_failed" && code != "authorization_changed" {
		return ErrInvalid
	}
	return q.Backend.Update(ctx, func(s *State) error {
		blocked, err := QuarantineWorkPolicy(s.Quarantines)
		if err != nil || blocked[claim.ID] {
			return ErrConflict
		}
		j, ok := s.Jobs[claim.ID]
		if !ok || j.Status != "running" || j.LeaseToken != claim.LeaseToken || j.Version != claim.Version || !j.LeaseUntil.After(now) || now.Before(j.UpdatedAt) {
			return ErrConflict
		}
		j.Status = "succeeded"
		j.ErrorCode = code
		if code == "authorization_changed" {
			j.Status = "cancelled"
		} else if code != "" {
			j.Status = "failed"
			if j.Attempts < MaxAttempts {
				j.Status = "retry"
				j.AvailableAt = now.Add(time.Second * time.Duration(1<<j.Attempts)).UTC()
			}
		}
		j.LeaseToken = ""
		j.LeaseUntil = time.Time{}
		j.UpdatedAt = now.UTC()
		j.Version++
		s.Jobs[j.ID] = j
		return nil
	})
}

// Change requires an observed version. Retrying preserves identity and payload;
// callers must re-authorize the original principal and epoch before execution.
func (q *Queue) Change(ctx context.Context, plugin, id string, version int64, action string, now time.Time) error {
	if action != "retry" && action != "cancel" {
		return ErrInvalid
	}
	return q.Backend.Update(ctx, func(s *State) error {
		if action == "retry" {
			blocked, err := QuarantineWorkPolicy(s.Quarantines)
			if err != nil || blocked[id] {
				return ErrQuarantined
			}
		}
		j, ok := s.Jobs[id]
		if !ok || j.Plugin != plugin || j.Version != version || now.Before(j.UpdatedAt) {
			return ErrConflict
		}
		if action == "retry" {
			if j.Status != "failed" {
				return ErrConflict
			}
			j.Status = "pending"
			j.Attempts = 0
			j.ErrorCode = ""
			j.AvailableAt = now.UTC()
		} else {
			if j.Status != "pending" && j.Status != "retry" && j.Status != "running" {
				return ErrConflict
			}
			j.Status = "cancelled"
		}
		j.LeaseToken = ""
		j.LeaseUntil = time.Time{}
		j.UpdatedAt = now.UTC()
		j.Version++
		s.Jobs[id] = j
		return nil
	})
}
