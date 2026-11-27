package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

// SnippetReferenceQuery bounds one transaction by article ID and page size.
// ThroughID=0 chooses the current maximum ID for the first page. Continuations
// must carry that ceiling; separate pages are not one database snapshot.
type SnippetReferenceQuery struct {
	AfterID   int64
	ThroughID int64
	Limit     int
}

func (q SnippetReferenceQuery) Validate() error {
	if q.AfterID < 0 || q.ThroughID < 0 || q.AfterID > q.ThroughID || q.Limit < 1 || q.Limit > 100 {
		return errors.New("snippet reference inspection requires 0 <= after <= through and limit 1-100")
	}
	return nil
}

// Counts describe the index before this operation. No titles, bodies or
// snippet contents are included. Missing snippet objects are valid references.
type SnippetReferenceCheck struct {
	PostID               int64 `json:"post_id"`
	PostVersion          int64 `json:"post_version"`
	ReadyBefore          bool  `json:"ready_before"`
	ExpectedEntries      int   `json:"expected_entries"`
	ActualEntries        int   `json:"actual_entries"`
	MissingEntries       int   `json:"missing_entries"`
	UnexpectedEntries    int   `json:"unexpected_entries"`
	OccurrenceMismatches int   `json:"occurrence_mismatches"`
	NeedsRebuild         bool  `json:"needs_rebuild"`
	Rebuilt              bool  `json:"rebuilt"`
}

type SnippetReferencePage struct {
	AfterID      int64                   `json:"after_id"`
	ThroughID    int64                   `json:"through_id"`
	NextAfterID  int64                   `json:"next_after_id"`
	HasMore      bool                    `json:"has_more"`
	Limit        int                     `json:"limit"`
	Checked      int                     `json:"checked"`
	NeedsRebuild int                     `json:"needs_rebuild"`
	Rebuilt      int                     `json:"rebuilt"`
	Items        []SnippetReferenceCheck `json:"items"`
}

// InspectSnippetReferences is read-only, including for unprepared indexes.
func InspectSnippetReferences(ctx context.Context, pool *pgxpool.Pool, q SnippetReferenceQuery) (SnippetReferencePage, error) {
	return maintainSnippetReferences(ctx, pool, q, false)
}

// RebuildSnippetReferences replaces only inconsistent/unprepared projections.
// Locks are held on canonical articles in ascending order until commit. A
// concurrent edit after the snapshot causes a serialization failure instead
// of rebuilding from stale content. Statement errors roll back the whole page.
// A lost commit acknowledgement can be resolved by inspecting/repeating it.
func RebuildSnippetReferences(ctx context.Context, pool *pgxpool.Pool, q SnippetReferenceQuery) (SnippetReferencePage, error) {
	return maintainSnippetReferences(ctx, pool, q, true)
}

func maintainSnippetReferences(ctx context.Context, pool *pgxpool.Pool, q SnippetReferenceQuery, rebuild bool) (SnippetReferencePage, error) {
	if err := q.Validate(); err != nil {
		return SnippetReferencePage{}, err
	}
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if rebuild {
		options.AccessMode = pgx.ReadWrite
	}
	tx, err := pool.BeginTx(ctx, options)
	if err != nil {
		return SnippetReferencePage{}, err
	}
	defer tx.Rollback(context.Background())
	page, err := snippetReferencePage(ctx, tx, q, rebuild)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return SnippetReferencePage{}, fmt.Errorf("snippet reference page: %w", err)
	}
	return page, nil
}

func snippetReferencePage(ctx context.Context, tx pgx.Tx, q SnippetReferenceQuery, rebuild bool) (SnippetReferencePage, error) {
	page := SnippetReferencePage{AfterID: q.AfterID, ThroughID: q.ThroughID, NextAfterID: q.AfterID, Limit: q.Limit, Items: []SnippetReferenceCheck{}}
	if page.ThroughID == 0 {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM np_posts`).Scan(&page.ThroughID); err != nil {
			return page, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id FROM np_posts WHERE id>$1 AND id<=$2 ORDER BY id LIMIT $3`, q.AfterID, page.ThroughID, q.Limit+1)
	if err != nil {
		return page, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return page, err
	}
	if len(ids) > q.Limit {
		page.HasMore = true
		ids = ids[:q.Limit]
	}
	// Load at most one bounded body at a time. A corrupt oversized canonical
	// source is an error, never silently truncated or used to replace the index.
	query := `SELECT version,snippet_refs_ready,CASE WHEN octet_length(content_source)<=$2 THEN content_source END FROM np_posts WHERE id=$1`
	if rebuild {
		query += ` FOR UPDATE`
	}
	for _, id := range ids {
		check := SnippetReferenceCheck{PostID: id}
		var source *string
		if err = tx.QueryRow(ctx, query, id, content.MaxContentBytes).Scan(&check.PostVersion, &check.ReadyBefore, &source); err != nil {
			return page, fmt.Errorf("post %d: %w", id, err)
		}
		if source == nil {
			return page, fmt.Errorf("post %d: canonical source exceeds %d bytes; no index changes committed", id, content.MaxContentBytes)
		}
		expected := contenthtml.CountSnippets(*source)
		if err = compareSnippetReferences(ctx, tx, expected, &check); err != nil {
			return page, fmt.Errorf("post %d: %w", id, err)
		}
		check.NeedsRebuild = !check.ReadyBefore || check.MissingEntries > 0 || check.UnexpectedEntries > 0 || check.OccurrenceMismatches > 0
		if check.NeedsRebuild {
			page.NeedsRebuild++
			if rebuild {
				batch := &pgx.Batch{}
				queueSnippetProjectionCounts(batch, id, expected)
				if err = tx.SendBatch(ctx, batch).Close(); err != nil {
					return page, fmt.Errorf("rebuild post %d: %w", id, err)
				}
				check.Rebuilt = true
				page.Rebuilt++
			}
		}
		page.Items = append(page.Items, check)
		page.Checked++
		page.NextAfterID = id
	}
	return page, nil
}

// Merge the ordered expected counts with a row stream, retaining no copy of
// the stored index. This also detects extra entries on articles without refs.
func compareSnippetReferences(ctx context.Context, tx pgx.Tx, expected []contenthtml.SnippetCount, check *SnippetReferenceCheck) error {
	check.ExpectedEntries = len(expected)
	rows, err := tx.Query(ctx, `SELECT snippet_id,body_version,occurrences FROM np_post_snippet_refs WHERE post_id=$1 ORDER BY snippet_id,body_version`, check.PostID)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := 0
	for rows.Next() {
		var id, version int64
		var count int
		if err = rows.Scan(&id, &version, &count); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		check.ActualEntries++
		for next < len(expected) && (expected[next].ID < id || expected[next].ID == id && expected[next].Version < version) {
			check.MissingEntries++
			next++
		}
		if next < len(expected) && expected[next].ID == id && expected[next].Version == version {
			if expected[next].Count != count {
				check.OccurrenceMismatches++
			}
			next++
		} else {
			check.UnexpectedEntries++
		}
	}
	check.MissingEntries += len(expected) - next
	return rows.Err()
}
