package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/maildelivery"
	"newpress.local/newpress/internal/packagefs"
)

type Config struct {
	SMTP                  maildelivery.Config
	Address               string
	DatabaseURL           string
	DBMaxConns            int32
	DBMinConns            int32
	SetupToken            string
	AdminUsername         string
	AdminPassHash         string
	AdminEmail            string
	AdminDisplayName      string
	AdminLocale           string
	AdminSessionTTL       time.Duration
	AdminIdleTTL          time.Duration
	DataDir               string
	ThemesDir             string
	PluginsDir            string
	PluginWebhooksEnabled bool
	ActiveTheme           string
	SiteTitle             string
	SiteTagline           string
	PublicURL             string
	DeviceMockupURL       string
	TrustedProxyCIDRs     []netip.Prefix
	CDNProxies            []CDNProxy
	AutoMigrate           bool
	SeedDemo              bool
	AnalyticsEnabled      bool
	CommunityEnabled      bool
	MaxUploadBytes        int64
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	ShutdownTimeout       time.Duration
}

func Load() (Config, error) {
	dataDir := env("NEWPRESS_DATA_DIR", "./var")
	databaseURL, err := envOrFile("NEWPRESS_DATABASE_URL", "NEWPRESS_DATABASE_URL_FILE")
	if err != nil {
		return Config{}, err
	}
	adminPassHash, err := envOrFile("NEWPRESS_ADMIN_PASSWORD_HASH", "NEWPRESS_ADMIN_PASSWORD_HASH_FILE")
	if err != nil {
		return Config{}, err
	}
	setupToken, err := envOrFile("NEWPRESS_SETUP_TOKEN", "NEWPRESS_SETUP_TOKEN_FILE")
	if err != nil {
		return Config{}, err
	}
	smtpPassword, err := envOrFile("NEWPRESS_SMTP_PASSWORD", "NEWPRESS_SMTP_PASSWORD_FILE")
	if err != nil {
		return Config{}, err
	}
	autoMigrate, err := envBool("NEWPRESS_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	seedDemo, err := envBool("NEWPRESS_SEED_DEMO", false)
	if err != nil {
		return Config{}, err
	}
	analyticsEnabled, err := envBool("NEWPRESS_ANALYTICS_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	communityEnabled, err := envBool("NEWPRESS_COMMUNITY_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	pluginWebhooksEnabled, err := envBool("NEWPRESS_PLUGIN_WEBHOOKS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	maxUploadBytes, err := envInt64("NEWPRESS_MAX_UPLOAD_BYTES", 64<<20)
	if err != nil {
		return Config{}, err
	}
	dbMinConns, dbMaxConns, err := loadDatabasePoolLimits()
	if err != nil {
		return Config{}, err
	}
	adminSessionTTL, err := envDuration("NEWPRESS_ADMIN_SESSION_TTL", 12*time.Hour)
	if err != nil {
		return Config{}, err
	}
	adminIdleTTL, err := envDuration("NEWPRESS_ADMIN_IDLE_TTL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := envDuration("NEWPRESS_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := envDuration("NEWPRESS_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := envDuration("NEWPRESS_IDLE_TIMEOUT", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("NEWPRESS_SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	trustedProxyCIDRs, err := parseTrustedProxyCIDRs(os.Getenv("NEWPRESS_TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return Config{}, fmt.Errorf("NEWPRESS_TRUSTED_PROXY_CIDRS: %w", err)
	}
	cdnProxies, err := loadCDNProxies()
	if err != nil {
		return Config{}, err
	}
	if err := validateCDNOverlap(cdnProxies, trustedProxyCIDRs); err != nil {
		return Config{}, err
	}
	cfg := Config{
		SMTP:                  maildelivery.Config{Address: strings.TrimSpace(os.Getenv("NEWPRESS_SMTP_ADDR")), Username: os.Getenv("NEWPRESS_SMTP_USERNAME"), Password: smtpPassword, From: strings.TrimSpace(os.Getenv("NEWPRESS_SMTP_FROM")), Mode: env("NEWPRESS_SMTP_MODE", "starttls")},
		Address:               env("NEWPRESS_ADDR", ":8080"),
		DeviceMockupURL:       env("NEWPRESS_DEVICE_MOCKUP_URL", "http://127.0.0.1:4173"),
		DatabaseURL:           databaseURL,
		DBMaxConns:            int32(dbMaxConns),
		DBMinConns:            int32(dbMinConns),
		SetupToken:            setupToken,
		AdminUsername:         env("NEWPRESS_ADMIN_USERNAME", "admin"),
		AdminPassHash:         adminPassHash,
		AdminEmail:            env("NEWPRESS_ADMIN_EMAIL", "admin@localhost.invalid"),
		AdminDisplayName:      env("NEWPRESS_ADMIN_DISPLAY_NAME", "站点管理员"),
		AdminLocale:           env("NEWPRESS_ADMIN_LOCALE", "zh-CN"),
		AdminSessionTTL:       adminSessionTTL,
		AdminIdleTTL:          adminIdleTTL,
		DataDir:               dataDir,
		ThemesDir:             env("NEWPRESS_THEMES_DIR", "./themes"),
		PluginsDir:            env("NEWPRESS_PLUGINS_DIR", "./plugins"),
		ActiveTheme:           env("NEWPRESS_ACTIVE_THEME", "chunnel"),
		SiteTitle:             env("NEWPRESS_SITE_TITLE", "NewPress"),
		SiteTagline:           env("NEWPRESS_SITE_TAGLINE", "文章、资源与社区"),
		PublicURL:             env("NEWPRESS_PUBLIC_URL", "http://localhost:8080"),
		TrustedProxyCIDRs:     trustedProxyCIDRs,
		CDNProxies:            cdnProxies,
		AutoMigrate:           autoMigrate,
		SeedDemo:              seedDemo,
		AnalyticsEnabled:      analyticsEnabled,
		CommunityEnabled:      communityEnabled,
		PluginWebhooksEnabled: pluginWebhooksEnabled,
		MaxUploadBytes:        maxUploadBytes,
		ReadTimeout:           readTimeout,
		WriteTimeout:          writeTimeout,
		IdleTimeout:           idleTimeout,
		ShutdownTimeout:       shutdownTimeout,
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	for _, dir := range []string{cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir} {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Config{}, fmt.Errorf("resolve %q: %w", dir, err)
		}
		if err := packagefs.MkdirAllNoSymlink(abs, 0o750); err != nil {
			return Config{}, fmt.Errorf("create %q: %w", abs, err)
		}
	}

	return cfg, nil
}

func parseTrustedProxyCIDRs(value string) ([]netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	const maximumTrustedProxyCIDRs = 64
	parts := strings.Split(value, ",")
	if len(parts) > maximumTrustedProxyCIDRs {
		return nil, fmt.Errorf("must contain at most %d comma-separated CIDRs", maximumTrustedProxyCIDRs)
	}
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("must not contain an empty CIDR")
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", part)
		}
		if prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("IPv4-mapped IPv6 CIDR %q is not allowed", part)
		}
		prefix = prefix.Masked()
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("trust-all CIDR %q is not allowed", part)
		}
		address := prefix.Addr()
		if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalMulticast() {
			return nil, fmt.Errorf("non-unicast CIDR %q is not allowed", part)
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func (cfg *Config) validate() error {
	if err := cfg.SMTP.Validate(); err != nil {
		return err
	}
	cfg.Address = strings.TrimSpace(cfg.Address)
	if err := validateAddress(cfg.Address); err != nil {
		return fmt.Errorf("NEWPRESS_ADDR: %w", err)
	}
	if cfg.SetupToken != "" {
		if len(cfg.SetupToken) < 32 || len(cfg.SetupToken) > 512 || strings.TrimSpace(cfg.SetupToken) != cfg.SetupToken {
			return errors.New("NEWPRESS_SETUP_TOKEN must be 32-512 bytes with no surrounding whitespace")
		}
		if strings.TrimSpace(cfg.DatabaseURL) == "" {
			return errors.New("NEWPRESS_SETUP_TOKEN requires PostgreSQL mode")
		}
		if cfg.AdminPassHash != "" {
			return errors.New("NEWPRESS_SETUP_TOKEN cannot be used with NEWPRESS_ADMIN_PASSWORD_HASH")
		}
	}
	publicURL, err := normalizePublicURL(cfg.PublicURL)
	if err != nil {
		return fmt.Errorf("NEWPRESS_PUBLIC_URL: %w", err)
	}
	cfg.PublicURL = publicURL
	if cfg.DeviceMockupURL != "" {
		worker, err := url.Parse(cfg.DeviceMockupURL)
		if err != nil || worker.Scheme != "http" || worker.User != nil || worker.RawQuery != "" || worker.Fragment != "" || (worker.Path != "" && worker.Path != "/") || (worker.Hostname() != "127.0.0.1" && worker.Hostname() != "localhost") {
			return errors.New("NEWPRESS_DEVICE_MOCKUP_URL must be a loopback HTTP origin")
		}
		port := worker.Port()
		if port == "" {
			port = "80"
		}
		if err := validateAddress(net.JoinHostPort(worker.Hostname(), port)); err != nil {
			return fmt.Errorf("NEWPRESS_DEVICE_MOCKUP_URL: %w", err)
		}
		cfg.DeviceMockupURL = strings.TrimRight(cfg.DeviceMockupURL, "/")
	}
	cfg.ActiveTheme = strings.TrimSpace(cfg.ActiveTheme)
	if err := packagefs.ValidateSlug(cfg.ActiveTheme); err != nil {
		return fmt.Errorf("NEWPRESS_ACTIVE_THEME: %w", err)
	}
	if cfg.AdminIdleTTL > cfg.AdminSessionTTL {
		return errors.New("NEWPRESS_ADMIN_IDLE_TTL cannot exceed NEWPRESS_ADMIN_SESSION_TTL")
	}
	for _, value := range []struct {
		key     string
		text    *string
		maximum int
	}{
		{key: "NEWPRESS_ADMIN_USERNAME", text: &cfg.AdminUsername, maximum: 80},
		{key: "NEWPRESS_ADMIN_EMAIL", text: &cfg.AdminEmail, maximum: 320},
		{key: "NEWPRESS_ADMIN_DISPLAY_NAME", text: &cfg.AdminDisplayName, maximum: 160},
		{key: "NEWPRESS_ADMIN_LOCALE", text: &cfg.AdminLocale, maximum: 35},
	} {
		*value.text = strings.TrimSpace(*value.text)
		if *value.text == "" {
			return fmt.Errorf("%s must not be empty", value.key)
		}
		if len([]rune(*value.text)) > value.maximum {
			return fmt.Errorf("%s must not exceed %d characters", value.key, value.maximum)
		}
	}
	for _, directory := range []struct {
		key   string
		value *string
	}{
		{key: "NEWPRESS_DATA_DIR", value: &cfg.DataDir},
		{key: "NEWPRESS_THEMES_DIR", value: &cfg.ThemesDir},
		{key: "NEWPRESS_PLUGINS_DIR", value: &cfg.PluginsDir},
	} {
		*directory.value = strings.TrimSpace(*directory.value)
		if *directory.value == "" {
			return fmt.Errorf("%s must not be empty", directory.key)
		}
	}
	return nil
}

func validateAddress(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("must use host:port form")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return errors.New("port must be an integer from 1 to 65535")
	}
	return nil
}

func normalizePublicURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("must be an absolute HTTP(S) URL with a host")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", errors.New("scheme must be http or https")
	}
	if parsed.User != nil {
		return "", errors.New("credentials are not allowed")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must not include a query or fragment")
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return "", errors.New("port must be an integer from 1 to 65535")
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme == "http" && !strings.EqualFold(parsed.Hostname(), "localhost") {
		address, err := netip.ParseAddr(parsed.Hostname())
		if err != nil || address.Zone() != "" || !address.IsLoopback() {
			return "", errors.New("non-loopback public URLs require HTTPS")
		}
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func envOrFile(key, fileKey string) (string, error) {
	if value, configured := os.LookupEnv(key); configured {
		if value == "" {
			// An explicitly empty direct value is an intentional empty optional
			// setting and must not silently fall through to a stale secret file.
			return "", nil
		}
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s must not contain only whitespace", key)
		}
		return value, nil
	}
	path, configured := os.LookupEnv(fileKey)
	if !configured {
		return "", nil
	}
	if path == "" {
		return "", fmt.Errorf("%s must not be empty", fileKey)
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s must not contain only whitespace", fileKey)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", fileKey, err)
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return "", fmt.Errorf("%s points to an empty secret file", fileKey)
	}
	return trimmed, nil
}

func env(key, fallback string) string {
	if value, configured := os.LookupEnv(key); configured {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value, configured := os.LookupEnv(key)
	if !configured {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	value, configured := os.LookupEnv(key)
	if !configured {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive base-10 integer", key)
	}
	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value, configured := os.LookupEnv(key)
	if !configured {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", key)
	}
	return parsed, nil
}
