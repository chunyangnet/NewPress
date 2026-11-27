package content

import (
	"errors"
	"newpress.local/newpress/internal/activity"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func importFixture() ImportBatch {
	return ImportBatch{Source: "old-site", Format: "markdown", Items: []ImportEntry{{Key: "a.md", Digest: strings.Repeat("a", 64), Input: PostInput{Title: "First", Slug: "import-first", ContentSource: "<p>first</p>"}, Categories: []string{"设计", "笔记"}, OldPath: "/2020/first/"}, {Key: "b.md", Digest: strings.Repeat("b", 64), Input: PostInput{Title: "Second", Slug: "import-second"}}}}
}
func TestImportTransactions(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			file := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				var err error
				r, err = NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			batch := importFixture()
			batch.Items[0].Input.AuthorID = 1
			batch.Items[0].Input.Status = StatusPublish
			batch.Items[0].Input.PublishedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			plan, err := r.PreviewImport(t.Context(), batch)
			if err != nil || plan.New != 2 {
				t.Fatal(plan, err)
			}
			before, _ := r.ListAdmin(t.Context(), AdminQuery{})
			if before.Total != 0 {
				t.Fatal("preview wrote content")
			}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := r.ApplyImport(t.Context(), batch, plan.Revision); results <- err }()
			}
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "file" {
				r, err = NewFileRepository(file, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			again, err := r.PreviewImport(t.Context(), batch)
			events, eventErr := r.(activity.Source).PendingActivity(t.Context(), time.Now(), 100)
			if eventErr != nil || len(events) != 0 {
				t.Fatal("historical import emitted publication notifications", events, eventErr)
			}
			if err != nil || again.New != 0 || again.Existing != 2 {
				t.Fatal(again, err)
			}
			id := again.Items[0].PostID
			p, _ := r.GetByID(t.Context(), id)
			if len(p.Categories) != 2 {
				t.Fatal(p)
			}
			p, err = r.Update(t.Context(), id, Mutation{ExpectedVersion: p.Version}, PostInput{Title: "Manual edit", Slug: p.Slug, Status: StatusPublish})
			if err != nil {
				t.Fatal(err)
			}
			batch.Items[0].Digest = strings.Repeat("c", 64)
			result, err := r.ApplyImport(t.Context(), batch, plan.Revision)
			if err != nil || result.Items[0].Action != "changed" {
				t.Fatal(result, err)
			}
			p, _ = r.GetByID(t.Context(), id)
			if p.Title != "Manual edit" {
				t.Fatal("import overwrote edited content")
			}
			if target, err := r.ResolveRedirect(t.Context(), "/2020/first/"); err != nil || target != "/posts/import-first" {
				t.Fatal(target, err)
			}
			collision := importFixture()
			collision.Source = "other-site"
			collision.Items[0].Input.Slug = "unique-new"
			collision.Items[0].OldPath = ""
			collision.Items[1].Input.Slug = p.Slug
			conflict, _ := r.PreviewImport(t.Context(), collision)
			if conflict.Conflicts != 1 {
				t.Fatal(conflict)
			}
			if _, err = r.ApplyImport(t.Context(), collision, conflict.Revision); !errors.Is(err, ErrImportConflict) {
				t.Fatal(err)
			}
			after, _ := r.ListAdmin(t.Context(), AdminQuery{})
			if after.Total != 2 {
				t.Fatal("partial import", after.Total)
			}
			if kind == "file" {
				clean := importFixture()
				clean.Source = "new-site"
				clean.Items = clean.Items[:1]
				clean.Items[0].Input.Slug = "file-failure"
				clean.Items[0].OldPath = ""
				preview, _ := r.PreviewImport(t.Context(), clean)
				payload, _ := os.ReadFile(file)
				if err = os.WriteFile(file, append(payload, ' '), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = r.ApplyImport(t.Context(), clean, preview.Revision); err == nil {
					t.Fatal("external edit overwritten")
				}
				current, _ := r.ListAdmin(t.Context(), AdminQuery{})
				if current.Total != 2 {
					t.Fatal("failed write mutated memory")
				}
			}
		})
	}
}
