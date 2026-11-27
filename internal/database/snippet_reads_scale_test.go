package database

import (
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestPostgreSQLSnippetReadScale(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_READ_SCALE") != "1" {
		t.Skip("set NEWPRESS_SNIPPET_READ_SCALE=1 for local scale measurements")
	}
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := NewPostRepository(pool)
	a := content.SnippetAccess{Identity: "legacy:scale", Actor: "scale", Read: true, Manage: true}
	s, err := content.NextSnippetRecord(content.SnippetRecord{}, 1, content.Mutation{}, content.SnippetInput{Title: "scale", ContentSource: "<p>" + strings.Repeat("x", 1450) + "</p>", Visibility: content.SnippetShared}, a, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	first := s.Revisions[0]
	for i := int64(2); i <= content.MaxSnippetRevisions; i++ {
		rev := first
		rev.Version = i
		s.Revisions = append(s.Revisions, rev)
	}
	s.Version, s.CurrentVersion = content.MaxSnippetRevisions, content.MaxSnippetRevisions
	if err = content.ValidateSnippetState([]content.SnippetRecord{s}, nil, 2); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(s.Snippet)
	if _, err = pool.Exec(ctx, `INSERT INTO np_snippets(id,version,visibility,data) OVERRIDING SYSTEM VALUE VALUES(1,$1,'shared',$2)`, s.Version, metadata); err != nil {
		t.Fatal(err)
	}
	revisionRows := make([][]any, 0, len(s.Revisions))
	for _, rev := range s.Revisions {
		raw, _ := json.Marshal(rev)
		revisionRows = append(revisionRows, []any{int64(1), rev.Version, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_snippet_revisions"}, []string{"snippet_id", "version", "data"}, pgx.CopyFromRows(revisionRows)); err != nil {
		t.Fatal(err)
	}
	plain := "<p>" + strings.Repeat("p", 1000) + "</p>"
	referenced := plain + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1}) + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: content.MaxSnippetRevisions})
	seed, err := r.Create(ctx, content.PostInput{Title: "post", Slug: "scale-1", ContentSource: referenced})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO np_posts(author_id,type,status,slug,title,excerpt,content_source,content_html,cover_url,comment_status,created_at,updated_at,version,metadata)
 SELECT p.author_id,p.type,p.status,'scale-'||i,'post '||i,p.excerpt,CASE WHEN (i-1)%10=0 THEN $2 ELSE $3 END,CASE WHEN (i-1)%10=0 THEN $4 ELSE $5 END,p.cover_url,p.comment_status,p.created_at,p.updated_at,p.version,p.metadata
 FROM np_posts p CROSS JOIN generate_series(2,10000) i WHERE p.id=$1`, seed.ID, referenced, plain, contenthtml.Sanitize(referenced), contenthtml.Sanitize(plain)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = PrepareSnippetReferences(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture revisions=%d snippet_logical_bytes=%d posts=10000 projection_build_ms=%.3f", len(s.Revisions), content.SnippetStorageSize(s), float64(time.Since(started).Microseconds())/1000)
	scan := func() content.SnippetImpacts {
		t.Helper()
		rows, e := pool.Query(ctx, `SELECT `+postColumns+` FROM np_posts p LEFT JOIN np_users u ON u.id=p.author_id ORDER BY p.id`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := content.SnippetImpacts{Snippet: s.Snippet, Items: []content.SnippetImpact{}}
		for rows.Next() {
			post, e := scanPost(rows)
			if e != nil {
				t.Fatal(e)
			}
			v := content.SnippetPostImpact(post, 1, s.CurrentVersion)
			if v.References > 0 {
				out.Items = append(out.Items, v)
			}
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		out.Total = len(out.Items)
		out.Items = out.Items[:min(24, len(out.Items))]
		return out
	}
	want := scan()
	got, err := r.SnippetImpacts(ctx, 1, a, 24, 0)
	if err != nil || !reflect.DeepEqual(got, want) || got.Total != 1000 {
		t.Fatal(got, err)
	}
	for _, operation := range []string{"summary_json", "full_history_json", "indexed_impact", "canonical_scan"} {
		for round := 1; round <= 3; round++ {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			bytes := 0
			switch operation {
			case "summary_json":
				page, e := r.GetSnippetPage(ctx, 1, a, 0, 24)
				if e != nil || len(page.History) != 24 {
					t.Fatal(e)
				}
				raw, e := json.Marshal(page)
				if e != nil {
					t.Fatal(e)
				}
				bytes = len(raw)
			case "full_history_json":
				record, e := r.GetSnippet(ctx, 1, a)
				if e != nil || len(record.Revisions) != content.MaxSnippetRevisions {
					t.Fatal(e)
				}
				raw, e := json.Marshal(record)
				if e != nil {
					t.Fatal(e)
				}
				bytes = len(raw)
			case "indexed_impact":
				got, e := r.SnippetImpacts(ctx, 1, a, 24, 0)
				if e != nil || !reflect.DeepEqual(got, want) {
					t.Fatal(e)
				}
			case "canonical_scan":
				if !reflect.DeepEqual(scan(), want) {
					t.Fatal("scan differs")
				}
			}
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			entry, _ := json.Marshal(map[string]any{"store": "postgres", "operation": operation, "round": round, "milliseconds": float64(elapsed.Microseconds()) / 1000, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "response_bytes": bytes})
			t.Log(string(entry))
		}
	}
}
