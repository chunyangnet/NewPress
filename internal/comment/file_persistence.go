package comment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/packagefs"
)

func (store *FileStore) persistLocked(items map[int64]Comment, revisions map[int64][]Revision, nextID int64, updatedAt time.Time, event *audit.Event, submissions ...map[string]SubmissionReceipt) error {
	return store.persistContextLocked(context.Background(), items, revisions, nextID, updatedAt, event, submissions...)
}

func (store *FileStore) persistContextLocked(ctx context.Context, items map[int64]Comment, revisions map[int64][]Revision, nextID int64, updatedAt time.Time, event *audit.Event, submissions ...map[string]SubmissionReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	activities := append([]activity.Event(nil), store.activities...)
	if event != nil && event.Action != "comment.seeded" {
		changed := make([]int64, 0)
		for id, item := range items {
			if store.items[id].Version != item.Version {
				changed = append(changed, id)
			}
		}
		sort.Slice(changed, func(i, j int) bool { return changed[i] < changed[j] })
		for _, id := range changed {
			activities = append(activities, ActivityEvents(store.items[id], items[id])...)
		}
	}
	if err := store.persistActivitiesContextLocked(ctx, items, revisions, nextID, updatedAt, event, activities, submissions...); err != nil {
		return err
	}
	store.activities = activities
	return nil
}

func (store *FileStore) persistActivitiesContextLocked(ctx context.Context, items map[int64]Comment, revisions map[int64][]Revision, nextID int64, updatedAt time.Time, event *audit.Event, activities []activity.Event, submissions ...map[string]SubmissionReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.path == "" {
		return ctx.Err()
	}
	document := fileDocument{
		Schema: fileSchema, Kind: fileKind, NextID: nextID, UpdatedAt: updatedAt,
		Items: make([]Comment, 0, len(items)), Revisions: flattenRevisions(revisions),
		Activities:  activities,
		Submissions: flattenSubmissions(store.submissions),
	}
	if len(submissions) > 0 {
		document.Submissions = flattenSubmissions(submissions[0])
	}
	for _, item := range items {
		document.Items = append(document.Items, item)
	}
	SortPublic(document.Items)
	if err := ctx.Err(); err != nil {
		return err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(true)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode comment document: %w", err)
	}
	if encoded.Len() > maxFileBytes {
		return errors.New("comment document exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// From the audit/file commit boundary onward, finish publishing the same
	// durable state in memory even if cancellation arrives during disk I/O.
	payload := encoded.Bytes()
	write := func() error { return writeFileBytes(store.path, payload) }
	if store.audit == nil || event == nil {
		return write()
	}
	if _, err := store.audit.ApplyFile(audit.FactComments, *event, payload, store.path, write); err != nil {
		store.auditFault = err
		return err
	}
	return nil
}

func writeFileBytes(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("inspect comment store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".newpress-comments-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary comment document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary comment document: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary comment document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary comment document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary comment document: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	if err := packagefs.ChmodRegularNoSymlink(path, 0o600); err != nil {
		return fmt.Errorf("protect comment document: %w", err)
	}
	if runtime.GOOS != "windows" {
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	return nil
}

func auditActor(value string) string {
	if actor, ok := NormalizeActor(value); ok && utf8.ValidString(actor) {
		return actor
	}
	return "system"
}

func loadFileDocument(path string) (fileDocument, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		file, _, err = packagefs.OpenRegularNoSymlink(path + ".bak")
	}
	if err != nil {
		return fileDocument{}, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxFileBytes + 1}
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fileDocument{}, fmt.Errorf("read comment document: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxFileBytes {
		return fileDocument{}, errors.New("comment document size is invalid")
	}
	if err := packagefs.ValidateUniqueJSONFor(payload, &fileDocument{}); err != nil {
		return fileDocument{}, fmt.Errorf("validate comment document JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document fileDocument
	if err := decoder.Decode(&document); err != nil {
		return fileDocument{}, fmt.Errorf("decode comment document: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fileDocument{}, err
	}
	return document, nil
}

func validateFileDocument(document fileDocument) error {
	if err := activity.Validate(document.Activities); err != nil {
		return err
	}
	if document.Schema != fileSchema || document.Kind != fileKind || document.NextID <= 0 || document.UpdatedAt.IsZero() {
		return errors.New("unsupported comment document metadata")
	}
	ids := make(map[int64]Comment, len(document.Items))
	for _, item := range document.Items {
		if item.ID <= 0 || item.ID >= document.NextID || item.Version <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return errors.New("invalid stored comment metadata")
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("duplicate stored comment id %d", item.ID)
		}
		normalized, fields := NormalizeCreateInput(CreateInput{
			PostID: item.PostID, ParentID: item.ParentID, UserID: item.UserID,
			AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail, Status: item.Status,
			ContentSource: item.ContentSource, AuthorIPHash: item.AuthorIPHash, UserAgent: item.UserAgent,
		})
		if len(fields) > 0 || normalized.AuthorName != item.AuthorName || normalized.AuthorEmail != item.AuthorEmail ||
			normalized.ContentSource != item.ContentSource || RenderPlainText(item.ContentSource) != item.ContentHTML {
			return fmt.Errorf("stored comment %d failed validation", item.ID)
		}
		for _, actor := range []string{item.CreatedBy, item.EditedBy, item.ModeratedBy} {
			if len([]rune(actor)) > 160 || containsDisallowedControl(actor, false) {
				return fmt.Errorf("stored comment %d has invalid actor metadata", item.ID)
			}
		}
		if item.EditedAt.IsZero() != (item.EditedBy == "") || (!item.EditedAt.IsZero() && item.EditedAt.Before(item.CreatedAt)) {
			return fmt.Errorf("stored comment %d has invalid edit metadata", item.ID)
		}
		if item.StaffReply && item.CreatedBy == "" {
			return fmt.Errorf("stored comment %d has incomplete staff reply metadata", item.ID)
		}
		ids[item.ID] = item
	}
	if err := validateSubmissionReceipts(document, ids); err != nil {
		return err
	}
	for _, item := range ids {
		if item.ParentID == 0 {
			continue
		}
		parent, ok := ids[item.ParentID]
		if !ok || parent.PostID != item.PostID || parent.ID == item.ID {
			return fmt.Errorf("stored comment %d has invalid parent", item.ID)
		}
		seen := map[int64]struct{}{item.ID: {}}
		currentID := item.ParentID
		for depth := 2; currentID > 0; depth++ {
			if depth > MaxThreadDepth {
				return fmt.Errorf("stored comment %d exceeds maximum thread depth", item.ID)
			}
			if _, exists := seen[currentID]; exists {
				return fmt.Errorf("stored comment %d has a cyclic parent chain", item.ID)
			}
			seen[currentID] = struct{}{}
			current, exists := ids[currentID]
			if !exists || current.PostID != item.PostID {
				return fmt.Errorf("stored comment %d has invalid ancestry", item.ID)
			}
			currentID = current.ParentID
		}
	}
	revisionVersions := make(map[int64]int64, len(ids))
	for _, revision := range document.Revisions {
		item, exists := ids[revision.CommentID]
		if !exists || revision.Version <= 0 || revision.Version >= item.Version || revision.CreatedAt.IsZero() || !ValidStatus(revision.Status) {
			return fmt.Errorf("stored comment revision %d:v%d has invalid metadata", revision.CommentID, revision.Version)
		}
		normalized, fields := NormalizeEditInput(EditInput{
			AuthorName: revision.AuthorName, AuthorEmail: revision.AuthorEmail, ContentSource: revision.ContentSource,
		})
		if len(fields) > 0 || normalized.AuthorName != revision.AuthorName || normalized.AuthorEmail != revision.AuthorEmail ||
			normalized.ContentSource != revision.ContentSource || RenderPlainText(revision.ContentSource) != revision.ContentHTML ||
			len([]rune(revision.Actor)) > 160 || containsDisallowedControl(revision.Actor, false) {
			return fmt.Errorf("stored comment revision %d:v%d failed validation", revision.CommentID, revision.Version)
		}
		if previous := revisionVersions[revision.CommentID]; revision.Version <= previous {
			return fmt.Errorf("stored comment revisions for %d are duplicated or unordered", revision.CommentID)
		}
		revisionVersions[revision.CommentID] = revision.Version
	}
	return nil
}

func replaceFile(temporaryPath, target string) error {
	return packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

var _ Store = (*FileStore)(nil)
