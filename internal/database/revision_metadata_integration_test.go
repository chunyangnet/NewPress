package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLRevisionRestoresEditorialMetadata(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewPostRepository(pool)
	for _, kind := range []string{content.TypePost, content.TypePage, content.TypeFlash} {
		t.Run(kind, func(t *testing.T) {
			pinned := true
			input := content.PostInput{Type: kind, Title: "Original", Slug: "metadata-" + kind, ContentSource: "Original body", Status: content.StatusPublish, PublishedAt: time.Now().Add(-24 * time.Hour), Pinned: &pinned, ArticleBadge: &content.ArticleBadge{Kind: "original"}, AIPolicy: "off", AITopics: "original topics"}
			if kind == content.TypeFlash {
				input.Flash = &content.FlashFields{SourceName: "Original source", SourceURL: "https://example.test/original", Important: true, ReviewNote: "Original review"}
			}
			original, err := repo.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			originalRevisions, err := repo.ListRevisions(ctx, original.ID, 10)
			if err != nil || len(originalRevisions) != 1 {
				t.Fatal(originalRevisions, err)
			}
			pinned = false
			input.Title, input.ContentSource = "Edited", "Edited body"
			input.Status, input.PublishedAt = content.StatusFuture, time.Now().Add(24*time.Hour)
			input.ArticleBadge = &content.ArticleBadge{}
			input.AIPolicy, input.AITopics = "on", "edited topics"
			if input.Flash != nil {
				input.Flash = &content.FlashFields{SourceName: "Edited source", SourceURL: "https://example.test/edited", ReviewNote: "Edited review"}
			}
			edited, err := repo.Update(ctx, original.ID, content.Mutation{ExpectedVersion: original.Version}, input)
			if err != nil {
				t.Fatal(err)
			}
			editedRevisions, err := repo.ListRevisions(ctx, original.ID, 10)
			if err != nil || len(editedRevisions) != 2 {
				t.Fatal(editedRevisions, err)
			}
			check := func(want content.Post, version int64) content.Post {
				t.Helper()
				got, err := NewPostRepository(pool).GetByID(ctx, original.ID)
				if err != nil {
					t.Fatal(err)
				}
				gotMeta, err := json.Marshal(got.Meta)
				if err != nil {
					t.Fatal(err)
				}
				wantMeta, err := json.Marshal(want.Meta)
				if err != nil {
					t.Fatal(err)
				}
				if string(gotMeta) != string(wantMeta) || got.Title != want.Title || got.ContentSource != want.ContentSource {
					t.Errorf("revision did not restore editorial fields: meta=%s want=%s title=%q body=%q", gotMeta, wantMeta, got.Title, got.ContentSource)
				}
				if got.Version != version || got.Status != edited.Status || !got.PublishedAt.Equal(edited.PublishedAt) || got.AuthorID != edited.AuthorID || content.PublicAt(got, time.Now()) {
					t.Errorf("restore changed workflow or identity: %+v", got)
				}
				latest, err := repo.ListRevisions(ctx, original.ID, 1)
				if err != nil || len(latest) != 1 {
					t.Fatal(latest, err)
				}
				recorded, err := json.Marshal(latest[0].Snapshot.Meta)
				if err != nil || string(recorded) != string(gotMeta) {
					t.Fatalf("restored metadata and new revision diverged: %s %v", recorded, err)
				}
				return got
			}
			if _, err := repo.RestoreRevision(ctx, original.ID, originalRevisions[0].ID, content.Mutation{ExpectedVersion: edited.Version}); err != nil {
				t.Fatal(err)
			}
			restored := check(original, edited.Version+1)
			key := sha256.Sum256([]byte("restore-metadata-" + kind))
			digest := hex.EncodeToString(key[:])
			request := content.Submission{Key: digest, Fingerprint: digest, Action: "restore_revision", PostID: restored.ID, Version: restored.Version, RevisionID: editedRevisions[0].ID}
			if _, err := repo.Submit(ctx, request); err != nil {
				t.Fatal(err)
			}
			check(edited, restored.Version+1)
			if result, err := repo.Submit(ctx, request); err != nil || !result.Replayed {
				t.Fatalf("restore replay = %+v, %v", result, err)
			}
			check(edited, restored.Version+1)
		})
	}
}
