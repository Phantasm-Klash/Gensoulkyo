package storage

import (
	"database/sql"
	"errors"
	"os"
)

const DefaultPostgresDriver = "pgx"

type DatabaseConfig struct {
	Driver string
	URL    string
}

func DatabaseConfigFromEnv() DatabaseConfig {
	return NormalizeDatabaseConfig(DatabaseConfig{
		Driver: os.Getenv("GENSOULKYO_DATABASE_DRIVER"),
		URL:    os.Getenv("GENSOULKYO_DATABASE_URL"),
	})
}

func (config DatabaseConfig) Enabled() bool {
	return config.Driver != "" || config.URL != ""
}

func NormalizeDatabaseConfig(config DatabaseConfig) DatabaseConfig {
	if config.Driver == "" && config.URL != "" {
		config.Driver = DefaultPostgresDriver
	}
	return config
}

func OpenDatabase(config DatabaseConfig) (*sql.DB, error) {
	config = NormalizeDatabaseConfig(config)
	if !config.Enabled() {
		return nil, nil
	}
	if config.Driver == "" {
		return nil, errors.New("database driver is required when database url is set")
	}
	if config.URL == "" {
		return nil, errors.New("database url is required when database driver is set")
	}
	return sql.Open(config.Driver, config.URL)
}
