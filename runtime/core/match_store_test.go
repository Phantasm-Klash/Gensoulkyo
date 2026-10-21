package core

import (
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

func matchRowColumns() []string {
	return []string{
		"match_id", "mode_id", "room_code", "status", "boss_id", "stage_id",
		"ruleset_version", "mode_ruleset_version", "reward_table_id", "server_seed",
		"tick", "boss_hp", "boss_max_hp", "player_count", "battle_allocation_json",
		"battle_result_hash", "battle_result_replay", "battle_result_key_id",
		"battle_result_at", "created_at", "started_at", "ended_at", "updated_at",
		"expires_at",
	}
}

func matchRowValues() []driver.Value {
	return []driver.Value{
		"m-1",            // match_id
		"pvp",            // mode_id
		"ABCD",           // room_code
		"running",        // status
		"boss-1",         // boss_id
		"stage-1",        // stage_id
		"mvp-boss-race-s0", // ruleset_version
		"s0",             // mode_ruleset_version
		"reward-s0",      // reward_table_id
		int64(4242),      // server_seed
		int64(120),       // tick
		int64(80),        // boss_hp
		int64(100),       // boss_max_hp
		int64(2),         // player_count
		[]byte(`{"endpoint":"127.0.0.1:9001"}`), // battle_allocation_json
		"",               // battle_result_hash
		"",               // battle_result_replay
		"",               // battle_result_key_id
		nil,              // battle_result_at
		time.Unix(1700000000, 0).UTC(), // created_at
		time.Unix(1700000030, 0).UTC(), // started_at
		nil,                            // ended_at
		time.Unix(1700000060, 0).UTC(), // updated_at
		time.Unix(1700003600, 0).UTC(), // expires_at
	}
}

func TestSQLMatchStoreSaveMatchUsesUpsert(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, err := NewSQLMatchStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	match := MatchRecord{
		MatchID:            "m-1",
		ModeID:             "pvp",
		RoomCode:           "ABCD",
		Status:             MatchStatusLoading,
		RulesetVersion:     "mvp-boss-race-s0",
		ServerSeed:         4242,
		PlayerCount:        2,
		BattleAllocation:   &BattleServerAllocation{Endpoint: "127.0.0.1:9001"},
		ExpiresAt:          time.Unix(1700003600, 0).UTC(),
	}
	if err := store.SaveMatch(match); err != nil {
		t.Fatalf("save match: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one exec, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "INSERT INTO lobby_matches") ||
		!strings.Contains(calls[0].query, "ON CONFLICT (match_id) DO UPDATE") {
		t.Fatalf("query is not an upsert: %s", calls[0].query)
	}
	if len(calls[0].args) != 24 {
		t.Fatalf("expected 24 args, got %d", len(calls[0].args))
	}
	// server_seed is an int64 in the record, so it stays int64 in the args.
	if calls[0].args[0] != "m-1" || calls[0].args[3] != MatchStatusLoading || calls[0].args[9] != int64(4242) {
		t.Fatalf("args = %v", calls[0].args)
	}
	if !strings.Contains(calls[0].args[14].(string), "127.0.0.1:9001") {
		t.Fatalf("allocation json = %v", calls[0].args[14])
	}
}

func TestSQLMatchStoreSettleMatchEndsTheMatch(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLMatchStore(openCaptureDB(t, name))
	at := time.Unix(1700000900, 0).UTC()
	if err := store.SettleMatch("m-1", "sha256:result", "replay-1", "dev-ed25519-0", at); err != nil {
		t.Fatalf("settle match: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one exec, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "status = $2") || !strings.Contains(calls[0].query, "ended_at = $6") {
		t.Fatalf("settle query = %s", calls[0].query)
	}
	if calls[0].args[1] != MatchStatusEnded || calls[0].args[2] != "sha256:result" || calls[0].args[4] != "dev-ed25519-0" {
		t.Fatalf("args = %v", calls[0].args)
	}
}

func TestSQLMatchStoreLoadMatchDecodesRow(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	capture.queueResults([]storeRow{{
		columns: matchRowColumns(),
		values:  matchRowValues(),
	}})
	store, _ := NewSQLMatchStore(openCaptureDB(t, name))
	match, ok, err := store.LoadMatch("m-1")
	if err != nil || !ok {
		t.Fatalf("load match: ok=%v err=%v", ok, err)
	}
	if match.MatchID != "m-1" || match.Status != MatchStatusRunning || match.Tick != 120 || match.BossHP != 80 {
		t.Fatalf("match = %+v", match)
	}
	if match.BattleAllocation == nil || match.BattleAllocation.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("allocation = %+v", match.BattleAllocation)
	}
	if match.StartedAt.IsZero() || !match.EndedAt.IsZero() || match.ExpiresAt.IsZero() {
		t.Fatalf("timestamps = %v / %v / %v", match.StartedAt, match.EndedAt, match.ExpiresAt)
	}
}

func TestSQLMatchStorePlayersAreUpsertedAndSettled(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLMatchStore(openCaptureDB(t, name))
	player := MatchPlayerRecord{MatchID: "m-1", UserID: "u-1", SeatIndex: 0, Ready: true, Score: 10}
	if err := store.SavePlayer(player); err != nil {
		t.Fatalf("save player: %v", err)
	}
	if err := store.SettlePlayer("m-1", "u-1", "win", 320, 15, time.Unix(1700000900, 0).UTC()); err != nil {
		t.Fatalf("settle player: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 2 {
		t.Fatalf("expected two execs, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "ON CONFLICT (match_id, user_id) DO UPDATE") {
		t.Fatalf("player upsert = %s", calls[0].query)
	}
	if !strings.Contains(calls[1].query, "SET result = $3") {
		t.Fatalf("player settle = %s", calls[1].query)
	}
	if calls[1].args[2] != "win" || calls[1].args[3] != 320 || calls[1].args[4] != 15 {
		t.Fatalf("settle args = %v", calls[1].args)
	}
}

func TestSQLMatchStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewSQLMatchStore(nil); err == nil {
		t.Fatalf("expected nil db rejection")
	}
	var store *SQLMatchStore
	if err := store.SaveMatch(MatchRecord{}); err == nil {
		t.Fatalf("expected nil store rejection")
	}
}

func TestMatchMigrationCoversStoreColumns(t *testing.T) {
	upSQL := readMigrationFile(t, "005_lobby_matches.up.sql")
	downSQL := readMigrationFile(t, "005_lobby_matches.down.sql")
	assertMigrationCoversInsertColumns(t, upSQL, insertMatchSQL, "lobby_matches")
	assertMigrationCoversInsertColumns(t, upSQL, upsertMatchPlayerSQL, "lobby_match_players")
	if !strings.Contains(upSQL, "status IN ('loading', 'running', 'ended', 'abandoned', 'expired')") {
		t.Fatalf("match migration must constrain the status values the store writes")
	}
	assertDownMigrationDropsTable(t, downSQL, "lobby_matches")
	assertDownMigrationDropsTable(t, downSQL, "lobby_match_players")
}

func TestMemoryMatchStoreRoundTrip(t *testing.T) {
	store := NewMemoryMatchStore()
	now := time.Now().UTC()
	match := MatchRecord{
		MatchID:   "m-1",
		ModeID:    "coop_boss",
		Status:    MatchStatusLoading,
		ExpiresAt: now.Add(time.Minute),
	}
	if err := store.SaveMatch(match); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Not expired yet, so the sweep must not see it.
	expiring, err := store.ListExpiringMatches(now, 10)
	if err != nil || len(expiring) != 0 {
		t.Fatalf("expiring = %+v, %v", expiring, err)
	}
	if err := store.UpdateMatchStatus("m-1", MatchStatusRunning, now); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := store.SavePlayer(MatchPlayerRecord{MatchID: "m-1", UserID: "u-1", SeatIndex: 0}); err != nil {
		t.Fatalf("save player: %v", err)
	}
	if err := store.SavePlayer(MatchPlayerRecord{MatchID: "m-1", UserID: "u-2", SeatIndex: 1}); err != nil {
		t.Fatalf("save player: %v", err)
	}
	players, err := store.ListPlayers("m-1")
	if err != nil || len(players) != 2 || players[0].UserID != "u-1" {
		t.Fatalf("players = %+v, %v", players, err)
	}
	if err := store.SettlePlayer("m-1", "u-1", "win", 500, 20, now); err != nil {
		t.Fatalf("settle player: %v", err)
	}
	if err := store.SettleMatch("m-1", "sha256:result", "replay-1", "dev-ed25519-0", now); err != nil {
		t.Fatalf("settle match: %v", err)
	}
	loaded, ok, err := store.LoadMatch("m-1")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.Status != MatchStatusEnded || loaded.BattleResultHash != "sha256:result" || loaded.EndedAt.IsZero() {
		t.Fatalf("settled match = %+v", loaded)
	}
	players, _ = store.ListPlayers("m-1")
	if players[0].Result != "win" || players[0].Score != 500 || players[0].RankDelta != 20 {
		t.Fatalf("settled player = %+v", players[0])
	}
	// Ended matches drop out of the TTL sweep.
	expiring, err = store.ListExpiringMatches(now.Add(2*time.Minute), 10)
	if err != nil || len(expiring) != 0 {
		t.Fatalf("ended match must not be swept: %+v %v", expiring, err)
	}
}

func TestNewMatchStoreSelectsImplementation(t *testing.T) {
	store, err := NewMatchStore(nil)
	if err != nil {
		t.Fatalf("new match store: %v", err)
	}
	if _, ok := store.(*MemoryMatchStore); !ok {
		t.Fatalf("expected the memory store, got %T", store)
	}
	_, name := registerStoreCaptureDriver(t)
	sqlStore, err := NewMatchStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new match store: %v", err)
	}
	if _, ok := sqlStore.(*SQLMatchStore); !ok {
		t.Fatalf("expected the sql store, got %T", sqlStore)
	}
}

func TestMatchStoreImplementationsSatisfyTheInterface(t *testing.T) {
	var _ MatchStore = (*SQLMatchStore)(nil)
	var _ MatchStore = (*MemoryMatchStore)(nil)
}
