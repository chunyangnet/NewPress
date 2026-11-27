package database

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLPublicSearchMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_PUBLIC_SEARCH_LOAD") != "1" {
		t.Skip("opt-in public search scale measurement")
	}
	for _, size := range []struct {
		rows, body int
		chinese    bool
	}{{1000, 1024, false}, {10000, 1024, false}, {1000, 16384, false}, {10000, 1024, true}} {
		label := fmt.Sprintf("%d-%d", size.rows, size.body)
		if size.chinese {
			label += "-chinese"
		}
		t.Run(label, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			body := "<p>" + strings.Repeat("plain text ", size.body/11) + "</p>"
			if _, err := pool.Exec(ctx, `INSERT INTO np_posts(type,status,slug,title,excerpt,content_source,content_html,published_at)
 SELECT 'post',CASE WHEN n%10=0 THEN 'private' ELSE 'publish' END,'scale-'||n,
 CASE WHEN n%100=1 THEN 'needle title '||n ELSE 'ordinary '||n END,'unrelated summary',
 $2,CASE WHEN n%5=1 THEN $2||'<p>needle 深度正文</p>' ELSE $2 END,now()-interval '1 hour'
 FROM generate_series(1,$1::int) n`, size.rows, body); err != nil {
				t.Fatal(err)
			}
			if size.chinese {
				if _, err := pool.Exec(ctx, `UPDATE np_posts SET title='中文搜索测量 '||title,excerpt='专门用于检索规模测量的摘要',metadata=jsonb_build_object('author_name','中文作者')`); err != nil {
					t.Fatal(err)
				}
			}
			began := time.Now()
			if err := PreparePublicSearch(ctx, pool); err != nil {
				t.Fatal(err)
			}
			prepare := time.Since(began)
			store := NewPostRepository(pool)
			samples := []map[string]any{}
			for round := range 3 {
				for _, needle := range []string{"needle", "深度正文", "missing-phrase"} {
					began = time.Now()
					page, err := store.SearchPublished(ctx, content.PublishedQuery{Search: needle, Limit: 20})
					elapsed := time.Since(began)
					if err != nil {
						t.Fatal(err)
					}
					want := size.rows / 5
					if needle == "missing-phrase" {
						want = 0
					}
					if page.Total != want {
						t.Fatal(needle, page.Total, want)
					}
					samples = append(samples, map[string]any{"round": round + 1, "query": needle, "milliseconds": float64(elapsed.Microseconds()) / 1000, "total": page.Total})
				}
			}
			var bytes int64
			if err := pool.QueryRow(ctx, `SELECT sum(octet_length(public_search_body)) FROM np_posts`).Scan(&bytes); err != nil {
				t.Fatal(err)
			}
			args := []any{content.TypePost, false, false, "", "", int64(0), "", []int64(nil), []int64(nil), "needle"}
			rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT count(*)"+publicSearchJoin+publicSearchRank+publicSearchFilters+" AND search.score>0", args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan json.RawMessage
			for rows.Next() {
				if err := rows.Scan(&plan); err != nil {
					t.Fatal(err)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"rows": size.rows, "chinese_metadata": size.chinese, "base_html_bytes": len(body), "projection_bytes": bytes, "prepare_milliseconds": float64(prepare.Microseconds()) / 1000, "samples": samples, "count_plan": plan})
			if err != nil {
				t.Fatal(err)
			}
			if directory := os.Getenv("NEWPRESS_PUBLIC_SEARCH_MEASUREMENTS_DIR"); directory != "" {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(directory, label+".json")
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.Write(raw)
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatal(writeErr, closeErr)
				}
				t.Log("PUBLIC_SEARCH_MEASUREMENT " + path)
			} else {
				t.Log("PUBLIC_SEARCH_LOAD " + string(raw))
			}
		})
	}
}
