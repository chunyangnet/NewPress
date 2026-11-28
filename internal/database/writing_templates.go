package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/writingtemplate"
)

type WritingTemplateStorage struct{ pool *pgxpool.Pool }

var _ writingtemplate.Storage = (*WritingTemplateStorage)(nil)

func NewWritingTemplateStorage(pool *pgxpool.Pool) *WritingTemplateStorage {
	return &WritingTemplateStorage{pool: pool}
}

type writingTemplateReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readWritingTemplates(ctx context.Context, reader writingTemplateReader) ([]byte, error) {
	var size int
	var raw []byte
	err := reader.QueryRow(ctx, `SELECT octet_length(data), CASE WHEN octet_length(data) <= $1 THEN data ELSE NULL END FROM np_writing_template_library WHERE id=1`, writingtemplate.MaxLibraryBytes).Scan(&size, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if size < 1 || size > writingtemplate.MaxLibraryBytes || size != len(raw) {
		return nil, writingtemplate.ErrCorrupt
	}
	return raw, nil
}

func (s *WritingTemplateStorage) Read(ctx context.Context) ([]byte, error) {
	return readWritingTemplates(ctx, s.pool)
}

func (s *WritingTemplateStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	if err := writingtemplate.ValidateSnapshot(raw); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Covers the absent first row as well as later updates. No process-local
	// cache or read-before-write decision is trusted after this lock is taken.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x5754504c)); err != nil {
		return err
	}
	current, err := readWritingTemplates(ctx, tx)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != expected {
		return writingtemplate.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_writing_template_library(id,data) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`, raw); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
