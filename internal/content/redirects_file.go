package content

import "context"

func (r *FileRepository) ListRedirects(ctx context.Context, q RedirectQuery) (RedirectList, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.ListRedirects(ctx, q)
}
func (r *FileRepository) GetRedirect(ctx context.Context, id int64) (Redirect, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.GetRedirect(ctx, id)
}
func (r *FileRepository) ResolveRedirect(ctx context.Context, path string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.ResolveRedirect(ctx, path)
}
func (r *FileRepository) SaveRedirect(ctx context.Context, id int64, m Mutation, input RedirectInput) (Redirect, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (Redirect, error) { return state.SaveRedirect(ctx, id, m, input) })
}
func (r *FileRepository) DeleteRedirect(ctx context.Context, id int64, m Mutation) error {
	_, err := fileContentChange(ctx, r, func(state *MemoryRepository) (struct{}, error) { return struct{}{}, state.DeleteRedirect(ctx, id, m) })
	return err
}
