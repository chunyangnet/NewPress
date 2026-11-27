package content

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/contenthtml"
)

type MemoryRepository struct {
	mu                 sync.RWMutex
	posts              []Post
	terms              []Term
	revisions          map[int64][]Revision
	autosaves          map[int64]map[string]Autosave
	nextID             int64
	nextRevisionID     int64
	nextTermID         int64
	now                func() time.Time
	activities         []activity.Event
	series             []Series
	nextSeriesID       int64
	redirects          []Redirect
	nextRedirectID     int64
	imports            []ImportMapping
	submissions        []SubmissionReceipt
	snippets           []SnippetRecord
	snippetReceipts    []SnippetReceipt
	nextSnippetID      int64
	contentSchema      int
	snippetQuarantines []SnippetFileQuarantine
	snippetPostCounts  map[int64][]contenthtml.SnippetCount
}

func NewMemoryRepository(seed bool) *MemoryRepository {
	now := time.Now()
	repository := &MemoryRepository{
		revisions:         make(map[int64][]Revision),
		autosaves:         make(map[int64]map[string]Autosave),
		nextID:            1,
		nextRevisionID:    1,
		nextTermID:        2,
		nextSeriesID:      1,
		nextRedirectID:    1,
		nextSnippetID:     1,
		contentSchema:     1,
		snippetPostCounts: make(map[int64][]contenthtml.SnippetCount),
		now:               time.Now,
		terms: []Term{{
			ID: 1, Taxonomy: TaxonomyCategory, Slug: "uncategorized", Name: "未分类",
			System: true, Version: 1, CreatedAt: now, UpdatedAt: now,
		}},
	}
	if !seed {
		return repository
	}
	repository.posts = demoPosts()
	for index := range repository.posts {
		post := &repository.posts[index]
		repository.attachLegacyTermsLocked(post)
		if post.ID >= repository.nextID {
			repository.nextID = post.ID + 1
		}
		if post.Version <= 0 {
			post.Version = 1
		}
		if post.CreatedAt.IsZero() {
			post.CreatedAt = post.PublishedAt
		}
		repository.recordRevisionLocked(*post, post.AuthorName)
	}
	return repository
}

func (repository *MemoryRepository) ListPublished(ctx context.Context, limit int) ([]Post, error) {
	page, err := repository.ListPublishedPage(ctx, limit, 0)
	return page.Items, err
}

func (repository *MemoryRepository) ListPublishedPage(_ context.Context, limit, offset int) (PublishedPage, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	now := repository.now()
	posts := make([]Post, 0, len(repository.posts))
	for _, post := range repository.posts {
		if post.Type == TypePost && PublicAt(post, now) {
			posts = append(posts, post)
		}
	}
	sort.SliceStable(posts, func(left, right int) bool {
		if posts[left].IsPinned() != posts[right].IsPinned() {
			return posts[left].IsPinned()
		}
		if posts[left].PublishedAt.Equal(posts[right].PublishedAt) {
			return posts[left].ID > posts[right].ID
		}
		return posts[left].PublishedAt.After(posts[right].PublishedAt)
	})
	total := len(posts)
	start := min(offset, total)
	end := min(start+limit, total)
	items := make([]Post, end-start)
	for index, post := range posts[start:end] {
		items[index] = clonePost(post)
	}
	return PublishedPage{Items: items, Total: total}, nil
}

func (repository *MemoryRepository) GetPublishedBySlug(_ context.Context, slug string) (Post, error) {
	return repository.getPublishedBySlug(TypePost, slug)
}

func (repository *MemoryRepository) GetPublishedPageBySlug(_ context.Context, slug string) (Post, error) {
	return repository.getPublishedBySlug(TypePage, slug)
}

func (repository *MemoryRepository) GetPublishedFlashBySlug(_ context.Context, slug string) (Post, error) {
	return repository.getPublishedBySlug(TypeFlash, slug)
}

func (repository *MemoryRepository) getPublishedBySlug(postType, slug string) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	now := repository.now()
	for _, post := range repository.posts {
		if post.Type == postType && post.Slug == slug && PublicAt(post, now) {
			return clonePost(post), nil
		}
	}
	return Post{}, ErrNotFound
}

func (repository *MemoryRepository) ListAdmin(_ context.Context, query AdminQuery) (AdminList, error) {
	query, err := NormalizeAdminQuery(query)
	if err != nil {
		return AdminList{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	search := strings.ToLower(query.Search)
	allMatches := make([]Post, 0, len(repository.posts))
	statusCounts := make(map[string]int)
	typeCounts := make(map[string]int)
	for _, post := range repository.posts {
		if query.AuthorID > 0 && post.AuthorID != query.AuthorID {
			continue
		}
		if query.Type != "" && post.Type != query.Type {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(post.Title+"\n"+post.Slug+"\n"+post.Excerpt+"\n"+post.AuthorName+"\n"+strings.Join(termNames(post.Categories), " ")+"\n"+strings.Join(termNames(post.Tags), " ")), search) {
			continue
		}
		if !query.PublishedAfter.IsZero() && !post.PublishedAt.After(query.PublishedAfter) || !query.PublishedBefore.IsZero() && (post.PublishedAt.IsZero() || post.PublishedAt.After(query.PublishedBefore)) {
			continue
		}
		statusCounts[post.Status]++
		typeCounts[post.Type]++
		if query.Status != "" && post.Status != query.Status {
			continue
		}
		allMatches = append(allMatches, clonePost(post))
	}
	sort.SliceStable(allMatches, func(left, right int) bool {
		if query.PublicationAscending {
			if allMatches[left].PublishedAt.Equal(allMatches[right].PublishedAt) {
				return allMatches[left].ID < allMatches[right].ID
			}
			return allMatches[left].PublishedAt.Before(allMatches[right].PublishedAt)
		}
		if allMatches[left].UpdatedAt.Equal(allMatches[right].UpdatedAt) {
			return allMatches[left].ID > allMatches[right].ID
		}
		return allMatches[left].UpdatedAt.After(allMatches[right].UpdatedAt)
	})
	total := len(allMatches)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	return AdminList{
		Items: append([]Post(nil), allMatches[start:end]...), Total: total,
		StatusCounts: statusCounts, TypeCounts: typeCounts,
	}, nil
}

func (repository *MemoryRepository) GetByID(_ context.Context, id int64) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, post := range repository.posts {
		if post.ID == id {
			return clonePost(post), nil
		}
	}
	return Post{}, ErrNotFound
}

func (repository *MemoryRepository) Create(_ context.Context, input PostInput) (Post, error) {
	now := repository.now()
	normalized, fields := NormalizePostInput(input, now)
	if len(fields) > 0 {
		return Post{}, &ValidationError{Fields: fields}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.slugExistsLocked(normalized.Type, normalized.Slug, 0) {
		return Post{}, ErrSlugConflict
	}
	categories, tags, err := repository.resolvePostTermsLocked(normalized)
	if err != nil {
		return Post{}, err
	}
	category := ""
	if len(categories) > 0 {
		category = categories[0].Name
	}
	post := Post{
		ID: repository.nextID, AuthorID: normalized.AuthorID, Type: normalized.Type, Status: normalized.Status,
		Slug: normalized.Slug, Title: normalized.Title, Excerpt: normalized.Excerpt,
		ContentSource: normalized.ContentSource, ContentHTML: normalized.ContentHTML,
		CoverURL: normalized.CoverURL, AuthorName: normalized.AuthorName, Category: category,
		Categories: categories, Tags: tags,
		CommentStatus: normalized.CommentStatus, PublishedAt: normalized.PublishedAt,
		CreatedAt: now, UpdatedAt: now, Version: 1,
		Meta: map[string]any{"category": category, "author_name": normalized.AuthorName},
	}
	ApplyAIMetadata(post.Meta, normalized)
	repository.nextID++
	repository.posts = append(repository.posts, post)
	repository.recordRevisionLocked(post, normalized.Actor)
	return clonePost(post), nil
}

func (repository *MemoryRepository) Update(_ context.Context, id int64, mutation Mutation, input PostInput) (Post, error) {
	if err := ValidateRevisionActor(mutation.Actor); err != nil {
		return Post{}, err
	}
	now := repository.now()
	normalized, fields := NormalizePostInput(input, now)
	if len(fields) > 0 {
		return Post{}, &ValidationError{Fields: fields}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.postIndexLocked(id)
	if index < 0 {
		return Post{}, ErrNotFound
	}
	current := repository.posts[index]
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return Post{}, ErrVersionConflict
	}
	if repository.slugExistsLocked(normalized.Type, normalized.Slug, id) {
		return Post{}, ErrSlugConflict
	}
	oldPath, err := repository.planOldPathLocked(current, normalized.Type, normalized.Slug, now)
	if err != nil {
		return Post{}, err
	}
	categories, tags, err := repository.resolvePostTermsLocked(normalized)
	if err != nil {
		return Post{}, err
	}
	category := ""
	if len(categories) > 0 {
		category = categories[0].Name
	}
	current.Type = normalized.Type
	current.Status = normalized.Status
	current.Slug = normalized.Slug
	current.Title = normalized.Title
	current.Excerpt = normalized.Excerpt
	current.ContentSource = normalized.ContentSource
	current.ContentHTML = normalized.ContentHTML
	current.CoverURL = normalized.CoverURL
	current.AuthorName = normalized.AuthorName
	current.Category = category
	current.Categories = categories
	current.Tags = tags
	current.CommentStatus = normalized.CommentStatus
	current.PublishedAt = normalized.PublishedAt
	current.UpdatedAt = now
	current.Version++
	current.Meta = cloneMap(current.Meta)
	if current.Meta == nil {
		current.Meta = make(map[string]any)
	}
	current.Meta["category"] = category
	current.Meta["author_name"] = normalized.AuthorName
	ApplyAIMetadata(current.Meta, normalized)
	repository.applyOldPathLocked(oldPath)
	repository.posts[index] = current
	delete(repository.autosaves, id)
	actor := strings.TrimSpace(mutation.Actor)
	if actor == "" {
		actor = normalized.Actor
	}
	repository.recordRevisionLocked(current, actor)
	return clonePost(current), nil
}

func (repository *MemoryRepository) ChangeStatus(_ context.Context, id int64, mutation Mutation, status string) (Post, error) {
	if err := ValidateRevisionActor(mutation.Actor); err != nil {
		return Post{}, err
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if !ValidStatus(status) {
		return Post{}, &ValidationError{Fields: map[string]string{"status": "内容状态不受支持"}}
	}
	now := repository.now()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.postIndexLocked(id)
	if index < 0 {
		return Post{}, ErrNotFound
	}
	post := repository.posts[index]
	if mutation.ExpectedVersion <= 0 || post.Version != mutation.ExpectedVersion {
		return Post{}, ErrVersionConflict
	}
	if status == StatusPublish && (post.PublishedAt.IsZero() || post.PublishedAt.After(now)) {
		post.PublishedAt = now
	}
	if status == StatusFuture {
		if post.PublishedAt.IsZero() || !post.PublishedAt.After(now) {
			return Post{}, &ValidationError{Fields: map[string]string{"published_at": "定时发布必须选择未来时间"}}
		}
	}
	post.Status = status
	post.Version++
	post.UpdatedAt = now
	repository.posts[index] = post
	delete(repository.autosaves, id)
	repository.recordRevisionLocked(post, mutation.Actor)
	return clonePost(post), nil
}

func (repository *MemoryRepository) BulkChangeStatus(_ context.Context, change BulkStatusChange) ([]Post, error) {
	normalized, fields := NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	now := repository.now()
	repository.mu.Lock()
	defer repository.mu.Unlock()

	// Complete the read/validation phase before the first write so a missing
	// object, stale version or invalid scheduled transition cannot partially
	// change the selection.
	indexes := make([]int, len(normalized.Items))
	for itemIndex, item := range normalized.Items {
		index := repository.postIndexLocked(item.ID)
		if index < 0 {
			return nil, ErrNotFound
		}
		post := repository.posts[index]
		if normalized.AuthorID > 0 && post.AuthorID != normalized.AuthorID {
			return nil, ErrAuthorScope
		}
		if post.Version != item.ExpectedVersion {
			return nil, ErrVersionConflict
		}
		if normalized.Status == StatusFuture && (post.PublishedAt.IsZero() || !post.PublishedAt.After(now)) {
			return nil, &ValidationError{Fields: map[string]string{"published_at": "定时发布必须选择未来时间"}}
		}
		indexes[itemIndex] = index
		if normalized.Delete && post.Status != StatusTrash {
			return nil, &ValidationError{Fields: map[string]string{"selection": "请先将全部所选内容移入回收站，再永久删除"}}
		}
	}

	posts := make([]Post, 0, len(indexes))
	if normalized.Delete {
		deleted := make(map[int64]bool, len(indexes))
		for _, index := range indexes {
			post := repository.posts[index]
			posts = append(posts, clonePost(post))
			deleted[post.ID] = true
			delete(repository.autosaves, post.ID)
			delete(repository.revisions, post.ID)
			delete(repository.snippetPostCounts, post.ID)
		}
		repository.posts = slices.DeleteFunc(repository.posts, func(p Post) bool { return deleted[p.ID] })
		repository.redirects = slices.DeleteFunc(repository.redirects, func(r Redirect) bool { return deleted[r.PostID] })
		repository.imports = slices.DeleteFunc(repository.imports, func(m ImportMapping) bool { return deleted[m.PostID] })
		for i := range repository.series {
			s := &repository.series[i]
			before := len(s.PostIDs)
			s.PostIDs = slices.DeleteFunc(s.PostIDs, func(id int64) bool { return deleted[id] })
			if len(s.PostIDs) != before {
				s.Version++
				s.UpdatedAt = now
			}
		}
		// Keep submission receipts so retries cannot recreate deleted articles.
		for i := range repository.submissions {
			if deleted[repository.submissions[i].PostID] {
				repository.submissions[i].Deleted = true
			}
		}
		return posts, nil
	}
	for itemIndex, index := range indexes {
		post := repository.posts[index]
		if normalized.Status == StatusPublish && (post.PublishedAt.IsZero() || post.PublishedAt.After(now)) {
			post.PublishedAt = now
		}
		post.Status = normalized.Status
		post.Version++
		post.UpdatedAt = now
		repository.posts[index] = post
		delete(repository.autosaves, normalized.Items[itemIndex].ID)
		repository.recordRevisionLocked(post, normalized.Actor)
		posts = append(posts, clonePost(post))
	}
	return posts, nil
}

func (repository *MemoryRepository) ListRevisions(_ context.Context, postID int64, limit int) ([]Revision, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.postIndexLocked(postID) < 0 {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	source := repository.revisions[postID]
	result := make([]Revision, 0, min(limit, len(source)))
	for index := len(source) - 1; index >= 0 && len(result) < limit; index-- {
		revision := source[index]
		revision.Snapshot = clonePost(revision.Snapshot)
		result = append(result, revision)
	}
	return result, nil
}

func (repository *MemoryRepository) GetRevision(_ context.Context, postID, revisionID int64) (Revision, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.postIndexLocked(postID) < 0 {
		return Revision{}, ErrNotFound
	}
	for _, revision := range repository.revisions[postID] {
		if revision.ID == revisionID {
			revision.Snapshot = clonePost(revision.Snapshot)
			return revision, nil
		}
	}
	return Revision{}, ErrNotFound
}

func (repository *MemoryRepository) RestoreRevision(_ context.Context, postID, revisionID int64, mutation Mutation) (Post, error) {
	if err := ValidateRevisionActor(mutation.Actor); err != nil {
		return Post{}, err
	}
	now := repository.now()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.postIndexLocked(postID)
	if index < 0 {
		return Post{}, ErrNotFound
	}
	current := repository.posts[index]
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return Post{}, ErrVersionConflict
	}
	var snapshot Post
	found := false
	for _, revision := range repository.revisions[postID] {
		if revision.ID == revisionID {
			snapshot = revision.Snapshot
			found = true
			break
		}
	}
	if !found {
		return Post{}, ErrNotFound
	}
	if snapshot.Type == TypePost && len(snapshot.Categories) == 0 {
		return Post{}, &ValidationError{Fields: map[string]string{"category_ids": "文章修订必须包含完整的分类关系"}}
	}
	if repository.slugExistsLocked(snapshot.Type, snapshot.Slug, postID) {
		return Post{}, ErrSlugConflict
	}
	oldPath, err := repository.planOldPathLocked(current, snapshot.Type, snapshot.Slug, now)
	if err != nil {
		return Post{}, err
	}
	// Restoring a revision restores editorial fields while preserving the
	// current workflow state so an old snapshot cannot publish by surprise.
	current.Type = snapshot.Type
	current.Slug = snapshot.Slug
	current.Title = snapshot.Title
	current.Excerpt = snapshot.Excerpt
	current.ContentSource = snapshot.ContentSource
	current.ContentHTML = snapshot.ContentHTML
	current.CoverURL = snapshot.CoverURL
	current.AuthorName = snapshot.AuthorName
	categories, tags := repository.resolveSnapshotTermsLocked(snapshot)
	current.Category = ""
	if len(categories) > 0 {
		current.Category = categories[0].Name
	}
	current.Categories = categories
	current.Tags = tags
	current.CommentStatus = snapshot.CommentStatus
	current.Meta = cloneMap(snapshot.Meta)
	if current.Meta == nil {
		current.Meta = make(map[string]any)
	}
	current.Meta["category"] = current.Category
	current.UpdatedAt = now
	current.Version++
	repository.applyOldPathLocked(oldPath)
	repository.posts[index] = current
	delete(repository.autosaves, postID)
	repository.recordRevisionLocked(current, mutation.Actor)
	return clonePost(current), nil
}

func (repository *MemoryRepository) SaveAutosave(_ context.Context, autosave Autosave) (Autosave, error) {
	var err error
	autosave, err = NormalizeAutosave(autosave)
	if err != nil {
		return Autosave{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.postIndexLocked(autosave.PostID)
	if index < 0 {
		return Autosave{}, ErrNotFound
	}
	current := repository.posts[index]
	if current.Version != autosave.SourceVersion || autosave.Snapshot.Version != autosave.SourceVersion {
		return Autosave{}, ErrVersionConflict
	}
	if autosave.Snapshot.ID != current.ID || autosave.Snapshot.AuthorID != current.AuthorID {
		return Autosave{}, &ValidationError{Fields: map[string]string{"snapshot": "自动保存快照与内容对象不匹配"}}
	}
	now := repository.now()
	if previous, ok := repository.autosaves[autosave.PostID][autosave.OwnerKey]; ok && !now.After(previous.UpdatedAt) {
		now = previous.UpdatedAt.Add(time.Nanosecond)
	}
	if !autosave.ExpiresAt.After(now) {
		return Autosave{}, &ValidationError{Fields: map[string]string{"expires_at": "自动保存过期时间必须晚于恢复点更新时间"}}
	}
	if repository.autosaves[autosave.PostID] == nil {
		repository.autosaves[autosave.PostID] = make(map[string]Autosave)
	}
	autosave.UpdatedAt = now
	autosave.Snapshot = clonePost(autosave.Snapshot)
	repository.autosaves[autosave.PostID][autosave.OwnerKey] = autosave
	return cloneAutosave(autosave), nil
}

func (repository *MemoryRepository) GetAutosave(_ context.Context, postID int64, ownerKey string) (Autosave, error) {
	if postID <= 0 || !ValidAutosaveOwnerKey(ownerKey) {
		return Autosave{}, ErrAutosaveNotFound
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.postIndexLocked(postID) < 0 {
		return Autosave{}, ErrNotFound
	}
	autosave, ok := repository.autosaves[postID][ownerKey]
	if !ok {
		return Autosave{}, ErrAutosaveNotFound
	}
	if !autosave.ExpiresAt.After(repository.now()) {
		delete(repository.autosaves[postID], ownerKey)
		if len(repository.autosaves[postID]) == 0 {
			delete(repository.autosaves, postID)
		}
		return Autosave{}, ErrAutosaveNotFound
	}
	return cloneAutosave(autosave), nil
}

func (repository *MemoryRepository) DeleteAutosave(_ context.Context, postID int64, ownerKey string) error {
	if postID <= 0 || !ValidAutosaveOwnerKey(ownerKey) {
		return nil
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.postIndexLocked(postID) < 0 {
		return ErrNotFound
	}
	delete(repository.autosaves[postID], ownerKey)
	if len(repository.autosaves[postID]) == 0 {
		delete(repository.autosaves, postID)
	}
	return nil
}

func (repository *MemoryRepository) Ready(context.Context) error { return nil }

func (repository *MemoryRepository) PostsForSeed() []Post {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	posts := make([]Post, len(repository.posts))
	for index, post := range repository.posts {
		posts[index] = clonePost(post)
	}
	return posts
}

func (repository *MemoryRepository) postIndexLocked(id int64) int {
	for index := range repository.posts {
		if repository.posts[index].ID == id {
			return index
		}
	}
	return -1
}

func (repository *MemoryRepository) slugExistsLocked(postType, slug string, exceptID int64) bool {
	for _, post := range repository.posts {
		if post.ID != exceptID && post.Type == postType && post.Slug == slug {
			return true
		}
	}
	return false
}

func (repository *MemoryRepository) recordRevisionLocked(post Post, actor string) {
	repository.indexSnippetReferencesLocked(post)
	var previous Post
	if revisions := repository.revisions[post.ID]; len(revisions) > 0 {
		previous = revisions[len(revisions)-1].Snapshot
	}
	repository.activities = append(repository.activities, ActivityEvents(previous, post)...)
	revision := Revision{
		ID: repository.nextRevisionID, PostID: post.ID, Version: post.Version,
		Snapshot: clonePost(post), Actor: strings.TrimSpace(actor), CreatedAt: post.UpdatedAt,
	}
	if revision.Actor == "" {
		revision.Actor = post.AuthorName
	}
	repository.nextRevisionID++
	repository.revisions[post.ID] = append(repository.revisions[post.ID], revision)
}

func clonePost(post Post) Post {
	post.Meta = cloneMap(post.Meta)
	post.Categories = append([]Term(nil), post.Categories...)
	post.Tags = append([]Term(nil), post.Tags...)
	return post
}

func cloneAutosave(autosave Autosave) Autosave {
	autosave.Snapshot = clonePost(autosave.Snapshot)
	return autosave
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []int64:
		return append([]int64(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		// External recovery metadata is frozen as JSON by NormalizeAutosave.
		// Canonical writers add only immutable typed values, such as badges.
		return value
	}
}

// ValidateAutosave validates repository-level identity, object and lossless
// JSON representation invariants.
// HTTP-specific field validation remains the caller's responsibility.
func ValidateAutosave(autosave Autosave) error {
	return validateAutosave(autosave, validSnapshotJSON(autosave.Snapshot))
}

func validateAutosave(autosave Autosave, snapshotValid bool) error {
	fields := make(map[string]string)
	if autosave.PostID <= 0 {
		fields["post_id"] = "自动保存必须关联有效内容"
	}
	if !ValidAutosaveOwnerKey(autosave.OwnerKey) {
		fields["owner_key"] = "自动保存身份键不正确"
	}
	if autosave.SourceVersion <= 0 {
		fields["source_version"] = "自动保存来源版本不正确"
	}
	if autosave.Snapshot.ID != autosave.PostID {
		fields["snapshot"] = "自动保存快照与内容编号不匹配"
	}
	if autosave.ExpiresAt.IsZero() {
		fields["expires_at"] = "自动保存必须包含过期时间"
	}
	if !snapshotValid {
		fields["snapshot"] = "自动保存快照必须包含有效的 UTF-8 文本和无歧义的 JSON 数据"
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func ValidAutosaveOwnerKey(value string) bool {
	if strings.HasPrefix(value, "user:") {
		raw := strings.TrimPrefix(value, "user:")
		if raw == "" || len(raw) > 19 || raw[0] == '0' {
			return false
		}
		for _, character := range raw {
			if character < '0' || character > '9' {
				return false
			}
		}
		return true
	}
	if !strings.HasPrefix(value, "legacy:") || len(value) != len("legacy:")+64 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "legacy:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func demoPosts() []Post {
	now := time.Now()
	post := func(id int64, slug, title, excerpt, body, cover, author, category string, publishedAgo, updatedAgo time.Duration) Post {
		publishedAt := now.Add(-publishedAgo)
		updatedAt := now.Add(-updatedAgo)
		return Post{
			// Demo bylines are labels, not persistent user identities. Keeping AuthorID
			// unowned prevents newly created file-store users from inheriting fixtures
			// when their numeric IDs happen to overlap the demo sequence.
			ID: id, AuthorID: 0, Type: TypePost, Status: StatusPublish, Slug: slug,
			Title: title, Excerpt: excerpt, ContentSource: body, ContentHTML: body,
			CoverURL: cover, AuthorName: author, Category: category, CommentStatus: CommentOpen,
			PublishedAt: publishedAt, CreatedAt: publishedAt, UpdatedAt: updatedAt, Version: 1,
			Meta: map[string]any{"category": category, "author_name": author},
		}
	}
	return []Post{
		post(1, "welcome-to-newpress", "NewPress：把 WordPress 的心智，带进 AI 时代", "Go 内核、模块化主题与预留的 AI 运维接口，重新定义内容站点的运行方式。", "<p>NewPress 正在建立一套高性能、可扩展且默认安全的建站引擎。</p><p>它保留 WordPress 易用的内容、插件和主题心智，同时重写运行时边界，让站点从模板进入可组合的产品时代。</p>", "/core-assets/images/cover-core.svg", "NewPress Team", "项目日志", 2*time.Hour, 2*time.Hour),
		post(2, "chunnel-design-system", "Chunnel（隧）：一套主题，适配六种站点", "资源分享、讨论社区、个人博客、论坛圈子、企业官网与影视站，共用同一套高级视觉语言。", "<p>Chunnel 借鉴成熟中文内容站点的信息密度，但所有布局、样式和组件均重新设计。</p><p>首页模块、侧栏与内容卡片按场景组合，而不是把站点锁死在某一种内容形态里。</p>", "/core-assets/images/cover-chunnel.svg", "隧设计组", "主题设计", 26*time.Hour, 20*time.Hour),
		post(3, "wasm-plugin-sandbox", "插件不再拥有整个服务器", "WASM 插件只获得显式授权的内容、存储、网络和任务能力。", "<p>每个插件都必须声明能力。核心可以限制执行时间、内存、出站网络和数据访问范围。</p>", "/core-assets/images/cover-plugin.svg", "NewPress Team", "扩展架构", 50*time.Hour, 46*time.Hour),
		post(4, "low-memory-production-stack", "一台 4G 服务器，怎样跑稳内容社区", "从 Go 内存上限、连接池到缓存淘汰，给低配生产环境一套可复现的资源预算。", "<p>性能不是只看单次请求的跑分，更要看数据库、缓存、代理和操作系统共同争抢资源时，站点能否保持稳定。</p><p>NewPress 默认部署将可选组件留在 profile 中，并为每个进程保留明确的内存余量。</p>", "/core-assets/images/cover-core.svg", "NewPress Team", "项目日志", 74*time.Hour, 70*time.Hour),
		post(5, "dense-layout-rhythm", "高密度首页的节奏，藏在十二个小地方", "标题字重、卡片边界、数字字体和侧栏节奏，决定一个页面看起来是丰富还是拥挤。", "<p>信息密度不是把更多东西塞进屏幕，而是让重要信息更快被识别，让次要信息仍然可以被找到。</p><p>Chunnel 用强标题、薄分隔和少量深色模块建立视觉节拍。</p>", "/core-assets/images/cover-chunnel.svg", "隧设计组", "主题设计", 98*time.Hour, 90*time.Hour),
		post(6, "wordpress-migration-map", "从 WordPress 迁移到 NewPress，要保留什么", "文章、分类、媒体和 URL 需要精确迁移；PHP 全局状态和任意插件执行则不应该被带回来。", "<p>迁移兼容的重点是用户资产和公开行为，而不是复制旧运行时的全部约束。</p><p>导入器会记录旧 ID、规范化路径，并在切换前生成可审计的差异报告。</p>", "/core-assets/images/cover-core.svg", "NewPress Team", "项目日志", 122*time.Hour, 116*time.Hour),
		post(7, "typed-hooks-and-outbox", "Hook 不只是回调：还要有顺序、超时与事务", "Guard、Filter 和 Observer 各自承担不同责任，异步事件必须从提交后的 outbox 出发。", "<p>同步 Guard 可以拒绝操作，Filter 必须返回新的数据，而 Observer 不应该再影响主请求的结果。</p><p>明确这些规则，插件之间才不会依靠偶然的执行顺序工作。</p>", "/core-assets/images/cover-plugin.svg", "WASM Lab", "扩展架构", 146*time.Hour, 139*time.Hour),
		post(8, "creator-membership-path", "从公开内容到会员社区，中间不是一道付费墙", "先建立持续价值，再把资源、讨论和服务组合成清楚的会员路径。", "<p>会员不是隐藏更多文章，而是把内容、关系和服务组织成长期可感知的价值。</p><p>主题只负责表达这些权益，订单、积分和访问控制仍然属于插件。</p>", "/core-assets/images/cover-chunnel.svg", "一页产品", "主题设计", 170*time.Hour, 164*time.Hour),
		post(9, "postgres-content-model", "别把所有字段都塞进 postmeta", "常用字段规范化、扩展字段进入 JSONB，需要检索的字段再建立显式索引。", "<p>通用 EAV 看起来灵活，却会把查询、约束和索引成本推给每一个后续功能。</p><p>NewPress 的内容模型保留结构化扩展，同时让常见列表和归档保持清晰的执行计划。</p>", "/core-assets/images/cover-plugin.svg", "NewPress Team", "扩展架构", 194*time.Hour, 188*time.Hour),
	}
}

var _ Repository = (*MemoryRepository)(nil)
