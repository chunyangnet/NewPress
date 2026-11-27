package content

import (
	"context"
	"newpress.local/newpress/internal/contenthtml"
)

func (r *FileRepository) ListSnippets(ctx context.Context, a SnippetAccess, limit, offset int) (SnippetList, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.ListSnippets(ctx, a, limit, offset)
}
func (r *FileRepository) GetSnippet(ctx context.Context, id int64, a SnippetAccess) (SnippetRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.GetSnippet(ctx, id, a)
}
func (r *FileRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.ResolveSnippets(ctx, keys)
}
func (r *FileRepository) SnippetImpacts(ctx context.Context, id int64, a SnippetAccess, limit, offset int) (SnippetImpacts, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.SnippetImpacts(ctx, id, a, limit, offset)
}
func (r *FileRepository) PreviewSnippetUpdate(ctx context.Context, u SnippetUpdate, a SnippetAccess) (SnippetPlan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PreviewSnippetUpdate(ctx, u, a)
}
func (r *FileRepository) SaveSnippet(ctx context.Context, id int64, m Mutation, input SnippetInput, a SnippetAccess) (Snippet, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (Snippet, error) { return state.SaveSnippet(ctx, id, m, input, a) })
}
func (r *FileRepository) WithdrawSnippet(ctx context.Context, id int64, m Mutation, a SnippetAccess) (Snippet, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (Snippet, error) { return state.WithdrawSnippet(ctx, id, m, a) })
}
func (r *FileRepository) ApplySnippetUpdate(ctx context.Context, u SnippetUpdate, a SnippetAccess) (SnippetResult, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (SnippetResult, error) { return state.ApplySnippetUpdate(ctx, u, a) })
}
