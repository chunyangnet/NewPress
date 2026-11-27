package mediausage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func usageImportBatch() content.ImportBatch {
	return content.ImportBatch{Source: "notes", Format: "markdown", Items: []content.ImportEntry{{
		Key: "old.md", Digest: strings.Repeat("a", 64),
		Input: content.PostInput{Title: "Old source", Slug: "old", ContentSource: "<p>Original body</p>"},
	}}}
}

func usageImportRepository(t *testing.T, mode string) (content.Repository, string) {
	t.Helper()
	if mode == "memory" {
		return content.NewMemoryRepository(false), ""
	}
	file := filepath.Join(t.TempDir(), "content.json")
	repo, err := content.NewFileRepository(file, false)
	if err != nil {
		t.Fatal(err)
	}
	return repo, file
}

func TestImportReplaySurvivesUnavailableHistoricalMedia(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, action := range []string{"skip", "changed", "metadata_changed"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				raw, file := usageImportRepository(t, mode)
				s, item := newUsageTestService(t, raw)
				repo := s.Content()
				batch := usageImportBatch()
				if action == "metadata_changed" {
					batch.Format = "wechat"
					batch.Items[0].MetadataDigest = strings.Repeat("a", 64)
				}
				batch.Items[0].Input.CoverURL = item.PublicURL
				batch.Items[0].Input.ContentSource = `<a href="` + item.PublicURL + `">Original attachment</a>`
				preview, err := repo.PreviewImport(t.Context(), batch)
				if err != nil {
					t.Fatal(err)
				}
				applied, err := repo.ApplyImport(t.Context(), batch, preview.Revision)
				if err != nil {
					t.Fatal(err)
				}
				old, err := repo.Update(t.Context(), applied.Items[0].PostID, content.Mutation{ExpectedVersion: applied.Items[0].Version}, content.PostInput{
					Title: "Manual edit", Slug: "renamed", AuthorID: 7, ContentSource: "<p>Saved edit</p>",
				})
				if err != nil {
					t.Fatal(err)
				}
				// Simulate media metadata changing outside the application guard
				// after a committed import, as in a mismatched media restore.
				if _, err := s.library.Trash(item.ID, item.Version); err != nil {
					t.Fatal(err)
				}
				if action == "changed" {
					batch.Items[0].Digest = strings.Repeat("b", 64)
				}
				if action == "metadata_changed" {
					batch.Items[0].MetadataDigest = strings.Repeat("b", 64)
				}
				check := func(plan content.ImportPlan, err error) {
					t.Helper()
					want := action
					if want == "metadata_changed" {
						want = "changed"
					}
					if err != nil || plan.New != 0 || plan.Existing != 1 || len(plan.Items) != 1 || plan.Items[0].Action != want || plan.Items[0].PostID != old.ID || plan.Items[0].Version != old.Version {
						t.Errorf("retained import rejected or changed: %+v, %v", plan, err)
					}
				}
				check(repo.PreviewImport(t.Context(), batch))
				check(repo.ApplyImport(t.Context(), batch, preview.Revision))
				if file != "" {
					raw, err = content.NewFileRepository(file, false)
					if err != nil {
						t.Fatal(err)
					}
				}
				retained, err := raw.GetByID(t.Context(), old.ID)
				if err != nil || retained.Version != old.Version || retained.Title != old.Title || retained.ContentHTML != old.ContentHTML || retained.AuthorID != old.AuthorID || retained.CoverURL != "" {
					t.Fatalf("replay changed the manually edited target: %+v, %v", retained, err)
				}
			})
		}
	}
}

func TestMixedImportValidatesOnlyNewMediaReferences(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			raw, file := usageImportRepository(t, mode)
			s, unused := newUsageTestService(t, raw)
			repo := s.Content()
			batch := usageImportBatch()
			preview, err := repo.PreviewImport(t.Context(), batch)
			if err != nil {
				t.Fatal(err)
			}
			applied, err := repo.ApplyImport(t.Context(), batch, preview.Revision)
			if err != nil {
				t.Fatal(err)
			}
			old, err := raw.GetByID(t.Context(), applied.Items[0].PostID)
			if err != nil {
				t.Fatal(err)
			}
			// The next source version references an unused image that was
			// legitimately trashed. This entry will be retained, not rewritten.
			if err := s.Trash(t.Context(), unused.ID, unused.Version); err != nil {
				t.Fatal(err)
			}
			batch.Items[0].Digest = strings.Repeat("b", 64)
			batch.Items[0].Input.CoverURL = unused.PublicURL
			batch.Items = append(batch.Items, content.ImportEntry{
				Key: "new.md", Digest: strings.Repeat("c", 64),
				Input: content.PostInput{Title: "New source", Slug: "new", CoverURL: unused.PublicURL},
			})
			if _, err := repo.PreviewImport(t.Context(), batch); content.FieldErrors(err)["cover_url"] == "" {
				t.Fatal("new item bypassed media validation", err)
			}
			unsafe, err := raw.PreviewImport(t.Context(), batch)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ApplyImport(t.Context(), batch, unsafe.Revision); content.FieldErrors(err)["cover_url"] == "" {
				t.Fatal("apply accepted a new invalid reference", err)
			}
			valid, err := s.library.Upload(t.Context(), strings.NewReader("%PDF-1.7\nnew attachment\n%%EOF\n"), media.UploadInput{Filename: "new.pdf"}, 8192)
			if err != nil {
				t.Fatal(err)
			}
			batch.Items[1].Input.CoverURL = valid.PublicURL
			preview, err = repo.PreviewImport(t.Context(), batch)
			if err != nil || preview.New != 1 || preview.Existing != 1 || preview.Items[0].Action != "changed" {
				t.Fatalf("existing source blocked the valid new item: %+v, %v", preview, err)
			}
			if err := s.Trash(t.Context(), valid.ID, valid.Version); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ApplyImport(t.Context(), batch, preview.Revision); content.FieldErrors(err)["cover_url"] == "" {
				t.Fatal("media trashed after preview was accepted", err)
			}
			unchanged, err := raw.ListAdmin(t.Context(), content.AdminQuery{})
			if err != nil || unchanged.Total != 1 {
				t.Fatalf("rejected import created partial content: %+v, %v", unchanged, err)
			}
			if err := s.Restore(t.Context(), valid.ID, valid.Version+1); err != nil {
				t.Fatal(err)
			}
			applied, err = repo.ApplyImport(t.Context(), batch, preview.Revision)
			if err != nil || applied.Items[0].Action != "changed" || applied.Items[1].Action != "created" {
				t.Fatalf("valid mixed import failed: %+v, %v", applied, err)
			}
			if file != "" {
				raw, err = content.NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			retained, err := raw.GetByID(t.Context(), old.ID)
			if err != nil || retained.Version != old.Version || retained.ContentHTML != old.ContentHTML || retained.CoverURL != old.CoverURL {
				t.Fatalf("existing source was overwritten: %+v, %v", retained, err)
			}
			created, err := raw.GetByID(t.Context(), applied.Items[1].PostID)
			if err != nil || created.CoverURL != valid.PublicURL {
				t.Fatalf("new item missing its validated media: %+v, %v", created, err)
			}
			if err := s.Trash(t.Context(), valid.ID, valid.Version+2); !errors.Is(err, ErrInUse) {
				t.Fatalf("new media reference did not block deletion: %v", err)
			}
		})
	}
}

type pausedImport struct {
	content.Repository
	entered chan struct{}
	proceed chan struct{}
}

func (r pausedImport) ApplyImport(ctx context.Context, batch content.ImportBatch, revision string) (content.ImportPlan, error) {
	close(r.entered)
	<-r.proceed
	return r.Repository.ApplyImport(ctx, batch, revision)
}

func TestImportAndMediaRemovalAreSerialized(t *testing.T) {
	for _, importFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(importFirst), func(t *testing.T) {
			s, item := newUsageTestService(t, content.NewMemoryRepository(false))
			batch := usageImportBatch()
			batch.Items[0].Input.CoverURL = item.PublicURL
			preview, err := s.Content().PreviewImport(t.Context(), batch)
			if err != nil {
				t.Fatal(err)
			}
			applied, removed := make(chan error, 1), make(chan error, 1)
			if importFirst {
				paused := pausedImport{Repository: s.content, entered: make(chan struct{}), proceed: make(chan struct{})}
				s.content = paused
				go func() { _, err := s.Content().ApplyImport(t.Context(), batch, preview.Revision); applied <- err }()
				<-paused.entered
				go func() { removed <- s.Trash(t.Context(), item.ID, item.Version) }()
				select {
				case err := <-removed:
					close(paused.proceed)
					t.Fatalf("deletion crossed pending import: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
				close(paused.proceed)
				if err := <-applied; err != nil {
					t.Fatal(err)
				}
				if err := <-removed; !errors.Is(err, ErrInUse) {
					t.Fatalf("imported reference did not protect media: %v", err)
				}
			} else {
				paused := pausedScan{Repository: s.content, entered: make(chan struct{}), proceed: make(chan struct{})}
				s.content = paused
				go func() { removed <- s.Trash(t.Context(), item.ID, item.Version) }()
				<-paused.entered
				go func() { _, err := s.Content().ApplyImport(t.Context(), batch, preview.Revision); applied <- err }()
				select {
				case err := <-applied:
					close(paused.proceed)
					t.Fatalf("import crossed pending deletion: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
				close(paused.proceed)
				if err := <-removed; err != nil {
					t.Fatal(err)
				}
				if err := <-applied; content.FieldErrors(err)["cover_url"] == "" {
					t.Fatalf("import persisted a reference after deletion: %v", err)
				}
			}
		})
	}
}

type changedImportState struct {
	content.Repository
	preview content.Repository
}

func (r changedImportState) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	return r.preview.PreviewImport(ctx, batch)
}

func TestImportRechecksRetainedPlanBeforeCreatingReferences(t *testing.T) {
	previous := content.NewMemoryRepository(false)
	batch := usageImportBatch()
	preview, err := previous.PreviewImport(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ApplyImport(t.Context(), batch, preview.Revision); err != nil {
		t.Fatal(err)
	}
	current := content.NewMemoryRepository(false)
	// Use two real states to model an import mapping disappearing between the
	// guard's preview and the repository's transaction.
	s, item := newUsageTestService(t, changedImportState{Repository: current, preview: previous})
	if err := s.Trash(t.Context(), item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	batch.Items[0].Input.CoverURL = item.PublicURL
	retained, err := s.Content().PreviewImport(t.Context(), batch)
	if err != nil || retained.Existing != 1 {
		t.Fatalf("retained preview failed: %+v, %v", retained, err)
	}
	if _, err := s.Content().ApplyImport(t.Context(), batch, retained.Revision); !errors.Is(err, content.ErrImportConflict) {
		t.Fatalf("changed plan was not rejected at commit: %v", err)
	}
	list, err := current.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil || list.Total != 0 {
		t.Fatalf("stale retained plan created unvalidated references: %+v, %v", list, err)
	}
}
