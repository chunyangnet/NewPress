package content

import (
	"context"
	"errors"
	"strings"
	"time"

	"newpress.local/newpress/internal/idempotency"
)

const MaxSubmissionReceipts = 131072
const SubmissionReplayWindow = 7 * 24 * time.Hour

var ErrSubmissionResultUnavailable = errors.New("committed editorial resource unavailable")

// Submission is an already-authorized editorial operation. Key and Fingerprint
// are SHA-256 digests, bound by the HTTP layer to the original identity, route
// and request (excluding renewable CSRF credentials).
type Submission struct {
	SnippetDraft     *SnippetDraftGuard
	Key, Fingerprint string
	Action           string
	PostID, Version  int64
	RevisionID       int64
	Actor            string
	Input            PostInput
}

// MutationAction preserves the requested receipt action when an editor publishes a full draft.
func (request Submission) MutationAction() string {
	if request.Action == "publish" && request.Input.Status == StatusPublish {
		return "save"
	}
	return request.Action
}

type SubmissionReceipt struct {
	Deleted     bool      `json:"deleted,omitempty"`
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	PostID      int64     `json:"post_id"`
	Version     int64     `json:"version"`
	Action      string    `json:"action"`
	CreatedAt   time.Time `json:"created_at"`
}

type SubmissionResult struct {
	Receipt  SubmissionReceipt
	Replayed bool
}

func validSubmissionDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func ValidSubmissionAction(action string) bool {
	return action == "create" || action == "save" || action == "trash" || action == "restore" || action == "publish" || action == "restore_revision"
}

func ValidateSubmission(request Submission) error {
	if g := request.SnippetDraft; g != nil {
		expected, err := NewSnippetDraftSubmission(g.Request, g.Access)
		if err != nil {
			return err
		}
		if request.Action != "create" || request.Key != expected.Key || request.Fingerprint != expected.Fingerprint {
			return errors.New("invalid guarded snippet draft submission")
		}
	}
	if !validSubmissionDigest(request.Key) || !validSubmissionDigest(request.Fingerprint) || !ValidSubmissionAction(request.Action) ||
		(request.Action == "create" && (request.PostID != 0 || request.Version != 0)) ||
		(request.Action != "create" && (request.PostID <= 0 || request.Version <= 0)) ||
		(request.Action == "restore_revision" && request.RevisionID <= 0) {
		return errors.New("invalid editorial submission")
	}
	return nil
}

func ValidateSubmissionReceipts(receipts []SubmissionReceipt, posts map[int64]Post) error {
	if len(receipts) > MaxSubmissionReceipts {
		return errors.New("too many editorial submission receipts")
	}
	seen := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		post, exists := posts[receipt.PostID]
		if !validSubmissionDigest(receipt.Key) || !validSubmissionDigest(receipt.Fingerprint) || seen[receipt.Key] || receipt.PostID <= 0 || (!exists && !receipt.Deleted) || (exists && receipt.Deleted) ||
			receipt.Version <= 0 || (exists && receipt.Version > post.Version) || !ValidSubmissionAction(receipt.Action) || receipt.CreatedAt.IsZero() ||
			(receipt.Action == "create" && receipt.Version != 1) {
			return errors.New("invalid editorial submission receipt")
		}
		seen[receipt.Key] = true
	}
	return nil
}

func findSubmission(receipts []SubmissionReceipt, key, fingerprint string, now time.Time) (SubmissionResult, error) {
	if !validSubmissionDigest(key) || !validSubmissionDigest(fingerprint) {
		return SubmissionResult{}, errors.New("invalid editorial submission lookup")
	}
	for _, receipt := range receipts {
		if receipt.Key != key || (receipt.Action != "create" && !receipt.CreatedAt.Add(SubmissionReplayWindow).After(now)) {
			continue
		}
		if receipt.Fingerprint != fingerprint {
			return SubmissionResult{}, idempotency.ErrConflict
		}
		return SubmissionResult{Receipt: receipt, Replayed: true}, nil
	}
	return SubmissionResult{}, ErrNotFound
}

func (repository *MemoryRepository) FindSubmission(ctx context.Context, key, fingerprint string) (SubmissionResult, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return SubmissionResult{}, err
	}
	return findSubmission(repository.submissions, key, fingerprint, repository.now())
}

func (repository *FileRepository) FindSubmission(ctx context.Context, key, fingerprint string) (SubmissionResult, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.FindSubmission(ctx, key, fingerprint)
}

// applySubmission operates on a private candidate. The caller publishes the
// candidate and receipt together, or discards all changes on any failure.
func applySubmission(ctx context.Context, state *MemoryRepository, request Submission) (SubmissionResult, error) {
	if err := ValidateSubmission(request); err != nil {
		return SubmissionResult{}, err
	}
	now := state.now().UTC()
	retained := state.submissions[:0]
	for _, receipt := range state.submissions {
		// Creation receipts must never expire: a new post has no version
		// precondition that could otherwise prevent a late duplicate.
		if receipt.Action == "create" || receipt.CreatedAt.Add(SubmissionReplayWindow).After(now) {
			retained = append(retained, receipt)
		}
	}
	state.submissions = retained
	for _, receipt := range retained {
		if receipt.Key != request.Key {
			continue
		}
		if receipt.Fingerprint != request.Fingerprint {
			return SubmissionResult{}, idempotency.ErrConflict
		}
		if g := request.SnippetDraft; g != nil {
			if err := state.checkSnippetDraftReplay(receipt, g.Access); err != nil {
				return SubmissionResult{}, err
			}
		}
		return SubmissionResult{Receipt: receipt, Replayed: true}, nil
	}
	if len(retained) >= MaxSubmissionReceipts {
		return SubmissionResult{}, idempotency.ErrCapacity
	}
	if request.SnippetDraft != nil {
		if err := state.prepareSnippetDraft(ctx, &request); err != nil {
			return SubmissionResult{}, err
		}
	}
	mutation := Mutation{ExpectedVersion: request.Version, Actor: request.Actor}
	var post Post
	var err error
	switch request.MutationAction() {
	case "create":
		post, err = state.Create(ctx, request.Input)
	case "save":
		post, err = state.Update(ctx, request.PostID, mutation, request.Input)
	case "restore_revision":
		post, err = state.RestoreRevision(ctx, request.PostID, request.RevisionID, mutation)
	default:
		status := map[string]string{"trash": StatusTrash, "restore": StatusDraft, "publish": StatusPublish}[request.Action]
		post, err = state.ChangeStatus(ctx, request.PostID, mutation, status)
	}
	if err != nil {
		return SubmissionResult{}, err
	}
	receipt := SubmissionReceipt{Key: request.Key, Fingerprint: request.Fingerprint, PostID: post.ID, Version: post.Version, Action: request.Action, CreatedAt: post.UpdatedAt.UTC()}
	state.submissions = append(state.submissions, receipt)
	return SubmissionResult{Receipt: receipt}, nil
}

func (repository *MemoryRepository) Submit(ctx context.Context, request Submission) (SubmissionResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SubmissionResult{}, err
	}
	candidate := cloneContentState(repository)
	result, err := applySubmission(ctx, candidate, request)
	if err != nil {
		return SubmissionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return SubmissionResult{}, err
	}
	// Keep this repository's mutex and clock; only publish the editorial state.
	repository.posts, repository.terms = candidate.posts, candidate.terms
	repository.revisions, repository.autosaves = candidate.revisions, candidate.autosaves
	repository.nextID, repository.nextTermID, repository.nextRevisionID = candidate.nextID, candidate.nextTermID, candidate.nextRevisionID
	repository.activities = candidate.activities
	repository.redirects, repository.nextRedirectID = candidate.redirects, candidate.nextRedirectID
	repository.submissions = candidate.submissions
	repository.snippetPostCounts = candidate.snippetPostCounts
	return result, nil
}

func (repository *FileRepository) Submit(ctx context.Context, request Submission) (SubmissionResult, error) {
	return fileContentChange(ctx, repository, func(candidate *MemoryRepository) (SubmissionResult, error) {
		return applySubmission(ctx, candidate, request)
	})
}
