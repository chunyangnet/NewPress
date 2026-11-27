package content

import (
	"context"
)

func (r *MemoryRepository) importPlanLocked(batch ImportBatch) ImportPlan {
	return BuildImportPlan(batch, ImportState{Mappings: r.imports, Posts: r.posts, Redirects: r.redirects, MappingCount: len(r.imports), RedirectCount: len(r.redirects)})
}
func (r *MemoryRepository) PreviewImport(ctx context.Context, batch ImportBatch) (ImportPlan, error) {
	batch, err := NormalizeImportBatch(batch, r.now())
	if err != nil {
		return ImportPlan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err = ctx.Err(); err != nil {
		return ImportPlan{}, err
	}
	return r.importPlanLocked(batch), nil
}
func (r *MemoryRepository) ApplyImport(ctx context.Context, batch ImportBatch, revision string) (ImportPlan, error) {
	batch, err := NormalizeImportBatch(batch, r.now())
	if err != nil {
		return ImportPlan{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return ImportPlan{}, err
	}
	plan := r.importPlanLocked(batch)
	if err = CheckImportPlan(plan, revision); err != nil {
		return plan, err
	}
	if plan.New == 0 {
		return plan, nil
	}
	candidate := cloneContentState(r)
	activityCount := len(candidate.activities)
	for i, entry := range batch.Items {
		if plan.Items[i].Action != "create" {
			continue
		}
		if err = ctx.Err(); err != nil {
			return ImportPlan{}, err
		}
		for _, name := range entry.Categories {
			entry.Input.CategoryIDs = append(entry.Input.CategoryIDs, candidate.ensureTermByNameLocked(TaxonomyCategory, name).ID)
		}
		post, e := candidate.Create(ctx, entry.Input)
		if e != nil {
			return ImportPlan{}, e
		}
		if entry.OldPath != "" && entry.OldPath != PublicPath(post) {
			if _, e = candidate.SaveRedirect(ctx, 0, Mutation{}, RedirectInput{Path: entry.OldPath, PostID: post.ID}); e != nil {
				return ImportPlan{}, e
			}
		}
		candidate.imports = append(candidate.imports, ImportMapping{Source: batch.Source, Format: batch.Format, Key: entry.Key, Digest: entry.Digest, MetadataDigest: entry.MetadataDigest, PostID: post.ID, CreatedAt: candidate.now().UTC()})
		plan.Items[i].Action = "created"
		plan.Items[i].PostID = post.ID
		plan.Items[i].Version = post.Version
	}
	if err = ctx.Err(); err != nil {
		return ImportPlan{}, err
	}
	// Migration establishes historical state; it must not announce the whole
	// archive as new publications to followers.
	candidate.activities = candidate.activities[:activityCount]
	r.posts, r.terms, r.revisions, r.autosaves = candidate.posts, candidate.terms, candidate.revisions, candidate.autosaves
	r.nextID, r.nextTermID, r.nextRevisionID = candidate.nextID, candidate.nextTermID, candidate.nextRevisionID
	r.activities, r.redirects, r.nextRedirectID, r.imports = candidate.activities, candidate.redirects, candidate.nextRedirectID, candidate.imports
	return plan, nil
}

func (r *FileRepository) PreviewImport(ctx context.Context, batch ImportBatch) (ImportPlan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PreviewImport(ctx, batch)
}
func (r *FileRepository) ApplyImport(ctx context.Context, batch ImportBatch, revision string) (ImportPlan, error) {
	return fileContentChange(ctx, r, func(state *MemoryRepository) (ImportPlan, error) { return state.ApplyImport(ctx, batch, revision) })
}
