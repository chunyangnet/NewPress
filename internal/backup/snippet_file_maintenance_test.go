package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestBackupRestoresRepairedSnippetEvidenceAndRefusesUnresolved(t *testing.T) {
	f := fixtureSite(t, false)
	path := filepath.Join(f.cfg.DataDir, "content.json")
	r, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:backup", Actor: "backup", Read: true, Manage: true}
	s, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>retained independent</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "original", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("a", 64)
	request, err := content.NewSnippetDraftSubmission(u, a)
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	target := content.SnippetFileTarget{SnippetID: s.ID}
	replacement, err := content.ExportSnippetFileRepair(t.Context(), path, target)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var snippets []content.SnippetRecord
	if err = json.Unmarshal(doc["snippets"], &snippets); err != nil {
		t.Fatal(err)
	}
	snippets[0].Revisions[0].ContentHTML = "corrupt"
	doc["snippets"], _ = json.Marshal(snippets)
	raw, _ = json.Marshal(doc)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	review, err := content.ReviewSnippetFileMaintenance(t.Context(), path, target)
	if err != nil {
		t.Fatal(err)
	}
	q, err := content.QuarantineSnippetFile(t.Context(), path, content.SnippetFileCaptureRequest{Target: target, Reference: review.Reference, RequestKey: strings.Repeat("b", 64), Operator: "backup", Reason: "retain damage"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	unresolved, _ := os.ReadFile(path)
	failedArchive := filepath.Join(f.parent, "must-not-publish.zip")
	if _, err = Create(t.Context(), f.cfg, failedArchive); err == nil {
		t.Fatal("archive accepted unresolved file repository")
	}
	if _, err = os.Stat(failedArchive); !os.IsNotExist(err) {
		t.Fatal("failed backup published archive")
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unresolved, unchanged) {
		t.Fatal("failed backup mutated unresolved evidence")
	}
	if _, err = content.RepairSnippetFile(t.Context(), path, content.SnippetFileRepairRequest{ID: q.ID, Operator: "backup", Reason: "verified source", Replacement: replacement, ReplacementSHA256: fmt.Sprintf("%x", sha256.Sum256(replacement))}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	var beforeDoc map[string]json.RawMessage
	if err = json.Unmarshal(before, &beforeDoc); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "repaired-snippet.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-repaired-snippet"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	restoredPath := filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json")
	after, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	var afterDoc map[string]json.RawMessage
	if err = json.Unmarshal(after, &afterDoc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDoc["snippet_quarantines"], afterDoc["snippet_quarantines"]) {
		t.Fatal("archive recovery changed permanent original evidence or audit")
	}
	page, err := content.ListSnippetFileQuarantines(t.Context(), restoredPath, content.SnippetInspectionQuery{Limit: 10})
	if err != nil || page.Total != 1 || page.Unresolved != 0 || page.Items[0].Repair == nil {
		t.Fatal(page, err)
	}
	r, err = content.NewFileRepository(restoredPath, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Submit(t.Context(), request)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Receipt, created.Receipt) {
		t.Fatal(replay, err)
	}
	state, err := r.GetSnippet(t.Context(), s.ID, a)
	if err != nil || state.Visibility != content.SnippetWithdrawn {
		t.Fatal(state, err)
	}
	assertNoStaging(t, f.parent)
}
