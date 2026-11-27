package content

import "context"

func (repository *FileRepository) ListPublished(ctx context.Context, limit int) ([]Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListPublished(ctx, limit)
}

func (repository *FileRepository) ListPublishedPage(ctx context.Context, limit, offset int) (PublishedPage, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListPublishedPage(ctx, limit, offset)
}

func (repository *FileRepository) GetPublishedBySlug(ctx context.Context, slug string) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetPublishedBySlug(ctx, slug)
}

func (repository *FileRepository) GetPublishedPageBySlug(ctx context.Context, slug string) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetPublishedPageBySlug(ctx, slug)
}

func (repository *FileRepository) GetPublishedFlashBySlug(ctx context.Context, slug string) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetPublishedFlashBySlug(ctx, slug)
}

func (repository *FileRepository) ListAdmin(ctx context.Context, query AdminQuery) (AdminList, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListAdmin(ctx, query)
}

func (repository *FileRepository) GetByID(ctx context.Context, id int64) (Post, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetByID(ctx, id)
}

func (repository *FileRepository) ListRevisions(ctx context.Context, id int64, limit int) ([]Revision, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListRevisions(ctx, id, limit)
}

func (repository *FileRepository) GetRevision(ctx context.Context, id, revisionID int64) (Revision, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetRevision(ctx, id, revisionID)
}

func (repository *FileRepository) GetAutosave(ctx context.Context, id int64, owner string) (Autosave, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetAutosave(ctx, id, owner)
}

func (repository *FileRepository) ListPublishedByTerm(ctx context.Context, taxonomy, slug string, limit, offset int) (Term, []Post, int, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListPublishedByTerm(ctx, taxonomy, slug, limit, offset)
}

func (repository *FileRepository) ListTerms(ctx context.Context, query TermQuery) (TermList, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.ListTerms(ctx, query)
}

func (repository *FileRepository) GetTermByID(ctx context.Context, id int64) (Term, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetTermByID(ctx, id)
}

func (repository *FileRepository) Create(ctx context.Context, input PostInput) (Post, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Post, error) {
		return state.Create(ctx, input)
	})
}

func (repository *FileRepository) Update(ctx context.Context, id int64, mutation Mutation, input PostInput) (Post, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Post, error) {
		return state.Update(ctx, id, mutation, input)
	})
}

func (repository *FileRepository) ChangeStatus(ctx context.Context, id int64, mutation Mutation, status string) (Post, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Post, error) {
		return state.ChangeStatus(ctx, id, mutation, status)
	})
}

func (repository *FileRepository) BulkChangeStatus(ctx context.Context, change BulkStatusChange) ([]Post, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) ([]Post, error) {
		return state.BulkChangeStatus(ctx, change)
	})
}

func (repository *FileRepository) RestoreRevision(ctx context.Context, id, revisionID int64, mutation Mutation) (Post, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Post, error) {
		return state.RestoreRevision(ctx, id, revisionID, mutation)
	})
}

func (repository *FileRepository) SaveAutosave(ctx context.Context, autosave Autosave) (Autosave, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Autosave, error) {
		return state.SaveAutosave(ctx, autosave)
	})
}

func (repository *FileRepository) CreateTerm(ctx context.Context, input TermInput) (Term, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Term, error) {
		return state.CreateTerm(ctx, input)
	})
}

func (repository *FileRepository) UpdateTerm(ctx context.Context, id int64, mutation TermMutation, input TermInput) (Term, error) {
	return fileContentChange(ctx, repository, func(state *MemoryRepository) (Term, error) {
		return state.UpdateTerm(ctx, id, mutation, input)
	})
}

func (repository *FileRepository) DeleteAutosave(ctx context.Context, id int64, owner string) error {
	_, err := fileContentChange(ctx, repository, func(state *MemoryRepository) (struct{}, error) {
		return struct{}{}, state.DeleteAutosave(ctx, id, owner)
	})
	return err
}

func (repository *FileRepository) DeleteTerm(ctx context.Context, id int64, mutation TermMutation) error {
	_, err := fileContentChange(ctx, repository, func(state *MemoryRepository) (struct{}, error) {
		return struct{}{}, state.DeleteTerm(ctx, id, mutation)
	})
	return err
}
