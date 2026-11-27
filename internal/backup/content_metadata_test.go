package backup

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestBackupRestoresExactRecoveryMetadata(t *testing.T) {
	f := fixtureSite(t, false)
	posts, err := content.NewFileRepository(filepath.Join(f.cfg.DataDir, "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := posts.Create(t.Context(), content.PostInput{Title: "元数据备份", Slug: "exact-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	post.Meta = map[string]any{
		"integer": json.Number("9007199254740993"), "decimal": json.Number("0.1234567890123456789"),
		"huge": json.Number("1e400"), "small": json.Number("1e-400"),
		"nested": []any{map[string]any{"bytes": []byte{0xff, 0x80}}},
	}
	want, err := json.Marshal(post.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := posts.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, SourceVersion: post.Version, OwnerKey: "user:1", Snapshot: post, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "exact-metadata.zip")
	if _, err := Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-metadata"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatalf("restored recovery failed application validation: %v", err)
	}
	posts, err = content.NewFileRepository(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := posts.GetAutosave(t.Context(), post.ID, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(got.Snapshot.Meta)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("backup changed metadata: got=%s want=%s err=%v", actual, want, err)
	}
	if _, err := posts.Create(t.Context(), content.PostInput{Title: "恢复后写入", Slug: "after-restore-metadata"}); err != nil {
		t.Fatal(err)
	}
	posts, err = content.NewFileRepository(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err = posts.GetAutosave(t.Context(), post.ID, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	actual, err = json.Marshal(got.Snapshot.Meta)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatal("writing after restore changed exact metadata")
	}
}
