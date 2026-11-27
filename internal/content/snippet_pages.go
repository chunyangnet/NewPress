package content

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"
)

func validSnippetTitle(title string) bool {
	return title != "" && strings.TrimSpace(title) == title && utf8.ValidString(title) && utf8.RuneCountInString(title) <= MaxTitleRunes && !containsControl(title)
}

func ValidateSnippetMetadata(s Snippet) error {
	if s.ID <= 0 || s.Version <= 0 || s.CurrentVersion <= 0 || s.CurrentVersion > s.Version || !validSnippetTitle(s.Title) || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) ||
		(s.Visibility != SnippetPrivate && s.Visibility != SnippetShared && s.Visibility != SnippetWithdrawn) ||
		(s.Visibility == SnippetShared && (s.SharedSince <= 0 || s.SharedSince > s.CurrentVersion)) || (s.Visibility != SnippetShared && s.SharedSince != 0) {
		return ErrSnippetCorrupt
	}
	return nil
}

func SnippetRevisionMetadata(rev SnippetRevision) SnippetRevisionInfo {
	return SnippetRevisionInfo{Version: rev.Version, Title: rev.Title, Actor: rev.Actor, CreatedAt: rev.CreatedAt}
}

func ValidateSnippetRevisionInfo(s Snippet, rev SnippetRevisionInfo) error {
	if rev.Version <= 0 || rev.Version > s.CurrentVersion || !validSnippetTitle(rev.Title) || rev.CreatedAt.Before(s.CreatedAt) || rev.CreatedAt.After(s.UpdatedAt) || ValidateRevisionActor(rev.Actor) != nil {
		return ErrSnippetCorrupt
	}
	if rev.Version == s.CurrentVersion && rev.Title != s.Title {
		return ErrSnippetCorrupt
	}
	return nil
}

func ValidateSnippetRevision(s Snippet, rev SnippetRevision) error {
	input, body, err := NormalizeSnippetInput(SnippetInput{rev.Title, rev.ContentSource, SnippetPrivate})
	if err != nil || input.ContentSource != rev.ContentSource || body != rev.ContentHTML || ValidateSnippetRevisionInfo(s, SnippetRevisionMetadata(rev)) != nil {
		return ErrSnippetCorrupt
	}
	return nil
}

func NormalizeSnippetPage(before int64, limit int) (int64, int, error) {
	if before < 0 {
		return 0, 0, &ValidationError{Fields: map[string]string{"before": "历史游标须为非负版本号"}}
	}
	if limit <= 0 {
		limit = 24
	}
	return before, min(limit, 100), nil
}

func (r *MemoryRepository) GetSnippetPage(ctx context.Context, id int64, a SnippetAccess, before int64, limit int) (SnippetPage, error) {
	if err := CheckSnippetAccess(a, false); err != nil {
		return SnippetPage{}, err
	}
	before, limit, err := NormalizeSnippetPage(before, limit)
	if err != nil {
		return SnippetPage{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, _, err := r.snippetLocked(id)
	if err != nil || !SnippetReadable(record.Snippet, a) {
		return SnippetPage{}, ErrNotFound
	}
	result := SnippetPage{Snippet: record.Snippet, Current: record.Revisions[len(record.Revisions)-1], History: []SnippetRevisionInfo{}}
	end := len(record.Revisions)
	if before > 0 {
		end, _ = slices.BinarySearchFunc(record.Revisions, before, func(rev SnippetRevision, v int64) int {
			if rev.Version < v {
				return -1
			}
			if rev.Version > v {
				return 1
			}
			return 0
		})
	}
	for i := end - 1; i >= 0; i-- {
		rev := record.Revisions[i]
		if !a.Manage && !SnippetVersionShared(record.Snippet, rev.Version) {
			break
		}
		if len(result.History) == limit {
			result.NextBefore = result.History[len(result.History)-1].Version
			break
		}
		result.History = append(result.History, SnippetRevisionMetadata(rev))
	}
	return result, ctx.Err()
}

func (r *FileRepository) GetSnippetPage(ctx context.Context, id int64, a SnippetAccess, before int64, limit int) (SnippetPage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.GetSnippetPage(ctx, id, a, before, limit)
}
