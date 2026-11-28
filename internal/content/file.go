package content

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"sync"
	"sync/atomic"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

const maxContentFileBytes = 128 << 20

// FileRepository persists the complete editorial state as one atomic snapshot.
// It is intended for a single process. PostgreSQL remains the shared-store
// option. Readers only see a candidate after its durable replacement succeeds.
type FileRepository struct {
	mu     sync.RWMutex
	state  *MemoryRepository
	path   string
	digest [sha256.Size]byte
	// Public searches may retain a committed state while the next file write
	// prepares its detached candidate. Publish only after durable replacement.
	searchState atomic.Pointer[MemoryRepository]
}

type contentDocument struct {
	Schema             int                     `json:"schema"`
	Kind               string                  `json:"kind"`
	NextID             int64                   `json:"next_id"`
	NextTermID         int64                   `json:"next_term_id"`
	NextRevisionID     int64                   `json:"next_revision_id"`
	Posts              []Post                  `json:"posts"`
	Terms              []Term                  `json:"terms"`
	Revisions          []Revision              `json:"revisions"`
	Autosaves          []Autosave              `json:"autosaves"`
	Activities         []activity.Event        `json:"activities,omitempty"`
	Series             []Series                `json:"series,omitempty"`
	NextSeriesID       int64                   `json:"next_series_id,omitempty"`
	Redirects          []Redirect              `json:"redirects,omitempty"`
	NextRedirectID     int64                   `json:"next_redirect_id,omitempty"`
	Imports            []ImportMapping         `json:"imports,omitempty"`
	Submissions        []SubmissionReceipt     `json:"submissions,omitempty"`
	Snippets           []SnippetRecord         `json:"snippets,omitempty"`
	SnippetReceipts    []SnippetReceipt        `json:"snippet_receipts,omitempty"`
	NextSnippetID      int64                   `json:"next_snippet_id,omitempty"`
	SnippetQuarantines []SnippetFileQuarantine `json:"snippet_quarantines,omitempty"`
}

var _ Repository = (*FileRepository)(nil)

func NewFileRepository(path string, seed bool) (*FileRepository, error) {
	if path == "" {
		return nil, errors.New("content store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	repository := &FileRepository{path: absolute}
	payload, err := readContentFile(absolute)
	if errors.Is(err, os.ErrNotExist) {
		// Recover only a missing primary. A corrupt primary is never silently
		// replaced with an older backup or fresh demonstration content.
		payload, err = readContentFile(absolute + ".bak")
		if err == nil {
			if _, err := decodeContentDocument(payload); err != nil {
				return nil, fmt.Errorf("validate content rollback: %w", err)
			}
			if err := packagefs.ReplaceRegularFileNoSymlink(absolute+".bak", absolute); err != nil {
				return nil, fmt.Errorf("recover content rollback: %w", err)
			}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		repository.state = NewMemoryRepository(seed)
		repository.digest, err = writeContentState(context.Background(), absolute, repository.state)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read content store: %w", err)
	} else {
		repository.state, err = decodeContentDocument(payload)
		if err != nil {
			return nil, err
		}
		repository.digest = sha256.Sum256(payload)
	}
	repository.searchState.Store(repository.state)
	return repository, nil
}

func readContentFile(path string) ([]byte, error) {
	file, before, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := readContentFileBytes(file, before.Size())
	if err != nil {
		return nil, err
	}
	if err := checkContentFileRead(path, before, int64(len(payload))); err != nil {
		return nil, err
	}
	return payload, nil
}

func readContentFileBytes(reader io.Reader, size int64) ([]byte, error) {
	if size <= 0 || size > maxContentFileBytes {
		return nil, errors.New("content document size is invalid")
	}
	// The checked file length bounds one allocation. One extra byte detects
	// growth without allocating or consuming the rest of a changing file.
	payload := make([]byte, size+1)
	n, err := io.ReadFull(reader, payload)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if int64(n) != size {
		return nil, errors.New("content document changed while reading")
	}
	return payload[:n], nil
}

// Readiness and writes need the original bytes' digest, not another complete
// copy of the document. Still read every byte and reject a changing file.
func contentFileDigest(path string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	file, before, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return digest, err
	}
	defer file.Close()
	if before.Size() <= 0 || before.Size() > maxContentFileBytes {
		return digest, errors.New("content document size is invalid")
	}
	hash := sha256.New()
	// One byte past the observed length detects growth. Limiting by the
	// actual size also keeps the copy buffer small for small documents.
	size, err := io.Copy(hash, io.LimitReader(file, before.Size()+1))
	if err != nil {
		return digest, err
	}
	if err := checkContentFileRead(path, before, size); err != nil {
		return digest, err
	}
	hash.Sum(digest[:0])
	return digest, nil
}

func checkContentFileRead(path string, before os.FileInfo, size int64) error {
	after, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != size || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return errors.New("content document changed while reading")
	}
	return nil
}

func writeContentFile(path string, payload []byte) error {
	if len(payload) > maxContentFileBytes {
		return errContentFileCapacity
	}
	return replaceContentFile(context.Background(), path, func(writer io.Writer) error {
		_, err := writer.Write(payload)
		return err
	})
}

func replaceContentFile(ctx context.Context, path string, populate func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	region := trace.StartRegion(ctx, "content.file.directory")
	err := packagefs.MkdirAllNoSymlink(directory, 0o750)
	region.End()
	if err != nil {
		return err
	}
	region = trace.StartRegion(ctx, "content.file.create")
	temporary, err := os.CreateTemp(directory, ".newpress-content-*.tmp")
	region.End()
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	region = trace.StartRegion(ctx, "content.file.chmod")
	err = temporary.Chmod(0o600)
	region.End()
	if err != nil {
		return err
	}
	if err := populate(temporary); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	region = trace.StartRegion(ctx, "content.file.sync")
	err = temporary.Sync()
	region.End()
	if err != nil {
		return err
	}
	region = trace.StartRegion(ctx, "content.file.close")
	err = temporary.Close()
	region.End()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	region = trace.StartRegion(ctx, "content.file.replace")
	err = packagefs.ReplaceRegularFileNoSymlink(temporary.Name(), path)
	region.End()
	if err != nil {
		return fmt.Errorf("replace content store: %w", err)
	}
	if runtime.GOOS != "windows" {
		region = trace.StartRegion(ctx, "content.file.directory-sync")
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
		region.End()
	}
	return nil
}

func decodeContentDocument(payload []byte) (*MemoryRepository, error) {
	document, err := packagefs.DecodeJSONBytesWithNumbers[contentDocument](payload, maxContentFileBytes, "content store")
	if err != nil {
		return nil, err
	}
	if (document.Schema != 1 && document.Schema != 2 && document.Schema != 3) || document.Kind != "newpress.content" || document.NextID < 1 || document.NextTermID < 2 || document.NextRevisionID < 1 ||
		document.NextID == math.MaxInt64 || document.NextTermID == math.MaxInt64 || document.NextRevisionID == math.MaxInt64 {
		return nil, errors.New("invalid content store metadata")
	}
	state := NewMemoryRepository(false)
	state.contentSchema = document.Schema
	reserve, err := snippetFileEvidenceBudget(document.SnippetQuarantines)
	if err != nil {
		return nil, err
	}
	if document.Schema < 3 && len(document.SnippetQuarantines) != 0 || len(payload)+reserve > maxContentFileBytes {
		return nil, ErrSnippetFileMaintenanceEvidence
	}
	for _, q := range document.SnippetQuarantines {
		if q.Repair == nil {
			return nil, ErrSnippetMaintenance
		}
	}
	state.snippetQuarantines = document.SnippetQuarantines
	if document.Schema == 1 && (len(document.Snippets) != 0 || len(document.SnippetReceipts) != 0 || document.NextSnippetID != 0) {
		return nil, ErrSnippetCorrupt
	}
	if document.Schema >= 2 {
		receiptBytes, err := validateSnippetState(document.Snippets, document.SnippetReceipts, document.NextSnippetID)
		if err != nil {
			return nil, err
		}
		if err := validateSnippetFileLinks(document.Snippets, document.SnippetReceipts); err != nil {
			return nil, err
		}
		state.snippets, state.snippetReceipts, state.nextSnippetID = document.Snippets, document.SnippetReceipts, document.NextSnippetID
		state.snippetReceiptBytes, state.snippetReceiptCount = receiptBytes, len(document.SnippetReceipts)
	}
	if err := activity.Validate(document.Activities); err != nil {
		return nil, err
	}
	state.activities = document.Activities
	state.redirects = document.Redirects
	if document.NextRedirectID != 0 {
		state.nextRedirectID = document.NextRedirectID
	}
	state.posts, state.terms = document.Posts, document.Terms
	state.nextID, state.nextTermID, state.nextRevisionID = document.NextID, document.NextTermID, document.NextRevisionID
	terms := make(map[int64]Term, len(state.terms))
	slugs := make(map[string]bool)
	systemTerms := 0
	for _, term := range state.terms {
		normalized, fields := NormalizeTermInput(TermInput{Taxonomy: term.Taxonomy, ParentID: term.ParentID, Slug: term.Slug, Name: term.Name, Description: term.Description})
		key := term.Taxonomy + ":" + term.Slug
		if term.ID < 1 || term.ID >= state.nextTermID || term.Version < 1 || term.CreatedAt.IsZero() || term.UpdatedAt.Before(term.CreatedAt) ||
			terms[term.ID].ID != 0 || slugs[key] || len(fields) > 0 || normalized.Slug != term.Slug || normalized.Name != term.Name || normalized.Description != term.Description {
			return nil, errors.New("invalid stored content term")
		}
		if term.System {
			if term.Taxonomy != TaxonomyCategory {
				return nil, errors.New("invalid system category")
			}
			systemTerms++
		}
		terms[term.ID], slugs[key] = term, true
	}
	if systemTerms != 1 {
		return nil, errors.New("content store requires one default category")
	}
	for _, term := range state.terms {
		if err := state.validateParentLocked(term.Taxonomy, term.ParentID, term.ID); err != nil {
			return nil, fmt.Errorf("invalid stored category tree: %w", err)
		}
	}
	posts := make(map[int64]Post, len(state.posts))
	slugs = make(map[string]bool)
	for index := range state.posts {
		post := &state.posts[index]
		key := post.Type + ":" + post.Slug
		if post.ID >= state.nextID || posts[post.ID].ID != 0 || slugs[key] {
			return nil, errors.New("duplicate or invalid stored content identity")
		}
		if err := validateStoredPost(post); err != nil {
			return nil, err
		}
		for _, group := range []struct {
			values   []Term
			taxonomy string
		}{{post.Categories, TaxonomyCategory}, {post.Tags, TaxonomyTag}} {
			seen := make(map[int64]bool)
			for i, term := range group.values {
				current, ok := terms[term.ID]
				if !ok || current.Taxonomy != group.taxonomy || seen[term.ID] {
					return nil, errors.New("invalid stored content term relation")
				}
				seen[term.ID] = true
				group.values[i] = current
			}
		}
		if post.Type == TypePost && len(post.Categories) == 0 || post.Type == TypePage && (len(post.Categories) > 0 || len(post.Tags) > 0) {
			return nil, errors.New("invalid stored content classifications")
		}
		post.Category = ""
		if len(post.Categories) > 0 {
			post.Category = post.Categories[0].Name
		}
		posts[post.ID], slugs[key] = *post, true
		state.indexSnippetReferencesLocked(*post)
	}
	if state.nextRedirectID < 1 || state.nextRedirectID == math.MaxInt64 || len(state.redirects) > MaxRedirects {
		return nil, errors.New("invalid redirect metadata")
	}
	redirectIDs, redirectPaths := map[int64]bool{}, map[string]bool{}
	for _, item := range state.redirects {
		normalized, fields := NormalizeRedirectInput(item.RedirectInput)
		if len(fields) > 0 || normalized != item.RedirectInput || item.ID < 1 || item.ID >= state.nextRedirectID || item.Version < 1 || item.Version >= math.MaxInt64-1 || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) || redirectIDs[item.ID] || redirectPaths[item.Path] || posts[item.PostID].ID == 0 {
			return nil, errors.New("invalid stored redirect")
		}
		redirectIDs[item.ID], redirectPaths[item.Path] = true, true
	}
	if document.NextSeriesID != 0 {
		state.nextSeriesID = document.NextSeriesID
	}
	if state.nextSeriesID < 1 || state.nextSeriesID == math.MaxInt64 || len(document.Series) > MaxSeries {
		return nil, errors.New("invalid series metadata")
	}
	seriesIDs, seriesSlugs := map[int64]bool{}, map[string]bool{}
	for _, item := range document.Series {
		normalized, fields := NormalizeSeriesInput(item.SeriesInput)
		if item.ID < 1 || item.ID >= state.nextSeriesID || item.Version < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) || seriesIDs[item.ID] || seriesSlugs[item.Slug] || len(fields) > 0 || !EqualSeriesInput(item.SeriesInput, normalized) {
			return nil, errors.New("invalid stored series")
		}
		for _, id := range item.PostIDs {
			if _, ok := posts[id]; !ok {
				return nil, errors.New("invalid stored series chapter")
			}
		}
		seriesIDs[item.ID], seriesSlugs[item.Slug] = true, true
		state.series = append(state.series, cloneSeries(item))
	}
	// The document already contains every record. Count groups without
	// reordering them so loading long histories does not repeatedly grow and
	// copy the large revision values. Validation still precedes each append.
	revisionCounts := make(map[int64]int)
	for _, revision := range document.Revisions {
		revisionCounts[revision.PostID]++
	}
	state.revisions = make(map[int64][]Revision, len(revisionCounts))
	revisionIDs := make(map[int64]bool, len(document.Revisions))
	for _, revision := range document.Revisions {
		post, ok := posts[revision.PostID]
		previous := state.revisions[revision.PostID]
		if !ok || revision.ID < 1 || revision.ID >= state.nextRevisionID || revisionIDs[revision.ID] || revision.Version < 1 || revision.Version > post.Version ||
			revision.CreatedAt.IsZero() || revision.Snapshot.ID != post.ID || revision.Snapshot.Version != revision.Version ||
			len(previous) > 0 && previous[len(previous)-1].Version >= revision.Version {
			return nil, errors.New("invalid stored content revision")
		}
		if err := validateStoredPost(&revision.Snapshot); err != nil {
			return nil, err
		}
		revisionIDs[revision.ID] = true
		if previous == nil {
			previous = make([]Revision, 0, revisionCounts[revision.PostID])
		}
		state.revisions[revision.PostID] = append(previous, revision)
	}
	autosaveCounts := make(map[int64]int)
	for _, autosave := range document.Autosaves {
		autosaveCounts[autosave.PostID]++
	}
	state.autosaves = make(map[int64]map[string]Autosave, len(autosaveCounts))
	for _, autosave := range document.Autosaves {
		autosave, err := normalizeFileAutosave(autosave)
		post, ok := posts[autosave.PostID]
		if err != nil || !ok || autosave.SourceVersion != post.Version || autosave.Snapshot.Version != post.Version ||
			autosave.Snapshot.AuthorID != post.AuthorID || autosave.UpdatedAt.IsZero() || !autosave.ExpiresAt.After(autosave.UpdatedAt) {
			return nil, errors.New("invalid stored content autosave")
		}
		if state.autosaves[post.ID] == nil {
			state.autosaves[post.ID] = make(map[string]Autosave, autosaveCounts[post.ID])
		}
		if _, exists := state.autosaves[post.ID][autosave.OwnerKey]; exists {
			return nil, errors.New("duplicate stored content autosave")
		}
		state.autosaves[post.ID][autosave.OwnerKey] = autosave
	}
	if len(document.Imports) > MaxImportMappings {
		return nil, errors.New("too many stored content import mappings")
	}
	importKeys := make(map[string]bool)
	for _, mapping := range document.Imports {
		key := mapping.Source + "\x00" + mapping.Format + "\x00" + mapping.Key
		if !ValidImportIdentity(mapping.Source, mapping.Format, mapping.Key, mapping.Digest) || !validImportMetadataDigest(mapping.Format, mapping.MetadataDigest) || importKeys[key] || posts[mapping.PostID].ID == 0 || mapping.CreatedAt.IsZero() {
			return nil, errors.New("invalid stored content import mapping")
		}
		importKeys[key] = true
	}
	state.imports = document.Imports
	if err := ValidateSubmissionReceipts(document.Submissions, posts); err != nil {
		return nil, err
	}
	state.submissions = document.Submissions
	return state, nil
}

func validateStoredPost(post *Post) error {
	if post.ID < 1 || post.Version < 1 || post.AuthorID < 0 || !ValidType(post.Type) || !ValidStatus(post.Status) ||
		!ValidSlug(post.Slug) || post.CreatedAt.IsZero() || post.UpdatedAt.Before(post.CreatedAt) ||
		len(post.Categories) > MaxCategoriesPerPost || len(post.Tags) > MaxTagsPerPost {
		return errors.New("invalid stored content metadata")
	}
	normalized, fields := NormalizePostInput(PostInput{
		AuthorID: post.AuthorID, Type: post.Type, Status: post.Status, Slug: post.Slug, Title: post.Title,
		Excerpt: post.Excerpt, ContentSource: post.ContentSource, CoverURL: post.CoverURL, AuthorName: post.AuthorName,
		CommentStatus: post.CommentStatus, PublishedAt: post.PublishedAt,
	}, post.UpdatedAt)
	if len(fields) > 0 || normalized.Title != post.Title || normalized.Slug != post.Slug || normalized.ContentSource != post.ContentSource ||
		normalized.AuthorName != post.AuthorName || normalized.CommentStatus != post.CommentStatus || normalized.Excerpt != post.Excerpt {
		return errors.New("invalid stored content fields")
	}
	// HTML is always derived afresh; an edited file cannot introduce script
	// nodes into public pages or a revision/autosave preview.
	post.ContentHTML = normalized.ContentHTML
	return nil
}

func cloneContentState(source *MemoryRepository) *MemoryRepository {
	clone := NewMemoryRepository(false)
	clone.now = source.now
	clone.searchBodies = source.searchBodies
	clone.activities = append([]activity.Event(nil), source.activities...)
	clone.nextID, clone.nextTermID, clone.nextRevisionID = source.nextID, source.nextTermID, source.nextRevisionID
	clone.terms = append([]Term(nil), source.terms...)
	clone.nextSeriesID = source.nextSeriesID
	clone.nextRedirectID = source.nextRedirectID
	clone.redirects = append([]Redirect(nil), source.redirects...)
	clone.imports = append([]ImportMapping(nil), source.imports...)
	clone.submissions = append([]SubmissionReceipt(nil), source.submissions...)
	clone.contentSchema, clone.nextSnippetID = source.contentSchema, source.nextSnippetID
	// Quarantine entries and their byte slices are immutable after publication.
	clone.snippetQuarantines = append([]SnippetFileQuarantine(nil), source.snippetQuarantines...)
	clone.snippetPostCounts = make(map[int64][]contenthtml.SnippetCount, len(source.snippetPostCounts))
	for id, counts := range source.snippetPostCounts {
		// Derived entries are replaced, never mutated after publication.
		clone.snippetPostCounts[id] = counts
	}
	// Counts are known before cloning. Allocate each destination once while
	// retaining the existing deep copies and nil empty-list representation.
	if len(source.snippets) > 0 {
		clone.snippets = make([]SnippetRecord, len(source.snippets))
	}
	for i, record := range source.snippets {
		record.Revisions = append([]SnippetRevision(nil), record.Revisions...)
		clone.snippets[i] = record
	}
	// Stored receipt entries never change. Copy the outer list so appending or
	// replacing a candidate's receipt cannot affect an older snapshot, but share
	// its immutable selection arrays. API results always detach those arrays.
	clone.snippetReceipts = append([]SnippetReceipt(nil), source.snippetReceipts...)
	clone.snippetReceiptBytes, clone.snippetReceiptCount = source.snippetReceiptBytes, source.snippetReceiptCount
	if len(source.series) > 0 {
		clone.series = make([]Series, len(source.series))
	}
	for i, item := range source.series {
		clone.series[i] = cloneSeries(item)
	}
	if len(source.posts) > 0 {
		clone.posts = make([]Post, len(source.posts))
	}
	for i, post := range source.posts {
		clone.posts[i] = clonePost(post)
	}
	clone.revisions = make(map[int64][]Revision, len(source.revisions))
	for id, revisions := range source.revisions {
		if len(revisions) == 0 {
			continue
		}
		copied := make([]Revision, len(revisions))
		for i, revision := range revisions {
			revision.Snapshot = clonePost(revision.Snapshot)
			copied[i] = revision
		}
		clone.revisions[id] = copied
	}
	clone.autosaves = make(map[int64]map[string]Autosave, len(source.autosaves))
	for id, owners := range source.autosaves {
		clone.autosaves[id] = make(map[string]Autosave, len(owners))
		for owner, autosave := range owners {
			clone.autosaves[id][owner] = cloneAutosave(autosave)
		}
	}
	return clone
}

func fileContentChange[T any](ctx context.Context, repository *FileRepository, update func(*MemoryRepository) (T, error)) (T, error) {
	return fileContentChangeWithConfirmation(ctx, repository, nil, update)
}

// confirm may return an existing result after all applicable checks. It must
// only read the committed state and return detached values. The file lock and
// full original-byte verification also apply to confirmations.
func fileContentChangeWithConfirmation[T any](ctx context.Context, repository *FileRepository, confirm func(*MemoryRepository) (T, bool, error), update func(*MemoryRepository) (T, error)) (T, error) {
	return fileContentChangeWithClone(ctx, repository, confirm, cloneContentState, update)
}

func fileContentChangeWithClone[T any](ctx context.Context, repository *FileRepository, confirm func(*MemoryRepository) (T, bool, error), clone func(*MemoryRepository) *MemoryRepository, update func(*MemoryRepository) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var task *trace.Task
	if trace.IsEnabled() {
		ctx, task = trace.NewTask(ctx, "content.file.commit")
		defer task.End()
	}
	region := trace.StartRegion(ctx, "content.file.lock")
	repository.mu.Lock()
	region.End()
	defer repository.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	region = trace.StartRegion(ctx, "content.file.verify")
	err := repository.readyLocked()
	region.End()
	if err != nil {
		return zero, err
	}
	if confirm != nil {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		region = trace.StartRegion(ctx, "content.file.confirm")
		result, confirmed, err := confirm(repository.state)
		region.End()
		if err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if confirmed {
			if task != nil {
				trace.Log(ctx, "content.file.result", "confirmed")
			}
			return result, nil
		}
	}
	region = trace.StartRegion(ctx, "content.file.clone")
	candidate := clone(repository.state)
	region.End()
	region = trace.StartRegion(ctx, "content.file.mutate")
	result, err := update(candidate)
	region.End()
	if err != nil {
		return zero, err
	}
	digest, err := writeContentState(ctx, repository.path, candidate)
	if err != nil {
		return zero, err
	}
	region = trace.StartRegion(ctx, "content.file.publish")
	repository.state, repository.digest = candidate, digest
	repository.searchState.Store(candidate)
	region.End()
	if task != nil {
		trace.Log(ctx, "content.file.result", "committed")
	}
	return result, nil
}

func (repository *FileRepository) readyLocked() error {
	digest, err := contentFileDigest(repository.path)
	if err != nil {
		return err
	}
	if digest != repository.digest {
		return errors.New("content file changed outside this process; reopen the store before writing")
	}
	return nil
}

func (repository *FileRepository) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.readyLocked()
}
