package content

import (
	"context"
	"html"
	"newpress.local/newpress/internal/resources"
	"sort"
	"strings"
)

const (
	MediaSourceCurrent  = "current"
	MediaSourceRevision = "revision"
	MediaSourceAutosave = "autosave"
	MediaSourceSeries   = "series"
	MediaSourceSnippet  = "snippet"
)

// MediaSnapshot is an internal projection for reference scanning, never an
// HTTP DTO. AuthorID and Status belong to the current canonical record so
// historical ownership cannot grant access to a different author's content.
type MediaSnapshot struct {
	PostID        int64
	AuthorID      int64
	Status        string
	Source        string
	RevisionID    int64
	OwnerKey      string
	Version       int64
	Type          string
	Slug          string
	Title         string
	CoverURL      string
	ContentSource string
	ContentHTML   string
}

func MediaSnapshotOf(current, snapshot Post, source string, revisionID int64, owner string) MediaSnapshot {
	var privateAssets strings.Builder
	for _, r := range resources.FromMeta(snapshot.Meta).Items {
		if r.Cover != "" {
			privateAssets.WriteString(`<img src="` + html.EscapeString(r.Cover) + `">`)
		}
		for _, v := range r.Variants {
			privateAssets.WriteString(v.Body)
			if v.MediaURL != "" {
				privateAssets.WriteString(`<a href="` + html.EscapeString(v.MediaURL) + `">media</a>`)
			}
			for _, d := range v.Downloads {
				privateAssets.WriteString(`<a href="` + html.EscapeString(d.URL) + `">resource</a>`)
			}
		}
	}
	return MediaSnapshot{
		PostID: current.ID, AuthorID: current.AuthorID, Status: current.Status,
		Source: source, RevisionID: revisionID, OwnerKey: owner, Version: snapshot.Version,
		Type: snapshot.Type, Slug: snapshot.Slug, Title: snapshot.Title,
		CoverURL: snapshot.CoverURL, ContentSource: snapshot.ContentSource, ContentHTML: snapshot.ContentHTML + privateAssets.String(),
	}
}

// WalkMediaSnapshots visits every canonical record, every retained revision
// and every unexpired autosave in one consistent read. The callback must not
// call back into the repository. Result size is bounded by the caller, without
// truncating the scan to the editor's revision-list limit.
func (repository *MemoryRepository) WalkMediaSnapshots(ctx context.Context, visit func(MediaSnapshot) error) error {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	posts := append([]Post(nil), repository.posts...)
	sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })
	now := repository.now()
	for _, source := range []string{MediaSourceCurrent, MediaSourceRevision, MediaSourceAutosave} {
		for _, post := range posts {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch source {
			case MediaSourceCurrent:
				if err := visit(MediaSnapshotOf(post, post, source, 0, "")); err != nil {
					return err
				}
			case MediaSourceRevision:
				revisions := repository.revisions[post.ID]
				for i := len(revisions) - 1; i >= 0; i-- {
					if err := ctx.Err(); err != nil {
						return err
					}
					revision := revisions[i]
					if err := visit(MediaSnapshotOf(post, revision.Snapshot, source, revision.ID, "")); err != nil {
						return err
					}
				}
			case MediaSourceAutosave:
				owners := make([]string, 0, len(repository.autosaves[post.ID]))
				for owner := range repository.autosaves[post.ID] {
					owners = append(owners, owner)
				}
				sort.Strings(owners)
				for _, owner := range owners {
					autosave := repository.autosaves[post.ID][owner]
					if autosave.ExpiresAt.After(now) {
						if err := visit(MediaSnapshotOf(post, autosave.Snapshot, source, 0, owner)); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	for _, item := range repository.series {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(MediaSnapshot{PostID: item.ID, Status: item.Status, Source: MediaSourceSeries, Version: item.Version, Type: "series", Slug: item.Slug, Title: item.Title, CoverURL: item.CoverURL}); err != nil {
			return err
		}
	}
	for _, record := range repository.snippets {
		for _, revision := range record.Revisions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(MediaSnapshot{PostID: record.ID, Status: record.Visibility, Source: MediaSourceSnippet, Version: revision.Version, Type: "snippet", Title: revision.Title, ContentSource: revision.ContentSource, ContentHTML: revision.ContentHTML}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (repository *FileRepository) WalkMediaSnapshots(ctx context.Context, visit func(MediaSnapshot) error) error {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.WalkMediaSnapshots(ctx, visit)
}
