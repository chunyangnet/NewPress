package navigation

import (
	"context"
	"errors"
	"strings"
	"time"

	"newpress.local/newpress/internal/idempotency"
)

// Creation receipts never expire or disappear when their menu is deleted.
// An old create request must not resurrect deleted or replaced navigation.
const MaxCreationReceipts = 4096

type Creation struct {
	Key, Fingerprint string
	Input            MenuInput
	Actor            string
}

type CreationReceipt struct {
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	MenuID      int64     `json:"menu_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreationResult struct {
	Receipt  CreationReceipt
	Replayed bool
}

// ValidateCreationKey accepts only SHA-256 digests. HTTP binds these to the
// original identity, namespace and request bytes; bodies never enter receipts.
func ValidateCreationKey(key, fingerprint string) error {
	for _, digest := range []string{key, fingerprint} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return errors.New("invalid navigation creation key")
		}
	}
	return nil
}

func validateCreationReceipts(receipts []CreationReceipt, nextMenuID int64) error {
	if len(receipts) > MaxCreationReceipts {
		return errors.New("too many navigation creation receipts")
	}
	keys := make(map[string]bool, len(receipts))
	menus := make(map[int64]bool, len(receipts))
	for _, receipt := range receipts {
		if ValidateCreationKey(receipt.Key, receipt.Fingerprint) != nil || keys[receipt.Key] || menus[receipt.MenuID] ||
			receipt.MenuID <= 0 || receipt.MenuID >= nextMenuID || receipt.CreatedAt.IsZero() {
			return errors.New("invalid navigation creation receipt")
		}
		keys[receipt.Key], menus[receipt.MenuID] = true, true
	}
	return nil
}

func findCreation(receipts []CreationReceipt, key, fingerprint string) (CreationResult, error) {
	for _, receipt := range receipts {
		if receipt.Key != key {
			continue
		}
		if receipt.Fingerprint != fingerprint {
			return CreationResult{}, idempotency.ErrConflict
		}
		return CreationResult{Receipt: receipt, Replayed: true}, nil
	}
	return CreationResult{}, ErrNotFound
}

func (store *FileStore) FindCreation(ctx context.Context, key, fingerprint string) (CreationResult, error) {
	if store == nil {
		return CreationResult{}, errors.New("navigation store is nil")
	}
	if err := ValidateCreationKey(key, fingerprint); err != nil {
		return CreationResult{}, err
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return CreationResult{}, err
	}
	defer store.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return CreationResult{}, err
	}
	if store.persistFault != nil {
		return CreationResult{}, store.persistFault
	}
	result, err := findCreation(store.creations, key, fingerprint)
	if contextErr := ctx.Err(); contextErr != nil {
		return CreationResult{}, contextErr
	}
	return result, err
}

func (store *FileStore) CreateOnce(ctx context.Context, request Creation) (CreationResult, error) {
	if store == nil {
		return CreationResult{}, errors.New("navigation store is nil")
	}
	if err := ValidateCreationKey(request.Key, request.Fingerprint); err != nil {
		return CreationResult{}, err
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return CreationResult{}, err
	}
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CreationResult{}, err
	}
	if store.persistFault != nil {
		return CreationResult{}, store.persistFault
	}
	if result, err := findCreation(store.creations, request.Key, request.Fingerprint); !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if len(store.creations) >= MaxCreationReceipts {
		return CreationResult{}, idempotency.ErrCapacity
	}
	receipt := CreationReceipt{Key: request.Key, Fingerprint: request.Fingerprint}
	if _, err := store.createLocked(ctx, request.Input, request.Actor, &receipt); err != nil {
		return CreationResult{}, err
	}
	return CreationResult{Receipt: receipt}, nil
}
