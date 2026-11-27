package content

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

func (r *MemoryRepository) ListRedirects(ctx context.Context, query RedirectQuery) (RedirectList, error) {
	q, err := NormalizeRedirectQuery(query)
	if err != nil {
		return RedirectList{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := []Redirect{}
	for _, item := range r.redirects {
		if q.Search == "" || strings.Contains(strings.ToLower(item.Path), strings.ToLower(q.Search)) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	total := len(items)
	start := min(q.Offset, total)
	return RedirectList{Items: items[start:min(start+q.Limit, total)], Total: total}, ctx.Err()
}

func (r *MemoryRepository) GetRedirect(ctx context.Context, id int64) (Redirect, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, item := range r.redirects {
		if item.ID == id {
			return item, ctx.Err()
		}
	}
	return Redirect{}, ErrNotFound
}

func (r *MemoryRepository) SaveRedirect(ctx context.Context, id int64, m Mutation, input RedirectInput) (Redirect, error) {
	input, fields := NormalizeRedirectInput(input)
	if len(fields) > 0 {
		return Redirect{}, &ValidationError{Fields: fields}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Redirect{}, err
	}
	index := -1
	for i, item := range r.redirects {
		if item.ID == id {
			index = i
		}
	}
	if id < 0 || id > 0 && index < 0 {
		return Redirect{}, ErrNotFound
	}
	if id == 0 && m.ExpectedVersion != 0 || id > 0 && r.redirects[index].Version != m.ExpectedVersion {
		return Redirect{}, ErrVersionConflict
	}
	if r.postIndexLocked(input.PostID) < 0 {
		return Redirect{}, &ValidationError{Fields: map[string]string{"post_id": "目标内容不存在"}}
	}
	for _, post := range r.posts {
		if PublicPath(post) == input.Path {
			return Redirect{}, ErrRedirectConflict
		}
	}
	for _, item := range r.redirects {
		if item.ID != id && item.Path == input.Path {
			return Redirect{}, ErrRedirectConflict
		}
	}
	now := r.now().UTC()
	if id == 0 {
		if len(r.redirects) >= MaxRedirects || r.nextRedirectID >= math.MaxInt64-1 {
			return Redirect{}, RedirectLimitError()
		}
		item := Redirect{RedirectInput: input, ID: r.nextRedirectID, Version: 1, CreatedAt: now, UpdatedAt: now}
		r.nextRedirectID++
		r.redirects = append(r.redirects, item)
		return item, nil
	}
	item := r.redirects[index]
	if item.RedirectInput == input {
		return item, nil
	}
	if item.Version >= math.MaxInt64-1 {
		return Redirect{}, ErrVersionConflict
	}
	item.RedirectInput = input
	item.Version++
	item.Automatic = false
	item.UpdatedAt = now
	r.redirects[index] = item
	return item, nil
}

func (r *MemoryRepository) DeleteRedirect(ctx context.Context, id int64, m Mutation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, item := range r.redirects {
		if item.ID == id {
			if item.Version != m.ExpectedVersion {
				return ErrVersionConflict
			}
			r.redirects = append(r.redirects[:i], r.redirects[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (r *MemoryRepository) ResolveRedirect(ctx context.Context, path string) (string, error) {
	if !ValidRedirectPath(path) {
		return "", ErrNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for _, post := range r.posts {
		if PublicPath(post) == path {
			return "", ErrNotFound
		}
	}
	for _, item := range r.redirects {
		if item.Path == path {
			index := r.postIndexLocked(item.PostID)
			if index >= 0 && PublicAt(r.posts[index], r.now()) {
				return PublicPath(r.posts[index]), nil
			}
			break
		}
	}
	return "", ErrNotFound
}

// Check before other in-memory work, then apply only once every fallible content
// validation has passed. FileRepository commits both facts in one snapshot.
func (r *MemoryRepository) planOldPathLocked(current Post, newType, newSlug string, now time.Time) (*Redirect, error) {
	if !PublicAt(current, now) || current.Type == newType && current.Slug == newSlug {
		return nil, nil
	}
	path := PublicPath(current)
	for _, item := range r.redirects {
		if item.Path == path {
			if item.PostID != current.ID {
				return nil, ErrRedirectConflict
			}
			return nil, nil
		}
	}
	if len(r.redirects) >= MaxRedirects || r.nextRedirectID >= math.MaxInt64-1 {
		return nil, RedirectLimitError()
	}
	return &Redirect{RedirectInput: RedirectInput{Path: path, PostID: current.ID}, ID: r.nextRedirectID, Version: 1, Automatic: true, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

func (r *MemoryRepository) applyOldPathLocked(item *Redirect) {
	if item != nil {
		r.redirects = append(r.redirects, *item)
		r.nextRedirectID++
	}
}
