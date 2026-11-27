// Package backup implements portable, offline backups of a local NewPress
// installation. Database backups and deployment secrets have separate lifecycles.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

const (
	ManifestName      = "manifest.json"
	MaxEntries        = 20_000
	MaxFileBytes      = 512 << 20
	MaxTotalBytes     = int64(4 << 30)
	MaxArchiveBytes   = int64(2 << 30)
	maxManifestBytes  = 8 << 20
	maxDirectoryBytes = 16 << 20
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Software struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Runtime includes only the settings needed to interpret a local site's
// persisted files. Bind addresses, proxies, credentials and TLS remain the
// destination operator's deployment configuration.
type Runtime struct {
	ActiveTheme string `json:"active_theme"`
	SiteTitle   string `json:"site_title"`
	SiteTagline string `json:"site_tagline"`
	PublicURL   string `json:"public_url"`
}

type Manifest struct {
	Schema         int              `json:"schema"`
	Kind           string           `json:"kind"`
	Storage        string           `json:"storage"`
	CreatedAt      time.Time        `json:"created_at"`
	Software       Software         `json:"software"`
	Runtime        Runtime          `json:"runtime"`
	Audit          audit.Checkpoint `json:"audit_checkpoint"`
	Directories    []string         `json:"directories"`
	Files          []File           `json:"files"`
	TotalBytes     int64            `json:"total_bytes"`
	ManifestSHA256 string           `json:"manifest_sha256"`
}

type Section struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Report contains no account records, messages or credentials. Environment is
// provided only after restore, with explicit local storage and seed disabled.
type Report struct {
	Kind                 string             `json:"kind"`
	Verified             bool               `json:"verified"`
	ApplicationValidated bool               `json:"application_validated"`
	CreatedAt            time.Time          `json:"created_at"`
	Software             Software           `json:"software"`
	Runtime              Runtime            `json:"runtime"`
	Files                int                `json:"files"`
	Directories          int                `json:"directories"`
	TotalBytes           int64              `json:"total_bytes"`
	ArchiveSHA256        string             `json:"archive_sha256"`
	ManifestSHA256       string             `json:"manifest_sha256"`
	AuditSequence        uint64             `json:"audit_sequence"`
	Sections             map[string]Section `json:"sections"`
	Excluded             []string           `json:"excluded"`
	Destination          string             `json:"destination,omitempty"`
	Environment          map[string]string  `json:"environment,omitempty"`
}

func makeReport(manifest Manifest, digest string, applicationValidated bool) Report {
	sections := map[string]Section{"data": {}, "themes": {}, "plugins": {}}
	for _, file := range manifest.Files {
		root := strings.SplitN(file.Path, "/", 2)[0]
		section := sections[root]
		section.Files++
		section.Bytes += file.Size
		sections[root] = section
	}
	return Report{
		Kind: "newpress.local_backup_verification", Verified: true, ApplicationValidated: applicationValidated,
		CreatedAt: manifest.CreatedAt, Software: manifest.Software, Runtime: manifest.Runtime,
		Files: len(manifest.Files), Directories: len(manifest.Directories), TotalBytes: manifest.TotalBytes,
		ArchiveSHA256: digest, ManifestSHA256: manifest.ManifestSHA256,
		AuditSequence: manifest.Audit.Ledger.Sequence, Sections: sections,
		Excluded: []string{"process lock files", "data/media/tmp", "process sessions and in-memory API idempotency", "PostgreSQL", "application binary and deployment configuration or secrets outside the three roots"},
	}
}

func runtimeFromConfig(cfg config.Config) Runtime {
	return Runtime{ActiveTheme: cfg.ActiveTheme, SiteTitle: cfg.SiteTitle, SiteTagline: cfg.SiteTagline, PublicURL: cfg.PublicURL}
}

func manifestDigest(manifest Manifest) string {
	manifest.ManifestSHA256 = ""
	payload, _ := json.Marshal(manifest)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validateManifest(manifest Manifest) error {
	if manifest.Schema != 1 || manifest.Kind != "newpress.local_backup" || manifest.Storage != "file" ||
		manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC {
		return errors.New("unsupported local backup metadata")
	}
	for _, value := range []string{manifest.Software.Version, manifest.Software.Commit, manifest.Runtime.SiteTitle} {
		if value == "" || len(value) > 480 || !validText(value) {
			return errors.New("invalid backup software or site identity")
		}
	}
	if len(manifest.Runtime.SiteTagline) > 4000 || !validText(manifest.Runtime.SiteTagline) {
		return errors.New("invalid backup site tagline")
	}
	if err := packagefs.ValidateSlug(manifest.Runtime.ActiveTheme); err != nil {
		return err
	}
	publicURL, err := url.Parse(manifest.Runtime.PublicURL)
	if err != nil || len(manifest.Runtime.PublicURL) > 2048 || !validText(manifest.Runtime.PublicURL) ||
		(publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Hostname() == "" ||
		publicURL.User != nil || publicURL.Path != "" || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return errors.New("invalid backup public URL")
	}
	if err := audit.ValidateCheckpoint(manifest.Audit); err != nil {
		return fmt.Errorf("backup audit checkpoint: %w", err)
	}
	if manifest.Audit.Software.Version != manifest.Software.Version || manifest.Audit.Software.Commit != manifest.Software.Commit ||
		manifest.Audit.GeneratedAt != manifest.CreatedAt {
		return errors.New("backup audit checkpoint identity does not match manifest")
	}
	if len(manifest.Files) == 0 || len(manifest.Directories) < 3 || len(manifest.Files)+len(manifest.Directories) > MaxEntries ||
		manifest.TotalBytes < 0 || manifest.TotalBytes > MaxTotalBytes {
		return errors.New("backup inventory exceeds entry or size limits")
	}
	if !sort.StringsAreSorted(manifest.Directories) || !sort.SliceIsSorted(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path }) {
		return errors.New("backup inventory must be sorted")
	}
	type entry struct {
		name string
		dir  bool
	}
	entries := make(map[string]entry, len(manifest.Files)+len(manifest.Directories))
	add := func(name string, directory bool) error {
		if err := validatePath(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if previous, exists := entries[key]; exists {
			return fmt.Errorf("backup paths collide: %s and %s", previous.name, name)
		}
		entries[key] = entry{name: name, dir: directory}
		return nil
	}
	for _, directory := range manifest.Directories {
		if err := add(directory, true); err != nil {
			return err
		}
	}
	files := make(map[string]File, len(manifest.Files))
	var total int64
	for _, file := range manifest.Files {
		if err := add(file.Path, false); err != nil {
			return err
		}
		if file.Size < 0 || file.Size > MaxFileBytes || !validDigest(file.SHA256) || total > MaxTotalBytes-file.Size {
			return fmt.Errorf("invalid backup file size or digest: %s", file.Path)
		}
		total += file.Size
		files[file.Path] = file
	}
	if total != manifest.TotalBytes {
		return errors.New("backup total size does not match inventory")
	}
	for _, item := range entries {
		for parent := path.Dir(item.name); parent != "."; parent = path.Dir(parent) {
			actual, exists := entries[strings.ToLower(parent)]
			if !exists || !actual.dir || actual.name != parent {
				return fmt.Errorf("backup parent directory is missing, aliased or a file: %s", parent)
			}
		}
	}
	for _, root := range []string{"data", "themes", "plugins"} {
		if item := entries[root]; !item.dir || item.name != root {
			return fmt.Errorf("backup is missing root directory %s", root)
		}
	}
	for _, required := range []string{"data/content.json", "data/users.json", "data/" + audit.FileName, "themes/" + manifest.Runtime.ActiveTheme + "/theme.json"} {
		if _, exists := files[required]; !exists {
			return fmt.Errorf("backup is missing required file %s; initialize or recover the site before backing it up", required)
		}
	}
	facts := map[string]string{audit.FactUsers: "data/users.json", audit.FactComments: "data/comments.json", audit.FactNavigation: "data/navigation.json"}
	for _, fact := range manifest.Audit.Facts {
		if fact.Key == audit.FactCommunity {
			facts[audit.FactCommunity] = "data/community.json"
		}
	}
	if len(manifest.Audit.Facts) != len(facts) {
		return errors.New("backup checkpoint must cover users, comments and navigation")
	}
	for _, fact := range manifest.Audit.Facts {
		name, exists := facts[fact.Key]
		if !exists {
			return errors.New("unknown backup checkpoint fact")
		}
		digest := audit.MissingDigest()
		if file, exists := files[name]; exists {
			digest = file.SHA256
		}
		if fact.SHA256 != digest {
			return fmt.Errorf("backup checkpoint does not match %s", name)
		}
	}
	if !validDigest(manifest.ManifestSHA256) || manifestDigest(manifest) != manifest.ManifestSHA256 {
		return errors.New("backup manifest checksum does not match")
	}
	return nil
}

func validatePath(name string) error {
	if len(name) > 1024 || strings.Count(name, "/") > 32 || !validText(name) || strings.ContainsAny(name, "<>\"|?*") {
		return fmt.Errorf("backup path is not portable: %q", name)
	}
	if err := packagefs.ValidateRelativePath(name); err != nil {
		return err
	}
	root := strings.SplitN(name, "/", 2)[0]
	if root != "data" && root != "themes" && root != "plugins" {
		return fmt.Errorf("backup path is outside its three roots: %s", name)
	}
	for _, component := range strings.Split(name, "/") {
		if len(component) > 255 || strings.EqualFold(component, dirlock.FileName) {
			return fmt.Errorf("backup contains an excluded or oversized path: %s", name)
		}
	}
	if isTemporaryMediaPath(name) {
		return errors.New("backup must not include temporary media files")
	}
	if isSessionPath(name) {
		return errors.New("backup must not include authentication sessions")
	}
	return nil
}

func isSessionPath(name string) bool {
	parts := strings.SplitN(name, "/", 3)
	return len(parts) >= 2 && parts[0] == "data" && strings.EqualFold(parts[1], auth.SessionDirectory)
}

// Use the same component boundaries and case rules when scanning sources and
// validating archives so excluded paths remain excluded across filesystems.
func isTemporaryMediaPath(name string) bool {
	parts := strings.SplitN(name, "/", 4)
	return len(parts) >= 3 && parts[0] == "data" && strings.EqualFold(parts[1], "media") && strings.EqualFold(parts[2], "tmp")
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func currentSoftware() Software { return Software{Version: version.Version, Commit: version.Commit} }
