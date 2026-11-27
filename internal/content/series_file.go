package content

import "context"

func (r *FileRepository) ListSeries(ctx context.Context, q SeriesQuery) (SeriesList, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.ListSeries(ctx, q)
}
func (r *FileRepository) GetSeries(ctx context.Context, id int64) (Series, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.GetSeries(ctx, id)
}
func (r *FileRepository) PublishedSeries(ctx context.Context, slug string) (Series, []Post, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PublishedSeries(ctx, slug)
}
func (r *FileRepository) SaveSeries(ctx context.Context, id int64, m Mutation, input SeriesInput) (Series, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (Series, error) { return state.SaveSeries(ctx, id, m, input) })
}
func (r *FileRepository) DeleteSeries(ctx context.Context, id int64, m Mutation) error {
	_, err := fileContentChange(ctx, r, func(state *MemoryRepository) (struct{}, error) { return struct{}{}, state.DeleteSeries(ctx, id, m) })
	return err
}
