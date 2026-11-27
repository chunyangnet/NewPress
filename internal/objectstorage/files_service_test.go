package objectstorage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRemoteFileManagement(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	ctx := context.Background()
	version := s.settings.Version
	if err := s.RemoteUpload(ctx, "primary", "assets/manual/file.txt", version, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoteUpload(ctx, "primary", "assets/manual/file.txt", version, strings.NewReader("changed"), "text/plain"); err == nil {
		t.Fatal("overwrote existing file")
	}
	if err := s.RemoteAction(ctx, "primary", "assets/manual/file.txt", "primary", "assets/manual/renamed.txt", "move", version); err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.objects["assets/manual/file.txt"]; ok {
		t.Fatal("move left source")
	}
	reader, _, err := s.RemoteDownload(ctx, "primary", "assets/manual/renamed.txt", version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("%s %v", data, err)
	}
	if err := s.RemoteAction(ctx, "primary", "assets/manual/renamed.txt", "", "", "delete", version); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"outside.txt", "assets/../outside.txt", "assets/" + s.namespace + "/file.txt"} {
		if err := s.RemoteUpload(ctx, "primary", key, version, strings.NewReader("x"), "text/plain"); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	if err := s.RemoteUpload(ctx, "primary", "assets/stale.txt", version-1, strings.NewReader("x"), "text/plain"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestRemoteMoveVerificationFailureKeepsSource(t *testing.T) {
	s, _, _, backend := storageFixture(t)
	ctx := context.Background()
	version := s.settings.Version
	if err := s.RemoteUpload(ctx, "primary", "assets/source.txt", version, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	backend.corrupt = true
	if err := s.RemoteAction(ctx, "primary", "assets/source.txt", "primary", "assets/dest.txt", "move", version); err == nil {
		t.Fatal("accepted corrupt target")
	}
	if string(backend.objects["assets/source.txt"]) != "hello" || backend.deletes != 0 {
		t.Fatal("deleted source after failed verification")
	}
}
