package mediausage

import (
	"context"
	"errors"

	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

type contentRepository struct {
	content.Repository
	usage *Service
}

func (r *contentRepository) SaveSnippet(ctx context.Context, id int64, mutation content.Mutation, input content.SnippetInput, access content.SnippetAccess) (content.Snippet, error) {
	_, body, err := content.NormalizeSnippetInput(input)
	if err != nil {
		return content.Snippet{}, err
	}
	release, fields := r.usage.hold(r.usage.resolver.Content("", input.ContentSource, body, "/"))
	defer release()
	if len(fields) > 0 {
		return content.Snippet{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.SaveSnippet(ctx, id, mutation, input, access)
}

func (r *contentRepository) Submit(ctx context.Context, request content.Submission) (content.SubmissionResult, error) {
	if err := content.ValidateSubmission(request); err != nil {
		return content.SubmissionResult{}, err
	}
	// Already committed requests must remain confirmable after media changes.
	if result, err := r.Repository.FindSubmission(ctx, request.Key, request.Fingerprint); !errors.Is(err, content.ErrNotFound) {
		if err == nil && request.SnippetDraft != nil {
			// A guarded replay still checks current account and draft ownership.
			return r.Repository.Submit(ctx, request)
		}
		return result, err
	}
	if g := request.SnippetDraft; g != nil {
		r.usage.gate.RLock()
		defer r.usage.gate.RUnlock()
		// Another same-key request can commit between the first receipt lookup
		// and this preview. Confirm that receipt before reporting a stale source
		// or media failure, especially after a concurrent withdrawal.
		failedPreview := func(cause error) (content.SubmissionResult, error) {
			_, lookup := r.Repository.FindSubmission(ctx, request.Key, request.Fingerprint)
			if lookup == nil {
				return r.Repository.Submit(ctx, request)
			}
			if !errors.Is(lookup, content.ErrNotFound) {
				return content.SubmissionResult{}, lookup
			}
			return content.SubmissionResult{}, cause
		}
		u := g.Request
		u.Digest, u.RequestKey = "", ""
		plan, err := r.Repository.PreviewSnippetDraft(ctx, u, g.Access)
		if err != nil {
			return failedPreview(err)
		}
		if plan.Digest != g.Request.Digest {
			return failedPreview(content.ErrVersionConflict)
		}
		if fields := r.usage.validate(r.usage.resolver.Content(plan.CoverURL, plan.ContentHTML, "", contentPath(plan.Type, "snippet-draft-"+request.Key))); len(fields) > 0 {
			return failedPreview(&content.ValidationError{Fields: fields})
		}
		// Submit rebuilds and checks this same digest under the storage lock.
		return r.Repository.Submit(ctx, request)
	}
	if request.Action == "create" || request.MutationAction() == "save" {
		release, fields := r.hold(request.Input)
		defer release()
		if len(fields) > 0 {
			return content.SubmissionResult{}, &content.ValidationError{Fields: fields}
		}
	} else if request.Action == "restore_revision" {
		r.usage.gate.RLock()
		defer r.usage.gate.RUnlock()
		revision, err := r.Repository.GetRevision(ctx, request.PostID, request.RevisionID)
		if err != nil {
			return content.SubmissionResult{}, err
		}
		snapshot := revision.Snapshot
		fields := r.usage.validate(r.usage.resolver.Content(snapshot.CoverURL, snapshot.ContentSource, snapshot.ContentHTML, contentPath(snapshot.Type, snapshot.Slug)))
		if len(fields) > 0 {
			return content.SubmissionResult{}, &content.ValidationError{Fields: fields}
		}
	}
	if err := ctx.Err(); err != nil {
		return content.SubmissionResult{}, err
	}
	return r.Repository.Submit(ctx, request)
}

func (r *contentRepository) SaveSeries(ctx context.Context, id int64, mutation content.Mutation, input content.SeriesInput) (content.Series, error) {
	release, fields := r.usage.hold(r.usage.resolver.Content(input.CoverURL, "", "", "/series/"+input.Slug+"/"))
	defer release()
	if len(fields) > 0 {
		return content.Series{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.SaveSeries(ctx, id, mutation, input)
}

func (r *contentRepository) hold(input content.PostInput) (func(), map[string]string) {
	return r.usage.hold(r.usage.resolver.Content(input.CoverURL, input.ContentSource, "", contentPath(input.Type, input.Slug)))
}

// previewImport runs with the media gate held. Retained import identities do
// not write their input or introduce references, even when the source changed.
func (r *contentRepository) previewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	plan, err := r.Repository.PreviewImport(ctx, batch)
	if err != nil {
		return content.ImportPlan{}, err
	}
	links := make(assetref.Fields)
	for i, row := range plan.Items {
		if row.Action != "create" {
			continue
		}
		entry := batch.Items[i]
		for field, items := range r.usage.resolver.Content(entry.Input.CoverURL, entry.Input.ContentSource, "", contentPath(entry.Input.Type, entry.Input.Slug)) {
			links[field] = append(links[field], items...)
		}
	}
	if fields := r.usage.validate(links); len(fields) > 0 {
		return content.ImportPlan{}, &content.ValidationError{Fields: fields}
	}
	return plan, nil
}
func (r *contentRepository) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	r.usage.gate.RLock()
	defer r.usage.gate.RUnlock()
	return r.previewImport(ctx, batch)
}
func (r *contentRepository) ApplyImport(ctx context.Context, batch content.ImportBatch, revision string) (content.ImportPlan, error) {
	r.usage.gate.RLock()
	defer r.usage.gate.RUnlock()
	if _, err := r.previewImport(ctx, batch); err != nil {
		return content.ImportPlan{}, err
	}
	// The repository rechecks the plan at commit, so an intervening mapping or
	// target change cannot turn a retained row into an unvalidated new write.
	return r.Repository.ApplyImport(ctx, batch, revision)
}

func (r *contentRepository) Create(ctx context.Context, input content.PostInput) (content.Post, error) {
	release, fields := r.hold(input)
	defer release()
	if err := ctx.Err(); err != nil {
		return content.Post{}, err
	}
	if len(fields) > 0 {
		return content.Post{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.Create(ctx, input)
}

func (r *contentRepository) Update(ctx context.Context, id int64, mutation content.Mutation, input content.PostInput) (content.Post, error) {
	release, fields := r.hold(input)
	defer release()
	if err := ctx.Err(); err != nil {
		return content.Post{}, err
	}
	if len(fields) > 0 {
		return content.Post{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.Update(ctx, id, mutation, input)
}

func (r *contentRepository) RestoreRevision(ctx context.Context, id, revisionID int64, mutation content.Mutation) (content.Post, error) {
	r.usage.gate.RLock()
	defer r.usage.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return content.Post{}, err
	}
	revision, err := r.Repository.GetRevision(ctx, id, revisionID)
	if err != nil {
		return content.Post{}, err
	}
	snapshot := revision.Snapshot
	fields := r.usage.validate(r.usage.resolver.Content(snapshot.CoverURL, snapshot.ContentSource, snapshot.ContentHTML, contentPath(snapshot.Type, snapshot.Slug)))
	if len(fields) > 0 {
		return content.Post{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.RestoreRevision(ctx, id, revisionID, mutation)
}

func (r *contentRepository) SaveAutosave(ctx context.Context, autosave content.Autosave) (content.Autosave, error) {
	snapshot := autosave.Snapshot
	release, fields := r.usage.hold(r.usage.resolver.Content(snapshot.CoverURL, snapshot.ContentSource, snapshot.ContentHTML, contentPath(snapshot.Type, snapshot.Slug)))
	defer release()
	if err := ctx.Err(); err != nil {
		return content.Autosave{}, err
	}
	if len(fields) > 0 {
		return content.Autosave{}, &content.ValidationError{Fields: fields}
	}
	return r.Repository.SaveAutosave(ctx, autosave)
}

type navigationStore struct {
	navigation.Store
	usage *Service
}

func (r *navigationStore) fields(items []navigation.ItemInput) assetref.Fields {
	fields := make(assetref.Fields)
	for _, item := range items {
		if link, ok := r.usage.resolver.URL(item.URL, "/"); ok {
			fields["items"] = append(fields["items"], link)
		}
	}
	return fields
}

func (r *navigationStore) ReplaceItems(ctx context.Context, id int64, mutation navigation.Mutation, items []navigation.ItemInput) (navigation.Menu, error) {
	release, fields := r.usage.hold(r.fields(items))
	defer release()
	if err := ctx.Err(); err != nil {
		return navigation.Menu{}, err
	}
	if len(fields) > 0 {
		return navigation.Menu{}, &navigation.ValidationError{Fields: fields}
	}
	return r.Store.ReplaceItems(ctx, id, mutation, items)
}

func (r *navigationStore) ReplaceAll(ctx context.Context, mutation navigation.RegistryMutation, menus []navigation.RegistryMenuInput) (navigation.Snapshot, error) {
	links := make(assetref.Fields)
	for _, menu := range menus {
		links["items"] = append(links["items"], r.fields(menu.Items)["items"]...)
	}
	release, fields := r.usage.hold(links)
	defer release()
	if err := ctx.Err(); err != nil {
		return navigation.Snapshot{}, err
	}
	if len(fields) > 0 {
		return navigation.Snapshot{}, &navigation.ValidationError{Fields: fields}
	}
	return r.Store.ReplaceAll(ctx, mutation, menus)
}
