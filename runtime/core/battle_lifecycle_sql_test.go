package core

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
	"time"
)

func TestBattleLifecycleAuditMigrationMatchesRepositoryTables(t *testing.T) {
	upSQL := readMigrationFile(t, "001_business_security_audit.up.sql")
	downSQL := readMigrationFile(t, "001_business_security_audit.down.sql")

	assertMigrationCoversInsertColumns(t, upSQL, insertBattleResultAuditSQL, "battle_result_audits")
	assertMigrationCoversInsertColumns(t, upSQL, insertReplayAuditSQL, "replay_audits")
	assertDownMigrationDropsTable(t, downSQL, "battle_result_audits")
	assertDownMigrationDropsTable(t, downSQL, "replay_audits")
}

func TestSQLBattleLifecycleAuditRepositoryRecordsAllocationAndTicket(t *testing.T) {
	driverName := registerBattleAuditCaptureDriver(t)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo, err := NewSQLBattleLifecycleAuditRepository(db, withSQLBattleLifecycleAuditStatements(
		"INSERT INTO match_allocation_audits VALUES ($1)",
		"INSERT INTO battle_ticket_audits VALUES ($1)",
		"INSERT INTO battle_result_audits VALUES ($1)",
		"INSERT INTO replay_audits VALUES ($1)",
	))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	if err := repo.RecordMatchAllocationAudit(BattleAllocationAuditRecord{
		MatchID:         "match-a",
		ModeID:          "pvp_duel",
		BattleServerID:  "battle-a",
		Endpoint:        "127.0.0.1:7901",
		Region:          "local",
		ProtocolVersion: "1",
		RulesetVersion:  RulesetVersion,
		ModeConfigHash:  "sha256:mode",
		ServerSeedHash:  "sha256:seed",
		PlayerCount:     2,
		AllocationJSON:  `{"match_id":"match-a"}`,
		Status:          "allocated",
		CreatedAt:       now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordBattleTicketAudit(BattleTicketAuditRecord{
		TicketID:         "ticket-a",
		MatchID:          "match-a",
		UserID:           "user-a",
		PlayerID:         "player-a",
		BattleServerID:   "battle-a",
		Endpoint:         "127.0.0.1:7901",
		KeyID:            "dev-ed25519-0",
		RulesetVersion:   RulesetVersion,
		ProtocolVersion:  "1",
		DeckSnapshotHash: "sha256:deck",
		ModeConfigHash:   "sha256:mode",
		Nonce:            "nonce-a",
		SignaturePrefix:  "0123456789abcdef",
		Status:           "issued",
		IssuedAt:         now,
		ExpiresAt:        now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordBattleResultAudit(BattleResultAuditRecord{
		MatchID:             "match-a",
		ModeID:              "pvp_duel",
		BattleServerID:      "battle-a",
		ResultHash:          "sha256:result",
		ReplayID:            "battle-replay-a",
		KeyID:               "battle-a",
		PlayerIDs:           []string{"player-a", "player-b"},
		SettlementKey:       "battle-result:match-a",
		VerifiedAt:          now,
		SettledAt:           now,
		ServerAuthoritative: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordReplayAudit(ReplayAuditRecord{
		ReplayID:            "replay-a",
		MatchID:             "match-a",
		UserID:              "user-a",
		ModeID:              "pvp_duel",
		RulesetVersion:      RulesetVersion,
		ModeRulesetVersion:  "pvp-duel-s0",
		StateHash:           "state-a",
		InputCount:          2,
		EventCount:          3,
		SettlementKey:       "match-a:user-a",
		SettledAt:           now,
		ServerAuthoritative: true,
	}); err != nil {
		t.Fatal(err)
	}

	calls := battleAuditCaptureCalls()
	if len(calls) != 4 {
		t.Fatalf("expected four inserts, got %+v", calls)
	}
	if !strings.Contains(calls[0].query, "INSERT INTO match_allocation_audits") || len(calls[0].args) != 12 {
		t.Fatalf("allocation insert invalid: %+v", calls[0])
	}
	if calls[0].args[0] != "match-a" || calls[0].args[2] != "battle-a" || calls[0].args[10] != `{"match_id":"match-a"}` {
		t.Fatalf("allocation args invalid: %+v", calls[0].args)
	}
	if !strings.Contains(calls[1].query, "INSERT INTO battle_ticket_audits") || len(calls[1].args) != 16 {
		t.Fatalf("ticket insert invalid: %+v", calls[1])
	}
	if calls[1].args[0] != "ticket-a" || calls[1].args[3] != "user-a" || calls[1].args[14] != "0123456789abcdef" {
		t.Fatalf("ticket args invalid: %+v", calls[1].args)
	}
	if !strings.Contains(calls[2].query, "INSERT INTO battle_result_audits") || len(calls[2].args) != 11 {
		t.Fatalf("result insert invalid: %+v", calls[2])
	}
	if calls[2].args[0] != "match-a" || calls[2].args[6] != `["player-a","player-b"]` || calls[2].args[10] != true {
		t.Fatalf("result args invalid: %+v", calls[2].args)
	}
	if !strings.Contains(calls[3].query, "INSERT INTO replay_audits") || len(calls[3].args) != 12 {
		t.Fatalf("replay insert invalid: %+v", calls[3])
	}
	if calls[3].args[0] != "replay-a" || calls[3].args[2] != "user-a" || calls[3].args[11] != true {
		t.Fatalf("replay args invalid: %+v", calls[3].args)
	}
}

func TestSQLBattleLifecycleAuditRepositoryRejectsNilDB(t *testing.T) {
	if _, err := NewSQLBattleLifecycleAuditRepository(nil); err == nil {
		t.Fatalf("expected nil db rejection")
	}
	var repo *SQLBattleLifecycleAuditRepository
	if err := repo.RecordMatchAllocationAudit(BattleAllocationAuditRecord{}); err == nil {
		t.Fatalf("expected nil repo rejection")
	}
}

func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "migrations", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertMigrationCoversInsertColumns(t *testing.T, migrationSQL string, insertSQL string, table string) {
	t.Helper()
	insertColumns := extractSQLColumnList(t, insertSQL, "INSERT INTO "+table)
	createColumns := extractCreateTableColumns(t, migrationSQL, table)
	for _, column := range insertColumns {
		if !createColumns[column] {
			t.Fatalf("migration table %s missing repository column %s; columns=%v", table, column, keysForTest(createColumns))
		}
	}
}

func assertDownMigrationDropsTable(t *testing.T, migrationSQL string, table string) {
	t.Helper()
	if !strings.Contains(migrationSQL, "DROP TABLE IF EXISTS "+table+";") {
		t.Fatalf("down migration does not drop %s", table)
	}
}

func extractSQLColumnList(t *testing.T, sqlText string, prefix string) []string {
	t.Helper()
	start := strings.Index(sqlText, prefix)
	if start < 0 {
		t.Fatalf("SQL missing prefix %q", prefix)
	}
	open := strings.Index(sqlText[start:], "(")
	if open < 0 {
		t.Fatalf("SQL prefix %q missing column list", prefix)
	}
	open += start
	close := strings.Index(sqlText[open:], ")")
	if close < 0 {
		t.Fatalf("SQL prefix %q missing column list close", prefix)
	}
	close += open
	return splitSQLColumns(sqlText[open+1 : close])
}

func extractCreateTableColumns(t *testing.T, migrationSQL string, table string) map[string]bool {
	t.Helper()
	prefix := "CREATE TABLE IF NOT EXISTS " + table
	start := strings.Index(migrationSQL, prefix)
	if start < 0 {
		t.Fatalf("migration missing table %s", table)
	}
	open := strings.Index(migrationSQL[start:], "(")
	if open < 0 {
		t.Fatalf("migration table %s missing column list", table)
	}
	open += start
	close := strings.Index(migrationSQL[open:], "\n);")
	if close < 0 {
		t.Fatalf("migration table %s missing create-table close", table)
	}
	close += open
	columns := map[string]bool{}
	for _, line := range strings.Split(migrationSQL[open+1:close], "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, ","))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		column := strings.Trim(fields[0], `"`)
		if strings.ToLower(column) != column {
			continue
		}
		columns[column] = true
	}
	return columns
}

func splitSQLColumns(columnBlock string) []string {
	columns := []string{}
	for _, raw := range strings.Split(columnBlock, ",") {
		column := strings.TrimSpace(raw)
		if column == "" {
			continue
		}
		fields := strings.Fields(column)
		if len(fields) > 0 {
			columns = append(columns, strings.Trim(fields[0], `"`))
		}
	}
	return columns
}

func keysForTest(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

type battleAuditCaptureCall struct {
	query string
	args  []any
}

var battleAuditCaptureState = struct {
	sync.Mutex
	nextID int
	calls  []battleAuditCaptureCall
}{}

func registerBattleAuditCaptureDriver(t *testing.T) string {
	t.Helper()
	battleAuditCaptureState.Lock()
	defer battleAuditCaptureState.Unlock()
	battleAuditCaptureState.nextID++
	battleAuditCaptureState.calls = nil
	name := "battle_audit_capture_driver_" + string(rune('a'+battleAuditCaptureState.nextID))
	sql.Register(name, battleAuditCaptureDriver{})
	return name
}

func battleAuditCaptureCalls() []battleAuditCaptureCall {
	battleAuditCaptureState.Lock()
	defer battleAuditCaptureState.Unlock()
	calls := make([]battleAuditCaptureCall, len(battleAuditCaptureState.calls))
	copy(calls, battleAuditCaptureState.calls)
	return calls
}

type battleAuditCaptureDriver struct{}

func (battleAuditCaptureDriver) Open(name string) (driver.Conn, error) {
	return battleAuditCaptureConn{}, nil
}

type battleAuditCaptureConn struct{}

func (battleAuditCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return battleAuditCaptureStmt{query: query}, nil
}

func (battleAuditCaptureConn) Close() error {
	return nil
}

func (battleAuditCaptureConn) Begin() (driver.Tx, error) {
	return battleAuditCaptureTx{}, nil
}

func (battleAuditCaptureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	battleAuditCaptureState.Lock()
	battleAuditCaptureState.calls = append(battleAuditCaptureState.calls, battleAuditCaptureCall{query: query, args: values})
	battleAuditCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (battleAuditCaptureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return battleAuditCaptureRows{}, nil
}

type battleAuditCaptureStmt struct {
	query string
}

func (stmt battleAuditCaptureStmt) Close() error {
	return nil
}

func (stmt battleAuditCaptureStmt) NumInput() int {
	return -1
}

func (stmt battleAuditCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	battleAuditCaptureState.Lock()
	battleAuditCaptureState.calls = append(battleAuditCaptureState.calls, battleAuditCaptureCall{query: stmt.query, args: values})
	battleAuditCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (stmt battleAuditCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	return battleAuditCaptureRows{}, nil
}

type battleAuditCaptureTx struct{}

func (battleAuditCaptureTx) Commit() error {
	return nil
}

func (battleAuditCaptureTx) Rollback() error {
	return nil
}

type battleAuditCaptureRows struct{}

func (battleAuditCaptureRows) Columns() []string {
	return nil
}

func (battleAuditCaptureRows) Close() error {
	return nil
}

func (battleAuditCaptureRows) Next(dest []driver.Value) error {
	return io.EOF
}
