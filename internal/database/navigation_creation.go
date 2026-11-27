package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/idempotency"
	"newpress.local/newpress/internal/navigation"
)

func (repository *NavigationRepository) FindCreation(ctx context.Context, key, fingerprint string) (navigation.CreationResult, error) {
	if repository == nil || repository.pool == nil {
		return navigation.CreationResult{}, errors.New("navigation repository is nil")
	}
	if err := navigation.ValidateCreationKey(key, fingerprint); err != nil {
		return navigation.CreationResult{}, err
	}
	return readNavigationCreation(ctx, repository.pool, key, fingerprint)
}

func readNavigationCreation(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key, fingerprint string) (navigation.CreationResult, error) {
	var receipt navigation.CreationReceipt
	err := querier.QueryRow(ctx, `SELECT key_hash,fingerprint,menu_id,created_at FROM np_navigation_creations WHERE key_hash=$1`, key).
		Scan(&receipt.Key, &receipt.Fingerprint, &receipt.MenuID, &receipt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return navigation.CreationResult{}, navigation.ErrNotFound
	}
	if err != nil {
		return navigation.CreationResult{}, err
	}
	if receipt.Fingerprint != fingerprint {
		return navigation.CreationResult{}, idempotency.ErrConflict
	}
	return navigation.CreationResult{Receipt: receipt, Replayed: true}, nil
}

func (repository *NavigationRepository) CreateOnce(ctx context.Context, request navigation.Creation) (navigation.CreationResult, error) {
	if repository == nil || repository.pool == nil {
		return navigation.CreationResult{}, errors.New("navigation repository is nil")
	}
	if err := navigation.ValidateCreationKey(request.Key, request.Fingerprint); err != nil {
		return navigation.CreationResult{}, err
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return navigation.CreationResult{}, err
	}
	defer tx.Rollback(ctx)
	// Use the same lock as ordinary creates and whole-registry replacements.
	// Retention/capacity and creation remain in one transaction across nodes.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-navigation-menus',0))`); err != nil {
		return navigation.CreationResult{}, err
	}
	if result, err := readNavigationCreation(ctx, tx, request.Key, request.Fingerprint); !errors.Is(err, navigation.ErrNotFound) {
		return result, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_navigation_creations`).Scan(&count); err != nil {
		return navigation.CreationResult{}, err
	}
	if count >= navigation.MaxCreationReceipts {
		return navigation.CreationResult{}, idempotency.ErrCapacity
	}
	normalized, fields := navigation.NormalizeMenuInput(request.Input)
	if len(fields) > 0 {
		return navigation.CreationResult{}, &navigation.ValidationError{Fields: fields}
	}
	now := time.Now().UTC()
	menu, err := createNavigationInTransaction(ctx, tx, normalized, request.Actor, now)
	if err != nil {
		return navigation.CreationResult{}, err
	}
	receipt := navigation.CreationReceipt{Key: request.Key, Fingerprint: request.Fingerprint, MenuID: menu.ID, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO np_navigation_creations(key_hash,fingerprint,menu_id,created_at) VALUES($1,$2,$3,$4)`, receipt.Key, receipt.Fingerprint, receipt.MenuID, receipt.CreatedAt); err != nil {
		return navigation.CreationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return navigation.CreationResult{}, fmt.Errorf("commit navigation creation: %w", err)
	}
	return navigation.CreationResult{Receipt: receipt}, nil
}
