package writingtemplate_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/writingtemplate"
	"newpress.local/newpress/internal/writingtemplate/testkit"
)

func TestCapacityFileReopenPreservesHistoryAndReceipts(t *testing.T) {
	for _, shape := range []string{"revisions", "bytes"} {
		t.Run(shape, func(t *testing.T) {
			f := testkit.Capacity(t, shape)
			path := filepath.Join(t.TempDir(), "writing-templates.json")
			store, err := writingtemplate.NewFileStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Write(t.Context(), sha256.Sum256(nil), f.Raw); err != nil {
				t.Fatal(err)
			}
			store, err = writingtemplate.NewFileStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := writingtemplate.New(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				catalog, err := s.Catalog(t.Context())
				if err != nil || len(catalog) != f.Templates {
					t.Fatal("catalog after reopen", len(catalog), err)
				}
				item, err := s.Get(t.Context(), f.ID)
				if err != nil || len(item.Revisions) != int(f.Version) || item.Revisions[0].Definition != *f.Create.Definition {
					t.Fatal("history after reopen", err)
				}
				if _, err := s.Resolve(t.Context(), f.ID, f.Version); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.Apply(t.Context(), f.Create); err != nil || !got.Replayed || got.ID != f.ID || got.Version != 1 {
				t.Fatal("full library lost old receipt", got, err)
			}
			if _, err := s.Apply(t.Context(), writingtemplate.Command{Action: "archive", ID: f.ID, ExpectedVersion: f.Version, Actor: f.Create.Actor, RequestKey: fmt.Sprintf("%064x", 2)}); !errors.Is(err, writingtemplate.ErrCapacity) {
				t.Fatal("full library accepted archive", err)
			}
			after, err := store.Read(t.Context())
			if err != nil || !bytes.Equal(f.Raw, after) {
				t.Fatal("reads/rejected write changed bytes", err)
			}
			t.Logf("constructed %s: templates=%d revisions=%d encoded_bytes=%d; file write/reopen, reads, old replay and full refusal verified", shape, f.Templates, f.Revisions, len(f.Raw))
		})
	}
}

func BenchmarkTemplateReads(b *testing.B) {
	ctx := context.Background()
	for _, shape := range []string{"small", "revisions", "bytes"} {
		f := testkit.Capacity(b, shape)
		for _, backend := range []string{"memory", "file"} {
			b.Run(shape+"/"+backend, func(b *testing.B) {
				var store writingtemplate.Storage = &writingtemplate.MemoryStorage{}
				if backend == "file" {
					file, err := writingtemplate.NewFileStorage(filepath.Join(b.TempDir(), "writing-templates.json"))
					if err != nil {
						b.Fatal(err)
					}
					store = file
				}
				if err := store.Write(ctx, sha256.Sum256(nil), f.Raw); err != nil {
					b.Fatal(err)
				}
				s, err := writingtemplate.New(ctx, store)
				if err != nil {
					b.Fatal(err)
				}
				operations := []struct {
					name string
					run  func() error
				}{
					{"cold", func() error { _, err := writingtemplate.New(ctx, store); return err }},
					{"catalog", func() error { _, err := s.Catalog(ctx); return err }},
					{"list", func() error { _, err := s.List(ctx, true, 24, 0); return err }},
					{"resolve", func() error { _, err := s.Resolve(ctx, f.ID, f.Version); return err }},
				}
				for _, operation := range operations {
					b.Run(operation.name, func(b *testing.B) {
						b.ReportAllocs()
						b.SetBytes(int64(len(f.Raw)))
						for b.Loop() {
							if err := operation.run(); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			})
		}
	}
}
