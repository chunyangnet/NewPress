package comment

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"newpress.local/newpress/internal/audit"
)

func (store *FileStore) Create(ctx context.Context, input CreateInput) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	if store == nil {
		return Comment{}, errors.New("comment store is nil")
	}
	normalized, fields := NormalizeCreateInput(input)
	if len(fields) > 0 {
		return Comment{}, &ValidationError{Fields: fields}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.createLocked(ctx, normalized, nil)
}

func (store *FileStore) createLocked(ctx context.Context, normalized CreateInput, submission *Submission) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	if store.auditFault != nil {
		return Comment{}, fmt.Errorf("comment store audit failure: %w", store.auditFault)
	}
	if err := store.validateParentLocked(normalized.PostID, normalized.ParentID); err != nil {
		return Comment{}, err
	}
	now := time.Now().UTC()
	item := Comment{
		ID: store.nextID, PostID: normalized.PostID, ParentID: normalized.ParentID, UserID: normalized.UserID,
		Version: 1, AuthorName: normalized.AuthorName, AuthorEmail: normalized.AuthorEmail, Status: normalized.Status,
		ContentSource: normalized.ContentSource, ContentHTML: RenderPlainText(normalized.ContentSource),
		AuthorIPHash: normalized.AuthorIPHash, UserAgent: normalized.UserAgent, CreatedAt: now, UpdatedAt: now,
	}
	candidate := cloneItemMap(store.items)
	candidate[item.ID] = item
	actor := "anonymous"
	if item.UserID > 0 {
		actor = fmt.Sprintf("user:%d", item.UserID)
	}
	event := audit.Event{Action: "comment.created", ObjectType: "comment", ObjectID: fmt.Sprint(item.ID), Actor: actor, Metadata: map[string]any{
		"post_id": item.PostID, "parent_id": item.ParentID, "user_id": item.UserID, "version": item.Version, "status": item.Status, "staff_reply": false,
	}}
	submissions := store.submissions
	if submission != nil {
		submissions = make(map[string]SubmissionReceipt, len(store.submissions)+1)
		for key, receipt := range store.submissions {
			submissions[key] = receipt
		}
		submissions[submission.Key] = SubmissionReceipt{Key: submission.Key, Fingerprint: submission.Fingerprint, CommentID: item.ID, PostID: item.PostID, CreatedAt: now}
	}
	if err := store.persistContextLocked(ctx, candidate, store.revisions, store.nextID+1, now, &event, submissions); err != nil {
		return Comment{}, err
	}
	store.submissions = submissions
	store.items = candidate
	store.nextID++
	store.updatedAt = now
	return item, nil
}

func (store *FileStore) Edit(ctx context.Context, id int64, mutation Mutation, input EditInput) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	if store == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return Comment{}, ErrInvalidInput
	}
	actor, actorOK := NormalizeActor(mutation.Actor)
	if !actorOK {
		return Comment{}, &ValidationError{Fields: map[string]string{"actor": "编辑者名称需为 1–160 个字符且不能包含控制字符"}}
	}
	normalized, fields := NormalizeEditInput(input)
	if len(fields) > 0 {
		return Comment{}, &ValidationError{Fields: fields}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	item, ok := store.items[id]
	if !ok {
		return Comment{}, ErrNotFound
	}
	if item.Version != mutation.ExpectedVersion {
		return Comment{}, ErrVersionConflict
	}
	if item.AuthorName == normalized.AuthorName && item.AuthorEmail == normalized.AuthorEmail && item.ContentSource == normalized.ContentSource {
		return item, nil
	}

	now := time.Now().UTC()
	revision := Revision{
		CommentID: item.ID, Version: item.Version, AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail,
		Status: item.Status, ContentSource: item.ContentSource, ContentHTML: item.ContentHTML,
		Actor: actor, CreatedAt: now,
	}
	item.AuthorName = normalized.AuthorName
	item.AuthorEmail = normalized.AuthorEmail
	item.ContentSource = normalized.ContentSource
	item.ContentHTML = RenderPlainText(normalized.ContentSource)
	item.Version++
	item.UpdatedAt = now
	item.EditedBy = revision.Actor
	item.EditedAt = now

	candidateItems := cloneItemMap(store.items)
	candidateItems[id] = item
	candidateRevisions := cloneRevisionMap(store.revisions)
	candidateRevisions[id] = append(candidateRevisions[id], revision)
	event := audit.Event{Action: "comment.edit", ObjectType: "comment", ObjectID: fmt.Sprint(id), Actor: auditActor(actor), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "version": item.Version, "status": item.Status, "revision_count": len(candidateRevisions[id]),
	}}
	if err := store.persistContextLocked(ctx, candidateItems, candidateRevisions, store.nextID, now, &event); err != nil {
		return Comment{}, err
	}
	store.items = candidateItems
	store.revisions = candidateRevisions
	store.updatedAt = now
	return item, nil
}

func (store *FileStore) RestoreRevision(ctx context.Context, id, sourceVersion int64, mutation Mutation) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	if store == nil || id <= 0 || sourceVersion <= 0 || mutation.ExpectedVersion <= 0 {
		return Comment{}, ErrInvalidInput
	}
	actor, actorOK := NormalizeActor(mutation.Actor)
	if !actorOK {
		return Comment{}, &ValidationError{Fields: map[string]string{"actor": "恢复者名称需为 1–160 个字符且不能包含控制字符"}}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	item, ok := store.items[id]
	if !ok {
		return Comment{}, ErrNotFound
	}
	if item.Version != mutation.ExpectedVersion {
		return Comment{}, ErrVersionConflict
	}
	var source Revision
	found := false
	for _, revision := range store.revisions[id] {
		if revision.Version == sourceVersion {
			source = revision
			found = true
			break
		}
	}
	if !found {
		return Comment{}, ErrRevisionNotFound
	}
	normalized, fields := NormalizeEditInput(EditInput{
		AuthorName: source.AuthorName, AuthorEmail: source.AuthorEmail, ContentSource: source.ContentSource,
	})
	if len(fields) > 0 {
		return Comment{}, &ValidationError{Fields: fields}
	}
	if item.AuthorName == normalized.AuthorName && item.AuthorEmail == normalized.AuthorEmail && item.ContentSource == normalized.ContentSource {
		return item, nil
	}

	now := time.Now().UTC()
	revision := Revision{
		CommentID: item.ID, Version: item.Version, AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail,
		Status: item.Status, ContentSource: item.ContentSource, ContentHTML: item.ContentHTML,
		Actor: actor, CreatedAt: now,
	}
	item.AuthorName = normalized.AuthorName
	item.AuthorEmail = normalized.AuthorEmail
	item.ContentSource = normalized.ContentSource
	item.ContentHTML = RenderPlainText(normalized.ContentSource)
	item.Version++
	item.UpdatedAt = now
	item.EditedBy = actor
	item.EditedAt = now

	candidateItems := cloneItemMap(store.items)
	candidateItems[id] = item
	candidateRevisions := cloneRevisionMap(store.revisions)
	candidateRevisions[id] = append(candidateRevisions[id], revision)
	event := audit.Event{Action: "comment.revision.restore", ObjectType: "comment", ObjectID: fmt.Sprint(id), Actor: auditActor(actor), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "restored_version": sourceVersion, "version": item.Version, "status": item.Status, "revision_count": len(candidateRevisions[id]),
	}}
	if err := store.persistContextLocked(ctx, candidateItems, candidateRevisions, store.nextID, now, &event); err != nil {
		return Comment{}, err
	}
	store.items = candidateItems
	store.revisions = candidateRevisions
	store.updatedAt = now
	return item, nil
}

func (store *FileStore) Reply(ctx context.Context, input ReplyInput) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	if store == nil {
		return Comment{}, errors.New("comment store is nil")
	}
	normalized, fields := NormalizeReplyInput(input)
	if len(fields) > 0 {
		return Comment{}, &ValidationError{Fields: fields}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	parent, ok := store.items[normalized.ParentID]
	if !ok {
		return Comment{}, ErrNotFound
	}
	if parent.Version != normalized.ExpectedParentVersion {
		return Comment{}, ErrVersionConflict
	}
	if err := store.validateStaffReplyParentLocked(parent); err != nil {
		return Comment{}, err
	}

	now := time.Now().UTC()
	item := Comment{
		ID: store.nextID, PostID: parent.PostID, ParentID: parent.ID, UserID: normalized.UserID,
		Version: 1, AuthorName: normalized.AuthorName, AuthorEmail: normalized.AuthorEmail, Status: parent.Status,
		ContentSource: normalized.ContentSource, ContentHTML: RenderPlainText(normalized.ContentSource),
		StaffReply: true, CreatedBy: normalized.Actor, ModeratedBy: normalized.Actor, ModeratedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	candidate := cloneItemMap(store.items)
	candidate[item.ID] = item
	event := audit.Event{Action: "comment.reply", ObjectType: "comment", ObjectID: fmt.Sprint(item.ID), Actor: auditActor(normalized.Actor), Metadata: map[string]any{
		"post_id": item.PostID, "parent_id": item.ParentID, "user_id": item.UserID, "parent_version": normalized.ExpectedParentVersion,
		"version": item.Version, "status": item.Status, "staff_reply": true,
	}}
	if err := store.persistContextLocked(ctx, candidate, store.revisions, store.nextID+1, now, &event); err != nil {
		return Comment{}, err
	}
	store.items = candidate
	store.nextID++
	store.updatedAt = now
	return item, nil
}

func (store *FileStore) ListRevisions(ctx context.Context, id int64, limit int) ([]Revision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || id <= 0 {
		return nil, ErrNotFound
	}
	limit = NormalizeRevisionLimit(limit)
	store.mu.RLock()
	defer store.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := store.items[id]; !ok {
		return nil, ErrNotFound
	}
	source := store.revisions[id]
	result := make([]Revision, 0, min(limit, len(source)))
	for index := len(source) - 1; index >= 0 && len(result) < limit; index-- {
		result = append(result, source[index])
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *FileStore) ChangeStatus(ctx context.Context, id int64, mutation Mutation, status string) (Comment, error) {
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if store == nil || id <= 0 || !ValidStatus(status) {
		return Comment{}, ErrInvalidInput
	}
	actor, actorOK := NormalizeActor(mutation.Actor)
	if actor != "" && !actorOK {
		return Comment{}, &ValidationError{Fields: map[string]string{"actor": "审核者名称不能超过 160 个字符，且必须是无控制字符的有效 UTF-8 文本"}}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Comment{}, err
	}
	item, ok := store.items[id]
	if !ok {
		return Comment{}, ErrNotFound
	}
	if mutation.ExpectedVersion <= 0 || item.Version != mutation.ExpectedVersion {
		return Comment{}, ErrVersionConflict
	}
	now := time.Now().UTC()
	item.Status = status
	item.Version++
	item.UpdatedAt = now
	item.ModeratedAt = now
	item.ModeratedBy = actor
	candidate := cloneItemMap(store.items)
	candidate[id] = item
	event := audit.Event{Action: "comment.status." + status, ObjectType: "comment", ObjectID: fmt.Sprint(id), Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"from_status": store.items[id].Status, "status": status, "from_version": mutation.ExpectedVersion, "version": item.Version,
	}}
	if err := store.persistContextLocked(ctx, candidate, store.revisions, store.nextID, now, &event); err != nil {
		return Comment{}, err
	}
	store.items = candidate
	store.updatedAt = now
	return item, nil
}

func (store *FileStore) BulkChangeStatus(ctx context.Context, change BulkStatusChange) ([]Comment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("comment store is nil")
	}
	normalized, fields := NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Complete the read/validation phase before creating the candidate map so a
	// missing row or stale version can never leak a partial in-memory change.
	current := make([]Comment, len(normalized.Items))
	for index, mutation := range normalized.Items {
		item, ok := store.items[mutation.ID]
		if !ok {
			return nil, ErrNotFound
		}
		if item.Version != mutation.ExpectedVersion {
			return nil, ErrVersionConflict
		}
		current[index] = item
	}

	now := time.Now().UTC()
	actor := normalized.Actor
	candidate := cloneItemMap(store.items)
	updated := make([]Comment, 0, len(current))
	for _, item := range current {
		item.Status = normalized.Status
		item.Version++
		item.UpdatedAt = now
		item.ModeratedAt = now
		item.ModeratedBy = actor
		candidate[item.ID] = item
		updated = append(updated, item)
	}
	event := audit.Event{Action: "comment.status." + normalized.Status, ObjectType: "comment_batch", ObjectID: "bulk", Actor: auditActor(actor), Metadata: map[string]any{
		"first_comment_id": updated[0].ID, "last_comment_id": updated[len(updated)-1].ID, "comment_count": len(updated), "status": normalized.Status,
	}}
	if err := store.persistContextLocked(ctx, candidate, store.revisions, store.nextID, now, &event); err != nil {
		return nil, err
	}
	store.items = candidate
	store.updatedAt = now
	return updated, nil
}

func (store *FileStore) Delete(ctx context.Context, id int64, mutation Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || id <= 0 {
		return ErrNotFound
	}
	if actor, ok := NormalizeActor(mutation.Actor); actor != "" && !ok {
		return &ValidationError{Fields: map[string]string{"actor": "操作者名称不能超过 160 个字符，且必须是无控制字符的有效 UTF-8 文本"}}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	item, ok := store.items[id]
	if !ok {
		return ErrNotFound
	}
	if mutation.ExpectedVersion <= 0 || item.Version != mutation.ExpectedVersion {
		return ErrVersionConflict
	}
	candidate := cloneItemMap(store.items)
	deletedIDs := deleteCommentTree(candidate, id)
	candidateRevisions := cloneRevisionMap(store.revisions)
	for _, deletedID := range deletedIDs {
		delete(candidateRevisions, deletedID)
	}
	now := time.Now().UTC()
	event := audit.Event{Action: "comment.delete", ObjectType: "comment", ObjectID: fmt.Sprint(id), Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"version": mutation.ExpectedVersion, "deleted_count": len(deletedIDs),
	}}
	if err := store.persistContextLocked(ctx, candidate, candidateRevisions, store.nextID, now, &event); err != nil {
		return err
	}
	store.items = candidate
	store.revisions = candidateRevisions
	store.updatedAt = now
	return nil
}

func cloneItemMap(source map[int64]Comment) map[int64]Comment {
	result := make(map[int64]Comment, len(source))
	for id, item := range source {
		result[id] = item
	}
	return result
}

func cloneRevisionMap(source map[int64][]Revision) map[int64][]Revision {
	result := make(map[int64][]Revision, len(source))
	for id, revisions := range source {
		result[id] = append([]Revision(nil), revisions...)
	}
	return result
}

func flattenRevisions(source map[int64][]Revision) []Revision {
	ids := make([]int64, 0, len(source))
	count := 0
	for id, revisions := range source {
		ids = append(ids, id)
		count += len(revisions)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	result := make([]Revision, 0, count)
	for _, id := range ids {
		result = append(result, source[id]...)
	}
	return result
}

func deleteCommentTree(items map[int64]Comment, rootID int64) []int64 {
	queue := []int64{rootID}
	deleted := make([]int64, 0, 1)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for childID, item := range items {
			if item.ParentID == id {
				queue = append(queue, childID)
			}
		}
		delete(items, id)
		deleted = append(deleted, id)
	}
	return deleted
}
