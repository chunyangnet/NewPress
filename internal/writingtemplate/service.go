package writingtemplate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/aieditor"
)

type Service struct {
	storage Storage
	now     func() time.Time
	cacheMu sync.Mutex
	cache   *validatedLibrary
}

// Exactly one validated snapshot is retained. Every operation still obtains
// fresh storage bytes; neither a read error nor changed bytes use old results.
type validatedLibrary struct {
	digest [sha256.Size]byte
	doc    library
}

type CatalogItem struct {
	aieditor.Prompt
	TemplateID      string `json:"template_id"`
	TemplateVersion int64  `json:"template_version"`
	BasePrompt      string `json:"base_prompt"`
}

func (s *Service) Catalog(ctx context.Context) ([]CatalogItem, error) {
	doc, _, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	items := []CatalogItem{}
	prompts := aieditor.Prompts()
	for _, t := range doc.Templates {
		v := t.Revisions[len(t.Revisions)-1]
		if v.Archived {
			continue
		}
		for _, p := range prompts {
			if p.ID == v.Definition.BasePrompt {
				p.ID, p.Category, p.Title, p.Description, p.Instruction, p.Example = "team:"+t.ID, "团队模板", v.Definition.Title, v.Definition.Description, v.Definition.Instruction, v.Definition.Example
				items = append(items, CatalogItem{Prompt: p, TemplateID: t.ID, TemplateVersion: v.Version, BasePrompt: v.Definition.BasePrompt})
				break
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TemplateID < items[j].TemplateID })
	return items, nil
}

func New(ctx context.Context, storage Storage) (*Service, error) {
	if storage == nil {
		return nil, ErrInvalid
	}
	s := &Service{storage: storage, now: time.Now}
	_, _, err := s.load(ctx)
	return s, err
}

// load returns an immutable document. Callers must copy any slices they change
// or expose; strings and the other value fields may be shared.
func (s *Service) load(ctx context.Context) (library, []byte, error) {
	raw, err := s.storage.Read(ctx)
	if err != nil {
		return library{}, nil, err
	}
	if err = ctx.Err(); err != nil {
		return library{}, nil, err
	}
	if len(raw) > MaxLibraryBytes {
		return library{}, nil, ErrCorrupt
	}
	sum := sha256.Sum256(raw)
	// Coalesce concurrent decoding of the same large snapshot. The mutex does
	// not cover storage I/O, and a caller waiting for it checks cancellation.
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if err = ctx.Err(); err != nil {
		return library{}, nil, err
	}
	if s.cache != nil && s.cache.digest == sum {
		return s.cache.doc, raw, nil
	}
	doc, err := decodeLibrary(raw)
	if err != nil {
		return library{}, nil, err
	}
	if err = ctx.Err(); err != nil {
		return library{}, nil, err
	}
	s.cache = &validatedLibrary{digest: sum, doc: doc}
	return doc, raw, nil
}

func (s *Service) List(ctx context.Context, archived bool, limit, offset int) (Page, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > MaxTemplates {
		return Page{}, ErrInvalid
	}
	doc, raw, err := s.load(ctx)
	if err != nil {
		return Page{}, err
	}
	items := []Summary{}
	for _, t := range doc.Templates {
		v := t.Revisions[len(t.Revisions)-1]
		if v.Archived && !archived {
			continue
		}
		items = append(items, Summary{ID: t.ID, Version: v.Version, Title: v.Definition.Title, Description: v.Definition.Description, BasePrompt: v.Definition.BasePrompt, Archived: v.Archived, UpdatedAt: v.At})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	p := Page{Items: []Summary{}, Total: len(items), NextOffset: -1, SnapshotSHA256: digest(raw)}
	if offset < len(items) {
		end := min(offset+limit, len(items))
		p.Items = items[offset:end]
		if end < len(items) {
			p.NextOffset = end
		}
	}
	return p, nil
}

func (s *Service) Get(ctx context.Context, id string) (Template, error) {
	item, err := s.get(ctx, id)
	if err != nil {
		return Template{}, err
	}
	item.Revisions = slices.Clone(item.Revisions)
	return item, nil
}

// get is an internal immutable view; Resolve and Export need no history copy.
func (s *Service) get(ctx context.Context, id string) (Template, error) {
	if !hexID(id, 32) {
		return Template{}, ErrInvalid
	}
	doc, _, err := s.load(ctx)
	if err != nil {
		return Template{}, err
	}
	for _, t := range doc.Templates {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, ErrNotFound
}

// Resolve requires the displayed version to remain current and active. It
// returns the original built-in mode so its draft/material checks still apply.
func (s *Service) Resolve(ctx context.Context, id string, version int64) (aieditor.Prompt, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return aieditor.Prompt{}, err
	}
	v := t.Revisions[len(t.Revisions)-1]
	if v.Archived {
		return aieditor.Prompt{}, ErrArchived
	}
	if version != v.Version {
		return aieditor.Prompt{}, ErrConflict
	}
	for _, p := range aieditor.Prompts() {
		if p.ID == v.Definition.BasePrompt {
			p.Title, p.Description, p.Instruction, p.Example = v.Definition.Title, v.Definition.Description, v.Definition.Instruction, v.Definition.Example
			return p, nil
		}
	}
	return aieditor.Prompt{}, ErrCorrupt
}

func (s *Service) Export(ctx context.Context, id string, version int64) ([]byte, error) {
	t, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if version < 1 || version > int64(len(t.Revisions)) {
		return nil, ErrNotFound
	}
	doc := ExportDocument{Schema: 1, Kind: "newpress.writing-template", TemplateID: id, Version: version, Definition: t.Revisions[version-1].Definition}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxExportBytes {
		return nil, ErrCapacity
	}
	return raw, nil
}

// Actor is supplied from current authenticated server identity, never from an
// untrusted HTTP body. Request keys are scoped to that identity and retained.
type Command struct {
	Action          string      `json:"action"`
	ID              string      `json:"id"`
	ExpectedVersion int64       `json:"expected_version"`
	Definition      *Definition `json:"definition,omitempty"`
	RestoreVersion  int64       `json:"restore_version,omitempty"`
	Import          []byte      `json:"import,omitempty"`
	ImportSHA256    string      `json:"import_sha256,omitempty"`
	RequestKey      string      `json:"request_key"`
	Actor           string      `json:"actor"`
}

type Result struct {
	ID       string `json:"id"`
	Version  int64  `json:"version"`
	Replayed bool   `json:"replayed"`
}

func (c Command) valid() bool {
	if c.Actor == "" || strings.TrimSpace(c.Actor) != c.Actor || len(c.Actor) > 160 || !validText(c.Actor, 160, false) || !hexID(c.RequestKey, 64) || c.ExpectedVersion < 0 || c.ExpectedVersion >= MaxVersion ||
		(c.ID == "" && c.ExpectedVersion != 0) || (c.ID != "" && (!hexID(c.ID, 32) || c.ExpectedVersion == 0)) {
		return false
	}
	if c.Action == "import" {
		return c.ID == "" && c.Definition == nil && c.RestoreVersion == 0 && len(c.Import) > 0 && len(c.Import) <= MaxExportBytes && c.ImportSHA256 == digest(c.Import)
	}
	if len(c.Import) > 0 || c.ImportSHA256 != "" {
		return false
	}
	switch c.Action {
	case "save":
		return c.Definition != nil && c.Definition.Validate() == nil && c.RestoreVersion == 0
	case "archive", "activate":
		return c.ID != "" && c.Definition == nil && c.RestoreVersion == 0
	case "restore":
		return c.ID != "" && c.Definition == nil && c.RestoreVersion > 0 && c.RestoreVersion <= c.ExpectedVersion
	}
	return false
}

func (s *Service) Apply(ctx context.Context, command Command) (Result, error) {
	return s.apply(ctx, command, nil)
}

// ApplyAuthorized rechecks request authority after loading the library (including
// receipt replays) and before submitting a replacement to storage. The callback
// must resolve current authority; it is not stored in the library. These checks
// are not a transaction with an external session or account store.
func (s *Service) ApplyAuthorized(ctx context.Context, command Command, authorize func() error) (Result, error) {
	if authorize == nil {
		return Result{}, ErrInvalid
	}
	return s.apply(ctx, command, authorize)
}

func (s *Service) apply(ctx context.Context, command Command, authorize func() error) (Result, error) {
	if !command.valid() {
		return Result{}, ErrInvalid
	}
	// Freeze pointer/byte fields before validation and persistence; caller-owned
	// values are never installed into the store.
	if command.Definition != nil {
		d := *command.Definition
		command.Definition = &d
	}
	command.Import = append([]byte(nil), command.Import...)
	encoded, _ := json.Marshal(command)
	fingerprint := digest(encoded)
	key := digest([]byte("writing-template-request\x00" + command.Actor + "\x00" + command.RequestKey))
	doc, raw, err := s.load(ctx)
	if err != nil {
		return Result{}, err
	}
	if err = checkApplyAuthority(ctx, authorize); err != nil {
		return Result{}, err
	}
	for _, r := range doc.Receipts {
		if r.Key == key {
			if r.Fingerprint != fingerprint {
				return Result{}, ErrConflict
			}
			return Result{ID: r.TemplateID, Version: r.Version, Replayed: true}, nil
		}
	}
	if len(doc.Receipts) >= MaxReceipts {
		return Result{}, ErrCapacity
	}
	selected := -1
	for i, t := range doc.Templates {
		if t.ID == command.ID {
			selected = i
			break
		}
	}
	var item Template
	next := Revision{Version: 1, Actor: command.Actor, At: s.now().UTC(), Source: Source{Kind: "manual"}}
	if next.At.IsZero() {
		return Result{}, ErrInvalid
	}
	if command.ID == "" {
		if len(doc.Templates) >= MaxTemplates {
			return Result{}, ErrCapacity
		}
		item.ID = digest([]byte("writing-template-id\x00" + key))[:32]
		for _, t := range doc.Templates {
			if t.ID == item.ID {
				return Result{}, ErrConflict
			}
		}
	} else {
		if selected < 0 {
			return Result{}, ErrNotFound
		}
		item = doc.Templates[selected]
		last := item.Revisions[len(item.Revisions)-1]
		if last.Version != command.ExpectedVersion {
			return Result{}, ErrConflict
		}
		if last.Version >= MaxVersion-1 {
			return Result{}, ErrCapacity
		}
		next.Version = last.Version + 1
		next.Definition = last.Definition
		if next.At.Before(last.At) {
			next.At = last.At
		}
		if command.Action == "save" && last.Archived {
			return Result{}, ErrArchived
		}
		if command.Action == "archive" && last.Archived || command.Action == "activate" && !last.Archived {
			return Result{}, ErrConflict
		}
	}
	switch command.Action {
	case "save":
		next.Definition = *command.Definition
	case "import":
		preview, err := PreviewImport(command.Import)
		if err != nil {
			return Result{}, err
		}
		next.Definition = preview.Document.Definition
		next.Source = Source{Kind: "import", TemplateID: preview.Document.TemplateID, Version: preview.Document.Version, SHA256: preview.SHA256}
	case "archive":
		next.Archived = true
		next.Source.Kind = "archive"
	case "activate":
		next.Source.Kind = "activate"
	case "restore":
		next.Definition = item.Revisions[command.RestoreVersion-1].Definition
		next.Source = Source{Kind: "history", TemplateID: item.ID, Version: command.RestoreVersion}
	}
	// A rejected or failed write must leave the cached snapshot untouched.
	// Other templates remain immutable and can share their history storage.
	item.Revisions = append(slices.Clone(item.Revisions), next)
	doc.Templates = slices.Clone(doc.Templates)
	if selected < 0 {
		doc.Templates = append(doc.Templates, item)
	} else {
		doc.Templates[selected] = item
	}
	doc.Receipts = append(slices.Clone(doc.Receipts), receipt{Key: key, Fingerprint: fingerprint, TemplateID: item.ID, Version: next.Version})
	payload, err := encodeLibrary(doc)
	if err != nil {
		return Result{}, err
	}
	if err = checkApplyAuthority(ctx, authorize); err != nil {
		return Result{}, err
	}
	if err = s.storage.Write(ctx, sha256.Sum256(raw), payload); err != nil {
		return Result{}, err
	}
	return Result{ID: item.ID, Version: next.Version}, nil
}

func checkApplyAuthority(ctx context.Context, authorize func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if authorize != nil {
		if err := authorize(); err != nil {
			return err
		}
	}
	return ctx.Err()
}
