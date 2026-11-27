package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"newpress.local/newpress/internal/announcement"
)

func TestPostgreSQLAnnouncementDocumentsAreIndependentAndRevisionChecked(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := announcement.NewRepositoryStore(ctx, NewAnnouncementRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := store.Snapshot(ctx)
	next, err := store.Save(ctx, initial.Revision, "save", announcement.Item{Title: "独立数据库公告", Body: "公告正文", Date: time.Now().UTC().Add(-time.Hour), Published: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := NewSiteSettingsRepository(pool).Load(ctx); err != nil || found {
		t.Fatal("announcement replaced site settings", err)
	}
	other, err := announcement.NewRepositoryStore(ctx, NewAnnouncementRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Save(ctx, initial.Revision, "delete", announcement.Item{ID: 1}); !errors.Is(err, announcement.ErrConflict) {
		t.Fatal(err)
	}
	actual, err := other.Snapshot(ctx)
	if err != nil || actual.Revision != next.Revision || len(actual.Items) != 1 {
		t.Fatal(actual, err)
	}
}

func TestPostgreSQLDocumentRevisionConstraints(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key, revision string
		valid         bool
	}{
		{"site", strings.Repeat("a", 16), true},
		{"announcements", strings.Repeat("b", 64), true},
		{"announcements", strings.Repeat("b", 16), true},
		{"site", strings.Repeat("a", 64), false},
		{"announcements", strings.Repeat("b", 63), false},
		{"announcements", strings.Repeat("B", 64), false},
		{"announcements", "", false},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO np_site_settings(settings_key,revision,document) VALUES($1,$2,'{}') ON CONFLICT(settings_key) DO UPDATE SET revision=excluded.revision`, tc.key, tc.revision)
		if tc.valid {
			if err != nil {
				t.Errorf("valid %s revision length %d: %v", tc.key, len(tc.revision), err)
			}
		} else {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "np_site_settings_revision_check" {
				t.Errorf("invalid %s revision length %d: %v", tc.key, len(tc.revision), err)
			}
		}
	}
}
