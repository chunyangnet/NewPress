package content

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (repository *MemoryRepository) ListPublishedByTerm(_ context.Context, taxonomy, slug string, limit, offset int) (Term, []Post, int, error) {
	taxonomy = strings.ToLower(strings.TrimSpace(taxonomy))
	slug = NormalizeTermSlug(slug)
	if !ValidTaxonomy(taxonomy) || !ValidTermSlug(slug) {
		return Term{}, nil, 0, ErrTermNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	term, ok := repository.termBySlugLocked(taxonomy, slug)
	if !ok {
		return Term{}, nil, 0, ErrTermNotFound
	}
	now := repository.now()
	items := make([]Post, 0)
	for _, post := range repository.posts {
		if post.Type != TypePost || !PublicAt(post, now) || !postHasTerm(post, term.ID) {
			continue
		}
		items = append(items, clonePost(post))
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].IsPinned() != items[right].IsPinned() {
			return items[left].IsPinned()
		}
		if items[left].PublishedAt.Equal(items[right].PublishedAt) {
			return items[left].ID > items[right].ID
		}
		return items[left].PublishedAt.After(items[right].PublishedAt)
	})
	total := len(items)
	start := min(offset, total)
	end := min(start+limit, total)
	term = repository.decorateTermLocked(term)
	term.Count = total
	return term, append([]Post(nil), items[start:end]...), total, nil
}

func (repository *MemoryRepository) ListTerms(_ context.Context, query TermQuery) (TermList, error) {
	query, err := NormalizeTermQuery(query)
	if err != nil {
		return TermList{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	needle := strings.ToLower(query.Search)
	items := make([]Term, 0, len(repository.terms))
	counts := map[string]int{TaxonomyCategory: 0, TaxonomyTag: 0}
	for _, source := range repository.terms {
		counts[source.Taxonomy]++
		if query.Taxonomy != "" && source.Taxonomy != query.Taxonomy {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(source.Name+"\n"+source.Slug+"\n"+source.Description), needle) {
			continue
		}
		items = append(items, repository.decorateTermLocked(source))
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Taxonomy != items[right].Taxonomy {
			return items[left].Taxonomy == TaxonomyCategory
		}
		leftPath := repository.termSortPathLocked(items[left])
		rightPath := repository.termSortPathLocked(items[right])
		if leftPath == rightPath {
			return items[left].ID < items[right].ID
		}
		return leftPath < rightPath
	})
	total := len(items)
	start := min(query.Offset, total)
	end := min(start+query.Limit, total)
	return TermList{Items: append([]Term(nil), items[start:end]...), Total: total, Counts: counts}, nil
}

func (repository *MemoryRepository) GetTermByID(_ context.Context, id int64) (Term, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	term, ok := repository.termByIDLocked(id)
	if !ok {
		return Term{}, ErrTermNotFound
	}
	return repository.decorateTermLocked(term), nil
}

func (repository *MemoryRepository) CreateTerm(_ context.Context, input TermInput) (Term, error) {
	normalized, fields := NormalizeTermInput(input)
	if len(fields) > 0 {
		return Term{}, &ValidationError{Fields: fields}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.termSlugExistsLocked(normalized.Taxonomy, normalized.Slug, 0) {
		return Term{}, ErrTermSlugConflict
	}
	if err := repository.validateParentLocked(normalized.Taxonomy, normalized.ParentID, 0); err != nil {
		return Term{}, err
	}
	now := repository.now()
	term := Term{
		ID: repository.nextTermID, Taxonomy: normalized.Taxonomy, ParentID: normalized.ParentID,
		Slug: normalized.Slug, Name: normalized.Name, Description: normalized.Description,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	repository.nextTermID++
	repository.terms = append(repository.terms, term)
	return repository.decorateTermLocked(term), nil
}

func (repository *MemoryRepository) UpdateTerm(_ context.Context, id int64, mutation TermMutation, input TermInput) (Term, error) {
	normalized, fields := NormalizeTermInput(input)
	if len(fields) > 0 {
		return Term{}, &ValidationError{Fields: fields}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.termIndexLocked(id)
	if index < 0 {
		return Term{}, ErrTermNotFound
	}
	current := repository.terms[index]
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return Term{}, ErrTermVersionConflict
	}
	if normalized.Taxonomy != current.Taxonomy {
		return Term{}, &ValidationError{Fields: map[string]string{"taxonomy": "已创建的术语不能切换分类法"}}
	}
	if repository.termSlugExistsLocked(normalized.Taxonomy, normalized.Slug, id) {
		return Term{}, ErrTermSlugConflict
	}
	if err := repository.validateParentLocked(normalized.Taxonomy, normalized.ParentID, id); err != nil {
		return Term{}, err
	}
	current.ParentID = normalized.ParentID
	current.Slug = normalized.Slug
	current.Name = normalized.Name
	current.Description = normalized.Description
	current.Version++
	current.UpdatedAt = repository.now()
	repository.terms[index] = current
	repository.refreshTermOnPostsLocked(current)
	return repository.decorateTermLocked(current), nil
}

func (repository *MemoryRepository) DeleteTerm(_ context.Context, id int64, mutation TermMutation) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	index := repository.termIndexLocked(id)
	if index < 0 {
		return ErrTermNotFound
	}
	term := repository.terms[index]
	if mutation.ExpectedVersion <= 0 || term.Version != mutation.ExpectedVersion {
		return ErrTermVersionConflict
	}
	if term.System {
		return ErrTermProtected
	}
	for childIndex := range repository.terms {
		if repository.terms[childIndex].ParentID == term.ID {
			repository.terms[childIndex].ParentID = term.ParentID
			repository.terms[childIndex].Version++
			repository.terms[childIndex].UpdatedAt = repository.now()
		}
	}
	repository.terms = append(repository.terms[:index], repository.terms[index+1:]...)
	defaultCategory := repository.defaultCategoryLocked()
	for postIndex := range repository.posts {
		post := &repository.posts[postIndex]
		if term.Taxonomy == TaxonomyCategory {
			post.Categories = removeTerm(post.Categories, term.ID)
			if post.Type == TypePost && len(post.Categories) == 0 {
				post.Categories = []Term{defaultCategory}
			}
			post.Category = ""
			if len(post.Categories) > 0 {
				post.Category = post.Categories[0].Name
			}
			if post.Meta == nil {
				post.Meta = make(map[string]any)
			}
			post.Meta["category"] = post.Category
		} else {
			post.Tags = removeTerm(post.Tags, term.ID)
		}
	}
	return nil
}

func (repository *MemoryRepository) attachLegacyTermsLocked(post *Post) {
	if post.Type != TypePost {
		post.Category = ""
		post.Categories = nil
		post.Tags = nil
		return
	}
	categoryName := strings.TrimSpace(post.Category)
	if categoryName == "" {
		categoryName = "未分类"
	}
	category := repository.ensureTermByNameLocked(TaxonomyCategory, categoryName)
	post.Category = category.Name
	post.Categories = []Term{category}
	for _, name := range demoPostTags[post.Slug] {
		post.Tags = append(post.Tags, repository.ensureTermByNameLocked(TaxonomyTag, name))
	}
	if post.Meta == nil {
		post.Meta = make(map[string]any)
	}
	post.Meta["category"] = post.Category
}

func (repository *MemoryRepository) resolvePostTermsLocked(input PostInput) ([]Term, []Term, error) {
	if input.Type != TypePost {
		return nil, nil, nil
	}
	categories := make([]Term, 0, max(1, len(input.CategoryIDs)))
	for _, id := range input.CategoryIDs {
		term, ok := repository.termByIDLocked(id)
		if !ok || term.Taxonomy != TaxonomyCategory {
			return nil, nil, &ValidationError{Fields: map[string]string{"category_ids": "所选分类不存在或已被删除"}}
		}
		categories = append(categories, term)
	}
	if len(categories) == 0 {
		name := strings.TrimSpace(input.Category)
		if name == "" {
			categories = append(categories, repository.defaultCategoryLocked())
		} else {
			categories = append(categories, repository.ensureTermByNameLocked(TaxonomyCategory, name))
		}
	}
	tags := make([]Term, 0, len(input.TagNames))
	for _, name := range input.TagNames {
		tags = append(tags, repository.ensureTermByNameLocked(TaxonomyTag, name))
	}
	return categories, tags, nil
}

func (repository *MemoryRepository) resolveSnapshotTermsLocked(post Post) ([]Term, []Term) {
	if post.Type != TypePost {
		return nil, nil
	}
	categories := make([]Term, 0, max(1, len(post.Categories)))
	for _, snapshot := range post.Categories {
		if term, ok := repository.termByIDLocked(snapshot.ID); ok && term.Taxonomy == TaxonomyCategory {
			categories = append(categories, term)
		} else if strings.TrimSpace(snapshot.Name) != "" {
			categories = append(categories, repository.ensureTermByNameLocked(TaxonomyCategory, snapshot.Name))
		}
	}
	tags := make([]Term, 0, len(post.Tags))
	for _, snapshot := range post.Tags {
		if term, ok := repository.termByIDLocked(snapshot.ID); ok && term.Taxonomy == TaxonomyTag {
			tags = append(tags, term)
		} else if strings.TrimSpace(snapshot.Name) != "" {
			tags = append(tags, repository.ensureTermByNameLocked(TaxonomyTag, snapshot.Name))
		}
	}
	return categories, tags
}

func (repository *MemoryRepository) ensureTermByNameLocked(taxonomy, name string) Term {
	name = strings.TrimSpace(name)
	for _, term := range repository.terms {
		if term.Taxonomy == taxonomy && strings.EqualFold(term.Name, name) {
			return term
		}
	}
	slug := NormalizeTermSlug(name)
	if slug == "" {
		slug = "term"
	}
	base := slug
	for suffix := 2; repository.termSlugExistsLocked(taxonomy, slug, 0); suffix++ {
		slug = TermSlugWithSuffix(base, suffix)
	}
	now := repository.now()
	term := Term{
		ID: repository.nextTermID, Taxonomy: taxonomy, Slug: slug, Name: name,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	repository.nextTermID++
	repository.terms = append(repository.terms, term)
	return term
}

func (repository *MemoryRepository) defaultCategoryLocked() Term {
	for _, term := range repository.terms {
		if term.Taxonomy == TaxonomyCategory && term.System {
			return term
		}
	}
	term := repository.ensureTermByNameLocked(TaxonomyCategory, "未分类")
	index := repository.termIndexLocked(term.ID)
	repository.terms[index].System = true
	return repository.terms[index]
}

func (repository *MemoryRepository) decorateTermLocked(term Term) Term {
	term.Count = repository.termCountLocked(term.ID)
	term.Depth = repository.termDepthLocked(term)
	if term.ParentID > 0 {
		if parent, ok := repository.termByIDLocked(term.ParentID); ok {
			term.ParentName = parent.Name
		}
	}
	return term
}

func (repository *MemoryRepository) termDepthLocked(term Term) int {
	depth := 0
	seen := map[int64]bool{term.ID: true}
	for term.ParentID > 0 && depth < 20 {
		parent, ok := repository.termByIDLocked(term.ParentID)
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		term = parent
		depth++
	}
	return depth
}

func (repository *MemoryRepository) termSortPathLocked(term Term) string {
	parts := []string{strings.ToLower(term.Name)}
	seen := map[int64]bool{term.ID: true}
	for term.ParentID > 0 && len(parts) < 20 {
		parent, ok := repository.termByIDLocked(term.ParentID)
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		parts = append(parts, strings.ToLower(parent.Name))
		term = parent
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, "\x00")
}

func (repository *MemoryRepository) validateParentLocked(taxonomy string, parentID, termID int64) error {
	if parentID == 0 {
		return nil
	}
	if taxonomy != TaxonomyCategory || parentID == termID {
		return ErrTermHierarchyConflict
	}
	parent, ok := repository.termByIDLocked(parentID)
	if !ok || parent.Taxonomy != taxonomy {
		return &ValidationError{Fields: map[string]string{"parent_id": "父级分类不存在"}}
	}
	seen := map[int64]bool{termID: true}
	for parent.ID > 0 {
		if seen[parent.ID] {
			return ErrTermHierarchyConflict
		}
		seen[parent.ID] = true
		if parent.ParentID == 0 {
			break
		}
		next, ok := repository.termByIDLocked(parent.ParentID)
		if !ok {
			break
		}
		parent = next
	}
	return nil
}

func (repository *MemoryRepository) refreshTermOnPostsLocked(term Term) {
	for postIndex := range repository.posts {
		post := &repository.posts[postIndex]
		for categoryIndex := range post.Categories {
			if post.Categories[categoryIndex].ID == term.ID {
				post.Categories[categoryIndex] = term
			}
		}
		for tagIndex := range post.Tags {
			if post.Tags[tagIndex].ID == term.ID {
				post.Tags[tagIndex] = term
			}
		}
		if len(post.Categories) > 0 {
			post.Category = post.Categories[0].Name
			if post.Meta == nil {
				post.Meta = make(map[string]any)
			}
			post.Meta["category"] = post.Category
		}
	}
}

func (repository *MemoryRepository) termCountLocked(termID int64) int {
	count := 0
	now := repository.now()
	for _, post := range repository.posts {
		if post.Type == TypePost && PublicAt(post, now) && postHasTerm(post, termID) {
			count++
		}
	}
	return count
}

func (repository *MemoryRepository) termIndexLocked(id int64) int {
	for index := range repository.terms {
		if repository.terms[index].ID == id {
			return index
		}
	}
	return -1
}

func (repository *MemoryRepository) termByIDLocked(id int64) (Term, bool) {
	index := repository.termIndexLocked(id)
	if index < 0 {
		return Term{}, false
	}
	return repository.terms[index], true
}

func (repository *MemoryRepository) termBySlugLocked(taxonomy, slug string) (Term, bool) {
	for _, term := range repository.terms {
		if term.Taxonomy == taxonomy && term.Slug == slug {
			return term, true
		}
	}
	return Term{}, false
}

func (repository *MemoryRepository) termSlugExistsLocked(taxonomy, slug string, exceptID int64) bool {
	for _, term := range repository.terms {
		if term.ID != exceptID && term.Taxonomy == taxonomy && term.Slug == slug {
			return true
		}
	}
	return false
}

func postHasTerm(post Post, termID int64) bool {
	for _, term := range post.Categories {
		if term.ID == termID {
			return true
		}
	}
	for _, term := range post.Tags {
		if term.ID == termID {
			return true
		}
	}
	return false
}

func removeTerm(terms []Term, id int64) []Term {
	result := terms[:0]
	for _, term := range terms {
		if term.ID != id {
			result = append(result, term)
		}
	}
	return result
}

var demoPostTags = map[string][]string{
	"welcome-to-newpress":         {"Go", "CMS", "架构"},
	"chunnel-design-system":       {"Chunnel", "界面设计", "主题"},
	"wasm-plugin-sandbox":         {"WASM", "插件", "安全"},
	"low-memory-production-stack": {"性能", "部署", "PostgreSQL"},
	"dense-layout-rhythm":         {"信息密度", "设计系统"},
	"wordpress-migration-map":     {"WordPress", "迁移", "兼容性"},
	"typed-hooks-and-outbox":      {"Hook", "Outbox", "事务"},
	"creator-membership-path":     {"创作者", "会员", "社区"},
	"postgres-content-model":      {"PostgreSQL", "JSONB", "数据模型"},
}

func taxonomyLabel(taxonomy string) string {
	if taxonomy == TaxonomyCategory {
		return "分类目录"
	}
	if taxonomy == TaxonomyTag {
		return "文章标签"
	}
	return fmt.Sprintf("分类法 %s", taxonomy)
}
