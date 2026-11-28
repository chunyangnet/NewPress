// Package writingtemplate stores versioned team instructions. Article drafts,
// generation briefs and model credentials are never part of this library.
package writingtemplate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxTemplates          = 100
	MaxRevisions          = 2000
	MaxReceipts           = 10000
	MaxLibraryBytes       = 8 << 20
	MaxExportBytes        = 128 << 10
	MaxVersion      int64 = 1<<53 - 1
)

var (
	ErrInvalid  = errors.New("invalid writing template input")
	ErrCorrupt  = errors.New("writing template library is invalid")
	ErrConflict = errors.New("writing template changed; reload before applying")
	ErrNotFound = errors.New("writing template or revision not found")
	ErrArchived = errors.New("writing template is archived")
	ErrCapacity = errors.New("writing template library capacity reached")
)

// Storage publishes one whole library only when the previous byte digest still
// matches. Implementations must not mutate data supplied by the caller.
type Storage interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, [sha256.Size]byte, []byte) error
}

type Definition struct {
	BasePrompt  string `json:"base_prompt"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Instruction string `json:"instruction"`
	Example     string `json:"example"`
}

type Source struct {
	Kind       string `json:"kind"`
	TemplateID string `json:"template_id,omitempty"`
	Version    int64  `json:"version,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type Revision struct {
	Version    int64      `json:"version"`
	Definition Definition `json:"definition"`
	Archived   bool       `json:"archived"`
	Actor      string     `json:"actor"`
	At         time.Time  `json:"at"`
	Source     Source     `json:"source"`
}

type Template struct {
	ID        string     `json:"id"`
	Revisions []Revision `json:"revisions"`
}

type Summary struct {
	ID          string    `json:"id"`
	Version     int64     `json:"version"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	BasePrompt  string    `json:"base_prompt"`
	Archived    bool      `json:"archived"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Page struct {
	Items          []Summary `json:"items"`
	Total          int       `json:"total"`
	NextOffset     int       `json:"next_offset"`
	SnapshotSHA256 string    `json:"snapshot_sha256"`
}

type receipt struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	TemplateID  string `json:"template_id"`
	Version     int64  `json:"version"`
}

type library struct {
	Schema    int        `json:"schema"`
	Kind      string     `json:"kind"`
	Templates []Template `json:"templates"`
	Receipts  []receipt  `json:"receipts"`
}

type ExportDocument struct {
	Schema     int        `json:"schema"`
	Kind       string     `json:"kind"`
	TemplateID string     `json:"template_id"`
	Version    int64      `json:"version"`
	Definition Definition `json:"definition"`
}

type ImportPreview struct {
	Document ExportDocument `json:"document"`
	SHA256   string         `json:"sha256"`
}

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func hexID(value string, n int) bool {
	return len(value) == n && strings.Trim(value, "0123456789abcdef") == ""
}
func validText(value string, limit int, multiline bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && (!multiline || r != '\n' && r != '\r' && r != '\t')
	}) < 0
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.Title) != d.Title || d.Title == "" || !validText(d.Title, 80, false) ||
		!validText(d.Description, 300, true) || strings.TrimSpace(d.Instruction) == "" || !validText(d.Instruction, 12000, true) || !validText(d.Example, 8000, true) {
		return ErrInvalid
	}
	for _, p := range aieditor.Prompts() {
		if p.ID == d.BasePrompt {
			return nil
		}
	}
	return ErrInvalid
}

func validateLibrary(doc library) error {
	if doc.Schema != 1 || doc.Kind != "newpress.writing-templates" {
		return ErrCorrupt
	}
	if len(doc.Templates) > MaxTemplates || len(doc.Receipts) > MaxReceipts {
		return ErrCapacity
	}
	versions, counts := map[string]int64{}, 0
	for _, item := range doc.Templates {
		if !hexID(item.ID, 32) || versions[item.ID] != 0 || len(item.Revisions) == 0 {
			return ErrCorrupt
		}
		counts += len(item.Revisions)
		if counts > MaxRevisions {
			return ErrCapacity
		}
		var previous time.Time
		for i, v := range item.Revisions {
			if v.Version != int64(i+1) || v.Version >= MaxVersion || v.Definition.Validate() != nil || v.At.IsZero() || v.At.Before(previous) ||
				v.Actor == "" || strings.TrimSpace(v.Actor) != v.Actor || len(v.Actor) > 160 || !validText(v.Actor, 160, false) {
				return ErrCorrupt
			}
			previous = v.At
			switch source := v.Source; source.Kind {
			case "manual", "archive", "activate":
				if source.TemplateID != "" || source.Version != 0 || source.SHA256 != "" {
					return ErrCorrupt
				}
			case "history":
				if source.TemplateID != item.ID || source.Version < 1 || source.Version >= v.Version || source.SHA256 != "" {
					return ErrCorrupt
				}
			case "import":
				if i != 0 || !hexID(source.TemplateID, 32) || source.Version < 1 || source.Version >= MaxVersion || !hexID(source.SHA256, 64) {
					return ErrCorrupt
				}
			default:
				return ErrCorrupt
			}
			if i == 0 && (v.Archived || v.Source.Kind != "manual" && v.Source.Kind != "import") {
				return ErrCorrupt
			}
			if i > 0 {
				last := item.Revisions[i-1]
				switch v.Source.Kind {
				case "archive":
					if !v.Archived || last.Archived || v.Definition != last.Definition {
						return ErrCorrupt
					}
				case "activate":
					if v.Archived || !last.Archived || v.Definition != last.Definition {
						return ErrCorrupt
					}
				case "history":
					if v.Archived || v.Definition != item.Revisions[v.Source.Version-1].Definition {
						return ErrCorrupt
					}
				case "manual":
					if v.Archived || last.Archived {
						return ErrCorrupt
					}
				default:
					return ErrCorrupt
				}
			}
		}
		versions[item.ID] = int64(len(item.Revisions))
	}
	seen := map[string]bool{}
	for _, r := range doc.Receipts {
		if !hexID(r.Key, 64) || seen[r.Key] || !hexID(r.Fingerprint, 64) || r.Version < 1 || r.Version > versions[r.TemplateID] {
			return ErrCorrupt
		}
		seen[r.Key] = true
	}
	return nil
}

func decodeLibrary(raw []byte) (library, error) {
	if len(raw) == 0 {
		return library{Schema: 1, Kind: "newpress.writing-templates", Templates: []Template{}, Receipts: []receipt{}}, nil
	}
	doc, err := packagefs.DecodeJSON[library](bytes.NewReader(raw), MaxLibraryBytes, "writing template library")
	if err != nil {
		return library{}, ErrCorrupt
	}
	return doc, validateLibrary(doc)
}

// ValidateSnapshot checks an existing, nonempty persisted library. Missing new
// libraries are represented separately by Storage.Read returning no bytes.
func ValidateSnapshot(raw []byte) error {
	if len(raw) == 0 {
		return ErrCorrupt
	}
	_, err := decodeLibrary(raw)
	return err
}

func encodeLibrary(doc library) ([]byte, error) {
	if err := validateLibrary(doc); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxLibraryBytes {
		return nil, ErrCapacity
	}
	return raw, nil
}

func PreviewImport(raw []byte) (ImportPreview, error) {
	doc, err := packagefs.DecodeJSON[ExportDocument](bytes.NewReader(raw), MaxExportBytes, "writing template export")
	if err != nil || doc.Schema != 1 || doc.Kind != "newpress.writing-template" || !hexID(doc.TemplateID, 32) || doc.Version < 1 || doc.Version >= MaxVersion || doc.Definition.Validate() != nil {
		return ImportPreview{}, ErrInvalid
	}
	return ImportPreview{Document: doc, SHA256: digest(raw)}, nil
}
