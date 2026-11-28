package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func contentHistoryLoadingFixture(t *testing.T, posts, versions, owners int) *MemoryRepository {
	t.Helper()
	state := contentFileEncodingFixture(t, posts, 1024)
	stamp := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return stamp }
	for i := range state.terms {
		state.terms[i].CreatedAt, state.terms[i].UpdatedAt = stamp, stamp
	}
	for i := range state.posts {
		post := &state.posts[i]
		post.CreatedAt, post.UpdatedAt, post.PublishedAt = stamp, stamp, stamp
		for _, terms := range [][]Term{post.Categories, post.Tags} {
			for j := range terms {
				terms[j].CreatedAt, terms[j].UpdatedAt = stamp, stamp
			}
		}
		post.Meta = map[string]any{"nested": []any{"中文", json.Number("9007199254740993")}}
		post.Version = int64(max(1, versions))
		for version := range versions {
			snapshot := clonePost(*post)
			snapshot.Version = int64(version + 1)
			state.revisions[post.ID] = append(state.revisions[post.ID], Revision{ID: state.nextRevisionID,
				PostID: post.ID, Version: snapshot.Version, Snapshot: snapshot, Actor: "editor", CreatedAt: stamp})
			state.nextRevisionID++
		}
		if owners > 0 {
			state.autosaves[post.ID] = make(map[string]Autosave)
		}
		for owner := range owners {
			key := fmt.Sprintf("user:%d", owner+1)
			state.autosaves[post.ID][key] = Autosave{PostID: post.ID, OwnerKey: key, SourceVersion: post.Version,
				Snapshot: clonePost(*post), UpdatedAt: stamp, ExpiresAt: stamp.Add(24 * time.Hour)}
		}
	}
	return state
}

func contentHistoryLoadingBytes(t *testing.T, document contentDocument) []byte {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, '\n')
}

func TestContentHistoryLoadingInterleaved(t *testing.T) {
	source := contentHistoryLoadingFixture(t, 4, 7, 3)
	document := contentDocumentForEncoding(source)
	document.Revisions = nil
	// Global IDs need not be in order. Per-article version order is the
	// persisted contract and must survive noncontiguous groups unchanged.
	for version := range 7 {
		for postID := int64(4); postID > 0; postID-- {
			document.Revisions = append(document.Revisions, source.revisions[postID][version])
		}
	}
	for i := 0; i < len(document.Autosaves)/2; i++ {
		j := len(document.Autosaves) - 1 - i
		document.Autosaves[i], document.Autosaves[j] = document.Autosaves[j], document.Autosaves[i]
	}
	payload := contentHistoryLoadingBytes(t, document)
	original := bytes.Clone(payload)
	loaded, err := decodeContentDocument(payload)
	if err != nil || !bytes.Equal(payload, original) {
		t.Fatal("history decoding failed or changed input", err)
	}
	loaded.now = source.now
	if !reflect.DeepEqual(loaded.revisions, source.revisions) || !reflect.DeepEqual(loaded.autosaves, source.autosaves) ||
		!bytes.Equal(contentCandidateBytes(t, loaded), contentCandidateBytes(t, source)) {
		t.Fatal("grouping changed stored histories or metadata numbers")
	}
	clear(payload)
	if !bytes.Equal(contentCandidateBytes(t, loaded), contentCandidateBytes(t, source)) {
		t.Fatal("decoded state borrowed input bytes")
	}
	changed, err := loaded.ChangeStatus(t.Context(), 1, Mutation{ExpectedVersion: 7}, StatusPrivate)
	if err != nil || changed.Version != 8 || len(loaded.revisions[1]) != 8 || len(loaded.autosaves[1]) != 0 {
		t.Fatal("first mutation after loading lost history or recovery cleanup", err)
	}
	for id := int64(2); id <= 4; id++ {
		if !reflect.DeepEqual(loaded.revisions[id], source.revisions[id]) || !reflect.DeepEqual(loaded.autosaves[id], source.autosaves[id]) {
			t.Fatal("appending to one history changed another group")
		}
	}
}

func TestContentHistoryLoadingRejectsBadRecords(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*contentDocument)
		message string
	}{
		{"duplicate-id", func(d *contentDocument) { d.Revisions[1].ID = d.Revisions[0].ID }, "invalid stored content revision"},
		{"orphan-history", func(d *contentDocument) { d.Revisions[1].PostID = 999 }, "invalid stored content revision"},
		{"zero-id", func(d *contentDocument) { d.Revisions[1].ID = 0 }, "invalid stored content revision"},
		{"next-id", func(d *contentDocument) { d.Revisions[1].ID = d.NextRevisionID }, "invalid stored content revision"},
		{"version-order", func(d *contentDocument) { d.Revisions[0], d.Revisions[1] = d.Revisions[1], d.Revisions[0] }, "invalid stored content revision"},
		{"snapshot-id", func(d *contentDocument) { d.Revisions[1].Snapshot.ID++ }, "invalid stored content revision"},
		{"snapshot-version", func(d *contentDocument) { d.Revisions[1].Snapshot.Version++ }, "invalid stored content revision"},
		{"missing-time", func(d *contentDocument) { d.Revisions[1].CreatedAt = time.Time{} }, "invalid stored content revision"},
		{"late-duplicate", func(d *contentDocument) { d.Revisions = append(d.Revisions, d.Revisions[0]) }, "invalid stored content revision"},
		{"duplicate-owner", func(d *contentDocument) { d.Autosaves = append(d.Autosaves, d.Autosaves[0]) }, "duplicate stored content autosave"},
		{"orphan-autosave", func(d *contentDocument) { d.Autosaves[1].PostID = 999 }, "invalid stored content autosave"},
		{"stale-autosave", func(d *contentDocument) { d.Autosaves[1].SourceVersion-- }, "invalid stored content autosave"},
		{"autosave-author", func(d *contentDocument) { d.Autosaves[1].Snapshot.AuthorID++ }, "invalid stored content autosave"},
		{"bad-owner", func(d *contentDocument) { d.Autosaves[1].OwnerKey = "anonymous" }, "invalid stored content autosave"},
		{"autosave-time", func(d *contentDocument) { d.Autosaves[1].ExpiresAt = d.Autosaves[1].UpdatedAt }, "invalid stored content autosave"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := contentDocumentForEncoding(contentHistoryLoadingFixture(t, 3, 4, 2))
			test.change(&document)
			payload := contentHistoryLoadingBytes(t, document)
			original := bytes.Clone(payload)
			loaded, err := decodeContentDocument(payload)
			if err == nil || err.Error() != test.message || loaded != nil || !bytes.Equal(original, payload) {
				t.Fatal("bad history accepted or validation outcome changed", err)
			}
			path := filepath.Join(t.TempDir(), "content.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			opened, err := NewFileRepository(path, false)
			after, readErr := os.ReadFile(path)
			if opened != nil || err == nil || readErr != nil || !bytes.Equal(after, payload) {
				t.Fatal("failed open changed the file or published a repository", err, readErr)
			}
		})
	}
}

func TestContentHistoryLoadingEmptyAndRecovery(t *testing.T) {
	for _, history := range []bool{false, true} {
		source := contentHistoryLoadingFixture(t, 2, 0, 0)
		if history {
			source = contentHistoryLoadingFixture(t, 2, 3, 2)
		}
		document := contentDocumentForEncoding(source)
		if history {
			document.Autosaves[0].Snapshot.Title = ""
			document.Autosaves[0].Snapshot.ContentHTML = "<script>stale</script>"
			document.Revisions[0].Snapshot.ContentHTML = "<script>stale</script>"
			// Expired recovery is still validated and retained on load. The
			// getter/encoder decides expiration using its own current clock.
			document.Autosaves[0].ExpiresAt = document.Autosaves[0].UpdatedAt.Add(time.Nanosecond)
		}
		loaded, err := decodeContentDocument(contentHistoryLoadingBytes(t, document))
		if err != nil || loaded.revisions == nil || loaded.autosaves == nil {
			t.Fatal("loading empty or partial recovery failed", err)
		}
		if !history {
			if len(loaded.revisions) != 0 || len(loaded.autosaves) != 0 {
				t.Fatal("empty history acquired phantom groups")
			}
			continue
		}
		a := document.Autosaves[0]
		got := loaded.autosaves[a.PostID][a.OwnerKey]
		if got.Snapshot.Title != "" || got.Snapshot.ContentHTML != got.Snapshot.ContentSource ||
			loaded.revisions[1][0].Snapshot.ContentHTML != loaded.revisions[1][0].Snapshot.ContentSource {
			t.Fatal("loading changed incomplete source or trusted stale HTML")
		}
		loaded.now = func() time.Time { return a.ExpiresAt }
		if len(loaded.autosaves[a.PostID]) != 2 {
			t.Fatal("loading removed an expired stored record")
		}
		if _, err := loaded.GetAutosave(t.Context(), a.PostID, a.OwnerKey); !errors.Is(err, ErrAutosaveNotFound) {
			t.Fatal("expired recovery became visible", err)
		}
		if len(loaded.autosaves[a.PostID]) != 1 {
			t.Fatal("getter no longer cleaned up the expired owner")
		}
	}
}

func TestContentHistoryLoadingConcurrentAndDetached(t *testing.T) {
	source := contentHistoryLoadingFixture(t, 5, 9, 4)
	payload := contentCandidateBytes(t, source)
	original := bytes.Clone(payload)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 6 {
				loaded, err := decodeContentDocument(payload)
				if err != nil {
					t.Error(err)
					return
				}
				loaded.now = source.now
				if !bytes.Equal(payload, contentCandidateBytes(t, loaded)) {
					t.Error("concurrent decode changed history")
					return
				}
				loaded.revisions[1][0].Snapshot.Meta["nested"].([]any)[0] = "local edit"
				a := loaded.autosaves[1]["user:1"]
				a.Snapshot.Meta["nested"].([]any)[0] = "local edit"
				loaded.autosaves[1]["user:1"] = a
				if source.revisions[1][0].Snapshot.Meta["nested"].([]any)[0] != "中文" ||
					loaded.revisions[1][1].Snapshot.Meta["nested"].([]any)[0] != "中文" ||
					loaded.autosaves[1]["user:2"].Snapshot.Meta["nested"].([]any)[0] != "中文" {
					t.Error("decoded records share mutable metadata")
				}
			}
		})
	}
	group.Wait()
	if !bytes.Equal(payload, original) {
		t.Fatal("concurrent decoding changed source bytes")
	}
}
