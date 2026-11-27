package content

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

func (r *MemoryRepository) ListSeries(ctx context.Context, query SeriesQuery) (SeriesList, error) {
	q, err := NormalizeSeriesQuery(query)
	if err != nil {
		return SeriesList{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := []Series{}
	now := r.now()
	visible := map[int64]bool{}
	if q.PublicOnly {
		for _, p := range r.posts {
			if p.Type == TypePost && PublicAt(p, now) {
				visible[p.ID] = true
			}
		}
	}
	for _, item := range r.series {
		if q.PublicOnly && item.Status != StatusPublish || q.Kind != "" && item.Kind != q.Kind || q.Search != "" && !strings.Contains(strings.ToLower(item.Title+"\n"+item.Description), strings.ToLower(q.Search)) {
			continue
		}
		item = cloneSeries(item)
		if q.PublicOnly {
			ids := []int64{}
			for _, id := range item.PostIDs {
				if visible[id] {
					ids = append(ids, id)
				}
			}
			item.PostIDs = ids
		}
		if q.PostID > 0 && !slices.Contains(item.PostIDs, q.PostID) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	total := len(items)
	start := min(q.Offset, total)
	return SeriesList{Items: items[start:min(start+q.Limit, total)], Total: total}, ctx.Err()
}

func (r *MemoryRepository) GetSeries(ctx context.Context, id int64) (Series, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.series {
		if s.ID == id {
			return cloneSeries(s), ctx.Err()
		}
	}
	return Series{}, ErrNotFound
}

func (r *MemoryRepository) SaveSeries(ctx context.Context, id int64, mutation Mutation, input SeriesInput) (Series, error) {
	input, fields := NormalizeSeriesInput(input)
	if len(fields) > 0 {
		return Series{}, &ValidationError{Fields: fields}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Series{}, err
	}
	index := -1
	for i, s := range r.series {
		if s.ID == id {
			index = i
		}
	}
	if id < 0 || id > 0 && index < 0 {
		return Series{}, ErrNotFound
	}
	if id == 0 && mutation.ExpectedVersion != 0 || id > 0 && r.series[index].Version != mutation.ExpectedVersion {
		return Series{}, ErrVersionConflict
	}
	for _, s := range r.series {
		if s.ID != id && s.Slug == input.Slug {
			return Series{}, ErrSlugConflict
		}
	}
	posts := map[int64]Post{}
	for _, p := range r.posts {
		posts[p.ID] = p
	}
	for _, postID := range input.PostIDs {
		if posts[postID].Type != TypePost {
			return Series{}, &ValidationError{Fields: map[string]string{"post_ids": "收录的文章已不存在或不再是文章类型，请重新选择"}}
		}
	}
	now := r.now().UTC()
	if id == 0 {
		if len(r.series) >= MaxSeries || r.nextSeriesID >= math.MaxInt64-1 {
			return Series{}, &ValidationError{Fields: map[string]string{"title": "专题与系列已达到数量上限"}}
		}
		item := Series{SeriesInput: input, ID: r.nextSeriesID, Version: 1, CreatedAt: now, UpdatedAt: now}
		r.nextSeriesID++
		r.series = append(r.series, item)
		return cloneSeries(item), nil
	}
	item := r.series[index]
	if EqualSeriesInput(item.SeriesInput, input) {
		return cloneSeries(item), nil
	}
	item.SeriesInput, item.Version, item.UpdatedAt = input, item.Version+1, now
	r.series[index] = item
	return cloneSeries(item), nil
}

func (r *MemoryRepository) DeleteSeries(ctx context.Context, id int64, m Mutation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, s := range r.series {
		if s.ID != id {
			continue
		}
		if m.ExpectedVersion != s.Version {
			return ErrVersionConflict
		}
		r.series = append(r.series[:i], r.series[i+1:]...)
		return nil
	}
	return ErrNotFound
}

func (r *MemoryRepository) publicSeriesLocked(item Series, now time.Time) (Series, []Post) {
	visible := map[int64]Post{}
	for _, p := range r.posts {
		if p.Type == TypePost && PublicAt(p, now) {
			visible[p.ID] = p
		}
	}
	ids := []int64{}
	posts := []Post{}
	for _, id := range item.PostIDs {
		if p, ok := visible[id]; ok {
			ids = append(ids, id)
			posts = append(posts, clonePost(p))
		}
	}
	item.PostIDs = ids
	return item, posts
}

func (r *MemoryRepository) PublishedSeries(ctx context.Context, slug string) (Series, []Post, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.series {
		if s.Slug == slug && s.Status == StatusPublish {
			item, posts := r.publicSeriesLocked(s, r.now())
			return item, posts, ctx.Err()
		}
	}
	return Series{}, nil, ErrNotFound
}
