package content

import (
	"context"
	"time"
)

// AutosaveState contains only the current authority and recovery identity. It
// never returns draft bodies, metadata, terms, or canonical content.
type AutosaveState struct {
	PostID        int64
	AuthorID      int64
	Type          string
	PostVersion   int64
	SourceVersion int64
	UpdatedAt     time.Time
	ExpiresAt     time.Time
}

// AutosaveStateReader is optional so repository adapters retain the safe fallback.
type AutosaveStateReader interface {
	GetAutosaveState(context.Context, int64, string) (AutosaveState, error)
}

func ReadAutosaveState(ctx context.Context, repository Repository, id int64, owner string) (AutosaveState, error) {
	if err := ctx.Err(); err != nil {
		return AutosaveState{}, err
	}
	if reader, ok := repository.(AutosaveStateReader); ok {
		state, err := reader.GetAutosaveState(ctx, id, owner)
		if err != nil {
			return AutosaveState{}, err
		}
		if err = ctx.Err(); err != nil {
			return AutosaveState{}, err
		}
		return state, nil
	}
	autosave, err := repository.GetAutosave(ctx, id, owner)
	if err != nil {
		return AutosaveState{}, err
	}
	post, err := repository.GetByID(ctx, id)
	if err != nil {
		return AutosaveState{}, err
	}
	if err = ctx.Err(); err != nil {
		return AutosaveState{}, err
	}
	return autosaveState(post, autosave), nil
}

func autosaveState(post Post, autosave Autosave) AutosaveState {
	return AutosaveState{PostID: post.ID, AuthorID: post.AuthorID, Type: post.Type,
		PostVersion: post.Version, SourceVersion: autosave.SourceVersion,
		UpdatedAt: autosave.UpdatedAt, ExpiresAt: autosave.ExpiresAt}
}

func (repository *MemoryRepository) GetAutosaveState(ctx context.Context, id int64, owner string) (AutosaveState, error) {
	if err := ctx.Err(); err != nil {
		return AutosaveState{}, err
	}
	if id <= 0 || !ValidAutosaveOwnerKey(owner) {
		return AutosaveState{}, ErrAutosaveNotFound
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return AutosaveState{}, err
	}
	index := repository.postIndexLocked(id)
	if index < 0 {
		return AutosaveState{}, ErrNotFound
	}
	autosave, ok := repository.autosaves[id][owner]
	if !ok || !autosave.ExpiresAt.After(repository.now()) {
		return AutosaveState{}, ErrAutosaveNotFound
	}
	return autosaveState(repository.posts[index], autosave), nil
}

func (repository *FileRepository) GetAutosaveState(ctx context.Context, id int64, owner string) (AutosaveState, error) {
	if err := ctx.Err(); err != nil {
		return AutosaveState{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetAutosaveState(ctx, id, owner)
}
