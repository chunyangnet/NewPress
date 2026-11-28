package comment

import "context"

// State is the current ownership, moderation and version without private text.
type State struct {
	ID, PostID, UserID, Version int64
	Status                      string
}

func state(c Comment) State {
	return State{ID: c.ID, PostID: c.PostID, UserID: c.UserID, Version: c.Version, Status: c.Status}
}

type StatesReader interface {
	GetCommentStates(context.Context, []int64) (map[int64]State, error)
}

func ReadStates(ctx context.Context, s Store, ids []int64) (map[int64]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values map[int64]State
	var err error
	if reader, ok := s.(StatesReader); ok {
		values, err = reader.GetCommentStates(ctx, ids)
	} else {
		values = make(map[int64]State, len(ids))
		for _, id := range ids {
			if _, ok := values[id]; ok {
				continue
			}
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			var c Comment
			c, err = s.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			values[id] = state(c)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if v, ok := values[id]; !ok || id <= 0 || v.ID != id {
			return nil, ErrNotFound
		}
	}
	return values, nil
}

func (s *FileStore) GetCommentStates(ctx context.Context, ids []int64) (map[int64]State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make(map[int64]State, len(ids))
	for _, id := range ids {
		c, ok := s.items[id]
		if !ok {
			return nil, ErrNotFound
		}
		values[id] = state(c)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
