package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
)

var snippetSaveAccess = content.SnippetAccess{Identity: "legacy:save-test", Actor: "save-test", Read: true, Manage: true}

// Seed immutable history directly after validating the canonical record. This
// is a fixture builder, not a measurement of many ordinary editor saves.
func seedSnippetSaveHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, revisions, bodyBytes int) content.Snippet {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('np_snippets','id'))`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	s, err := content.NextSnippetRecord(content.SnippetRecord{}, id, content.Mutation{}, content.SnippetInput{Title: "retained", ContentSource: "<p>" + strings.Repeat("x", bodyBytes) + "</p>", Visibility: content.SnippetShared}, snippetSaveAccess, time.Now().Add(-time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= revisions; i++ {
		rev := s.Revisions[0]
		rev.Version = int64(i)
		s.Revisions = append(s.Revisions, rev)
	}
	s.Version, s.CurrentVersion = int64(revisions), int64(revisions)
	if err = content.ValidateSnippetRecord(s); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(s.Snippet)
	if _, err = pool.Exec(ctx, `INSERT INTO np_snippets(id,version,visibility,data) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4)`, s.ID, s.Version, s.Visibility, meta); err != nil {
		t.Fatal(err)
	}
	rows := make([][]any, 0, revisions)
	for _, rev := range s.Revisions {
		raw, _ := json.Marshal(rev)
		rows = append(rows, []any{s.ID, rev.Version, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_snippet_revisions"}, []string{"snippet_id", "version", "data"}, pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}
	return s.Snippet
}

func snippetSaveHistoryDigest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, through int64) string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT version,data FROM np_snippet_revisions WHERE snippet_id=$1 AND version<=$2 ORDER BY version`, id, through)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var version int64
		var raw []byte
		if err = rows.Scan(&version, &raw); err != nil {
			t.Fatal(err)
		}
		_ = binary.Write(digest, binary.BigEndian, version)
		_ = binary.Write(digest, binary.BigEndian, int64(len(raw)))
		_, _ = digest.Write(raw)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func snippetSaveState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	var meta []byte
	var count int64
	if err := pool.QueryRow(ctx, `SELECT data,(SELECT count(*) FROM np_snippet_revisions WHERE snippet_id=$1) FROM np_snippets WHERE id=$1`, id).Scan(&meta, &count); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s/%d/%s", meta, count, snippetSaveHistoryDigest(t, ctx, pool, id, 1<<62))
}

func TestPostgreSQLSnippetSaveRejectsOldCorruptionWithoutWriting(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	cases := []struct {
		name string
		edit func(content.Snippet)
	}{
		{"old malformed JSON", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`{`))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"old rendered body mismatch", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"content_html":"<p>wrong</p>"}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=1`, s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"old body version disagrees with row", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"version":2}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=1`, s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"missing shared boundary", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `DELETE FROM np_snippet_revisions WHERE snippet_id=$1 AND version=1`, s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"missing current body", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `DELETE FROM np_snippet_revisions WHERE snippet_id=$1 AND version=$2`, s.ID, s.CurrentVersion)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"old timestamp outside record", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"created_at":"2001-01-01T00:00:00Z"}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=1`, s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"old unknown field", func(s content.Snippet) {
			_, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=convert_to((convert_from(data,'UTF8')::jsonb||'{"unexpected":true}'::jsonb)::text,'UTF8') WHERE snippet_id=$1 AND version=1`, s.ID)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := seedSnippetSaveHistory(t, ctx, pool, 4, 20)
			test.edit(s)
			before := snippetSaveState(t, ctx, pool, s.ID)
			if _, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "replacement", ContentSource: "<p>new</p>", Visibility: content.SnippetShared}, snippetSaveAccess); !errors.Is(err, content.ErrSnippetCorrupt) {
				t.Fatal("old history corruption did not block save", err)
			}
			if _, err := r.GetSnippet(ctx, s.ID, snippetSaveAccess); !errors.Is(err, content.ErrSnippetCorrupt) {
				t.Fatal("full detail disagrees with save", err)
			}
			if after := snippetSaveState(t, ctx, pool, s.ID); after != before {
				t.Fatal("rejected save modified retained bytes")
			}
		})
	}
}

func TestPostgreSQLSnippetSaveConcurrentAndRollbackPreserveHistory(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, 10, 300)
	r := NewPostRepository(pool)
	// Valid noncanonical JSON bytes must remain unchanged, not be re-encoded.
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_snippet_revisions WHERE snippet_id=$1 AND version=1`, s.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, pretty.Bytes()); err != nil {
		t.Fatal(err)
	}
	original := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, title := range []string{"left", "right"} {
		wg.Go(func() {
			_, err := NewPostRepository(pool).SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: title, ContentSource: "<p>new</p>", Visibility: content.SnippetShared}, snippetSaveAccess)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, content.ErrVersionConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	if got := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion); got != original {
		t.Fatal("old history changed")
	}
	current, err := r.GetSnippet(ctx, s.ID, snippetSaveAccess)
	if err != nil || current.Version != 11 || len(current.Revisions) != 11 {
		t.Fatal(current.Version, err)
	}
	before := snippetSaveState(t, ctx, pool, s.ID)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION snippet_save_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected revision failure'; END $$; CREATE TRIGGER snippet_save_failure BEFORE INSERT ON np_snippet_revisions FOR EACH ROW EXECUTE FUNCTION snippet_save_failure()`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: current.Version}, content.SnippetInput{Title: "must roll back", ContentSource: "<p>changed</p>", Visibility: content.SnippetPrivate}, snippetSaveAccess); err == nil {
		t.Fatal("injected failure ignored")
	}
	if got := snippetSaveState(t, ctx, pool, s.ID); got != before {
		t.Fatal("metadata committed without body")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER snippet_save_failure ON np_snippet_revisions; DROP FUNCTION snippet_save_failure()`); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: current.Version}, snippetSaveAccess)
	if err != nil {
		t.Fatal(err)
	}
	reshared, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: withdrawn.Version}, content.SnippetInput{Title: "new sharing period", ContentSource: "<p>reshared</p>", Visibility: content.SnippetShared}, snippetSaveAccess)
	if err != nil || reshared.SharedSince != 13 || reshared.CurrentVersion != 13 {
		t.Fatal(reshared, err)
	}
	if got := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion); got != original {
		t.Fatal("resharing rewrote old bytes")
	}
}

func TestPostgreSQLSnippetSaveRevisionCapacity(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := seedSnippetSaveHistory(t, ctx, pool, content.MaxSnippetRevisions-1, 1)
	r := NewPostRepository(pool)
	input := content.SnippetInput{Title: "last", ContentSource: "<p>x</p>", Visibility: content.SnippetShared}
	last, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, input, snippetSaveAccess)
	if err != nil || last.CurrentVersion != content.MaxSnippetRevisions {
		t.Fatal(last, err)
	}
	before := snippetSaveState(t, ctx, pool, s.ID)
	if _, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: last.Version}, input, snippetSaveAccess); !errors.Is(err, content.ErrSnippetCapacity) {
		t.Fatal(err)
	}
	if after := snippetSaveState(t, ctx, pool, s.ID); after != before {
		t.Fatal("count rejection changed state")
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: last.Version}, snippetSaveAccess); err != nil {
		t.Fatal("capacity blocked withdrawal", err)
	}
}

func TestPostgreSQLSnippetSaveByteCapacity(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Reach the budget with valid old JSON bytes; padding is legal whitespace
	// within per-row decoding bounds and must not disappear from accounting.
	s := seedSnippetSaveHistory(t, ctx, pool, 128, 1)
	r := NewPostRepository(pool)
	input := content.SnippetInput{Title: "new", ContentSource: "<p>x</p>", Visibility: content.SnippetShared}
	sample := content.SnippetRevision{Version: s.Version + 1, Title: input.Title, ContentSource: input.ContentSource, ContentHTML: input.ContentSource, Actor: snippetSaveAccess.Actor, CreatedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(sample)
	// Leave a little more than one encoded revision. A second revision cannot fit.
	remaining := len(encoded) + 32
	var total int64
	if err := pool.QueryRow(ctx, `SELECT 4096+sum(octet_length(data)) FROM np_snippet_revisions WHERE snippet_id=$1`, s.ID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	padding := int64(content.MaxSnippetStorageBytes-remaining) - total
	for version := int64(1); version <= 128; version++ {
		extra := padding / (129 - version)
		if _, err := pool.Exec(ctx, `UPDATE np_snippet_revisions SET data=data||convert_to(repeat(' ',$3),'UTF8') WHERE snippet_id=$1 AND version=$2`, s.ID, version, extra); err != nil {
			t.Fatal(err)
		}
		padding -= extra
	}
	if padding != 0 {
		t.Fatal("fixture padding incomplete")
	}
	original := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion)
	last, err := r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, input, snippetSaveAccess)
	if err != nil {
		t.Fatal("last fitting revision rejected", err)
	}
	if got := snippetSaveHistoryDigest(t, ctx, pool, s.ID, s.CurrentVersion); got != original {
		t.Fatal("old padded bytes changed")
	}
	before := snippetSaveState(t, ctx, pool, s.ID)
	if _, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: last.Version}, input, snippetSaveAccess); !errors.Is(err, content.ErrSnippetCapacity) {
		t.Fatal("byte budget not enforced", err)
	}
	if after := snippetSaveState(t, ctx, pool, s.ID); after != before {
		t.Fatal("byte rejection changed state")
	}
	if _, err = r.WithdrawSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: last.Version}, snippetSaveAccess); err != nil {
		t.Fatal("byte capacity blocked withdrawal", err)
	}
}
