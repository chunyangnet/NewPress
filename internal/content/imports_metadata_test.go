package content

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportMetadataValidationAndStoredSnapshot(t *testing.T) {
	for _, test := range []struct {
		name, format, digest string
	}{
		{"missing", "wechat", ""},
		{"short", "wechat", "abc"},
		{"uppercase", "wechat", strings.Repeat("A", 64)},
		{"nonhex", "wechat", strings.Repeat("x", 64)},
		{"markdown", "markdown", strings.Repeat("a", 64)},
		{"wordpress", "wordpress", strings.Repeat("a", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			batch := importFixture()
			batch.Format = test.format
			if batch.Format == "wechat" {
				for index := range batch.Items {
					batch.Items[index].MetadataDigest = strings.Repeat("b", 64)
				}
			}
			plan, err := r.PreviewImport(t.Context(), batch)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ApplyImport(t.Context(), batch, plan.Revision); err != nil {
				t.Fatal(err)
			}
			batch.Items[0].MetadataDigest = test.digest
			if _, err = r.PreviewImport(t.Context(), batch); len(FieldErrors(err)) == 0 {
				t.Fatal("invalid metadata accepted in preview", err)
			}
			before, err := os.ReadFile(r.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ApplyImport(t.Context(), batch, plan.Revision); len(FieldErrors(err)) == 0 {
				t.Fatal("invalid metadata accepted on apply", err)
			}
			after, err := os.ReadFile(r.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid batch wrote the snapshot", err)
			}
			var doc contentDocument
			if err = json.Unmarshal(before, &doc); err != nil {
				t.Fatal(err)
			}
			doc.Imports[0].MetadataDigest = test.digest
			corrupt, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = decodeContentDocument(corrupt); err == nil {
				t.Fatal("invalid stored metadata accepted")
			}
		})
	}
}

func TestImportMetadataCountsAndInvalidStoredMapping(t *testing.T) {
	batch, err := NormalizeImportBatch(ImportBatch{Source: "test", Format: "wechat", Items: []ImportEntry{{Key: "old", Digest: strings.Repeat("a", 64), MetadataDigest: strings.Repeat("b", 64), Input: PostInput{Title: "Old", Slug: "old"}}, {Key: "new", Digest: strings.Repeat("c", 64), MetadataDigest: strings.Repeat("d", 64), Input: PostInput{Title: "New", Slug: "new"}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state := ImportState{Mappings: []ImportMapping{{Source: batch.Source, Format: batch.Format, Key: "old", Digest: batch.Items[0].Digest, MetadataDigest: batch.Items[0].MetadataDigest, PostID: 1}}, Posts: []Post{{ID: 1, Type: TypePost, Slug: "old", Status: StatusDraft, Version: 2}}, MappingCount: 1}
	plan := BuildImportPlan(batch, state)
	if plan.New != 1 || plan.Existing != 1 || plan.Conflicts != 0 || plan.Items[0].Action != "skip" {
		t.Fatal(plan)
	}
	// A different metadata observation cannot reuse approval for a new post.
	batch.Items[0].MetadataDigest = strings.Repeat("e", 64)
	if err = CheckImportPlan(BuildImportPlan(batch, state), plan.Revision); err != ErrImportConflict {
		t.Fatal("changed source metadata reused approval", err)
	}
	state.Mappings[0].MetadataDigest = ""
	plan = BuildImportPlan(batch, state)
	if plan.Items[0].Action != "conflict" || plan.Conflicts != 1 {
		t.Fatal("incomplete stored mapping accepted", plan)
	}
	state.Mappings[0].MetadataDigest = batch.Items[0].MetadataDigest
	state.Posts = nil
	plan = BuildImportPlan(batch, state)
	if plan.Items[0].Action != "conflict" || plan.Conflicts != 1 {
		t.Fatal("missing target accepted", plan)
	}
}
