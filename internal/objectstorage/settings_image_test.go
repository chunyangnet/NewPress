package objectstorage

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

func TestSettingsImageRoutingDeliveryAndFallback(t *testing.T) {
	s, manager, library, _ := storageFixture(t)
	c := s.settings
	c.Profiles = append([]Profile(nil), c.Profiles...)
	c.Profiles[0].PublicURL = "https://images.example.test"
	c.UploadRoutes = map[string]UploadRoute{"settings-image": {Target: "primary", Mode: "redirect"}, "link-logo": {Target: "local"}}
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	items := map[string]media.Item{}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"settings-image", "link-logo"} {
		item, err := library.Upload(t.Context(), bytes.NewReader(picture.Bytes()), media.UploadInput{Filename: "route.png", UploadSource: source}, 4096)
		if err != nil {
			t.Fatal(err)
		}
		items[source] = item
	}
	if _, ok := s.RedirectURL(t.Context(), items["settings-image"]); ok {
		t.Fatal("unsynced media redirected")
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if url, ok := s.RedirectURL(t.Context(), items["settings-image"]); !ok || !strings.HasPrefix(url, "https://images.example.test/") {
		t.Fatal(url, ok)
	}
	if _, ok := s.RedirectURL(t.Context(), items["link-logo"]); ok {
		t.Fatal("local route redirected")
	}
	if origin := s.DeliveryOrigin(t.Context()); origin != "https://images.example.test" {
		t.Fatal(origin)
	}
	if err := manager.SetEnabled(plugin.ObjectStorage, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RedirectURL(t.Context(), items["settings-image"]); ok {
		t.Fatal("disabled plugin did not fall back to local")
	}
}
