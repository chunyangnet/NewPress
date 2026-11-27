package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/theme"
)

// Create holds every source directory lock until the archive and its isolated
// application load have both passed. The target must be new and outside the
// source roots. Publication uses an exclusive hard link on the same volume.
func Create(ctx context.Context, cfg config.Config, archivePath string) (Report, error) {
	roots, err := sources(cfg)
	if err != nil {
		return Report{}, err
	}
	archivePath, err = filepath.Abs(archivePath)
	if err != nil {
		return Report{}, err
	}
	for _, root := range roots {
		if within(root.path, archivePath) {
			return Report{}, errors.New("backup output must be outside the data, themes and plugins directories")
		}
	}
	if err := packagefs.ValidateRegularFileTargetNoSymlink(archivePath); err != nil {
		return Report{}, err
	}
	if _, err := os.Lstat(archivePath); !errors.Is(err, os.ErrNotExist) {
		return Report{}, errors.New("backup output already exists or cannot be inspected; choose a new file")
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return Report{}, err
	}
	defer locks.Close()
	cfg.ActiveTheme, err = theme.SelectedSlug(cfg.ThemesDir, cfg.ActiveTheme)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	ledgerPath := filepath.Join(cfg.DataDir, audit.FileName)
	if _, err := packagefs.LstatNoSymlink(ledgerPath); err != nil {
		return Report{}, errors.New("backup requires an initialized site with an existing audit ledger")
	}
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		return Report{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	software := currentSoftware()
	checkpoint, err := ledger.CreateCheckpoint(factPaths(cfg.DataDir, ledger), audit.CheckpointOptions{
		GeneratedAt: now, SoftwareVersion: software.Version, SoftwareCommit: software.Commit,
	})
	if err != nil {
		return Report{}, fmt.Errorf("capture backup audit checkpoint: %w", err)
	}
	directories, files, total, err := scan(ctx, roots)
	if err != nil {
		return Report{}, err
	}
	manifest := Manifest{
		Schema: 1, Kind: "newpress.local_backup", Storage: "file", CreatedAt: now, Software: software,
		Runtime: runtimeFromConfig(cfg), Audit: checkpoint, Directories: directories, Files: files, TotalBytes: total,
	}
	manifest.ManifestSHA256 = manifestDigest(manifest)
	if err := validateManifest(manifest); err != nil {
		return Report{}, err
	}
	parent := filepath.Dir(archivePath)
	temporary, err := os.CreateTemp(parent, ".newpress-backup-*.zip")
	if err != nil {
		return Report{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	defer temporary.Close()
	if err := writeArchive(ctx, temporary, roots, manifest); err != nil {
		return Report{}, err
	}
	if err := temporary.Close(); err != nil {
		return Report{}, err
	}
	if err := matchesInventory(ctx, roots, manifest); err != nil {
		return Report{}, err
	}
	archived, err := openArchive(ctx, temporaryPath)
	if err != nil {
		return Report{}, err
	}
	defer archived.file.Close()
	staging, cleanup, err := newStaging(parent)
	if err != nil {
		return Report{}, err
	}
	defer cleanup()
	digest, err := archived.verify(ctx, staging)
	if err != nil {
		return Report{}, err
	}
	if err := validateApplication(ctx, staging, manifest); err != nil {
		return Report{}, fmt.Errorf("validate backup recovery: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	// Link fails if the user-selected target appeared during verification;
	// unlike rename it cannot replace an existing archive on Unix.
	if err := packagefs.ValidateRegularFileTargetNoSymlink(archivePath); err != nil {
		return Report{}, err
	}
	if err := os.Link(temporaryPath, archivePath); err != nil {
		return Report{}, fmt.Errorf("publish backup without overwrite (same-volume hard links required): %w", err)
	}
	// Publication has committed. A directory-sync or temporary cleanup error
	// must not imply that the verified archive was never created.
	_ = syncDirectory(parent)
	return makeReport(manifest, digest, true), nil
}

// Inspect is read-only: it checks all archive paths, lengths, CRCs and SHA-256
// digests without loading configuration or opening a site's data directory.
// Domain validation additionally runs during Create and Restore.
func Inspect(ctx context.Context, archivePath string) (Report, error) {
	archived, err := openArchive(ctx, archivePath)
	if err != nil {
		return Report{}, err
	}
	defer archived.file.Close()
	digest, err := archived.verify(ctx, "")
	if err != nil {
		return Report{}, err
	}
	return makeReport(archived.manifest, digest, false), nil
}

// Restore never merges into an existing site. It validates and loads a private
// sibling directory before renaming it to a missing or still-empty destination.
func Restore(ctx context.Context, archivePath, destination string) (Report, error) {
	if strings.TrimSpace(destination) == "" {
		return Report{}, errors.New("restore destination is required")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return Report{}, err
	}
	parent := filepath.Dir(destination)
	parentInfo, err := packagefs.LstatNoSymlink(parent)
	if err != nil || !parentInfo.IsDir() || destination == parent {
		return Report{}, errors.New("restore destination must have an existing real parent directory")
	}
	previous, err := emptyDestination(destination)
	if err != nil {
		return Report{}, err
	}
	archived, err := openArchive(ctx, archivePath)
	if err != nil {
		return Report{}, err
	}
	defer archived.file.Close()
	staging, cleanup, err := newStaging(parent)
	if err != nil {
		return Report{}, err
	}
	defer cleanup()
	digest, err := archived.verify(ctx, staging)
	if err != nil {
		return Report{}, err
	}
	if err := validateApplication(ctx, staging, archived.manifest); err != nil {
		return Report{}, fmt.Errorf("validate restored application: %w", err)
	}
	// Only after verifying the exact archived inventory, fence recovered work.
	// Restore intentionally changes task receipts, never the source archive.
	taskPath := filepath.Join(staging, "data", "plugin-jobs.json")
	if _, statErr := os.Stat(taskPath); statErr == nil {
		backend, loadErr := pluginjobs.NewFileBackend(taskPath)
		if loadErr != nil {
			return Report{}, loadErr
		}
		if err := (&pluginjobs.Queue{Backend: backend}).CancelRestored(ctx, time.Now()); err != nil {
			return Report{}, fmt.Errorf("fence restored plugin tasks: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Report{}, statErr
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	currentParent, err := packagefs.LstatNoSymlink(parent)
	if err != nil || !os.SameFile(parentInfo, currentParent) {
		return Report{}, errors.New("restore parent changed during verification")
	}
	current, err := emptyDestination(destination)
	if err != nil {
		return Report{}, err
	}
	if (previous == nil) != (current == nil) || previous != nil && !os.SameFile(previous, current) {
		return Report{}, errors.New("restore destination changed during verification")
	}
	if current != nil {
		// This is deliberately non-recursive; a concurrent write makes Remove
		// fail instead of deleting an existing site's contents.
		if err := os.Remove(destination); err != nil {
			return Report{}, fmt.Errorf("reserve empty restore destination: %w", err)
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		if current != nil {
			_ = os.Mkdir(destination, current.Mode().Perm())
		}
		return Report{}, fmt.Errorf("publish restored site: %w", err)
	}
	_ = syncDirectory(parent)
	report := makeReport(archived.manifest, digest, true)
	report.Destination = destination
	report.Environment = map[string]string{
		"NEWPRESS_DATA_DIR": filepath.Join(destination, "data"), "NEWPRESS_THEMES_DIR": filepath.Join(destination, "themes"),
		"NEWPRESS_PLUGINS_DIR": filepath.Join(destination, "plugins"), "NEWPRESS_ACTIVE_THEME": archived.manifest.Runtime.ActiveTheme,
		"NEWPRESS_SITE_TITLE": archived.manifest.Runtime.SiteTitle, "NEWPRESS_SITE_TAGLINE": archived.manifest.Runtime.SiteTagline,
		"NEWPRESS_PUBLIC_URL": archived.manifest.Runtime.PublicURL, "NEWPRESS_SEED_DEMO": "false",
		"NEWPRESS_DATABASE_URL": "", "NEWPRESS_DATABASE_URL_FILE": "",
	}
	return report, nil
}

func emptyDestination(destination string) (os.FileInfo, error) {
	info, err := packagefs.LstatNoSymlink(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() {
		return nil, errors.New("restore destination must be a new or empty real directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("restore destination is not empty; existing sites are never overwritten")
	}
	return info, nil
}

func factPaths(dataDir string, ledgers ...*audit.Ledger) map[string]string {
	paths := map[string]string{
		audit.FactUsers: filepath.Join(dataDir, "users.json"), audit.FactComments: filepath.Join(dataDir, "comments.json"),
		audit.FactNavigation: filepath.Join(dataDir, "navigation.json"),
	}
	if len(ledgers) > 0 && ledgers[0].HasFact(audit.FactCommunity) {
		paths[audit.FactCommunity] = filepath.Join(dataDir, "community.json")
	}
	return paths
}

func validateApplication(ctx context.Context, directory string, manifest Manifest) error {
	if manifest.Software.Version != currentSoftware().Version {
		return fmt.Errorf("restore requires NewPress %s; run the matching release before upgrading", manifest.Software.Version)
	}
	cfg := config.Config{
		Address: "127.0.0.1:0", DataDir: filepath.Join(directory, "data"), ThemesDir: filepath.Join(directory, "themes"),
		PluginsDir: filepath.Join(directory, "plugins"), ActiveTheme: manifest.Runtime.ActiveTheme,
		SiteTitle: manifest.Runtime.SiteTitle, SiteTagline: manifest.Runtime.SiteTagline, PublicURL: manifest.Runtime.PublicURL,
		AdminUsername: "admin", AdminEmail: "admin@localhost.invalid", AdminDisplayName: "Administrator", AdminLocale: "zh-CN",
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	ledger, err := audit.Open(filepath.Join(cfg.DataDir, audit.FileName))
	if err != nil {
		return err
	}
	verification, err := ledger.VerifyCheckpoint(manifest.Audit, factPaths(cfg.DataDir, ledger))
	if err != nil {
		return err
	}
	if verification.Relation != audit.CheckpointRelationExact || !verification.CurrentFactsVerified {
		return errors.New("restored audit ledger is not the exact backup checkpoint")
	}
	library, err := media.NewManager(filepath.Join(cfg.DataDir, "media"))
	if err != nil {
		return err
	}
	files := make(map[string]File, len(manifest.Files))
	for _, file := range manifest.Files {
		files[file.Path] = file
	}
	for offset := 0; ; offset += 100 {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := library.List(media.Query{Status: media.StatusAll, Limit: 100, Offset: offset})
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			for _, object := range item.Objects() {
				file, exists := files["data/media/"+object.ObjectKey]
				if !exists || file.SHA256 != object.ChecksumSHA256 || file.Size != object.SizeBytes {
					return fmt.Errorf("media %d object checksum differs from its library entry", item.ID)
				}
			}
		}
		if offset+len(page.Items) >= page.Total {
			break
		}
	}
	installed, err := plugin.NewManager(cfg.PluginsDir).List()
	if err != nil {
		return err
	}
	for _, installedPlugin := range installed {
		if installedPlugin.Error != "" {
			return fmt.Errorf("backup plugin cannot be loaded: %s", installedPlugin.Error)
		}
	}
	roots, err := sources(cfg)
	if err != nil {
		return err
	}
	// Startup may detect interrupted writes and recover pending transactions. A
	// complete backup must already be canonical, so such changes fail here
	// instead of silently publishing a repaired or partially seeded site.
	return matchesInventory(ctx, roots, manifest)
}
