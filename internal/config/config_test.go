package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var configEnvironmentKeys = []string{
	"NEWPRESS_ADDR",
	"NEWPRESS_DATABASE_URL",
	"NEWPRESS_DATABASE_URL_FILE",
	"NEWPRESS_DB_MAX_CONNS",
	"NEWPRESS_DB_MIN_CONNS",
	"NEWPRESS_SETUP_TOKEN",
	"NEWPRESS_SETUP_TOKEN_FILE",
	"NEWPRESS_ADMIN_USERNAME",
	"NEWPRESS_ADMIN_PASSWORD_HASH",
	"NEWPRESS_ADMIN_PASSWORD_HASH_FILE",
	"NEWPRESS_ADMIN_EMAIL",
	"NEWPRESS_ADMIN_DISPLAY_NAME",
	"NEWPRESS_ADMIN_LOCALE",
	"NEWPRESS_ADMIN_SESSION_TTL",
	"NEWPRESS_ADMIN_IDLE_TTL",
	"NEWPRESS_DATA_DIR",
	"NEWPRESS_THEMES_DIR",
	"NEWPRESS_PLUGINS_DIR",
	"NEWPRESS_ACTIVE_THEME",
	"NEWPRESS_SITE_TITLE",
	"NEWPRESS_SITE_TAGLINE",
	"NEWPRESS_PUBLIC_URL",
	"NEWPRESS_DEVICE_MOCKUP_URL",
	"NEWPRESS_TRUSTED_PROXY_CIDRS",
	"NEWPRESS_CDN_CLOUDFLARE_CIDRS",
	"NEWPRESS_CDN_CLOUDFLARE_CIDRS_FILE",
	"NEWPRESS_CDN_ESA_CIDRS",
	"NEWPRESS_CDN_ESA_CIDRS_FILE",
	"NEWPRESS_CDN_EDGEONE_CIDRS",
	"NEWPRESS_CDN_EDGEONE_CIDRS_FILE",
	"NEWPRESS_CDN_CLOUDFLARE_ORIGIN_TOKEN",
	"NEWPRESS_CDN_CLOUDFLARE_ORIGIN_TOKEN_FILE",
	"NEWPRESS_CDN_ESA_ORIGIN_TOKEN",
	"NEWPRESS_CDN_ESA_ORIGIN_TOKEN_FILE",
	"NEWPRESS_CDN_EDGEONE_ORIGIN_TOKEN",
	"NEWPRESS_CDN_EDGEONE_ORIGIN_TOKEN_FILE",
	"NEWPRESS_AUTO_MIGRATE",
	"NEWPRESS_SEED_DEMO",
	"NEWPRESS_ANALYTICS_ENABLED",
	"NEWPRESS_COMMUNITY_ENABLED",
	"NEWPRESS_PLUGIN_WEBHOOKS_ENABLED",
	"NEWPRESS_MAX_UPLOAD_BYTES",
	"NEWPRESS_READ_TIMEOUT",
	"NEWPRESS_WRITE_TIMEOUT",
	"NEWPRESS_IDLE_TIMEOUT",
	"NEWPRESS_SHUTDOWN_TIMEOUT",
}

func TestLoadUsesValidatedDefaults(t *testing.T) {
	paths := isolateConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":8080" || cfg.PublicURL != "http://localhost:8080" {
		t.Fatalf("default network configuration = %q %q", cfg.Address, cfg.PublicURL)
	}
	if cfg.SetupToken != "" {
		t.Fatalf("default setup token = %q", cfg.SetupToken)
	}
	if !cfg.AutoMigrate || cfg.SeedDemo || !cfg.AnalyticsEnabled || cfg.CommunityEnabled || cfg.PluginWebhooksEnabled || cfg.MaxUploadBytes != 64<<20 || cfg.DBMaxConns != 16 || cfg.DBMinConns != 1 {
		t.Fatalf("default runtime configuration = migrate:%t seed:%t upload:%d", cfg.AutoMigrate, cfg.SeedDemo, cfg.MaxUploadBytes)
	}
	if cfg.AdminSessionTTL != 12*time.Hour || cfg.AdminIdleTTL != 30*time.Minute ||
		cfg.ReadTimeout != 10*time.Second || cfg.WriteTimeout != 30*time.Second ||
		cfg.IdleTimeout != 90*time.Second || cfg.ShutdownTimeout != 15*time.Second {
		t.Fatalf("default durations = %#v", cfg)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("configuration directory %q was not created: %v", path, err)
		}
	}
}

func TestLoadAcceptsExplicitValidatedValues(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_ADDR", "127.0.0.1:9090")
	t.Setenv("NEWPRESS_PUBLIC_URL", "https://example.test:8443/")
	t.Setenv("NEWPRESS_TRUSTED_PROXY_CIDRS", " 172.30.0.9/24, 2001:db8:10::1/64,172.30.0.0/24 ")
	t.Setenv("NEWPRESS_AUTO_MIGRATE", "false")
	t.Setenv("NEWPRESS_SEED_DEMO", "false")
	t.Setenv("NEWPRESS_ANALYTICS_ENABLED", "false")
	t.Setenv("NEWPRESS_COMMUNITY_ENABLED", "true")
	t.Setenv("NEWPRESS_PLUGIN_WEBHOOKS_ENABLED", "true")
	t.Setenv("NEWPRESS_MAX_UPLOAD_BYTES", "1048576")
	t.Setenv("NEWPRESS_DB_MAX_CONNS", "4")
	t.Setenv("NEWPRESS_DB_MIN_CONNS", "3")
	t.Setenv("NEWPRESS_ADMIN_SESSION_TTL", "2h")
	t.Setenv("NEWPRESS_ADMIN_IDLE_TTL", "15m")
	t.Setenv("NEWPRESS_READ_TIMEOUT", "4s")
	t.Setenv("NEWPRESS_WRITE_TIMEOUT", "12s")
	t.Setenv("NEWPRESS_IDLE_TIMEOUT", "45s")
	t.Setenv("NEWPRESS_SHUTDOWN_TIMEOUT", "8s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != "127.0.0.1:9090" || cfg.PublicURL != "https://example.test:8443" ||
		cfg.AutoMigrate || cfg.SeedDemo || !cfg.CommunityEnabled || !cfg.PluginWebhooksEnabled || cfg.MaxUploadBytes != 1048576 || cfg.DBMaxConns != 4 || cfg.DBMinConns != 3 {
		t.Fatalf("explicit configuration = %#v", cfg)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 || cfg.TrustedProxyCIDRs[0].String() != "172.30.0.0/24" ||
		cfg.TrustedProxyCIDRs[1].String() != "2001:db8:10::/64" {
		t.Fatalf("trusted proxy CIDRs = %#v", cfg.TrustedProxyCIDRs)
	}
	if cfg.AdminSessionTTL != 2*time.Hour || cfg.AdminIdleTTL != 15*time.Minute ||
		cfg.ReadTimeout != 4*time.Second || cfg.WriteTimeout != 12*time.Second ||
		cfg.IdleTimeout != 45*time.Second || cfg.ShutdownTimeout != 8*time.Second {
		t.Fatalf("explicit durations = %#v", cfg)
	}
}

func TestLoadRejectsInvalidScalarEnvironment(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "NEWPRESS_AUTO_MIGRATE", value: "sometimes"},
		{key: "NEWPRESS_SEED_DEMO", value: "enabled"},
		{key: "NEWPRESS_COMMUNITY_ENABLED", value: "sometimes"},
		{key: "NEWPRESS_PLUGIN_WEBHOOKS_ENABLED", value: "sometimes"},
		{key: "NEWPRESS_MAX_UPLOAD_BYTES", value: "0"},
		{key: "NEWPRESS_MAX_UPLOAD_BYTES", value: "64MiB"},
		{key: "NEWPRESS_DB_MAX_CONNS", value: "0"},
		{key: "NEWPRESS_DB_MAX_CONNS", value: "65"},
		{key: "NEWPRESS_DB_MAX_CONNS", value: "many"},
		{key: "NEWPRESS_DB_MIN_CONNS", value: "0"},
		{key: "NEWPRESS_DB_MIN_CONNS", value: "-1"},
		{key: "NEWPRESS_DB_MIN_CONNS", value: "17"},
		{key: "NEWPRESS_DB_MIN_CONNS", value: "many"},
		{key: "NEWPRESS_ADMIN_SESSION_TTL", value: "forever"},
		{key: "NEWPRESS_ADMIN_IDLE_TTL", value: "0s"},
		{key: "NEWPRESS_READ_TIMEOUT", value: "-1s"},
		{key: "NEWPRESS_WRITE_TIMEOUT", value: "thirty"},
		{key: "NEWPRESS_IDLE_TIMEOUT", value: "0"},
		{key: "NEWPRESS_SHUTDOWN_TIMEOUT", value: "-5s"},
		{key: "NEWPRESS_SETUP_TOKEN", value: "short"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			isolateConfigEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load error = %v, want %s validation", err, test.key)
			}
		})
	}
}

func TestLoadRejectsDatabaseMinimumAboveConfiguredMaximum(t *testing.T) {
	paths := isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_DB_MAX_CONNS", "2")
	t.Setenv("NEWPRESS_DB_MIN_CONNS", "3")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NEWPRESS_DB_MIN_CONNS") {
		t.Fatal("database minimum above ceiling was accepted", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid pool limits created configuration directories", path, err)
		}
	}
}

func TestLoadRejectsExplicitEmptyRequiredEnvironment(t *testing.T) {
	for _, key := range []string{
		"NEWPRESS_ADDR",
		"NEWPRESS_ADMIN_USERNAME",
		"NEWPRESS_ADMIN_EMAIL",
		"NEWPRESS_ADMIN_DISPLAY_NAME",
		"NEWPRESS_ADMIN_LOCALE",
		"NEWPRESS_ADMIN_SESSION_TTL",
		"NEWPRESS_ADMIN_IDLE_TTL",
		"NEWPRESS_DATA_DIR",
		"NEWPRESS_THEMES_DIR",
		"NEWPRESS_PLUGINS_DIR",
		"NEWPRESS_ACTIVE_THEME",
		"NEWPRESS_PUBLIC_URL",
		"NEWPRESS_AUTO_MIGRATE",
		"NEWPRESS_SEED_DEMO",
		"NEWPRESS_MAX_UPLOAD_BYTES",
		"NEWPRESS_DB_MIN_CONNS",
		"NEWPRESS_READ_TIMEOUT",
		"NEWPRESS_WRITE_TIMEOUT",
		"NEWPRESS_IDLE_TIMEOUT",
		"NEWPRESS_SHUTDOWN_TIMEOUT",
	} {
		t.Run(key, func(t *testing.T) {
			paths := isolateConfigEnvironment(t)
			t.Setenv(key, "")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load error = %v, want explicit empty %s to fail", err, key)
			}
			for _, path := range paths {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("invalid configuration created %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestLoadAllowsExplicitEmptyOptionalEnvironment(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, key := range []string{
		"NEWPRESS_DATABASE_URL",
		"NEWPRESS_DATABASE_URL_FILE",
		"NEWPRESS_ADMIN_PASSWORD_HASH",
		"NEWPRESS_ADMIN_PASSWORD_HASH_FILE",
		"NEWPRESS_SETUP_TOKEN",
		"NEWPRESS_SETUP_TOKEN_FILE",
		"NEWPRESS_TRUSTED_PROXY_CIDRS",
		"NEWPRESS_SITE_TAGLINE",
		"NEWPRESS_SITE_TITLE",
	} {
		t.Setenv(key, "")
	}
	if _, err := Load(); err != nil {
		t.Fatalf("explicit empty optional configuration: %v", err)
	}
}

func TestLoadDefersSiteBootstrapValidationToSettingsManager(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_SITE_TITLE", strings.Repeat("x", 121))
	if _, err := Load(); err != nil {
		t.Fatalf("obsolete bootstrap values must not block persisted settings: %v", err)
	}
}

func TestLoadDirectEmptySecretSuppressesSecretFile(t *testing.T) {
	isolateConfigEnvironment(t)
	secretPath := filepath.Join(t.TempDir(), "password-hash")
	if err := os.WriteFile(secretPath, []byte("$argon2id$v=19$m=65536,t=3,p=1$abc$def"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_ADMIN_PASSWORD_HASH", "")
	t.Setenv("NEWPRESS_ADMIN_PASSWORD_HASH_FILE", secretPath)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPassHash != "" {
		t.Fatalf("direct empty secret did not suppress file value: %q", cfg.AdminPassHash)
	}
}

func TestLoadRejectsWhitespaceDirectSecret(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_DATABASE_URL", " \t\r\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NEWPRESS_DATABASE_URL") {
		t.Fatalf("whitespace DSN error = %v", err)
	}
}

func TestSetupTokenRequiresPostgreSQLAndNoBootstrapHash(t *testing.T) {
	for _, test := range []struct {
		name         string
		database     string
		passwordHash string
	}{
		{name: "file mode"},
		{name: "environment bootstrap", database: "postgres://localhost/newpress", passwordHash: "$argon2id$v=19$m=65536,t=3,p=1$YWJjZGVmZ2hpamtsbW5vcA$YWJjZGVmZ2hpamtsbW5vcA"},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateConfigEnvironment(t)
			t.Setenv("NEWPRESS_DATABASE_URL", test.database)
			t.Setenv("NEWPRESS_ADMIN_PASSWORD_HASH", test.passwordHash)
			t.Setenv("NEWPRESS_SETUP_TOKEN", strings.Repeat("x", 32))
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "NEWPRESS_SETUP_TOKEN") {
				t.Fatalf("Load error = %v", err)
			}
		})
	}
}

func TestSetupTokenAcceptsPostgreSQLWithoutBootstrapHash(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_DATABASE_URL", "postgres://localhost/newpress")
	t.Setenv("NEWPRESS_SETUP_TOKEN", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SetupToken != strings.Repeat("x", 32) || cfg.AdminPassHash != "" {
		t.Fatalf("setup configuration = token length %d, hash configured %t", len(cfg.SetupToken), cfg.AdminPassHash != "")
	}
}

func TestSetupTokenCanBeLoadedFromSecretFile(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_DATABASE_URL", "postgres://localhost/newpress")
	token := strings.Repeat("random-token-byte", 3)
	secretPath := filepath.Join(t.TempDir(), "setup-token")
	if err := os.WriteFile(secretPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_SETUP_TOKEN_FILE", secretPath)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SetupToken != token {
		t.Fatalf("setup token loaded from secret file incorrectly (length %d)", len(cfg.SetupToken))
	}
}

func TestLoadRejectsUnsafeNetworkConfigurationBeforeCreatingDirectories(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "NEWPRESS_ADDR", value: "localhost"},
		{key: "NEWPRESS_ADDR", value: ":0"},
		{key: "NEWPRESS_PUBLIC_URL", value: "javascript:alert(1)"},
		{key: "NEWPRESS_PUBLIC_URL", value: "https://user:pass@example.test"},
		{key: "NEWPRESS_PUBLIC_URL", value: "https://example.test/newpress"},
		{key: "NEWPRESS_PUBLIC_URL", value: "https://example.test?mode=preview"},
		{key: "NEWPRESS_ACTIVE_THEME", value: "../outside"},
		{key: "NEWPRESS_ACTIVE_THEME", value: "C:\\outside"},
		{key: "NEWPRESS_ACTIVE_THEME", value: "x"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "172.30.0.2"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "172.30.0.0/24,,10.0.0.0/8"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "0.0.0.0/0"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "::/0"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "224.0.0.0/4"},
		{key: "NEWPRESS_TRUSTED_PROXY_CIDRS", value: "ff00::/8"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			paths := isolateConfigEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load error = %v, want %s validation", err, test.key)
			}
			for _, path := range paths {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("invalid configuration created %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestLoadRejectsIdleTTLAboveAbsoluteTTL(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_ADMIN_SESSION_TTL", "30m")
	t.Setenv("NEWPRESS_ADMIN_IDLE_TTL", "31m")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "NEWPRESS_ADMIN_IDLE_TTL") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestLoadRejectsEmptyConfiguredSecretFile(t *testing.T) {
	isolateConfigEnvironment(t)
	secretPath := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(secretPath, []byte(" \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_DATABASE_URL_FILE", secretPath)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "empty secret file") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestLoadRejectsEmptyOrWhitespaceSecretFilePath(t *testing.T) {
	for _, value := range []string{"", " \t\r\n"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			isolateConfigEnvironment(t)
			t.Setenv("NEWPRESS_DATABASE_URL_FILE", value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "NEWPRESS_DATABASE_URL_FILE") {
				t.Fatalf("Load error = %v, want configured empty file path to fail", err)
			}
		})
	}
}

func isolateConfigEnvironment(t *testing.T) []string {
	t.Helper()
	for _, key := range configEnvironmentKeys {
		unsetConfigEnvironment(t, key)
	}
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "data"),
		filepath.Join(root, "themes"),
		filepath.Join(root, "plugins"),
	}
	t.Setenv("NEWPRESS_DATA_DIR", paths[0])
	t.Setenv("NEWPRESS_THEMES_DIR", paths[1])
	t.Setenv("NEWPRESS_PLUGINS_DIR", paths[2])
	return paths
}

func unsetConfigEnvironment(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}
