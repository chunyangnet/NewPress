package comment

import (
	"context"
	"errors"
	"sort"
	"time"

	"newpress.local/newpress/internal/idempotency"
)

// Creation receipts never expire: deleting a comment must not make a late
// retry create it again. Capacity is explicit; existing receipts remain usable.
const MaxSubmissionReceipts = 32768

type Submission struct {
	Key, Fingerprint string
	Input            CreateInput
}

type SubmissionReceipt struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	CommentID   int64     `json:"comment_id"`
	PostID      int64     `json:"post_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type SubmissionResult struct {
	Receipt  SubmissionReceipt
	Replayed bool
}

func ValidateSubmissionKey(key, fingerprint string) error {
	if !validHexDigest(key) || !validHexDigest(fingerprint) {
		return ErrInvalidInput
	}
	return nil
}

func (store *FileStore) FindSubmission(ctx context.Context, key, fingerprint string) (SubmissionResult, error) {
	if store == nil {
		return SubmissionResult{}, ErrInvalidInput
	}
	if err := ValidateSubmissionKey(key, fingerprint); err != nil {
		return SubmissionResult{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return SubmissionResult{}, err
	}
	return store.findSubmissionLocked(key, fingerprint)
}

func (store *FileStore) findSubmissionLocked(key, fingerprint string) (SubmissionResult, error) {
	receipt, ok := store.submissions[key]
	if !ok {
		return SubmissionResult{}, ErrNotFound
	}
	if receipt.Fingerprint != fingerprint {
		return SubmissionResult{}, idempotency.ErrConflict
	}
	return SubmissionResult{Receipt: receipt, Replayed: true}, nil
}

func (store *FileStore) CreateOnce(ctx context.Context, request Submission) (SubmissionResult, error) {
	if store == nil {
		return SubmissionResult{}, ErrInvalidInput
	}
	if err := ValidateSubmissionKey(request.Key, request.Fingerprint); err != nil {
		return SubmissionResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SubmissionResult{}, err
	}
	result, err := store.findSubmissionLocked(request.Key, request.Fingerprint)
	if !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if len(store.submissions) >= MaxSubmissionReceipts {
		return SubmissionResult{}, idempotency.ErrCapacity
	}
	input, fields := NormalizeCreateInput(request.Input)
	if len(fields) > 0 {
		return SubmissionResult{}, &ValidationError{Fields: fields}
	}
	if _, err := store.createLocked(ctx, input, &request); err != nil {
		return SubmissionResult{}, err
	}
	return SubmissionResult{Receipt: store.submissions[request.Key]}, nil
}

func flattenSubmissions(receipts map[string]SubmissionReceipt) []SubmissionReceipt {
	result := make([]SubmissionReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		result = append(result, receipt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func validateSubmissionReceipts(document fileDocument, items map[int64]Comment) error {
	if len(document.Submissions) > MaxSubmissionReceipts {
		return errors.New("unsupported comment submission receipts")
	}
	keys, ids := make(map[string]bool), make(map[int64]bool)
	for _, receipt := range document.Submissions {
		item, exists := items[receipt.CommentID]
		if ValidateSubmissionKey(receipt.Key, receipt.Fingerprint) != nil || keys[receipt.Key] || ids[receipt.CommentID] ||
			receipt.PostID <= 0 || receipt.CommentID <= 0 || receipt.CommentID >= document.NextID || receipt.CreatedAt.IsZero() ||
			receipt.CreatedAt.After(document.UpdatedAt) || (exists && (item.PostID != receipt.PostID || !item.CreatedAt.Equal(receipt.CreatedAt))) {
			return errors.New("invalid comment submission receipt")
		}
		keys[receipt.Key], ids[receipt.CommentID] = true, true
	}
	return nil
}
