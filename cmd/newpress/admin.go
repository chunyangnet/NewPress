package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
)

const adminUsage = `usage: newpress admin <list|reset-password|rename|hash-password>
  list
  reset-password --id ID --expected-version VERSION --password-stdin
  rename --id ID --expected-version VERSION --username NAME
Passwords are read from a pipe, never command arguments. File mutations require the application to be stopped.
For hidden password input and automatic service handling, use bash scripts/deploy.sh admin.`

// These commands rely on access to the local service identity or database
// credentials. They are deliberately not exposed through an HTTP endpoint.
func adminCommand(arguments []string) error {
	if len(arguments) == 1 && arguments[0] == "hash-password" {
		password, err := readAdminPasswordPipe()
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		fmt.Println(hash)
		return nil
	}
	if len(arguments) == 0 {
		return errors.New(adminUsage)
	}
	action := arguments[0]
	flags := flag.NewFlagSet("admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.Int64("id", 0, "administrator ID")
	version := flags.Int64("expected-version", 0, "version from list")
	username := flags.String("username", "", "new login name")
	stdin := flags.Bool("password-stdin", false, "read password from a pipe")
	if flags.Parse(arguments[1:]) != nil || flags.NArg() != 0 {
		return errors.New(adminUsage)
	}
	allowed := map[string]bool{}
	switch action {
	case "list":
	case "reset-password":
		allowed = map[string]bool{"id": true, "expected-version": true, "password-stdin": true}
		if *id <= 0 || *version <= 0 || !*stdin {
			return errors.New(adminUsage)
		}
	case "rename":
		allowed = map[string]bool{"id": true, "expected-version": true, "username": true}
		if *id <= 0 || *version <= 0 || *username == "" {
			return errors.New(adminUsage)
		}
	default:
		return errors.New(adminUsage)
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) { invalid = invalid || !allowed[f.Name] })
	if invalid {
		return errors.New(adminUsage)
	}
	password := ""
	if action == "reset-password" {
		var err error
		password, err = readAdminPasswordPipe()
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return errors.New("cannot load service configuration; check the environment and secret file permissions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return withAdminStore(ctx, cfg, action != "list", func(store auth.UserStore) error {
		if action == "list" {
			items, err := listAdministrators(ctx, store)
			if err != nil {
				return errors.New("cannot read administrator accounts")
			}
			return printJSON(items)
		}
		user, err := changeAdministrator(ctx, store, action, *id, *version, *username, password)
		if err != nil {
			return err
		}
		return printJSON(adminAccount(user))
	})
}

func readAdminPasswordPipe() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("password input requires a pipe or redirected file; use the deployment menu for hidden interactive input")
	}
	return readAdminPassword(os.Stdin)
}

func readAdminPassword(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 1027))
	if err != nil || len(data) > 1026 {
		return "", errors.New("password input is invalid or too long")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 || strings.IndexFunc(password, unicode.IsControl) >= 0 {
		return "", errors.New("password must contain at least 12 characters, at most 1024 UTF-8 bytes, and no control characters")
	}
	return password, nil
}

type adminAccountView struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Status   string `json:"status"`
	Version  int64  `json:"version"`
}

func adminAccount(user auth.User) adminAccountView {
	return adminAccountView{user.ID, user.Username, user.Status, user.Version}
}

func isAdministrator(user auth.User) bool {
	for _, role := range user.Roles {
		if role.Slug == "administrator" {
			for _, capability := range role.Capabilities {
				if capability == auth.CapabilityAll {
					return true
				}
			}
		}
	}
	return false
}

func listAdministrators(ctx context.Context, store auth.UserStore) ([]adminAccountView, error) {
	items := []adminAccountView{}
	for offset := 0; ; {
		page, err := store.ListUsers(ctx, auth.UserQuery{Role: "administrator", Limit: 100, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, user := range page.Items {
			if isAdministrator(user) {
				items = append(items, adminAccount(user))
			}
		}
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return items, nil
		}
	}
}

func changeAdministrator(ctx context.Context, store auth.UserStore, action string, id, version int64, username, password string) (auth.User, error) {
	user, err := store.GetUser(ctx, id)
	if err != nil || !isAdministrator(user) || user.Status != auth.UserStatusActive {
		return auth.User{}, errors.New("target must be an existing active administrator; no roles or account status were changed")
	}
	if version <= 0 || user.Version != version {
		return auth.User{}, errors.New("account changed; list accounts again before retrying")
	}
	mutation := auth.UserMutation{ExpectedVersion: version, Actor: "local-admin-cli"}
	switch action {
	case "reset-password":
		validated, validationErr := readAdminPassword(strings.NewReader(password))
		if validationErr != nil || validated != password {
			return auth.User{}, errors.New("password does not meet the command line password requirements")
		}
		hash, hashErr := auth.HashPassword(password)
		if hashErr != nil {
			return auth.User{}, errors.New("could not securely hash the password")
		}
		user, err = store.UpdateUserPassword(ctx, id, mutation, hash)
	case "rename":
		roles := make([]string, 0, len(user.Roles))
		for _, role := range user.Roles {
			roles = append(roles, role.Slug)
		}
		user, err = store.UpdateUser(ctx, id, mutation, auth.UserInput{
			Username: username, Email: user.Email, DisplayName: user.DisplayName,
			Status: user.Status, Locale: user.Locale, RoleSlugs: roles,
		})
	default:
		return auth.User{}, errors.New(adminUsage)
	}
	if err != nil {
		// Do not log storage errors: drivers may include connection secrets or
		// query parameters. The stores retain their normal audit transactions.
		return auth.User{}, errors.New("account update failed; check name conflicts, account version and storage health before retrying")
	}
	return user, nil
}

func withAdminStore(ctx context.Context, cfg config.Config, mutate bool, action func(auth.UserStore) error) error {
	if cfg.DatabaseURL != "" {
		pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, 1, 2)
		if err != nil {
			return errors.New("cannot connect to the configured database")
		}
		defer pool.Close()
		return action(database.NewUserRepository(pool))
	}
	path := filepath.Join(cfg.DataDir, "users.json")
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return errors.New("cannot open existing account storage; complete installation first")
	}
	file.Close()
	var ledger *audit.Ledger
	if mutate {
		locks, err := dirlock.Acquire(cfg.DataDir)
		if err != nil {
			return errors.New("account storage is busy; stop the application before changing accounts")
		}
		defer locks.Close()
		ledger, err = audit.Open(filepath.Join(cfg.DataDir, audit.FileName))
		if err != nil {
			return errors.New("cannot open the account audit ledger")
		}
		facts := map[string]string{audit.FactUsers: path, audit.FactComments: filepath.Join(cfg.DataDir, "comments.json"),
			audit.FactNavigation: filepath.Join(cfg.DataDir, "navigation.json"), audit.FactCommunity: filepath.Join(cfg.DataDir, "community.json")}
		if ledger.ResolvePaths(facts) != nil || ledger.EnsureBaselinePath(audit.FactUsers, path) != nil {
			return errors.New("account audit verification failed; restore a verified backup before retrying")
		}
	}
	store, err := auth.NewFileUserStore(path, ledger)
	if err != nil {
		return errors.New("cannot read account storage")
	}
	return action(store)
}
