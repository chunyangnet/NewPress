package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestContentFileReadinessHashesAllBytes(t *testing.T) {
	for _, size := range []int{1, 32767, 32768, 32769, 1<<20 + 7} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i*131 + 17)
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			r := &FileRepository{path: path, digest: sha256.Sum256(payload)}
			if err := r.Ready(t.Context()); err != nil {
				t.Fatal("unchanged bytes", err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			payload[len(payload)-1] ^= 1
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := r.Ready(t.Context()); err == nil || !strings.Contains(err.Error(), "changed outside this process") {
				t.Fatal("same-size/time change at the last byte must be detected", err)
			}
		})
	}
}

func TestContentFileReadinessExternalEditPreservesCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), PostInput{Title: "original", Slug: "digest", Status: StatusPublish,
		ContentSource: "<p>needle</p>", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	external := bytes.ReplaceAll(original, []byte("original"), []byte("external"))
	if len(external) != len(original) || bytes.Equal(external, original) {
		t.Fatal("invalid same-length edit fixture")
	}
	if err := os.WriteFile(path, external, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := r.Ready(t.Context()); err == nil {
		t.Fatal("external edit passed readiness")
	}
	if _, err := r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusPrivate); err == nil {
		t.Fatal("write overwrote external bytes")
	}
	current, err := r.GetByID(t.Context(), p.ID)
	if err != nil || !reflect.DeepEqual(current, p) {
		t.Fatal("failed write changed memory", err)
	}
	page, err := r.SearchPublished(t.Context(), PublishedQuery{Search: "needle"})
	if err != nil || page.Total != 1 || page.Items[0].Title != "original" {
		t.Fatal("failed write changed search state", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, external) {
		t.Fatal("external bytes were overwritten", err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Ready(t.Context()); err != nil {
		t.Fatal("restored original bytes rejected", err)
	}
	if _, err := r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusPrivate); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err = reopened.GetByID(t.Context(), p.ID)
	if err != nil || current.Status != StatusPrivate || current.Version != p.Version+1 {
		t.Fatal("valid commit did not survive reopen", err)
	}
}

func TestContentFileReadinessRejectsInvalidFiles(t *testing.T) {
	for _, size := range []int64{0, maxContentFileBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.json")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(size); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			r := &FileRepository{path: path}
			if err := r.Ready(t.Context()); err == nil || !strings.Contains(err.Error(), "size is invalid") {
				t.Fatal("invalid file size accepted", err)
			}
		})
	}
	t.Run("directory", func(t *testing.T) {
		r := &FileRepository{path: t.TempDir()}
		if err := r.Ready(t.Context()); err == nil {
			t.Fatal("directory accepted")
		}
	})
	t.Run("missing", func(t *testing.T) {
		r := &FileRepository{path: filepath.Join(t.TempDir(), "missing")}
		if err := r.Ready(t.Context()); err == nil {
			t.Fatal("missing file accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
		if err := os.WriteFile(target, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlink unavailable:", err)
		}
		r := &FileRepository{path: link, digest: sha256.Sum256([]byte("data"))}
		if err := r.Ready(t.Context()); err == nil {
			t.Fatal("symlink accepted")
		}
	})
}

func BenchmarkContentFileReadiness(b *testing.B) {
	for _, size := range []int{1024, 1 << 20, 16 << 20, 64 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			payload := bytes.Repeat([]byte("x"), size)
			path := filepath.Join(b.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				b.Fatal(err)
			}
			r := &FileRepository{path: path, digest: sha256.Sum256(payload)}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if err := r.Ready(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
