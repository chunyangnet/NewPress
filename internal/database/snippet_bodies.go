package database

import (
	"bytes"
	"context"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

// Callers provide one transaction: public/preview reads use a repeatable-read
// snapshot, and draft creation already holds the snippet writer lock. Only
// selected bodies are decoded; current/sharing-boundary rows must also exist.
func loadSharedSnippetBodies(ctx context.Context, tx pgx.Tx, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]content.SnippetDraftBody, error) {
	if len(keys) > content.MaxSnippetResolve {
		return nil, content.ErrSnippetCapacity
	}
	if err := snippetMaintenanceGate(ctx, tx); err != nil {
		return nil, err
	}
	ids, versions := make([]int64, len(keys)), make([]int64, len(keys))
	for i, key := range keys {
		ids[i], versions[i] = key.ID, key.Version
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.version,s.visibility,s.data,r.version,r.data
 FROM unnest($1::bigint[],$2::bigint[]) AS wanted(id,version)
 JOIN np_snippets s ON s.id=wanted.id
 JOIN np_snippet_revisions r ON r.snippet_id=s.id AND r.version=wanted.version
 WHERE s.visibility='shared'`, ids, versions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bodies := map[contenthtml.SnippetKey]content.SnippetDraftBody{}
	boundaries := map[contenthtml.SnippetKey]bool{}
	for rows.Next() {
		var id, version, bodyVersion int64
		var visibility string
		var meta, raw []byte
		if err = rows.Scan(&id, &version, &visibility, &meta, &bodyVersion, &raw); err != nil {
			return nil, err
		}
		s, e := packagefs.DecodeJSON[content.Snippet](bytes.NewReader(meta), 4096, "snippet")
		if e != nil || s.ID != id || s.Version != version || s.Visibility != visibility || content.ValidateSnippetMetadata(s) != nil {
			return nil, content.ErrSnippetCorrupt
		}
		if !content.SnippetVersionShared(s, bodyVersion) {
			continue
		}
		rev, e := packagefs.DecodeJSON[content.SnippetRevision](bytes.NewReader(raw), 2<<20, "snippet revision")
		if e != nil || rev.Version != bodyVersion || content.ValidateSnippetRevision(s, rev) != nil {
			return nil, content.ErrSnippetCorrupt
		}
		bodies[contenthtml.SnippetKey{ID: id, Version: bodyVersion}] = content.SnippetDraftBody{Snippet: s, Revision: rev}
		boundaries[contenthtml.SnippetKey{ID: id, Version: s.SharedSince}] = true
		boundaries[contenthtml.SnippetKey{ID: id, Version: s.CurrentVersion}] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(boundaries) > 0 {
		ids, versions = make([]int64, 0, len(boundaries)), make([]int64, 0, len(boundaries))
		for key := range boundaries {
			ids, versions = append(ids, key.ID), append(versions, key.Version)
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM unnest($1::bigint[],$2::bigint[]) AS wanted(id,version)
 JOIN np_snippet_revisions r ON r.snippet_id=wanted.id AND r.version=wanted.version`, ids, versions).Scan(&count); err != nil {
			return nil, err
		}
		if count != len(boundaries) {
			return nil, content.ErrSnippetCorrupt
		}
	}
	return bodies, nil
}
