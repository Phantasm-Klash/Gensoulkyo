package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadAndApplyUpMigrations(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "002_second.up.sql", "CREATE TABLE second_table (id TEXT);")
	writeMigration(t, dir, "001_first.up.sql", "CREATE TABLE first_table (id TEXT);")
	writeMigration(t, dir, "001_first.down.sql", "DROP TABLE first_table;")

	migrations, err := LoadUpMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 2 || migrations[0].Version != "001_first" || migrations[1].Version != "002_second" {
		t.Fatalf("migrations should load in version order: %+v", migrations)
	}

	driverName := registerMigrationCaptureDriver(t, map[string]bool{"001_first": true})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	applied, err := ApplyUpMigrations(db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "002_second" {
		t.Fatalf("expected only pending migration to apply, got %+v", applied)
	}
	calls := migrationCaptureCalls()
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS gensoulkyo_schema_migrations") ||
		!strings.Contains(joined, "CREATE TABLE second_table") ||
		strings.Contains(joined, "CREATE TABLE first_table") ||
		!strings.Contains(joined, "INSERT INTO gensoulkyo_schema_migrations") {
		t.Fatalf("migration execution calls mismatch:\n%s", joined)
	}
}

func TestOpenDatabaseRequiresDriverAndURL(t *testing.T) {
	if db, err := OpenDatabase(DatabaseConfig{}); err != nil || db != nil {
		t.Fatalf("empty database config should be disabled: db=%v err=%v", db, err)
	}
	normalized := NormalizeDatabaseConfig(DatabaseConfig{URL: "postgres://example"})
	if normalized.Driver != DefaultPostgresDriver {
		t.Fatalf("expected default driver, got %+v", normalized)
	}
	if _, err := OpenDatabase(DatabaseConfig{Driver: "postgres"}); err == nil {
		t.Fatalf("expected url requirement")
	}
}

func writeMigration(t *testing.T, dir string, name string, sqlText string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sqlText), 0o644); err != nil {
		t.Fatal(err)
	}
}

var migrationCaptureState = struct {
	sync.Mutex
	nextID  int
	calls   []string
	applied map[string]bool
}{}

func registerMigrationCaptureDriver(t *testing.T, applied map[string]bool) string {
	t.Helper()
	migrationCaptureState.Lock()
	defer migrationCaptureState.Unlock()
	migrationCaptureState.nextID++
	migrationCaptureState.calls = nil
	migrationCaptureState.applied = applied
	name := "migration_capture_driver_" + string(rune('a'+migrationCaptureState.nextID))
	sql.Register(name, migrationCaptureDriver{})
	return name
}

func migrationCaptureCalls() []string {
	migrationCaptureState.Lock()
	defer migrationCaptureState.Unlock()
	calls := make([]string, len(migrationCaptureState.calls))
	copy(calls, migrationCaptureState.calls)
	return calls
}

type migrationCaptureDriver struct{}

func (migrationCaptureDriver) Open(name string) (driver.Conn, error) {
	return migrationCaptureConn{}, nil
}

type migrationCaptureConn struct{}

func (migrationCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return migrationCaptureStmt{query: query}, nil
}

func (migrationCaptureConn) Close() error {
	return nil
}

func (migrationCaptureConn) Begin() (driver.Tx, error) {
	return migrationCaptureTx{}, nil
}

func (migrationCaptureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	migrationCaptureState.Lock()
	migrationCaptureState.calls = append(migrationCaptureState.calls, query)
	if strings.Contains(query, "INSERT INTO gensoulkyo_schema_migrations") && len(args) > 0 {
		if migrationCaptureState.applied == nil {
			migrationCaptureState.applied = map[string]bool{}
		}
		migrationCaptureState.applied[toString(args[0].Value)] = true
	}
	migrationCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (migrationCaptureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	version := ""
	if len(args) > 0 {
		version = toString(args[0].Value)
	}
	migrationCaptureState.Lock()
	migrationCaptureState.calls = append(migrationCaptureState.calls, query)
	applied := migrationCaptureState.applied[version]
	migrationCaptureState.Unlock()
	return &migrationCaptureRows{hasRow: applied}, nil
}

type migrationCaptureStmt struct {
	query string
}

func (stmt migrationCaptureStmt) Close() error {
	return nil
}

func (stmt migrationCaptureStmt) NumInput() int {
	return -1
}

func (stmt migrationCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return migrationCaptureConn{}.ExecContext(context.Background(), stmt.query, named)
}

func (stmt migrationCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return migrationCaptureConn{}.QueryContext(context.Background(), stmt.query, named)
}

type migrationCaptureTx struct{}

func (migrationCaptureTx) Commit() error {
	return nil
}

func (migrationCaptureTx) Rollback() error {
	return nil
}

type migrationCaptureRows struct {
	hasRow bool
	read   bool
}

func (migrationCaptureRows) Columns() []string {
	return []string{"exists"}
}

func (rows *migrationCaptureRows) Close() error {
	return nil
}

func (rows *migrationCaptureRows) Next(dest []driver.Value) error {
	if rows.read || !rows.hasRow {
		return io.EOF
	}
	rows.read = true
	dest[0] = int64(1)
	return nil
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
