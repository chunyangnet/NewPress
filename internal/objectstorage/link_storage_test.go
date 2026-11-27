package objectstorage

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"

	"newpress.local/newpress/internal/linkpage"
)

func TestLinkLogoSyncAndRemoval(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	store, err := linkpage.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetLinkPages(store)
	actor := linkpage.Actor{ID: 1, Admin: true}
	settings := linkpage.Defaults()
	_, err = store.Apply(t.Context(), "friends", actor, linkpage.Command{Action: "create", Key: "create-link-routing", ExpectedUserID: 1, Settings: &settings}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	logo, err := linkpage.NormalizeLogo(body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	entry := linkpage.Item{Name: "Friend", URL: "https://example.com", Logo: logo, Enabled: true}
	_, err = store.Apply(t.Context(), "friends", actor, linkpage.Command{Action: "save_item", Key: "save-link-routing", Version: 1, ExpectedUserID: 1, Item: &entry}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	board, err := store.Get(t.Context(), "friends")
	if err != nil {
		t.Fatal(err)
	}
	entry = board.Items[0]
	c := s.settings
	c.UploadRoutes = map[string]UploadRoute{"link-logo": {Target: "primary", Mode: "redirect"}}
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.puts != 1 {
		t.Fatal(backend.puts)
	}
	if _, ok := s.LinkRedirectURL(t.Context(), "friends", entry); !ok {
		t.Fatal("logo not delivered")
	}
	entry.Logo = ""
	_, err = store.Apply(t.Context(), "friends", actor, linkpage.Command{Action: "save_item", Key: "clear-link-routing", Version: board.Version, ExpectedUserID: 1, Item: &entry}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Cleanup("primary"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.deletes != 1 {
		t.Fatal(backend.deletes)
	}
}
