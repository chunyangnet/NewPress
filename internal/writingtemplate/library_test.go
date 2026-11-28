package writingtemplate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func templateDefinition() Definition {
	return Definition{BasePrompt: "rewrite", Title: "团队编辑", Description: "保留事实并精简语言", Instruction: "保留事实、引用与代码，不新增资料。", Example: "整理当前草稿"}
}

func templateCommand(action, id string, version int64, key int) Command {
	return Command{Action: action, ID: id, ExpectedVersion: version, RequestKey: fmt.Sprintf("%064x", key), Actor: "user:7"}
}

func newTemplate(t *testing.T, s *Service, key int) (Result, Command) {
	t.Helper()
	d := templateDefinition()
	c := templateCommand("save", "", 0, key)
	c.Definition = &d
	r, err := s.Apply(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return r, c
}

func TestHistoryArchiveRestoreAndReplay(t *testing.T) {
	store := &MemoryStorage{}
	s, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	first, create := newTemplate(t, s, 1)
	prompt, err := s.Resolve(t.Context(), first.ID, 1)
	if err != nil || !prompt.NeedsDraft || prompt.ID != "rewrite" || prompt.Instruction != create.Definition.Instruction {
		t.Fatal(prompt, err)
	}
	before, _ := s.Get(t.Context(), first.ID)
	changed := templateDefinition()
	changed.Title = "修订后的编辑"
	update := templateCommand("save", first.ID, 1, 2)
	update.Definition = &changed
	second, err := s.Apply(t.Context(), update)
	if err != nil || second.Version != 2 {
		t.Fatal(second, err)
	}
	if _, err = s.Resolve(t.Context(), first.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale generation accepted", err)
	}
	if _, err = s.Apply(t.Context(), templateCommand("archive", first.ID, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), first.ID, 3); !errors.Is(err, ErrArchived) {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), false, 10, 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	page, err = s.List(t.Context(), true, 10, 0)
	if err != nil || page.Total != 1 || !page.Items[0].Archived {
		t.Fatal(page, err)
	}
	restore := templateCommand("restore", first.ID, 3, 4)
	restore.RestoreVersion = 1
	result, err := s.Apply(t.Context(), restore)
	if err != nil || result.Version != 4 {
		t.Fatal(result, err)
	}
	after, err := s.Get(t.Context(), first.ID)
	if err != nil || !reflect.DeepEqual(before.Revisions[0], after.Revisions[0]) || after.Revisions[3].Definition != before.Revisions[0].Definition || after.Revisions[3].Source.Kind != "history" {
		t.Fatal(after, err)
	}
	replay, err := s.Apply(t.Context(), create)
	if err != nil || !replay.Replayed || replay.Version != 1 || replay.ID != first.ID {
		t.Fatal(replay, err)
	}
	create.Definition = &changed
	if _, err = s.Apply(t.Context(), create); !errors.Is(err, ErrConflict) {
		t.Fatal("request body changed", err)
	}
	prompt, err = s.Resolve(t.Context(), first.ID, 4)
	if err != nil || prompt.Title != before.Revisions[0].Definition.Title {
		t.Fatal(prompt, err)
	}
}

func TestImportCopiesVerifiedVersionAndRetainsSource(t *testing.T) {
	s, _ := New(t.Context(), &MemoryStorage{})
	first, _ := newTemplate(t, s, 1)
	raw, err := s.Export(t.Context(), first.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewImport(raw)
	if err != nil {
		t.Fatal(err)
	}
	request := templateCommand("import", "", 0, 2)
	request.Import = raw
	request.ImportSHA256 = preview.SHA256
	result, err := s.Apply(t.Context(), request)
	if err != nil || result.ID == first.ID || result.Version != 1 {
		t.Fatal(result, err)
	}
	copy, err := s.Get(t.Context(), result.ID)
	if err != nil || copy.Revisions[0].Source.TemplateID != first.ID || copy.Revisions[0].Source.SHA256 != digest(raw) || copy.Revisions[0].Actor != "user:7" {
		t.Fatal(copy, err)
	}
	request.Import = append(request.Import, ' ')
	if _, err = s.Apply(t.Context(), request); !errors.Is(err, ErrInvalid) {
		t.Fatal("changed import bytes accepted", err)
	}
	for _, bad := range [][]byte{[]byte(`{"schema":2}`), bytes.Replace(raw, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1), append(bytes.Clone(raw), []byte(`{}`)...), bytes.Replace(raw, []byte(`"schema":1`), []byte(`"schema":1,"secret":"key"`), 1)} {
		if _, err = PreviewImport(bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid export accepted", err)
		}
	}
}

func TestFilePersistenceAndRefusedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writing-templates.json")
	store, _ := NewFileStorage(path)
	s, err := New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read initialized absent library", err)
	}
	first, create := newTemplate(t, s, 1)
	before, _ := os.ReadFile(path)
	cloneStore, _ := NewFileStorage(path)
	clone, err := New(t.Context(), cloneStore)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := clone.Apply(t.Context(), create)
	if err != nil || !replay.Replayed || replay.ID != first.ID {
		t.Fatal(replay, err)
	}
	if err = os.Mkdir(path+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = clone.Apply(t.Context(), templateCommand("archive", first.ID, 1, 2)); err == nil {
		t.Fatal("write failure accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write changed file")
	}
	if err = os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), templateCommand("archive", first.ID, 1, 3)); err != nil {
		t.Fatal(err)
	}
	if err = cloneStore.Write(t.Context(), sha256.Sum256(before), before); !errors.Is(err, ErrConflict) {
		t.Fatal("stale snapshot accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.Apply(ctx, create); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(t.Context(), true, 1, 0); !errors.Is(err, ErrCorrupt) {
		t.Fatal("lost known primary silently reset", err)
	}
	if err = os.WriteFile(path+".bak", before, 0600); err != nil {
		t.Fatal(err)
	}
	freshStore, _ := NewFileStorage(path)
	if _, err = New(t.Context(), freshStore); !errors.Is(err, ErrCorrupt) {
		t.Fatal("missing primary ignored rollback", err)
	}
	if err = os.WriteFile(path, []byte(`{"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(t.Context(), freshStore); err == nil {
		t.Fatal("corrupt primary accepted")
	}
	backup, _ := os.ReadFile(path + ".bak")
	if !bytes.Equal(backup, before) {
		t.Fatal("read mutated backup")
	}
}

func TestCompetingUpdatesDoNotLoseHistory(t *testing.T) {
	store := &MemoryStorage{}
	left, _ := New(t.Context(), store)
	right, _ := New(t.Context(), store)
	first, _ := newTemplate(t, left, 1)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range []*Service{left, right} {
		wg.Add(1)
		go func(i int, s *Service) {
			defer wg.Done()
			<-start
			_, err := s.Apply(t.Context(), templateCommand("archive", first.ID, 1, i+2))
			results <- err
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	passed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || conflicts != 1 {
		t.Fatal(passed, conflicts)
	}
	item, err := left.Get(t.Context(), first.ID)
	if err != nil || len(item.Revisions) != 2 || !item.Revisions[1].Archived {
		t.Fatal(item, err)
	}
}

func TestInputCapacityAndCorruptHistory(t *testing.T) {
	s, _ := New(t.Context(), &MemoryStorage{})
	first, create := newTemplate(t, s, 1)
	for _, bad := range []Definition{{BasePrompt: "unknown", Title: "a", Instruction: "b"}, {BasePrompt: "article", Title: "a", Instruction: strings.Repeat("字", 12001)}, {BasePrompt: "article", Title: "a", Instruction: "\x00"}, {BasePrompt: "article", Title: " a", Instruction: "b"}} {
		c := templateCommand("save", "", 0, 2)
		c.Definition = &bad
		if _, err := s.Apply(t.Context(), c); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad.BasePrompt, err)
		}
	}
	for i := 2; i <= MaxTemplates; i++ {
		newTemplate(t, s, i)
	}
	_, c := first, create
	c.RequestKey = fmt.Sprintf("%064x", MaxTemplates+1)
	if _, err := s.Apply(t.Context(), c); !errors.Is(err, ErrCapacity) {
		t.Fatal("template count overflow", err)
	}
	if replay, err := s.Apply(t.Context(), create); err != nil || !replay.Replayed {
		t.Fatal("full library lost replay", replay, err)
	}
	snapshot, err := s.storage.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeLibrary(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	doc.Templates[0].Revisions[0].Version = 2
	raw, _ := json.Marshal(doc)
	if err = ValidateSnapshot(raw); !errors.Is(err, ErrCorrupt) {
		t.Fatal("history renumbering accepted", err)
	}
	if err = ValidateSnapshot(nil); !errors.Is(err, ErrCorrupt) {
		t.Fatal("empty existing snapshot accepted", err)
	}
}

func TestRevisionReceiptAndEncodedByteBoundaries(t *testing.T) {
	t.Run("revisions", func(t *testing.T) {
		store := &MemoryStorage{}
		s, _ := New(t.Context(), store)
		first, create := newTemplate(t, s, 1)
		snapshot, _ := store.Read(t.Context())
		doc, _ := decodeLibrary(snapshot)
		base := doc.Templates[0].Revisions[0]
		for n := 2; n <= MaxRevisions; n++ {
			v := base
			v.Version = int64(n)
			doc.Templates[0].Revisions = append(doc.Templates[0].Revisions, v)
		}
		raw, err := encodeLibrary(doc)
		if err != nil {
			t.Fatal(err)
		}
		store.raw = raw
		before := bytes.Clone(raw)
		if _, err = s.Apply(t.Context(), templateCommand("archive", first.ID, MaxRevisions, 2)); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		after, _ := store.Read(t.Context())
		if !bytes.Equal(before, after) {
			t.Fatal("capacity failure changed library")
		}
		if replay, err := s.Apply(t.Context(), create); err != nil || !replay.Replayed {
			t.Fatal(replay, err)
		}
	})
	t.Run("receipts", func(t *testing.T) {
		store := &MemoryStorage{}
		s, _ := New(t.Context(), store)
		first, create := newTemplate(t, s, 1)
		snapshot, _ := store.Read(t.Context())
		doc, _ := decodeLibrary(snapshot)
		for n := 1; n < MaxReceipts; n++ {
			doc.Receipts = append(doc.Receipts, receipt{Key: fmt.Sprintf("%064x", n), Fingerprint: strings.Repeat("a", 64), TemplateID: first.ID, Version: 1})
		}
		raw, err := encodeLibrary(doc)
		if err != nil {
			t.Fatal(err)
		}
		store.raw = raw
		if _, err = s.Apply(t.Context(), templateCommand("archive", first.ID, 1, 2)); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		if replay, err := s.Apply(t.Context(), create); err != nil || !replay.Replayed {
			t.Fatal(replay, err)
		}
	})
	t.Run("encoded bytes", func(t *testing.T) {
		d := templateDefinition()
		d.Instruction = strings.Repeat("<", 12000)
		d.Example = strings.Repeat(">", 8000)
		base := Revision{Version: 1, Definition: d, Actor: "user:7", At: time.Now().UTC(), Source: Source{Kind: "manual"}}
		doc := library{Schema: 1, Kind: "newpress.writing-templates", Templates: []Template{{ID: strings.Repeat("1", 32), Revisions: []Revision{base}}}}
		var last []byte
		for n := 2; n < MaxRevisions; n++ {
			raw, err := encodeLibrary(doc)
			if errors.Is(err, ErrCapacity) {
				if len(last) == 0 || len(last) > MaxLibraryBytes {
					t.Fatal("missing accepted boundary")
				}
				if err = ValidateSnapshot(last); err != nil {
					t.Fatal(err)
				}
				store := &MemoryStorage{raw: last}
				s, err := New(t.Context(), store)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Apply(t.Context(), templateCommand("archive", doc.Templates[0].ID, int64(n-2), 1)); !errors.Is(err, ErrCapacity) {
					t.Fatal("over-byte-budget archive", err)
				}
				after, _ := store.Read(t.Context())
				if !bytes.Equal(last, after) {
					t.Fatal("failed write altered encoded boundary")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			last = raw
			v := base
			v.Version = int64(n)
			doc.Templates[0].Revisions = append(doc.Templates[0].Revisions, v)
		}
		t.Fatal("byte boundary not reached")
	})
}
