package database

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/site"
)

const defaultSiteSettingsKey = "site"

type siteSettingsRepository struct {
	pool *pgxpool.Pool
	key  string
}

var _ site.DocumentRepository = (*siteSettingsRepository)(nil)

func NewSiteSettingsRepository(pool *pgxpool.Pool) site.DocumentRepository {
	return newSiteSettingsRepository(pool, defaultSiteSettingsKey)
}

// Announcement documents use their own key and revision in the shared document table.
func NewAnnouncementRepository(pool *pgxpool.Pool) site.DocumentRepository {
	return newSiteSettingsRepository(pool, "announcements")
}

func newSiteSettingsRepository(pool *pgxpool.Pool, key string) *siteSettingsRepository {
	return &siteSettingsRepository{pool: pool, key: key}
}

func (r *siteSettingsRepository) Load(ctx context.Context) ([]byte, bool, error) {
	var revision string
	var payload []byte
	err := r.pool.QueryRow(ctx, `SELECT revision,document FROM np_site_settings WHERE settings_key=$1`, r.key).Scan(&revision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var envelope struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, false, err
	}
	if envelope.Revision == "" || envelope.Revision != revision {
		return nil, false, errors.New("persistent site settings revision metadata is inconsistent")
	}
	return payload, true, nil
}

func (r *siteSettingsRepository) Create(ctx context.Context, revision string, payload []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO np_site_settings(settings_key,revision,document) VALUES($1,$2,$3::jsonb) ON CONFLICT(settings_key) DO NOTHING`, r.key, revision, string(payload))
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *siteSettingsRepository) CompareAndSwap(ctx context.Context, expected, revision string, payload []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE np_site_settings SET revision=$3,document=$4::jsonb WHERE settings_key=$1 AND revision=$2`, r.key, expected, revision, string(payload))
	return err == nil && tag.RowsAffected() == 1, err
}
