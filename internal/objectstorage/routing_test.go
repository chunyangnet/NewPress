package objectstorage

import (
	"strings"
	"testing"

	"newpress.local/newpress/internal/media"
)

func TestEntryRoutingAutomaticManualDeliveryAndRestart(t *testing.T) {
	s, manager, library, backend := storageFixture(t)
	c := s.settings
	c.Profiles = append([]Profile(nil), c.Profiles...)
	second := c.Profiles[0]
	second.ID = "secondary"
	second.PublicURL = "https://second.example"
	c.Profiles = append(c.Profiles, second)
	c.Profiles[0].PublicURL = "https://first.example"
	c.UploadRoutes = map[string]UploadRoute{"avatar": {Target: "local"}, "editor-body": {Target: "secondary", Mode: "redirect"}, "media-library": {Target: "local"}}
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	items := map[string]media.Item{}
	for _, source := range []string{"avatar", "editor-body", "series-cover"} {
		item, err := library.Upload(t.Context(), strings.NewReader("%PDF-1.7\nrouting\n%%EOF"), media.UploadInput{Filename: "entry.pdf", UploadSource: source}, 4096)
		if err != nil {
			t.Fatal(err)
		}
		items[source] = item
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.records) != 2 {
		t.Fatalf("records: %+v", s.records)
	}
	for _, r := range s.records {
		if r.ItemID == items["avatar"].ID {
			t.Fatal("local file uploaded")
		}
		if r.ItemID == items["editor-body"].ID && r.ProfileID != "secondary" {
			t.Fatal("wrong destination", r)
		}
	}
	if n, err := s.Synchronize("primary"); err != nil || n != 0 {
		t.Fatal("manual sync bypassed routing", n, err)
	}
	if u, ok := s.RedirectURL(t.Context(), items["editor-body"]); !ok || !strings.HasPrefix(u, "https://second.example/") {
		t.Fatal(u, ok)
	}
	if _, ok := s.RedirectURL(t.Context(), items["avatar"]); ok {
		t.Fatal("local entry redirected")
	}
	if origins := s.DeliveryOrigin(t.Context()); origins != "https://second.example" {
		t.Fatal(origins)
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	status.Settings.UploadRoutes["avatar"] = UploadRoute{Target: "primary"}
	if s.settings.UploadRoutes["avatar"].Target != "local" {
		t.Fatal("status leaked mutable configuration")
	}
	restarted := New(manager, library)
	restarted.factory = func(Profile) (Backend, error) { return backend, nil }
	if err := restarted.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if u, ok := restarted.RedirectURL(t.Context(), items["editor-body"]); !ok || !strings.HasPrefix(u, "https://second.example/") {
		t.Fatal(u, ok)
	}
}

func TestQueuedUploadStopsWhenEntryBecomesLocal(t *testing.T) {
	s, _, library, backend := storageFixture(t)
	storageUpload(t, library)
	if _, err := s.Synchronize("primary"); err != nil {
		t.Fatal(err)
	}
	c := s.settings
	c.UploadRoutes = map[string]UploadRoute{"media-library": {Target: "local"}}
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.puts != 0 {
		t.Fatal("queued task bypassed local routing")
	}
}

func TestRouteConfigurationRejectsMissingTargetsAndUnknownEntries(t *testing.T) {
	s, _, _, _ := storageFixture(t)
	for _, routes := range []map[string]UploadRoute{
		{"avatar": {Target: "missing"}},
		{"unknown": {Target: "local"}},
		{"legacy": {Target: "local"}},
		{"avatar": {Target: "primary", Mode: "invalid"}},
	} {
		c := s.settings
		c.UploadRoutes = routes
		if _, err := s.Save(c, nil); err == nil {
			t.Fatal("invalid route accepted", routes)
		}
	}
}
