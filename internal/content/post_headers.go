package content

import (
	"context"
	"errors"
	"time"
)

// PostHeader contains only the fields needed to decide public visibility and
// build a title/link. It does not retain bodies, terms or nested metadata.
type PostHeader struct {
	ID, AuthorID, Version     int64
	Type, Status, Title, Slug string
	PublishedAt, UpdatedAt    time.Time
}

func (h PostHeader) Post() Post {
	return Post{ID: h.ID, AuthorID: h.AuthorID, Version: h.Version, Type: h.Type, Status: h.Status, Title: h.Title, Slug: h.Slug, PublishedAt: h.PublishedAt, UpdatedAt: h.UpdatedAt}
}

func postHeader(p Post) PostHeader {
	return PostHeader{ID: p.ID, AuthorID: p.AuthorID, Version: p.Version, Type: p.Type, Status: p.Status, Title: p.Title, Slug: p.Slug, PublishedAt: p.PublishedAt, UpdatedAt: p.UpdatedAt}
}

// Missing posts are omitted so callers can render a placeholder. A storage or
// cancellation error fails the whole read instead of hiding an outage.
type PostHeadersReader interface {
	GetPostHeaders(context.Context, []int64) (map[int64]PostHeader, error)
}

func ReadPostHeaders(ctx context.Context, r Repository, ids []int64) (map[int64]PostHeader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values map[int64]PostHeader
	var err error
	if reader, ok := r.(PostHeadersReader); ok {
		values, err = reader.GetPostHeaders(ctx, ids)
	} else {
		values = make(map[int64]PostHeader, len(ids))
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			var p Post
			p, err = r.GetByID(ctx, id)
			if errors.Is(err, ErrNotFound) {
				err = nil
				continue
			}
			if err != nil {
				return nil, err
			}
			if p.ID != id || id <= 0 {
				return nil, ErrNotFound
			}
			values[id] = postHeader(p)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for id, h := range values {
		if id <= 0 || h.ID != id {
			return nil, ErrNotFound
		}
	}
	return values, nil
}

func (r *MemoryRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]PostHeader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	values := make(map[int64]PostHeader, len(ids))
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i := 0; len(wanted) > 0 && i < len(r.posts); i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		p := &r.posts[i]
		if wanted[p.ID] {
			values[p.ID] = postHeader(*p)
			delete(wanted, p.ID)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r *FileRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]PostHeader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.GetPostHeaders(ctx, ids)
}
