package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"newpress.local/newpress/internal/activity"
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
		payload, err = encodeContentDocument(repository.state)
		if err != nil {
			return nil, err
		}
		if err := writeContentFile(absolute, payload); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read content store: %w", err)
	} else {
		repository.state, err = decodeContentDocument(payload)
		if err != nil {
			return nil, err
		}
	}
	repository.digest = sha256.Sum256(payload)
	return repository, nil
}

func readContentFile(path string) ([]byte, error) {
	file, before, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if before.Size() <= 0 || before.Size() > maxContentFileBytes {
		return nil, errors.New("content document size is invalid")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxContentFileBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != int64(len(payload)) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("content document changed while reading")
	}
	return payload, nil
}

func writeContentFile(path string, payload []byte) error {
	if len(payload) > maxContentFileBytes {
		return errors.New("content store exceeds 128 MiB; use PostgreSQL for a larger site")
	}
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".newpress-content-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := packagefs.ReplaceRegularFileNoSymlink(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace content store: %w", err)
	}
	if runtime.GOOS != "windows" {
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	return nil
}

func encodeContentDocument(state *MemoryRepository) ([]byte, error) {
	document := contentDocument{
		Schema: state.contentSchema, Kind: "newpress.content", NextID: state.nextID,
		NextTermID: state.nextTermID, NextRevisionID: state.nextRevisionID,
		Posts: state.posts, Terms: state.terms, Revisions: []Revision{}, Autosaves: []Autosave{},
		Activities: state.activities,
		Series:     state.series, NextSeriesID: state.nextSeriesID,
		Redirects: state.redirects, NextRedirectID: state.nextRedirectID,
		Imports:     state.imports,
		Submissions: state.submissions,
		Snippets:    state.snippets, SnippetReceipts: state.snippetReceipts,
		SnippetQuarantines: state.snippetQuarantines,
	}
	if state.contentSchema >= 2 {
		document.NextSnippetID = state.nextSnippetID
	}
	for _, revisions := range state.revisions {
		document.Revisions = append(document.Revisions, revisions...)
	}
	sort.Slice(document.Revisions, func(i, j int) bool { return document.Revisions[i].ID < document.Revisions[j].ID })
	for _, owners := range state.autosaves {
		for _, autosave := range owners {
			if autosave.ExpiresAt.After(state.now()) {
				document.Autosaves = append(document.Autosaves, autosave)
			}
		}
	}
	sort.Slice(document.Autosaves, func(i, j int) bool {
		left, right := document.Autosaves[i], document.Autosaves[j]
		if left.PostID == right.PostID {
			return left.OwnerKey < right.OwnerKey
		}
		return left.PostID < right.PostID
	})
	payload, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode content store: %w", err)
	}
	withdrawalReserve := 0
	for _, snippet := range state.snippets {
		if snippet.Visibility != SnippetWithdrawn {
			withdrawalReserve += 64
		}
	}
	evidenceReserve, err := snippetFileEvidenceBudget(state.snippetQuarantines)
	if err != nil {
		return nil, err
	}
	if len(state.snippetQuarantines) != 0 && state.contentSchema < 3 {
		return nil, ErrSnippetFileMaintenanceEvidence
	}
	if len(payload)+1+withdrawalReserve+evidenceReserve > maxContentFileBytes {
		return nil, errors.New("content store exceeds 128 MiB; use PostgreSQL for a larger site")
	}
	return append(payload, '\n'), nil
}

func decodeContentDocument(payload []byte) (*MemoryRepository, error) {
	document, err := packagefs.DecodeJSONWithNumbers[contentDocument](bytes.NewReader(payload), maxContentFileBytes, "content store")
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
		if err := ValidateSnippetState(document.Snippets, document.SnippetReceipts, document.NextSnippetID); err != nil {
			return nil, err
		}
		if err := validateSnippetFileLinks(document.Snippets, document.SnippetReceipts); err != nil {
			return nil, err
		}
		state.snippets, state.snippetReceipts, state.nextSnippetID = document.Snippets, document.SnippetReceipts, document.NextSnippetID
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
	revisionIDs := make(map[int64]bool)
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
		state.revisions[revision.PostID] = append(previous, revision)
	}
	for _, autosave := range document.Autosaves {
		autosave, err := NormalizeAutosave(autosave)
		post, ok := posts[autosave.PostID]
		if err != nil || !ok || autosave.SourceVersion != post.Version || autosave.Snapshot.Version != post.Version ||
			autosave.Snapshot.AuthorID != post.AuthorID || autosave.UpdatedAt.IsZero() || !autosave.ExpiresAt.After(autosave.UpdatedAt) {
			return nil, errors.New("invalid stored content autosave")
		}
		if state.autosaves[post.ID] == nil {
			state.autosaves[post.ID] = make(map[string]Autosave)
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
	for id, counts := range source.snippetPostCounts {
		// Derived entries are replaced, never mutated after publication.
		clone.snippetPostCounts[id] = counts
	}
	for _, record := range source.snippets {
		record.Revisions = append([]SnippetRevision(nil), record.Revisions...)
		clone.snippets = append(clone.snippets, record)
	}
	for _, receipt := range source.snippetReceipts {
		receipt.Items = append([]SnippetSelection(nil), receipt.Items...)
		clone.snippetReceipts = append(clone.snippetReceipts, receipt)
	}
	for _, item := range source.series {
		clone.series = append(clone.series, cloneSeries(item))
	}
	for _, post := range source.posts {
		clone.posts = append(clone.posts, clonePost(post))
	}
	for id, revisions := range source.revisions {
		for _, revision := range revisions {
			revision.Snapshot = clonePost(revision.Snapshot)
			clone.revisions[id] = append(clone.revisions[id], revision)
		}
	}
	for id, owners := range source.autosaves {
		clone.autosaves[id] = make(map[string]Autosave)
		for owner, autosave := range owners {
			clone.autosaves[id][owner] = cloneAutosave(autosave)
		}
	}
	return clone
}

func fileContentChange[T any](ctx context.Context, repository *FileRepository, update func(*MemoryRepository) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := repository.readyLocked(); err != nil {
		return zero, err
	}
	candidate := cloneContentState(repository.state)
	result, err := update(candidate)
	if err != nil {
		return zero, err
	}
	payload, err := encodeContentDocument(candidate)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := writeContentFile(repository.path, payload); err != nil {
		return zero, err
	}
	repository.state, repository.digest = candidate, sha256.Sum256(payload)
	return result, nil
}

func (repository *FileRepository) readyLocked() error {
	payload, err := readContentFile(repository.path)
	if err != nil {
		return err
	}
	if sha256.Sum256(payload) != repository.digest {
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
