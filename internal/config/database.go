package config

import "fmt"

type DatabaseConfig struct {
	DatabaseURL string
	DBMinConns  int32
	DBMaxConns  int32
}

// LoadDatabase reads only the connection settings, without creating site
// directories or requiring unrelated HTTP, mail, theme or account settings.
// It shares secret-file precedence and pool validation with normal startup.
func LoadDatabase() (DatabaseConfig, error) {
	url, err := envOrFile("NEWPRESS_DATABASE_URL", "NEWPRESS_DATABASE_URL_FILE")
	if err != nil {
		return DatabaseConfig{}, err
	}
	min, max, err := loadDatabasePoolLimits()
	if err != nil {
		return DatabaseConfig{}, err
	}
	return DatabaseConfig{DatabaseURL: url, DBMinConns: min, DBMaxConns: max}, nil
}

func loadDatabasePoolLimits() (int32, int32, error) {
	max, err := envInt64("NEWPRESS_DB_MAX_CONNS", 16)
	if err != nil {
		return 0, 0, err
	}
	if max < 1 || max > 64 {
		return 0, 0, fmt.Errorf("NEWPRESS_DB_MAX_CONNS must be between 1 and 64")
	}
	min, err := envInt64("NEWPRESS_DB_MIN_CONNS", 1)
	if err != nil {
		return 0, 0, err
	}
	if min < 1 || min > max {
		return 0, 0, fmt.Errorf("NEWPRESS_DB_MIN_CONNS must be between 1 and NEWPRESS_DB_MAX_CONNS")
	}
	return int32(min), int32(max), nil
}
