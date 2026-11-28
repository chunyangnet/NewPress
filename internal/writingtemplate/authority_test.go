package writingtemplate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type authorityStorage struct {
	Storage
	afterRead func()
	writes    int
}

func (s *authorityStorage) Read(ctx context.Context) ([]byte, error) {
	raw, err := s.Storage.Read(ctx)
	if s.afterRead != nil {
		after := s.afterRead
		s.afterRead = nil
		after()
	}
	return raw, err
}

func (s *authorityStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	s.writes++
	return s.Storage.Write(ctx, expected, raw)
}

func TestApplyAuthorityRefusalPreservesLibraryAndRequestKey(t *testing.T) {
	denied := errors.New("authority revoked")
	for _, backend := range []string{"memory", "file"} {
		for _, boundary := range []string{"read", "preparation", "cancel", "missing-check"} {
			t.Run(backend+"/"+boundary, func(t *testing.T) {
				var storage Storage = &MemoryStorage{}
				if backend == "file" {
					file, err := NewFileStorage(filepath.Join(t.TempDir(), "templates.json"))
					if err != nil {
						t.Fatal(err)
					}
					storage = file
				}
				store := &authorityStorage{Storage: storage}
				service, err := New(t.Context(), store)
				if err != nil {
					t.Fatal(err)
				}
				first, _ := newTemplate(t, service, 1)
				before, err := storage.Read(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				command := templateCommand("archive", first.ID, first.Version, 2)
				store.writes = 0
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				revoked := false
				authorize := func() error {
					if revoked {
						return denied
					}
					return nil
				}
				want := denied
				switch boundary {
				case "read":
					store.afterRead = func() { revoked = true }
				case "preparation":
					service.now = func() time.Time { revoked = true; return time.Now() }
				case "cancel":
					authorize = func() error { cancel(); return nil }
					want = context.Canceled
				case "missing-check":
					authorize = nil
					want = ErrInvalid
				}
				result, err := service.ApplyAuthorized(ctx, command, authorize)
				if !errors.Is(err, want) || result != (Result{}) || store.writes != 0 {
					t.Fatalf("rejected command reached storage: result=%+v writes=%d err=%v", result, store.writes, err)
				}
				current, err := storage.Read(t.Context())
				if err != nil || !bytes.Equal(current, before) {
					t.Fatal("refusal changed library bytes", err)
				}
				service.now = time.Now
				revoked = false
				item, err := service.Get(t.Context(), first.ID)
				if err != nil || len(item.Revisions) != 1 || item.Revisions[0].Archived {
					t.Fatal("refused command changed cached history", item, err)
				}
				result, err = service.ApplyAuthorized(t.Context(), command, func() error { return nil })
				if err != nil || result.Replayed || result.Version != 2 || store.writes != 1 {
					t.Fatal("refusal consumed request key or prevented retry", result, err)
				}
			})
		}
	}
}

func TestApplyAuthorityChecksIdempotentReplay(t *testing.T) {
	store := &authorityStorage{Storage: &MemoryStorage{}}
	service, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	created, command := newTemplate(t, service, 1)
	store.writes = 0
	denied := errors.New("replay authority revoked")
	if result, err := service.ApplyAuthorized(t.Context(), command, func() error { return denied }); !errors.Is(err, denied) || result != (Result{}) {
		t.Fatal("revoked replay disclosed receipt", result, err)
	}
	result, err := service.ApplyAuthorized(t.Context(), command, func() error { return nil })
	if err != nil || !result.Replayed || result.ID != created.ID || result.Version != created.Version || store.writes != 0 {
		t.Fatal("authorized replay changed the library", result, err)
	}
}
