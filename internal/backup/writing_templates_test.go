package backup

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"newpress.local/newpress/internal/writingtemplate"
	"newpress.local/newpress/internal/writingtemplate/testkit"
)

func TestWritingTemplateBackupHistoryAndReplay(t *testing.T) {
	f := fixtureSite(t, false)
	path := filepath.Join(f.cfg.DataDir, "writing-templates.json")
	storage, err := writingtemplate.NewFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := writingtemplate.New(t.Context(), storage)
	if err != nil {
		t.Fatal(err)
	}
	definition := writingtemplate.Definition{BasePrompt: "article", Title: "备份团队模板", Instruction: "保留事实和来源。"}
	create := writingtemplate.Command{Action: "save", Definition: &definition, RequestKey: fmt.Sprintf("%064x", 1), Actor: "user:7"}
	first, err := s.Apply(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), writingtemplate.Command{Action: "archive", ID: first.ID, ExpectedVersion: 1, RequestKey: fmt.Sprintf("%064x", 2), Actor: "user:7"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	archive := filepath.Join(f.parent, "writing-templates.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	result, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-writing-templates"))
	if err != nil || !result.ApplicationValidated {
		t.Fatal(result, err)
	}
	restoredPath := filepath.Join(result.Environment["NEWPRESS_DATA_DIR"], "writing-templates.json")
	after, err := os.ReadFile(restoredPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("template backup changed history or receipts", err)
	}
	restoredStore, _ := writingtemplate.NewFileStorage(restoredPath)
	restored, err := writingtemplate.New(t.Context(), restoredStore)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restored.Apply(t.Context(), create)
	if err != nil || !replay.Replayed || replay.ID != first.ID || replay.Version != 1 {
		t.Fatal(replay, err)
	}
	if err = os.WriteFile(path, []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	badArchive := filepath.Join(f.parent, "bad-writing-templates.zip")
	if _, err = Create(t.Context(), f.cfg, badArchive); err == nil {
		t.Fatal("backup accepted corrupt library")
	}
	if _, err = os.Stat(badArchive); !os.IsNotExist(err) {
		t.Fatal("failed backup published archive", err)
	}
}

func TestWritingTemplateCapacityBackupRestore(t *testing.T) {
	for _, shape := range []string{"revisions", "bytes"} {
		t.Run(shape, func(t *testing.T) {
			f := fixtureSite(t, false)
			library := testkit.Capacity(t, shape)
			storage, err := writingtemplate.NewFileStorage(filepath.Join(f.cfg.DataDir, "writing-templates.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = storage.Write(t.Context(), sha256.Sum256(nil), library.Raw); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(f.parent, "capacity.zip")
			if result, err := Create(t.Context(), f.cfg, archive); err != nil || !result.ApplicationValidated {
				t.Fatal("capacity backup validation", result, err)
			}
			result, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-capacity"))
			if err != nil || !result.ApplicationValidated {
				t.Fatal("capacity restore validation", result, err)
			}
			path := filepath.Join(result.Environment["NEWPRESS_DATA_DIR"], "writing-templates.json")
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, library.Raw) {
				t.Fatal("capacity restore changed bytes", err)
			}
			restoredStore, err := writingtemplate.NewFileStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := writingtemplate.New(t.Context(), restoredStore)
			if err != nil {
				t.Fatal(err)
			}
			if replay, err := s.Apply(t.Context(), library.Create); err != nil || !replay.Replayed || replay.ID != library.ID || replay.Version != 1 {
				t.Fatal("capacity restore lost receipt", replay, err)
			}
			t.Logf("constructed %s: revisions=%d encoded_bytes=%d; application-validated file backup/restore and old receipt verified", shape, library.Revisions, len(library.Raw))
		})
	}
}
