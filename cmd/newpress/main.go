package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/backup"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/licensing"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/update"
	"newpress.local/newpress/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	command := "run"
	if len(arguments) > 0 {
		command = arguments[0]
		arguments = arguments[1:]
	}

	switch command {
	case "run":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return update.Run(ctx, cfg)
	case "update-info":
		return printJSON(update.Info())
	case "serve":
		return serve()
	case "version", "--version", "-v":
		fmt.Printf("%s %s (%s, commit %s, built %s)\n", version.Name, version.Version, version.APIVersion, version.Commit, version.BuildDate)
		return nil
	case "licenses":
		if len(arguments) != 0 {
			return errors.New("usage: newpress licenses")
		}
		return licensing.WriteNotices(os.Stdout)
	case "theme":
		return themeCommand(arguments)
	case "plugin":
		return pluginCommand(arguments)
	case "snippet":
		return snippetCommand(arguments)
	case "admin":
		return adminCommand(arguments)
	case "audit":
		value, err := auditCommand(arguments)
		if err != nil {
			return err
		}
		return printJSON(value)
	case "backup":
		value, err := backupCommand(arguments)
		if err != nil {
			return err
		}
		return printJSON(value)
	case "migrate":
		return migrateCommand(arguments)
	case "healthcheck":
		return healthcheckCommand(arguments)
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func backupCommand(arguments []string) (backup.Report, error) {
	usage := "usage: newpress backup <create archive.zip|inspect archive.zip|restore archive.zip --destination empty-directory>"
	if len(arguments) < 2 ||
		(arguments[0] != "create" && arguments[0] != "inspect" && arguments[0] != "restore") ||
		(arguments[0] != "restore" && len(arguments) != 2) ||
		(arguments[0] == "restore" && (len(arguments) != 4 || arguments[2] != "--destination")) {
		return backup.Report{}, errors.New(usage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch arguments[0] {
	case "inspect":
		return backup.Inspect(ctx, arguments[1])
	case "restore":
		return backup.Restore(ctx, arguments[1], arguments[3])
	default:
		cfg, err := config.Load()
		if err != nil {
			return backup.Report{}, err
		}
		return backup.Create(ctx, cfg, arguments[1])
	}
}

func auditCommand(arguments []string) (any, error) {
	if len(arguments) == 0 || len(arguments) > 2 ||
		(arguments[0] != "checkpoint" && arguments[0] != "verify-checkpoint") ||
		(arguments[0] == "checkpoint" && len(arguments) != 1) ||
		(arguments[0] == "verify-checkpoint" && len(arguments) != 2) {
		return nil, errors.New("usage: newpress audit <checkpoint|verify-checkpoint checkpoint.json>")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.DatabaseURL != "" {
		return nil, errors.New("audit checkpoint commands currently require file storage mode")
	}
	locks, err := dirlock.Acquire(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer locks.Close()
	ledgerPath := filepath.Join(cfg.DataDir, audit.FileName)
	if _, err := packagefs.LstatNoSymlink(ledgerPath); err != nil {
		return nil, fmt.Errorf("inspect local audit ledger: %w", err)
	}
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		return nil, err
	}
	facts := map[string]string{
		audit.FactUsers:      filepath.Join(cfg.DataDir, "users.json"),
		audit.FactComments:   filepath.Join(cfg.DataDir, "comments.json"),
		audit.FactNavigation: filepath.Join(cfg.DataDir, "navigation.json"),
	}
	if ledger.HasFact(audit.FactCommunity) {
		facts[audit.FactCommunity] = filepath.Join(cfg.DataDir, "community.json")
	}
	if arguments[0] == "checkpoint" {
		return ledger.CreateCheckpoint(facts, audit.CheckpointOptions{
			SoftwareVersion: version.Version,
			SoftwareCommit:  version.Commit,
		})
	}
	checkpoint, err := audit.ReadCheckpoint(arguments[1])
	if err != nil {
		return nil, err
	}
	return ledger.VerifyCheckpoint(checkpoint, facts)
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("NEWPRESS_UPDATE_MANAGED") == "1" {
		go update.WaitForParent(stop)
	}

	application, err := app.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer application.Close()
	return application.Run(ctx)
}

func themeCommand(arguments []string) error {
	if isExtensionCommand(arguments) || len(arguments) >= 2 && arguments[0] == "install" && !strings.HasSuffix(strings.ToLower(arguments[1]), ".zip") {
		return extensionCommand("theme", arguments)
	}
	if len(arguments) != 2 || arguments[0] != "install" {
		return errors.New("usage: newpress theme install <package.zip>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	manifest, err := theme.NewManager(cfg.ThemesDir, cfg.ActiveTheme).InstallPackage(arguments[1])
	if err != nil {
		return err
	}
	return printJSON(manifest)
}

func pluginCommand(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "maintain-shared-records" {
		return maintainSharedPluginRecords(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "maintain-records" {
		return maintainPluginRecords(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "archive-tasks" {
		return archivePluginTasks(arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "cancel-restored-tasks" {
		return cancelRestoredPluginTasks(arguments[1:])
	}
	if isExtensionCommand(arguments) || len(arguments) >= 2 && arguments[0] == "install" && !plugin.IsBundledSlug(arguments[1]) {
		return extensionCommand("plugin", arguments)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	manager := plugin.NewManager(cfg.PluginsDir)
	if err := manager.Recover(); err != nil {
		return err
	}
	if len(arguments) == 2 && arguments[0] == "install" {
		return manager.InstallBundled(arguments[1])
	}
	if len(arguments) == 2 && (arguments[0] == "enable" || arguments[0] == "disable") {
		if arguments[0] == "enable" {
			items, err := manager.List()
			if err != nil {
				return err
			}
			official := false
			for _, item := range items {
				if item.Manifest.Slug == arguments[1] && item.Official {
					official = true
				}
			}
			if !official {
				return errors.New("only official plugins can be enabled")
			}
		}
		return manager.SetEnabled(arguments[1], arguments[0] == "enable")
	}
	if len(arguments) == 2 && arguments[0] == "uninstall" {
		return manager.Uninstall(arguments[1])
	}
	if len(arguments) == 1 && arguments[0] == "list" {
		items, err := manager.List()
		if err != nil {
			return err
		}
		return printJSON(items)
	}
	return errors.New("usage: newpress plugin <list|install official-slug|enable slug|disable slug|uninstall slug|maintain-records inspect|quarantines|quarantine|repair|maintain-shared-records inspect|quarantines|quarantine|repair>")
}

func migrateCommand(arguments []string) error {
	if len(arguments) != 1 || arguments[0] != "up" {
		return errors.New("usage: newpress migrate up")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errors.New("NEWPRESS_DATABASE_URL or NEWPRESS_DATABASE_URL_FILE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return err
	}
	if cfg.SeedDemo {
		if err := database.SeedDemo(ctx, pool); err != nil {
			return err
		}
	}
	if err := database.PreparePublicSearch(ctx, pool); err != nil {
		return err
	}
	return database.PrepareSnippetReferences(ctx, pool)
}

func healthcheckCommand(arguments []string) error {
	path := "/health/live"
	if len(arguments) > 1 {
		return errors.New("usage: newpress healthcheck [--ready]")
	}
	if len(arguments) == 1 {
		if arguments[0] != "--ready" {
			return errors.New("usage: newpress healthcheck [--ready]")
		}
		path = "/health/ready"
	}
	baseURL := os.Getenv("NEWPRESS_HEALTHCHECK_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(baseURL + path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage() {
	fmt.Print(`NewPress — high-performance content publishing

Usage:
  newpress run                       (managed serving with online updates; default)
  newpress serve
  newpress version
  newpress licenses                  (full software licenses and third-party notices)
  newpress theme install <package.zip>
  newpress theme install <official-slug> [--version x.y.z] [--preview]
  newpress theme check-updates [--preview]
  newpress theme update <slug> [--version x.y.z] [--preview]
  newpress theme rollback <slug>
  newpress plugin install <official-slug> [--version x.y.z] [--preview]
  newpress plugin check-updates [--preview]
  newpress plugin update <slug> [--version x.y.z] [--preview]
  newpress plugin rollback <slug>
  newpress plugin enable <slug>
  newpress plugin disable <slug>
  newpress plugin uninstall <slug>
  newpress plugin list
  newpress plugin cancel-restored-tasks --confirm-all-workers-stopped
  newpress plugin archive-tasks --before RFC3339 [--scope tasks|fanouts|all] [--plugin slug] [--limit 1-1000]
  newpress plugin maintain-records <inspect|quarantines|quarantine|repair> [options]
  newpress plugin maintain-shared-records <inspect|quarantines|quarantine|repair> [options]
  newpress snippet references <inspect|rebuild> [--limit 1-100] [--after ID --through ID]
  newpress snippet records inspect [--file content.json] [--limit 1-100] [--offset N]
  newpress snippet records <review|export-repair|quarantines|quarantine|repair> [options]
  newpress admin hash-password
  newpress admin list
  newpress admin reset-password --id ID --expected-version VERSION --password-stdin
  newpress admin rename --id ID --expected-version VERSION --username NAME
  newpress audit checkpoint
  newpress audit verify-checkpoint <checkpoint.json>
  newpress backup create <archive.zip>
  newpress backup inspect <archive.zip>
  newpress backup restore <archive.zip> --destination <empty-directory>
  newpress migrate up
  newpress healthcheck [--ready]
`)
}
