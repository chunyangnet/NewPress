package content

import "context"

// PostState is the current object authority without bodies, metadata or terms.
type PostState struct {
	ID, AuthorID, Version int64
	Type                  string
}

// PostStateReader is optional so repository adapters keep a safe full-read fallback.
type PostStateReader interface {
	GetPostState(context.Context, int64) (PostState, error)
}

// PostStatesReader checks a page in one storage read. Missing IDs fail the
// complete check; no partial state may authorize an already-prepared response.
type PostStatesReader interface {
	GetPostStates(context.Context, []int64) (map[int64]PostState, error)
}

func ReadPostStates(ctx context.Context, repository Repository, ids []int64) (map[int64]PostState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var states map[int64]PostState
	var err error
	if reader, ok := repository.(PostStatesReader); ok {
		states, err = reader.GetPostStates(ctx, ids)
	} else {
		states = make(map[int64]PostState, len(ids))
		for _, id := range ids {
			if _, found := states[id]; found {
				continue
			}
			states[id], err = ReadPostState(ctx, repository, id)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if state, ok := states[id]; !ok || id <= 0 || state.ID != id {
			return nil, ErrNotFound
		}
	}
	return states, nil
}

func ReadPostState(ctx context.Context, repository Repository, id int64) (PostState, error) {
	if err := ctx.Err(); err != nil {
		return PostState{}, err
	}
	var state PostState
	var err error
	if reader, ok := repository.(PostStateReader); ok {
		state, err = reader.GetPostState(ctx, id)
	} else {
		var post Post
		post, err = repository.GetByID(ctx, id)
		state = postState(post)
	}
	if err != nil {
		return PostState{}, err
	}
	if err = ctx.Err(); err != nil {
		return PostState{}, err
	}
	return state, nil
}

func postState(post Post) PostState {
	return PostState{ID: post.ID, AuthorID: post.AuthorID, Version: post.Version, Type: post.Type}
}

func (repository *MemoryRepository) GetPostState(ctx context.Context, id int64) (PostState, error) {
	if err := ctx.Err(); err != nil {
		return PostState{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return PostState{}, err
	}
	index := repository.postIndexLocked(id)
	if index < 0 {
		return PostState{}, ErrNotFound
	}
	return postState(repository.posts[index]), nil
}

func (repository *FileRepository) GetPostState(ctx context.Context, id int64) (PostState, error) {
	if err := ctx.Err(); err != nil {
		return PostState{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetPostState(ctx, id)
}

func (repository *MemoryRepository) GetPostStates(ctx context.Context, ids []int64) (map[int64]PostState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	states := make(map[int64]PostState, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrNotFound
		}
		states[id] = PostState{}
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remaining := len(states)
	for i := 0; remaining > 0 && i < len(repository.posts); i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		post := &repository.posts[i]
		if state, wanted := states[post.ID]; wanted && state.ID == 0 {
			states[post.ID] = postState(*post)
			remaining--
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remaining != 0 {
		return nil, ErrNotFound
	}
	return states, nil
}

func (repository *FileRepository) GetPostStates(ctx context.Context, ids []int64) (map[int64]PostState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.state.GetPostStates(ctx, ids)
}
