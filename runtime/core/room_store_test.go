package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- capture driver shared by the store tests --------------------------------

type storeCall struct {
	query string
	args  []any
}

type storeCaptureDriver struct {
	mu       sync.Mutex
	execs    []storeCall
	queries  []storeCall
	results  [][]storeRow
	affected int64
}

type storeRow struct {
	columns []string
	values  []driver.Value
}

var storeCaptureRegistry = struct {
	sync.Mutex
	next int
}{}

// registerStoreCaptureDriver installs a driver whose calls are recorded and
// whose query results are supplied by the test.
func registerStoreCaptureDriver(t *testing.T) (*storeCaptureDriver, string) {
	t.Helper()
	capture := &storeCaptureDriver{affected: 1}
	storeCaptureRegistry.Lock()
	defer storeCaptureRegistry.Unlock()
	storeCaptureRegistry.next++
	name := "gensoulkyo_store_capture_" + string(rune('a'+storeCaptureRegistry.next))
	sql.Register(name, capture)
	return capture, name
}

func openCaptureDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open(name, "capture")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// queueResults supplies the row sets the next queries will return, in order.
func (capture *storeCaptureDriver) queueResults(results ...[]storeRow) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.results = append(capture.results, results...)
}

func (capture *storeCaptureDriver) setAffected(affected int64) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.affected = affected
}

func (capture *storeCaptureDriver) execCalls() []storeCall {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	out := make([]storeCall, len(capture.execs))
	copy(out, capture.execs)
	return out
}

func (capture *storeCaptureDriver) queryCalls() []storeCall {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	out := make([]storeCall, len(capture.queries))
	copy(out, capture.queries)
	return out
}

func (capture *storeCaptureDriver) exec(query string, args []any) (driver.Result, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.execs = append(capture.execs, storeCall{query: query, args: args})
	return driver.RowsAffected(capture.affected), nil
}

func (capture *storeCaptureDriver) query(query string, args []any) (driver.Rows, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.queries = append(capture.queries, storeCall{query: query, args: args})
	if len(capture.results) == 0 {
		return &storeCaptureRows{}, nil
	}
	result := capture.results[0]
	capture.results = capture.results[1:]
	return &storeCaptureRows{rows: result}, nil
}

func (capture *storeCaptureDriver) Open(name string) (driver.Conn, error) {
	return storeCaptureConn{driver: capture}, nil
}

type storeCaptureConn struct {
	driver *storeCaptureDriver
}

func (conn storeCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return storeCaptureStmt{driver: conn.driver, query: query}, nil
}

func (conn storeCaptureConn) Close() error { return nil }

func (conn storeCaptureConn) Begin() (driver.Tx, error) { return storeCaptureTx{}, nil }

// CheckNamedValue passes arguments through untouched, the way pgx does, so the
// tests see exactly what the store passed — including the []string bound to
// `= ANY($1)`, which database/sql's default converter would reject.
func (conn storeCaptureConn) CheckNamedValue(value *driver.NamedValue) error { return nil }

func (conn storeCaptureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return conn.driver.exec(query, namedValuesToAny(args))
}

func (conn storeCaptureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return conn.driver.query(query, namedValuesToAny(args))
}

type storeCaptureStmt struct {
	driver *storeCaptureDriver
	query  string
}

func (stmt storeCaptureStmt) Close() error  { return nil }
func (stmt storeCaptureStmt) NumInput() int { return -1 }
func (stmt storeCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	return stmt.driver.exec(stmt.query, valuesToAny(args))
}
func (stmt storeCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	return stmt.driver.query(stmt.query, valuesToAny(args))
}

type storeCaptureTx struct{}

func (storeCaptureTx) Commit() error   { return nil }
func (storeCaptureTx) Rollback() error { return nil }

type storeCaptureRows struct {
	rows []storeRow
	pos  int
}

func (rows *storeCaptureRows) Columns() []string {
	if len(rows.rows) == 0 {
		return []string{"column"}
	}
	return rows.rows[0].columns
}

func (rows *storeCaptureRows) Close() error { return nil }

func (rows *storeCaptureRows) Next(dest []driver.Value) error {
	if rows.pos >= len(rows.rows) {
		return io.EOF
	}
	copy(dest, rows.rows[rows.pos].values)
	rows.pos++
	return nil
}

func namedValuesToAny(args []driver.NamedValue) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg.Value
	}
	return out
}

func valuesToAny(args []driver.Value) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
	}
	return out
}

func roomRowValues() []driver.Value {
	return []driver.Value{
		"ABCD",               // room_code
		"pvp",                // mode_id
		"u-host",             // host_user_id
		"waiting",            // status
		"boss-1",             // boss_id
		"stage-1",            // stage_id
		int64(2),             // coop_players
		"",                   // match_id
		[]byte(`{"seed":7}`), // mode_params_json
		[]byte(`[{"user_id":"u-host"}]`), // messages_json
		[]byte(`["t-1","t-2"]`),          // ticket_ids_json
		time.Unix(1700000000, 0).UTC(),   // created_at
		time.Unix(1700000060, 0).UTC(),   // updated_at
		time.Unix(1700003600, 0).UTC(),   // expires_at
		nil,                              // closed_at
	}
}

func roomRowColumns() []string {
	return []string{
		"room_code", "mode_id", "host_user_id", "status", "boss_id", "stage_id",
		"coop_players", "match_id", "mode_params_json", "messages_json",
		"ticket_ids_json", "created_at", "updated_at", "expires_at", "closed_at",
	}
}

// --- room store --------------------------------------------------------------

func TestSQLRoomStoreSaveRoomUsesUpsert(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, err := NewSQLRoomStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	room := RoomRecord{
		RoomCode:    "ABCD",
		ModeID:      "pvp",
		HostUserID:  "u-host",
		Status:      RoomStatusWaiting,
		BossID:      "boss-1",
		CoopPlayers: 2,
		ModeParams:  map[string]any{"seed": 7},
		Messages:    []LobbyMessage{{UserID: "u-host"}},
		TicketIDs:   []string{"t-1", "t-2"},
		ExpiresAt:   time.Unix(1700003600, 0).UTC(),
	}
	if err := store.SaveRoom(room); err != nil {
		t.Fatalf("save room: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one exec, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "INSERT INTO lobby_rooms") ||
		!strings.Contains(calls[0].query, "ON CONFLICT (room_code) DO UPDATE") {
		t.Fatalf("query is not an upsert: %s", calls[0].query)
	}
	if len(calls[0].args) != 15 {
		t.Fatalf("expected 15 args, got %d", len(calls[0].args))
	}
	if calls[0].args[0] != "ABCD" || calls[0].args[3] != RoomStatusWaiting || calls[0].args[6] != 2 {
		t.Fatalf("args = %v", calls[0].args)
	}
	if calls[0].args[8] != `{"seed":7}` {
		t.Fatalf("mode params = %v", calls[0].args[8])
	}
	if calls[0].args[10] != `["t-1","t-2"]` {
		t.Fatalf("ticket ids = %v", calls[0].args[10])
	}
	// created_at is part of the insert but must never be overwritten on conflict.
	if strings.Contains(calls[0].query, "created_at = EXCLUDED.created_at") {
		t.Fatalf("upsert must not overwrite created_at")
	}
}

func TestSQLRoomStoreLoadRoomDecodesRow(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	capture.queueResults([]storeRow{{columns: roomRowColumns(), values: roomRowValues()}})
	store, err := NewSQLRoomStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	room, ok, err := store.LoadRoom("ABCD")
	if err != nil {
		t.Fatalf("load room: %v", err)
	}
	if !ok {
		t.Fatalf("expected the room to be found")
	}
	if room.RoomCode != "ABCD" || room.ModeID != "pvp" || room.Status != RoomStatusWaiting || room.CoopPlayers != 2 {
		t.Fatalf("room = %+v", room)
	}
	if room.ModeParams["seed"] != float64(7) {
		t.Fatalf("mode params = %v", room.ModeParams)
	}
	if len(room.Messages) != 1 || room.Messages[0].UserID != "u-host" {
		t.Fatalf("messages = %+v", room.Messages)
	}
	if len(room.TicketIDs) != 2 || room.TicketIDs[0] != "t-1" {
		t.Fatalf("ticket ids = %v", room.TicketIDs)
	}
	if room.ExpiresAt.IsZero() || !room.ClosedAt.IsZero() {
		t.Fatalf("timestamps = %v / %v", room.ExpiresAt, room.ClosedAt)
	}
}

func TestSQLRoomStoreLoadRoomMissingIsNotAnError(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	capture.queueResults([]storeRow{})
	store, _ := NewSQLRoomStore(openCaptureDB(t, name))
	_, ok, err := store.LoadRoom("ZZZZ")
	if err != nil {
		t.Fatalf("missing room must not error: %v", err)
	}
	if ok {
		t.Fatalf("missing room must report false")
	}
}

func TestSQLRoomStoreSweepQueriesUseTheDurableIndexes(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLRoomStore(openCaptureDB(t, name))
	cutoff := time.Unix(1700003600, 0).UTC()

	if _, err := store.ListExpiringRooms(cutoff, 25); err != nil {
		t.Fatalf("list expiring: %v", err)
	}
	if _, err := store.ListRoomsByStatus([]string{RoomStatusWaiting, RoomStatusFound}, 10); err != nil {
		t.Fatalf("list by status: %v", err)
	}
	queries := capture.queryCalls()
	if len(queries) != 2 {
		t.Fatalf("expected two queries, got %d", len(queries))
	}
	if !strings.Contains(queries[0].query, "expires_at IS NOT NULL AND expires_at < $1") ||
		queries[0].args[0] != cutoff || queries[0].args[1] != 25 {
		t.Fatalf("expiring query = %s %v", queries[0].query, queries[0].args)
	}
	if !strings.Contains(queries[1].query, "status = ANY($1)") {
		t.Fatalf("status query = %s", queries[1].query)
	}
}

func TestSQLRoomStoreMembersAreUpsertedAndSoftRemoved(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLRoomStore(openCaptureDB(t, name))
	member := RoomMemberRecord{RoomCode: "ABCD", UserID: "u-1", TicketID: "t-1", SeatIndex: 1, IsHost: true}
	if err := store.SaveMember(member); err != nil {
		t.Fatalf("save member: %v", err)
	}
	if err := store.RemoveMember("ABCD", "u-1", time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 2 {
		t.Fatalf("expected two execs, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "ON CONFLICT (room_code, user_id) DO UPDATE") {
		t.Fatalf("member upsert = %s", calls[0].query)
	}
	if !strings.Contains(calls[1].query, "SET left_at = $3") {
		t.Fatalf("member removal must stamp left_at: %s", calls[1].query)
	}
}

func TestSQLRoomStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewSQLRoomStore(nil); err == nil {
		t.Fatalf("expected nil db rejection")
	}
	var store *SQLRoomStore
	if err := store.SaveRoom(RoomRecord{}); err == nil {
		t.Fatalf("expected nil store rejection")
	}
	if _, _, err := store.LoadRoom("ABCD"); err == nil {
		t.Fatalf("expected nil store rejection")
	}
}

func TestRoomMigrationCoversStoreColumns(t *testing.T) {
	upSQL := readMigrationFile(t, "004_lobby_rooms.up.sql")
	downSQL := readMigrationFile(t, "004_lobby_rooms.down.sql")
	assertMigrationCoversInsertColumns(t, upSQL, insertRoomSQL, "lobby_rooms")
	assertMigrationCoversInsertColumns(t, upSQL, upsertRoomMemberSQL, "lobby_room_members")
	assertMigrationHasColumns(t, upSQL, "lobby_rooms", []string{"room_code", "status", "expires_at", "closed_at", "ticket_ids_json"})
	if !strings.Contains(upSQL, "status IN ('waiting', 'found', 'cancelled', 'closed', 'expired')") {
		t.Fatalf("room migration must constrain the status values the store writes")
	}
	assertDownMigrationDropsTable(t, downSQL, "lobby_rooms")
	assertDownMigrationDropsTable(t, downSQL, "lobby_room_members")
}

func TestMemoryRoomStoreRoundTrip(t *testing.T) {
	store := NewMemoryRoomStore()
	now := time.Now().UTC()
	room := RoomRecord{
		RoomCode:  "ABCD",
		ModeID:    "coop_boss",
		HostUserID: "u-host",
		Status:    RoomStatusWaiting,
		ExpiresAt: now.Add(-time.Minute),
	}
	if err := store.SaveRoom(room); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SaveMember(RoomMemberRecord{RoomCode: "ABCD", UserID: "u-host", SeatIndex: 0, IsHost: true}); err != nil {
		t.Fatalf("save member: %v", err)
	}
	if err := store.SaveMember(RoomMemberRecord{RoomCode: "ABCD", UserID: "u-guest", SeatIndex: 1}); err != nil {
		t.Fatalf("save member: %v", err)
	}
	members, err := store.ListMembers("ABCD")
	if err != nil || len(members) != 2 || members[0].UserID != "u-host" {
		t.Fatalf("members = %+v, %v", members, err)
	}
	// The room is past its expiry, so the sweep must see it.
	expiring, err := store.ListExpiringRooms(now, 10)
	if err != nil || len(expiring) != 1 || expiring[0].RoomCode != "ABCD" {
		t.Fatalf("expiring = %+v, %v", expiring, err)
	}
	if err := store.MarkRoomFound("ABCD", "m-1", now); err != nil {
		t.Fatalf("mark found: %v", err)
	}
	loaded, ok, err := store.LoadRoom("ABCD")
	if err != nil || !ok || loaded.Status != RoomStatusFound || loaded.MatchID != "m-1" {
		t.Fatalf("after found = %+v %v %v", loaded, ok, err)
	}
	// A closed room is terminal, so it drops out of the sweep.
	if err := store.CloseRoom("ABCD", RoomStatusClosed, now); err != nil {
		t.Fatalf("close: %v", err)
	}
	expiring, err = store.ListExpiringRooms(now, 10)
	if err != nil || len(expiring) != 0 {
		t.Fatalf("closed room must not expire: %+v %v", expiring, err)
	}
	if err := store.RemoveMember("ABCD", "u-guest", now); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	members, _ = store.ListMembers("ABCD")
	if members[1].LeftAt.IsZero() {
		t.Fatalf("removed member must carry left_at")
	}
}

func TestNewRoomStoreFallsBackToMemoryWithoutDatabase(t *testing.T) {
	store, err := NewRoomStore(nil)
	if err != nil {
		t.Fatalf("new room store: %v", err)
	}
	if _, ok := store.(*MemoryRoomStore); !ok {
		t.Fatalf("expected the memory store, got %T", store)
	}
}

func TestNewRoomStoreUsesSQLWithDatabase(t *testing.T) {
	_, name := registerStoreCaptureDriver(t)
	store, err := NewRoomStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new room store: %v", err)
	}
	if _, ok := store.(*SQLRoomStore); !ok {
		t.Fatalf("expected the sql store, got %T", store)
	}
}

func TestRoomStoreImplementationsSatisfyTheInterface(t *testing.T) {
	var _ RoomStore = (*SQLRoomStore)(nil)
	var _ RoomStore = (*MemoryRoomStore)(nil)
}
