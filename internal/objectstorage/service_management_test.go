package objectstorage

import (
	"context"
	"errors"
	"testing"
)

func TestMigrationPreviewApplyAndResume(t *testing.T) {
	s, manager, library, backend := storageFixture(t)
	storageUpload(t, library)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := s.settings
	p := c.Profiles[0]
	p.ID = "destination"
	p.Prefix = "new-location"
	c.Profiles = append(c.Profiles, p)
	c.UploadRoutes = map[string]UploadRoute{"media-library": {Target: "primary"}}
	view, err := s.Save(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Migrate("primary", p.ID, view.Version, false)
	if err != nil || preview.Total != 1 || preview.Ready != 0 || s.settings.ActiveProfile != "primary" || len(s.records) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err = s.Migrate("primary", p.ID, view.Version-1, true); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Migrate("primary", p.ID, view.Version, true); err != nil {
		t.Fatal(err)
	}
	if s.settings.ActiveProfile != p.ID || s.settings.UploadRoutes["media-library"].Target != p.ID {
		t.Fatal("routes were not migrated")
	}
	restarted := New(manager, library)
	restarted.factory = func(Profile) (Backend, error) { return backend, nil }
	if err = restarted.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Status()
	if err != nil || status.ProfileCounts[p.ID]["synced"] != 1 || status.ProfileCounts["primary"]["synced"] != 1 || backend.deletes != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	page, err := restarted.Records(p.ID, "synced", "中文", 99)
	if err != nil || page.Total != 1 || page.Page != 1 || len(page.Records) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	page, err = restarted.Records("primary", "failed", "", 1)
	if err != nil || page.Total != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
