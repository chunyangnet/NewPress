package pluginjobs

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CurrentSchema            = 4
	ReviewSchema             = 3
	MaxReviewReasonBytes     = 512
	maxReviewReasonJSONBytes = 1024
)

// ReissueProvenance is private durable review evidence. Its request identity is
// independent of the current epoch, so an uncertain request can be confirmed
// after configuration changes without admitting another task.
type ReissueProvenance struct {
	RequestID          string    `json:"request_id"`
	RequestFingerprint string    `json:"request_fingerprint"`
	SourceID           string    `json:"source_id"`
	SourceVersion      int64     `json:"source_version"`
	SourceStatus       string    `json:"source_status"`
	SourceFingerprint  string    `json:"source_fingerprint"`
	ReviewerID         int64     `json:"reviewer_id"`
	Reason             string    `json:"reason"`
	ReviewedAt         time.Time `json:"reviewed_at"`
}

type ReissueSummary struct {
	SourceID      string    `json:"source_id"`
	SourceVersion int64     `json:"source_version"`
	SourceStatus  string    `json:"source_status"`
	ReviewedAt    time.Time `json:"reviewed_at"`
}

type ReviewAudit struct {
	ReissueSummary
	ReviewerID int64  `json:"reviewer_id"`
	Reason     string `json:"reason"`
}

type ReviewDetails struct {
	Task       Summary      `json:"task"`
	Reissuable bool         `json:"reissuable"`
	Provenance *ReviewAudit `json:"provenance,omitempty"`
}

type ReissueRequest struct {
	SourceID      string
	SourceVersion int64
	Input         Input
	Reason        string
	// AllowNew comes from the current host authorization check, not client JSON.
	// A matching existing receipt may still be returned when new work is denied.
	AllowNew bool
}

type ReissueIdentity struct{ ID, Fingerprint string }

type ReviewStore interface {
	GetReview(context.Context, string, string) (ReviewDetails, error)
	Reissue(context.Context, ReissueRequest, time.Time) (Summary, error)
}

func (s Summary) CanReissue() bool { return s.Status == "failed" || s.Status == "cancelled" }

func CloneReview(r *ReissueProvenance) *ReissueProvenance {
	if r == nil {
		return nil
	}
	out := *r
	return &out
}

func ReviewSummary(r *ReissueProvenance) *ReissueSummary {
	if r == nil {
		return nil
	}
	return &ReissueSummary{SourceID: r.SourceID, SourceVersion: r.SourceVersion, SourceStatus: r.SourceStatus, ReviewedAt: r.ReviewedAt}
}

func NewReviewDetails(task Summary, review *ReissueProvenance) ReviewDetails {
	out := ReviewDetails{Task: task, Reissuable: task.CanReissue()}
	if review != nil {
		out.Provenance = &ReviewAudit{ReissueSummary: *ReviewSummary(review), ReviewerID: review.ReviewerID, Reason: review.Reason}
	}
	return out
}

func reviewReason(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || len(text) > MaxReviewReasonBytes {
		return "", ErrInvalid
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", ErrInvalid
		}
	}
	raw, err := json.Marshal(text)
	if err != nil || len(raw) > maxReviewReasonJSONBytes {
		return "", ErrInvalid
	}
	return text, nil
}

func reissueRequestID(plugin, source, key string) string {
	raw, _ := json.Marshal([]string{plugin, source, key})
	return hash(raw)
}

func reissueRequestFingerprint(version, reviewer int64, reason, inputFingerprint string) string {
	raw, _ := json.Marshal(struct {
		Version, Reviewer        int64
		Reason, InputFingerprint string
	}{version, reviewer, reason, inputFingerprint})
	return hash(raw)
}

func PrepareReissue(request ReissueRequest) (ReissueRequest, ReissueIdentity, error) {
	if !digest.MatchString(request.SourceID) || request.SourceVersion < 1 || request.Input.ActorID < 1 {
		return ReissueRequest{}, ReissueIdentity{}, ErrInvalid
	}
	reason, err := reviewReason(request.Reason)
	if err != nil {
		return ReissueRequest{}, ReissueIdentity{}, err
	}
	fp, err := inputFingerprint(request.Input)
	if err != nil {
		return ReissueRequest{}, ReissueIdentity{}, err
	}
	request.Reason = reason
	return request, ReissueIdentity{
		ID:          reissueRequestID(request.Input.Plugin, request.SourceID, request.Input.Key),
		Fingerprint: reissueRequestFingerprint(request.SourceVersion, request.Input.ActorID, reason, fp),
	}, nil
}

func ValidateReview(summary Summary, fingerprint string, review *ReissueProvenance) error {
	if review == nil {
		if summary.Reissue != nil {
			return ErrInvalid
		}
		return nil
	}
	reason, err := reviewReason(review.Reason)
	if err != nil || reason != review.Reason || !digest.MatchString(review.RequestID) ||
		!digest.MatchString(review.RequestFingerprint) || !digest.MatchString(review.SourceID) ||
		!digest.MatchString(review.SourceFingerprint) || review.SourceID == summary.ID ||
		review.SourceVersion < 1 || (review.SourceStatus != "failed" && review.SourceStatus != "cancelled") ||
		review.ReviewerID < 1 || !review.ReviewedAt.Equal(summary.CreatedAt) ||
		review.RequestFingerprint != reissueRequestFingerprint(review.SourceVersion, review.ReviewerID, review.Reason, fingerprint) {
		return ErrInvalid
	}
	expected := ReviewSummary(review)
	if summary.Reissue == nil || *summary.Reissue != *expected {
		return ErrInvalid
	}
	return nil
}

func MatchReissue(review *ReissueProvenance, request ReissueRequest, identity ReissueIdentity) error {
	if review == nil || review.RequestID != identity.ID || review.RequestFingerprint != identity.Fingerprint ||
		review.SourceID != request.SourceID || review.SourceVersion != request.SourceVersion ||
		review.ReviewerID != request.Input.ActorID || review.Reason != request.Reason {
		return ErrConflict
	}
	return nil
}

// BuildReissuedJob checks the source snapshot and constructs a distinct task.
// Source state itself is never changed by review or reissue.
func BuildReissuedJob(request ReissueRequest, source Summary, sourceFingerprint string, now time.Time) (Job, error) {
	request, receipt, err := PrepareReissue(request)
	if err != nil {
		return Job{}, err
	}
	if !request.AllowNew {
		return Job{}, ErrAuthorization
	}
	if now.IsZero() || !digest.MatchString(sourceFingerprint) || source.ID != request.SourceID ||
		source.Plugin != request.Input.Plugin || source.Kind != request.Input.Kind ||
		source.Version != request.SourceVersion || !source.CanReissue() || now.Before(source.UpdatedAt) {
		return Job{}, ErrConflict
	}
	id, fp, err := identity(request.Input)
	if err != nil {
		return Job{}, err
	}
	if id == source.ID {
		return Job{}, ErrConflict
	}
	job := newJob(request.Input, id, fp, now)
	job.Review = &ReissueProvenance{
		RequestID: receipt.ID, RequestFingerprint: receipt.Fingerprint, SourceID: source.ID,
		SourceVersion: source.Version, SourceStatus: source.Status, SourceFingerprint: sourceFingerprint,
		ReviewerID: request.Input.ActorID, Reason: request.Reason, ReviewedAt: now.UTC(),
	}
	return job, ValidateReview(Summarize(job), job.Fingerprint, job.Review)
}

func (q *Queue) GetReview(ctx context.Context, plugin, id string) (ReviewDetails, error) {
	if !name.MatchString(plugin) || !digest.MatchString(id) {
		return ReviewDetails{}, ErrInvalid
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return ReviewDetails{}, err
	}
	if task, ok := state.Jobs[id]; ok && task.Plugin == plugin {
		return NewReviewDetails(Summarize(task), task.Review), nil
	}
	if task, ok := state.ArchivedJobs[id]; ok && task.Plugin == plugin {
		return NewReviewDetails(task.Summary, task.Review), nil
	}
	return ReviewDetails{}, ErrConflict
}

func (q *Queue) Reissue(ctx context.Context, request ReissueRequest, now time.Time) (Summary, error) {
	request, receipt, err := PrepareReissue(request)
	if err != nil || now.IsZero() {
		if err == nil {
			err = ErrInvalid
		}
		return Summary{}, err
	}
	var out Summary
	err = q.Backend.Update(ctx, func(state *State) error {
		for _, task := range state.Jobs {
			if task.Review != nil && task.Review.RequestID == receipt.ID {
				if err := MatchReissue(task.Review, request, receipt); err != nil {
					return err
				}
				out = Summarize(task)
				return nil
			}
		}
		for _, task := range state.ArchivedJobs {
			if task.Review != nil && task.Review.RequestID == receipt.ID {
				if err := MatchReissue(task.Review, request, receipt); err != nil {
					return err
				}
				out = task.Summary
				return nil
			}
		}
		if !request.AllowNew {
			return ErrAuthorization
		}
		if err := QuarantineAdmission(state.Quarantines, true); err != nil {
			return err
		}
		var source Summary
		var sourceFingerprint string
		if task, ok := state.Jobs[request.SourceID]; ok {
			source, sourceFingerprint = Summarize(task), task.Fingerprint
		} else if task, ok := state.ArchivedJobs[request.SourceID]; ok {
			source, sourceFingerprint = task.Summary, task.Fingerprint
		} else {
			return ErrConflict
		}
		job, err := BuildReissuedJob(request, source, sourceFingerprint, now)
		if err != nil {
			return err
		}
		if _, exists := state.Jobs[job.ID]; exists {
			return ErrConflict
		}
		if _, exists := state.ArchivedJobs[job.ID]; exists {
			return ErrConflict
		}
		count := 0
		for _, existing := range state.Jobs {
			if existing.Plugin == job.Plugin {
				count++
			}
		}
		if len(state.Jobs) >= MaxJobs || count >= MaxPluginJobs {
			return ErrCapacity
		}
		state.Jobs[job.ID] = job
		state.Schema = CurrentSchema
		out = Summarize(job)
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	return out, nil
}
