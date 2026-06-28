package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const schemaMigrationsTableSQL = `
CREATE TABLE IF NOT EXISTS gensoulkyo_schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

type Migration struct {
	Version string
	Path    string
	SQL     string
}

func LoadUpMigrations(dir string) ([]Migration, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("migration directory is required")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	migrations := []Migration{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		version := strings.TrimSuffix(entry.Name(), ".up.sql")
		migrations = append(migrations, Migration{Version: version, Path: path, SQL: string(raw)})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

func ApplyUpMigrations(db *sql.DB, migrations []Migration) ([]string, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if _, err := db.Exec(schemaMigrationsTableSQL); err != nil {
		return nil, err
	}
	applied := []string{}
	for _, migration := range migrations {
		if strings.TrimSpace(migration.Version) == "" {
			return applied, errors.New("migration version is required")
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return applied, fmt.Errorf("migration %s is empty", migration.Version)
		}
		alreadyApplied, err := migrationApplied(db, migration.Version)
		if err != nil {
			return applied, err
		}
		if alreadyApplied {
			continue
		}
		if _, err := db.Exec(migration.SQL); err != nil {
			return applied, fmt.Errorf("apply migration %s: %w", migration.Version, err)
		}
		if _, err := db.Exec("INSERT INTO gensoulkyo_schema_migrations (version) VALUES ($1)", migration.Version); err != nil {
			return applied, fmt.Errorf("record migration %s: %w", migration.Version, err)
		}
		applied = append(applied, migration.Version)
	}
	return applied, nil
}

func migrationApplied(db *sql.DB, version string) (bool, error) {
	row := db.QueryRow("SELECT 1 FROM gensoulkyo_schema_migrations WHERE version = $1", version)
	var marker int
	if err := row.Scan(&marker); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
