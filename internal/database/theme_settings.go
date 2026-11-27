package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/theme"
)

type themeSettingsRepository struct {
	pool *pgxpool.Pool
}

var _ theme.SettingsRepository = (*themeSettingsRepository)(nil)

func NewThemeSettingsRepository(pool *pgxpool.Pool) theme.SettingsRepository {
	return &themeSettingsRepository{pool: pool}
}

func (r *themeSettingsRepository) Load(ctx context.Context, slug string) ([]byte, int64, bool, error) {
	var payload []byte
	var version int64
	err := r.pool.QueryRow(ctx, `SELECT document,version FROM np_theme_settings WHERE theme_slug=$1`, slug).Scan(&payload, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	return payload, version, err == nil, err
}

func (r *themeSettingsRepository) Create(ctx context.Context, slug string, payload []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO np_theme_settings(theme_slug,document) VALUES($1,$2::json) ON CONFLICT(theme_slug) DO NOTHING`, slug, string(payload))
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *themeSettingsRepository) CompareAndSwap(ctx context.Context, slug string, expected int64, payload []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE np_theme_settings SET version=version+1,document=$3::json WHERE theme_slug=$1 AND version=$2`, slug, expected, string(payload))
	return err == nil && tag.RowsAffected() == 1, err
}
