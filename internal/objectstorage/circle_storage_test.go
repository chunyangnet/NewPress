package objectstorage

import (
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
)

func TestPrivateCircleRoutingAndProxy(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	store, err := community.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	files, err := community.NewFiles("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunity(store, files)
	actor := community.Actor{ID: 1, Admin: true}
	_, err = store.Apply(t.Context(), "private", actor, community.Command{Action: "create", Key: "create-circle-routing", Name: "Private circle", OwnerID: 1, Visibility: "members", JoinPolicy: "open"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	circle, err := store.Get(t.Context(), "private")
	if err != nil {
		t.Fatal(err)
	}
	file, _, err := files.Upload(t.Context(), store, "private", actor, "upload-circle-routing", circle.Version, "private.txt", strings.NewReader("private attachment body"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Synchronize("primary"); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.puts != 0 {
		t.Fatal("private attachments inherited public default")
	}
	c := s.settings
	c.UploadRoutes = map[string]UploadRoute{"circle-attachment": {Target: "primary", Mode: "redirect"}}
	if _, err := s.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.puts != 1 {
		t.Fatal(backend.puts)
	}
	data, ok := s.ReadCircle(t.Context(), "private", file)
	if !ok || string(data) != "private attachment body" {
		t.Fatal(string(data), ok)
	}
	if _, ok := s.ReadCircle(t.Context(), "other", file); ok {
		t.Fatal("cross-circle receipt reused")
	}
	for key := range backend.objects {
		backend.objects[key] = []byte("corrupt")
	}
	if _, ok := s.ReadCircle(t.Context(), "private", file); ok {
		t.Fatal("corrupt remote content served")
	}
}
