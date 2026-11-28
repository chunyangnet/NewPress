package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"

	"newpress.local/newpress/internal/writingtemplate"
)

func TestPostgreSQLWritingTemplates(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := NewWritingTemplateStorage(pool)
	left, err := writingtemplate.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	right, err := writingtemplate.New(ctx, NewWritingTemplateStorage(pool))
	if err != nil {
		t.Fatal(err)
	}
	d := writingtemplate.Definition{BasePrompt: "article", Title: "共享团队模板", Instruction: "保留材料中的事实。"}
	create := writingtemplate.Command{Action: "save", Definition: &d, RequestKey: fmt.Sprintf("%064x", 1), Actor: "user:1"}
	first, err := left.Apply(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range []*writingtemplate.Service{left, right} {
		wg.Add(1)
		go func(i int, s *writingtemplate.Service) {
			defer wg.Done()
			<-start
			_, err := s.Apply(ctx, writingtemplate.Command{Action: "archive", ID: first.ID, ExpectedVersion: 1, RequestKey: fmt.Sprintf("%064x", i+2), Actor: "user:1"})
			results <- err
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, writingtemplate.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	if _, err = right.Resolve(ctx, first.ID, 2); !errors.Is(err, writingtemplate.ErrArchived) {
		t.Fatal(err)
	}
	if replay, err := right.Apply(ctx, create); err != nil || !replay.Replayed || replay.ID != first.ID || replay.Version != 1 {
		t.Fatal(replay, err)
	}
	current, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write(ctx, sha256.Sum256(before), before); !errors.Is(err, writingtemplate.ErrConflict) {
		t.Fatal("stale PG snapshot accepted", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = store.Write(cancelled, sha256.Sum256(current), current); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_writing_template_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected writing template write failure'; END $$; CREATE TRIGGER reject_writing_template_write BEFORE UPDATE ON np_writing_template_library FOR EACH ROW EXECUTE FUNCTION reject_writing_template_write()`); err != nil {
		t.Fatal(err)
	}
	if _, err = left.Apply(ctx, writingtemplate.Command{Action: "activate", ID: first.ID, ExpectedVersion: 2, RequestKey: fmt.Sprintf("%064x", 4), Actor: "user:1"}); err == nil {
		t.Fatal("injected failure ignored")
	}
	after, err := store.Read(ctx)
	if err != nil || !bytes.Equal(current, after) {
		t.Fatal("failed PG write changed library", err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_writing_template_write ON np_writing_template_library; DROP FUNCTION reject_writing_template_write()`); err != nil {
		t.Fatal(err)
	}
	if _, err = left.Apply(ctx, writingtemplate.Command{Action: "activate", ID: first.ID, ExpectedVersion: 2, RequestKey: fmt.Sprintf("%064x", 4), Actor: "user:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = right.Resolve(ctx, first.ID, 2); !errors.Is(err, writingtemplate.ErrConflict) {
		t.Fatal("stale template used", err)
	}
	export, err := right.Export(ctx, first.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := writingtemplate.PreviewImport(export)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := left.Apply(ctx, writingtemplate.Command{Action: "import", Import: export, ImportSHA256: preview.SHA256, RequestKey: fmt.Sprintf("%064x", 5), Actor: "user:1"})
	if err != nil || copy.ID == first.ID {
		t.Fatal(copy, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_writing_template_library SET data=$1`, []byte(`{"schema":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = writingtemplate.New(ctx, NewWritingTemplateStorage(pool)); !errors.Is(err, writingtemplate.ErrCorrupt) {
		t.Fatal("bad PG library ignored", err)
	}
	if _, err = right.Catalog(ctx); !errors.Is(err, writingtemplate.ErrCorrupt) {
		t.Fatal("warm reader hid corrupt PG bytes", err)
	}
	if _, err = left.Apply(ctx, create); !errors.Is(err, writingtemplate.ErrCorrupt) {
		t.Fatal("warm replay hid corrupt PG bytes", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_writing_template_library SET data=$1`, current); err != nil {
		t.Fatal(err)
	}
	if _, err = right.Resolve(ctx, first.ID, 2); !errors.Is(err, writingtemplate.ErrArchived) {
		t.Fatal("warm reader did not observe restored earlier snapshot", err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM np_writing_template_library`); err != nil {
		t.Fatal(err)
	}
	if items, err := right.Catalog(ctx); err != nil || len(items) != 0 {
		t.Fatal("missing PG row served old cache", err)
	}
}
