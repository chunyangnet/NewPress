package site

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// DocumentRepository stores the validated settings document and applies
// revision-checked updates. Create must never replace an existing document.
type DocumentRepository interface {
	Load(context.Context) ([]byte, bool, error)
	Create(context.Context, string, []byte) (bool, error)
	CompareAndSwap(context.Context, string, string, []byte) (bool, error)
}

// NewManagerWithRepository uses a durable document repository as the source of
// truth. migrationPath is consulted only when that repository has no document;
// this imports existing file-backed site settings on first use without writing
// them back to a local disk that may disappear on restart.
func NewManagerWithRepository(ctx context.Context, repository DocumentRepository, migrationPath string, bootstrap Settings) (*Manager, error) {
	if repository == nil {
		return nil, errors.New("site settings repository is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, found, err := repository.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load persistent site settings: %w", err)
	}
	if found {
		settings, updatedAt, err := decodeStoredDocument(payload)
		if err != nil {
			return nil, fmt.Errorf("validate persistent site settings: %w", err)
		}
		return &Manager{repository: repository, settings: settings, updatedAt: updatedAt}, nil
	}

	settings := bootstrap
	var updatedAt time.Time
	if migrationPath != "" {
		loaded, savedAt, loadErr := loadDocument(migrationPath)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return nil, fmt.Errorf("load file-backed site settings for migration: %w", loadErr)
		}
		if loadErr == nil {
			settings, updatedAt = loaded, savedAt
		}
	}
	if updatedAt.IsZero() {
		var fields map[string]string
		settings, fields = Validate(settings)
		if len(fields) > 0 {
			return nil, fmt.Errorf("invalid initial site settings: %v", fields)
		}
		updatedAt = time.Now().UTC().Truncate(time.Second)
	}
	revision := settingsRevision(settings)
	doc := document{
		Schema: documentSchema, Kind: documentKind, Revision: revision,
		UpdatedAt: updatedAt.UTC(), Settings: settings,
	}
	payload, err = encodeDocument(doc)
	if err != nil {
		return nil, err
	}
	created, err := repository.Create(ctx, revision, payload)
	if err != nil {
		return nil, fmt.Errorf("initialize persistent site settings: %w", err)
	}
	if created {
		return &Manager{repository: repository, settings: settings, updatedAt: doc.UpdatedAt}, nil
	}

	// Another instance may have initialized the row after our first read. Load
	// that committed value; never replace it with this instance's defaults.
	payload, found, err = repository.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload concurrently initialized site settings: %w", err)
	}
	if !found {
		return nil, errors.New("site settings initialization was not committed")
	}
	settings, updatedAt, err = decodeStoredDocument(payload)
	if err != nil {
		return nil, fmt.Errorf("validate concurrently initialized site settings: %w", err)
	}
	return &Manager{repository: repository, settings: settings, updatedAt: updatedAt}, nil
}

func decodeStoredDocument(payload []byte) (Settings, time.Time, error) {
	doc, err := decodeDocument(bytes.NewReader(payload))
	if err != nil {
		return Settings{}, time.Time{}, err
	}
	return validateStoredDocument(doc)
}
