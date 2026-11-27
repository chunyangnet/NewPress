package content

import (
	"context"
	"math"
	"slices"

	"newpress.local/newpress/internal/contenthtml"
)

func snippetPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 24
	}
	return min(limit, 100), max(0, offset)
}

func (r *MemoryRepository) snippetLocked(id int64) (SnippetRecord, int, error) {
	for i, s := range r.snippets {
		if s.ID == id {
			return s, i, nil
		}
	}
	return SnippetRecord{}, -1, ErrNotFound
}

func (r *MemoryRepository) ListSnippets(ctx context.Context, a SnippetAccess, limit, offset int) (SnippetList, error) {
	if err := CheckSnippetAccess(a, false); err != nil {
		return SnippetList{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := []Snippet{}
	for i := len(r.snippets) - 1; i >= 0; i-- {
		if SnippetReadable(r.snippets[i].Snippet, a) {
			items = append(items, r.snippets[i].Snippet)
		}
	}
	limit, offset = snippetPage(limit, offset)
	start := min(offset, len(items))
	return SnippetList{items[start:min(start+limit, len(items))], len(items)}, ctx.Err()
}

func (r *MemoryRepository) GetSnippet(ctx context.Context, id int64, a SnippetAccess) (SnippetRecord, error) {
	if err := CheckSnippetAccess(a, false); err != nil {
		return SnippetRecord{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, _, err := r.snippetLocked(id)
	if err != nil || !SnippetReadable(s.Snippet, a) {
		return SnippetRecord{}, ErrNotFound
	}
	return SnippetRecordForAccess(s, a), ctx.Err()
}

func (r *MemoryRepository) SaveSnippet(ctx context.Context, id int64, m Mutation, input SnippetInput, a SnippetAccess) (Snippet, error) {
	if CheckSnippetAccess(a, true) != nil || !a.Manage {
		return Snippet{}, ErrSnippetPermission
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snippet{}, err
	}
	var current SnippetRecord
	index := -1
	if id != 0 {
		var err error
		current, index, err = r.snippetLocked(id)
		if err != nil {
			return Snippet{}, err
		}
	}
	if id == 0 {
		if len(r.snippets) >= MaxSnippets || r.nextSnippetID >= math.MaxInt64-1 {
			return Snippet{}, ErrSnippetCapacity
		}
		id = r.nextSnippetID
	}
	next, err := NextSnippetRecord(current, id, m, input, a, r.now().UTC())
	if err != nil {
		return Snippet{}, err
	}
	size, revisions := SnippetStorageSize(next), len(next.Revisions)
	for i, s := range r.snippets {
		if i != index {
			size += SnippetStorageSize(s)
			revisions += len(s.Revisions)
		}
	}
	if size > MaxSnippetStorageBytes || revisions > MaxSnippetRevisions {
		return Snippet{}, ErrSnippetCapacity
	}
	if index < 0 {
		r.snippets = append(r.snippets, next)
		r.nextSnippetID++
	} else {
		r.snippets[index] = next
	}
	r.contentSchema = max(r.contentSchema, 2)
	return next.Snippet, nil
}

func (r *MemoryRepository) WithdrawSnippet(ctx context.Context, id int64, m Mutation, a SnippetAccess) (Snippet, error) {
	if CheckSnippetAccess(a, true) != nil || !a.Manage {
		return Snippet{}, ErrSnippetPermission
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snippet{}, err
	}
	s, index, err := r.snippetLocked(id)
	if err != nil {
		return Snippet{}, err
	}
	s, err = WithdrawSnippetRecord(s, m, a, r.now().UTC())
	if err != nil {
		return Snippet{}, err
	}
	r.snippets[index] = s
	r.contentSchema = max(r.contentSchema, 2)
	return s.Snippet, nil
}

func (r *MemoryRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	if len(keys) > MaxSnippetResolve {
		return nil, ErrSnippetCapacity
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := map[contenthtml.SnippetKey]string{}
	for _, key := range keys {
		s, _, err := r.snippetLocked(key.ID)
		if err != nil || !SnippetVersionShared(s.Snippet, key.Version) {
			continue
		}
		if index, found := slices.BinarySearchFunc(s.Revisions, key.Version, func(rev SnippetRevision, version int64) int {
			if rev.Version < version {
				return -1
			}
			if rev.Version > version {
				return 1
			}
			return 0
		}); found {
			result[key] = s.Revisions[index].ContentHTML
		}
	}
	return result, ctx.Err()
}

func (r *MemoryRepository) SnippetImpacts(ctx context.Context, id int64, a SnippetAccess, limit, offset int) (SnippetImpacts, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return SnippetImpacts{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, _, err := r.snippetLocked(id)
	if err != nil || !SnippetReadable(s.Snippet, a) {
		return SnippetImpacts{}, ErrNotFound
	}
	items := []SnippetImpact{}
	for _, p := range r.posts {
		if err := ctx.Err(); err != nil {
			return SnippetImpacts{}, err
		}
		if SnippetPostWritable(p, a) {
			impact := indexedSnippetImpact(p, id, s.CurrentVersion, r.snippetPostCounts[p.ID])
			if impact.References > 0 {
				items = append(items, impact)
			}
		}
	}
	slices.SortFunc(items, func(a, b SnippetImpact) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	limit, offset = snippetPage(limit, offset)
	start := min(offset, len(items))
	return SnippetImpacts{s.Snippet, items[start:min(start+limit, len(items))], len(items)}, ctx.Err()
}

func (r *MemoryRepository) snippetPlanLocked(u SnippetUpdate, a SnippetAccess) (SnippetPlan, error) {
	s, _, err := r.snippetLocked(u.SnippetID)
	if err != nil {
		return SnippetPlan{}, err
	}
	posts := make([]Post, 0, len(u.Items))
	for _, item := range u.Items {
		for _, p := range r.posts {
			if p.ID == item.ID {
				posts = append(posts, p)
				break
			}
		}
	}
	return BuildSnippetPlan(s.Snippet, posts, u, a)
}

func (r *MemoryRepository) PreviewSnippetUpdate(ctx context.Context, u SnippetUpdate, a SnippetAccess) (SnippetPlan, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return SnippetPlan{}, err
	}
	u, err := NormalizeSnippetUpdate(u, false)
	if err != nil {
		return SnippetPlan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err = ctx.Err(); err != nil {
		return SnippetPlan{}, err
	}
	return r.snippetPlanLocked(u, a)
}

func (r *MemoryRepository) ApplySnippetUpdate(ctx context.Context, u SnippetUpdate, a SnippetAccess) (SnippetResult, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return SnippetResult{}, err
	}
	u, err := NormalizeSnippetUpdate(u, true)
	if err != nil {
		return SnippetResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return SnippetResult{}, err
	}
	fingerprint := SnippetUpdateFingerprint(u, a)
	size := 0
	for _, receipt := range r.snippetReceipts {
		size += SnippetReceiptSize(receipt)
		if receipt.Identity == a.Identity && receipt.RequestKey == u.RequestKey {
			if receipt.Fingerprint != fingerprint {
				return SnippetResult{}, ErrSnippetReplay
			}
			receipt.Items = slices.Clone(receipt.Items)
			return SnippetResult{Receipt: receipt, Replayed: true}, nil
		}
	}
	plan, err := r.snippetPlanLocked(u, a)
	if err != nil {
		return SnippetResult{}, err
	}
	if plan.Digest != u.Digest {
		return SnippetResult{}, ErrVersionConflict
	}
	now := r.now().UTC()
	receipt := NewSnippetReceipt(u, plan, a, now)
	if len(r.snippetReceipts) >= MaxSnippetReceipts || size+SnippetReceiptSize(receipt) > MaxSnippetReceiptBytes || r.nextRevisionID > math.MaxInt64-int64(len(plan.Items))-1 {
		return SnippetResult{}, ErrSnippetCapacity
	}
	// Every fallible check precedes mutation. Readers share this one mutex.
	for _, item := range plan.Items {
		for i, p := range r.posts {
			if p.ID == item.ID {
				p.ContentSource, _ = contenthtml.UpdateSnippetReferences(p.ContentSource, u.SnippetID, plan.Snippet.CurrentVersion)
				p.ContentHTML = contenthtml.Sanitize(p.ContentSource)
				p.Version++
				p.UpdatedAt = now
				r.posts[i] = p
				r.recordRevisionLocked(p, a.Actor)
				delete(r.autosaves, p.ID)
				break
			}
		}
	}
	r.snippetReceipts = append(r.snippetReceipts, receipt)
	r.contentSchema = max(r.contentSchema, 2)
	receipt.Items = slices.Clone(receipt.Items)
	return SnippetResult{Receipt: receipt}, nil
}
