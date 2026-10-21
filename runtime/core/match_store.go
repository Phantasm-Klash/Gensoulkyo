package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// MatchStore is the persistence seam for matches started from a room or from
// matchmaking.
//
// Today Service.matches is an in-process map and a match that never leaves
// "loading" (a player who never sends match.ready) is only reaped when the
// battle agent happens to poll it 15 minutes later. The durable row gives that
// sweep a place to live and a reason to record.
type MatchStore interface {
	SaveMatch(match MatchRecord) error
	LoadMatch(matchID string) (MatchRecord, bool, error)
	UpdateMatchStatus(matchID string, status string, at time.Time) error
	// SettleMatch marks the match ended and stamps the verified battle result.
	SettleMatch(matchID string, resultHash string, replayID string, keyID string, at time.Time) error
	ListMatchesByStatus(statuses []string, limit int) ([]MatchRecord, error)
	// ListExpiringMatches returns unfinished matches past their TTL.
	ListExpiringMatches(before time.Time, limit int) ([]MatchRecord, error)
	SavePlayer(player MatchPlayerRecord) error
	ListPlayers(matchID string) ([]MatchPlayerRecord, error)
	// SettlePlayer records one player's outcome.
	SettlePlayer(matchID string, userID string, result string, score int, rankDelta int, at time.Time) error
}

// Match status values accepted by the lobby_matches check constraint.
const (
	MatchStatusLoading   = "loading"
	MatchStatusRunning   = "running"
	MatchStatusEnded     = "ended"
	MatchStatusAbandoned = "abandoned"
	MatchStatusExpired   = "expired"
)

// MatchRecord is the durable copy of the fields of matchState that must survive
// a lobby restart. The per-tick simulation state stays in memory until the
// match ends; only the settlement-relevant summary is persisted.
type MatchRecord struct {
	MatchID            string
	ModeID             string
	RoomCode           string
	Status             string
	BossID             string
	StageID            string
	RulesetVersion     string
	ModeRulesetVersion string
	RewardTableID      string
	ServerSeed         int64
	Tick               int
	BossHP             int
	BossMaxHP          int
	PlayerCount        int
	BattleAllocation   *BattleServerAllocation
	BattleResultHash   string
	BattleResultReplay string
	BattleResultKeyID  string
	BattleResultAt     time.Time
	CreatedAt          time.Time
	StartedAt          time.Time
	EndedAt            time.Time
	UpdatedAt          time.Time
	ExpiresAt          time.Time
}

// MatchPlayerRecord is one seat of a match.
type MatchPlayerRecord struct {
	MatchID         string
	UserID          string
	SeatIndex       int
	DisplayName     string
	BossID          string
	DeckSnapshot    DeckSnapshot
	Loadout         PlayerLoadout
	Ready           bool
	Result          string
	Score           int
	RankDelta       int
	WorldBossDamage int
	JoinedAt        time.Time
	SettledAt       time.Time
}

// --- SQL implementation ------------------------------------------------------

const insertMatchSQL = `
INSERT INTO lobby_matches (
    match_id,
    mode_id,
    room_code,
    status,
    boss_id,
    stage_id,
    ruleset_version,
    mode_ruleset_version,
    reward_table_id,
    server_seed,
    tick,
    boss_hp,
    boss_max_hp,
    player_count,
    battle_allocation_json,
    battle_result_hash,
    battle_result_replay,
    battle_result_key_id,
    battle_result_at,
    created_at,
    started_at,
    ended_at,
    updated_at,
    expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb,
    $16, $17, $18, $19, $20, $21, $22, $23, $24
) ON CONFLICT (match_id) DO UPDATE SET
    mode_id = EXCLUDED.mode_id,
    room_code = EXCLUDED.room_code,
    status = EXCLUDED.status,
    boss_id = EXCLUDED.boss_id,
    stage_id = EXCLUDED.stage_id,
    ruleset_version = EXCLUDED.ruleset_version,
    mode_ruleset_version = EXCLUDED.mode_ruleset_version,
    reward_table_id = EXCLUDED.reward_table_id,
    server_seed = EXCLUDED.server_seed,
    tick = EXCLUDED.tick,
    boss_hp = EXCLUDED.boss_hp,
    boss_max_hp = EXCLUDED.boss_max_hp,
    player_count = EXCLUDED.player_count,
    battle_allocation_json = EXCLUDED.battle_allocation_json,
    battle_result_hash = EXCLUDED.battle_result_hash,
    battle_result_replay = EXCLUDED.battle_result_replay,
    battle_result_key_id = EXCLUDED.battle_result_key_id,
    battle_result_at = EXCLUDED.battle_result_at,
    started_at = EXCLUDED.started_at,
    ended_at = EXCLUDED.ended_at,
    updated_at = EXCLUDED.updated_at,
    expires_at = EXCLUDED.expires_at`

const matchColumnsSQL = `match_id, mode_id, room_code, status, boss_id, stage_id,
       ruleset_version, mode_ruleset_version, reward_table_id, server_seed, tick,
       boss_hp, boss_max_hp, player_count, battle_allocation_json,
       battle_result_hash, battle_result_replay, battle_result_key_id,
       battle_result_at, created_at, started_at, ended_at, updated_at, expires_at`

const selectMatchSQL = `
SELECT ` + matchColumnsSQL + `
  FROM lobby_matches
 WHERE match_id = $1`

const updateMatchStatusSQL = `
UPDATE lobby_matches
   SET status = $2, updated_at = $3
 WHERE match_id = $1`

const settleMatchSQL = `
UPDATE lobby_matches
   SET status = $2,
       battle_result_hash = $3,
       battle_result_replay = $4,
       battle_result_key_id = $5,
       battle_result_at = $6,
       ended_at = $6,
       updated_at = $6
 WHERE match_id = $1`

const listMatchesByStatusSQL = `
SELECT ` + matchColumnsSQL + `
  FROM lobby_matches
 WHERE status = ANY($1)
 ORDER BY updated_at ASC
 LIMIT $2`

const listExpiringMatchesSQL = `
SELECT ` + matchColumnsSQL + `
  FROM lobby_matches
 WHERE expires_at IS NOT NULL AND expires_at < $1
   AND status IN ('loading', 'running')
 ORDER BY expires_at ASC
 LIMIT $2`

const upsertMatchPlayerSQL = `
INSERT INTO lobby_match_players (
    match_id, user_id, seat_index, display_name, boss_id,
    deck_snapshot_json, loadout_json, ready, result, score, rank_delta,
    world_boss_damage, joined_at, settled_at
) VALUES (
    $1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10, $11, $12, $13, $14
) ON CONFLICT (match_id, user_id) DO UPDATE SET
    seat_index = EXCLUDED.seat_index,
    display_name = EXCLUDED.display_name,
    boss_id = EXCLUDED.boss_id,
    deck_snapshot_json = EXCLUDED.deck_snapshot_json,
    loadout_json = EXCLUDED.loadout_json,
    ready = EXCLUDED.ready,
    result = EXCLUDED.result,
    score = EXCLUDED.score,
    rank_delta = EXCLUDED.rank_delta,
    world_boss_damage = EXCLUDED.world_boss_damage,
    settled_at = EXCLUDED.settled_at`

const listMatchPlayersSQL = `
SELECT match_id, user_id, seat_index, display_name, boss_id,
       deck_snapshot_json, loadout_json, ready, result, score, rank_delta,
       world_boss_damage, joined_at, settled_at
  FROM lobby_match_players
 WHERE match_id = $1
 ORDER BY seat_index ASC`

const settleMatchPlayerSQL = `
UPDATE lobby_match_players
   SET result = $3, score = $4, rank_delta = $5, settled_at = $6
 WHERE match_id = $1 AND user_id = $2`

// SQLMatchStore persists matches in Postgres.
type SQLMatchStore struct {
	db         *sql.DB
	statements matchStoreStatements
}

type matchStoreStatements struct {
	insertMatch  string
	selectMatch  string
	updateStatus string
	settleMatch  string
	listByStatus string
	listExpiring string
	upsertPlayer string
	listPlayers  string
	settlePlayer string
}

// MatchStoreOption rewrites the statements a store executes.
type MatchStoreOption func(*SQLMatchStore)

// NewSQLMatchStore returns a store backed by db.
func NewSQLMatchStore(db *sql.DB, options ...MatchStoreOption) (*SQLMatchStore, error) {
	if db == nil {
		return nil, errors.New("match store requires db")
	}
	store := &SQLMatchStore{db: db, statements: defaultMatchStoreStatements()}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

func defaultMatchStoreStatements() matchStoreStatements {
	return matchStoreStatements{
		insertMatch:  insertMatchSQL,
		selectMatch:  selectMatchSQL,
		updateStatus: updateMatchStatusSQL,
		settleMatch:  settleMatchSQL,
		listByStatus: listMatchesByStatusSQL,
		listExpiring: listExpiringMatchesSQL,
		upsertPlayer: upsertMatchPlayerSQL,
		listPlayers:  listMatchPlayersSQL,
		settlePlayer: settleMatchPlayerSQL,
	}
}

func withMatchStoreStatements(statements matchStoreStatements) MatchStoreOption {
	return func(store *SQLMatchStore) { store.statements = statements }
}

// NewMatchStore picks the SQL store when a database is configured and the
// process-local store otherwise.
func NewMatchStore(db *sql.DB) (MatchStore, error) {
	if db == nil {
		return NewMemoryMatchStore(), nil
	}
	return NewSQLMatchStore(db)
}

func (store *SQLMatchStore) SaveMatch(match MatchRecord) error {
	if store == nil || store.db == nil {
		return errors.New("match store is not configured")
	}
	allocation, err := marshalRoomJSON(match.BattleAllocation)
	if err != nil {
		return err
	}
	createdAt := match.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := match.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = store.db.Exec(
		store.statements.insertMatch,
		match.MatchID,
		match.ModeID,
		match.RoomCode,
		firstNonEmptyCore(match.Status, MatchStatusLoading),
		match.BossID,
		match.StageID,
		match.RulesetVersion,
		match.ModeRulesetVersion,
		match.RewardTableID,
		match.ServerSeed,
		match.Tick,
		match.BossHP,
		match.BossMaxHP,
		match.PlayerCount,
		allocation,
		match.BattleResultHash,
		match.BattleResultReplay,
		match.BattleResultKeyID,
		nullableTime(match.BattleResultAt),
		createdAt,
		nullableTime(match.StartedAt),
		nullableTime(match.EndedAt),
		updatedAt,
		nullableTime(match.ExpiresAt),
	)
	return err
}

func (store *SQLMatchStore) LoadMatch(matchID string) (MatchRecord, bool, error) {
	if store == nil || store.db == nil {
		return MatchRecord{}, false, errors.New("match store is not configured")
	}
	match, err := scanMatchRecord(store.db.QueryRow(store.statements.selectMatch, matchID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MatchRecord{}, false, nil
		}
		return MatchRecord{}, false, err
	}
	return match, true, nil
}

func (store *SQLMatchStore) UpdateMatchStatus(matchID string, status string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("match store is not configured")
	}
	_, err := store.db.Exec(store.statements.updateStatus, matchID, firstNonEmptyCore(status, MatchStatusEnded), stampOrNow(at))
	return err
}

func (store *SQLMatchStore) SettleMatch(matchID string, resultHash string, replayID string, keyID string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("match store is not configured")
	}
	_, err := store.db.Exec(store.statements.settleMatch, matchID, MatchStatusEnded, resultHash, replayID, keyID, stampOrNow(at))
	return err
}

func (store *SQLMatchStore) ListMatchesByStatus(statuses []string, limit int) ([]MatchRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("match store is not configured")
	}
	rows, err := store.db.Query(store.statements.listByStatus, statusListForSQL(statuses), normalizeRoomLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMatchRecords(rows)
}

func (store *SQLMatchStore) ListExpiringMatches(before time.Time, limit int) ([]MatchRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("match store is not configured")
	}
	rows, err := store.db.Query(store.statements.listExpiring, before, normalizeRoomLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMatchRecords(rows)
}

func (store *SQLMatchStore) SavePlayer(player MatchPlayerRecord) error {
	if store == nil || store.db == nil {
		return errors.New("match store is not configured")
	}
	deck, err := marshalRoomJSON(player.DeckSnapshot)
	if err != nil {
		return err
	}
	loadout, err := marshalRoomJSON(player.Loadout)
	if err != nil {
		return err
	}
	_, err = store.db.Exec(
		store.statements.upsertPlayer,
		player.MatchID,
		player.UserID,
		player.SeatIndex,
		player.DisplayName,
		player.BossID,
		deck,
		loadout,
		player.Ready,
		player.Result,
		player.Score,
		player.RankDelta,
		player.WorldBossDamage,
		stampOrNow(player.JoinedAt),
		nullableTime(player.SettledAt),
	)
	return err
}

func (store *SQLMatchStore) ListPlayers(matchID string) ([]MatchPlayerRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("match store is not configured")
	}
	rows, err := store.db.Query(store.statements.listPlayers, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	players := []MatchPlayerRecord{}
	for rows.Next() {
		player, err := scanMatchPlayer(rows)
		if err != nil {
			return nil, err
		}
		players = append(players, player)
	}
	return players, rows.Err()
}

func (store *SQLMatchStore) SettlePlayer(matchID string, userID string, result string, score int, rankDelta int, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("match store is not configured")
	}
	_, err := store.db.Exec(store.statements.settlePlayer, matchID, userID, result, score, rankDelta, stampOrNow(at))
	return err
}

func collectMatchRecords(rows *sql.Rows) ([]MatchRecord, error) {
	matches := []MatchRecord{}
	for rows.Next() {
		match, err := scanMatchRecord(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, match)
	}
	return matches, rows.Err()
}

func scanMatchRecord(scanner roomRowScanner) (MatchRecord, error) {
	var (
		match      MatchRecord
		allocation []byte
		resultAt   sql.NullTime
		startedAt  sql.NullTime
		endedAt    sql.NullTime
		expiresAt  sql.NullTime
	)
	err := scanner.Scan(
		&match.MatchID,
		&match.ModeID,
		&match.RoomCode,
		&match.Status,
		&match.BossID,
		&match.StageID,
		&match.RulesetVersion,
		&match.ModeRulesetVersion,
		&match.RewardTableID,
		&match.ServerSeed,
		&match.Tick,
		&match.BossHP,
		&match.BossMaxHP,
		&match.PlayerCount,
		&allocation,
		&match.BattleResultHash,
		&match.BattleResultReplay,
		&match.BattleResultKeyID,
		&resultAt,
		&match.CreatedAt,
		&startedAt,
		&endedAt,
		&match.UpdatedAt,
		&expiresAt,
	)
	if err != nil {
		return MatchRecord{}, err
	}
	if len(allocation) > 0 {
		parsed := &BattleServerAllocation{}
		if err := json.Unmarshal(allocation, parsed); err != nil {
			return MatchRecord{}, err
		}
		match.BattleAllocation = parsed
	}
	if resultAt.Valid {
		match.BattleResultAt = resultAt.Time
	}
	if startedAt.Valid {
		match.StartedAt = startedAt.Time
	}
	if endedAt.Valid {
		match.EndedAt = endedAt.Time
	}
	if expiresAt.Valid {
		match.ExpiresAt = expiresAt.Time
	}
	return match, nil
}

func scanMatchPlayer(scanner roomRowScanner) (MatchPlayerRecord, error) {
	var (
		player    MatchPlayerRecord
		deck      []byte
		loadout   []byte
		settledAt sql.NullTime
	)
	err := scanner.Scan(
		&player.MatchID,
		&player.UserID,
		&player.SeatIndex,
		&player.DisplayName,
		&player.BossID,
		&deck,
		&loadout,
		&player.Ready,
		&player.Result,
		&player.Score,
		&player.RankDelta,
		&player.WorldBossDamage,
		&player.JoinedAt,
		&settledAt,
	)
	if err != nil {
		return MatchPlayerRecord{}, err
	}
	if len(deck) > 0 {
		if err := json.Unmarshal(deck, &player.DeckSnapshot); err != nil {
			return MatchPlayerRecord{}, err
		}
	}
	if len(loadout) > 0 {
		if err := json.Unmarshal(loadout, &player.Loadout); err != nil {
			return MatchPlayerRecord{}, err
		}
	}
	if settledAt.Valid {
		player.SettledAt = settledAt.Time
	}
	return player, nil
}

// --- in-memory implementation ------------------------------------------------

// MemoryMatchStore keeps matches in the process.
type MemoryMatchStore struct {
	mu      sync.Mutex
	matches map[string]MatchRecord
	players map[string][]MatchPlayerRecord
}

// NewMemoryMatchStore returns an empty process-local store.
func NewMemoryMatchStore() *MemoryMatchStore {
	return &MemoryMatchStore{
		matches: map[string]MatchRecord{},
		players: map[string][]MatchPlayerRecord{},
	}
}

func (store *MemoryMatchStore) SaveMatch(match MatchRecord) error {
	if store == nil {
		return errors.New("match store is not configured")
	}
	if match.CreatedAt.IsZero() {
		match.CreatedAt = time.Now().UTC()
	}
	if match.UpdatedAt.IsZero() {
		match.UpdatedAt = match.CreatedAt
	}
	if match.Status == "" {
		match.Status = MatchStatusLoading
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.matches[match.MatchID]; ok {
		match.CreatedAt = existing.CreatedAt
	}
	store.matches[match.MatchID] = match
	return nil
}

func (store *MemoryMatchStore) LoadMatch(matchID string) (MatchRecord, bool, error) {
	if store == nil {
		return MatchRecord{}, false, errors.New("match store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	match, ok := store.matches[matchID]
	return match, ok, nil
}

func (store *MemoryMatchStore) UpdateMatchStatus(matchID string, status string, at time.Time) error {
	if store == nil {
		return errors.New("match store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	match, ok := store.matches[matchID]
	if !ok {
		return nil
	}
	match.Status = firstNonEmptyCore(status, MatchStatusEnded)
	match.UpdatedAt = stampOrNow(at)
	store.matches[matchID] = match
	return nil
}

func (store *MemoryMatchStore) SettleMatch(matchID string, resultHash string, replayID string, keyID string, at time.Time) error {
	if store == nil {
		return errors.New("match store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	match, ok := store.matches[matchID]
	if !ok {
		return nil
	}
	match.Status = MatchStatusEnded
	match.BattleResultHash = resultHash
	match.BattleResultReplay = replayID
	match.BattleResultKeyID = keyID
	match.BattleResultAt = stamp
	match.EndedAt = stamp
	match.UpdatedAt = stamp
	store.matches[matchID] = match
	return nil
}

func (store *MemoryMatchStore) ListMatchesByStatus(statuses []string, limit int) ([]MatchRecord, error) {
	if store == nil {
		return nil, errors.New("match store is not configured")
	}
	wanted := map[string]bool{}
	for _, status := range statuses {
		if trimmed := strings.TrimSpace(status); trimmed != "" {
			wanted[trimmed] = true
		}
	}
	store.mu.Lock()
	matches := make([]MatchRecord, 0, len(store.matches))
	for _, match := range store.matches {
		if len(wanted) == 0 || wanted[match.Status] {
			matches = append(matches, match)
		}
	}
	store.mu.Unlock()
	sort.Slice(matches, func(i, j int) bool { return matches[i].UpdatedAt.Before(matches[j].UpdatedAt) })
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (store *MemoryMatchStore) ListExpiringMatches(before time.Time, limit int) ([]MatchRecord, error) {
	if store == nil {
		return nil, errors.New("match store is not configured")
	}
	store.mu.Lock()
	matches := []MatchRecord{}
	for _, match := range store.matches {
		if match.ExpiresAt.IsZero() || !match.ExpiresAt.Before(before) {
			continue
		}
		if match.Status != MatchStatusLoading && match.Status != MatchStatusRunning {
			continue
		}
		matches = append(matches, match)
	}
	store.mu.Unlock()
	sort.Slice(matches, func(i, j int) bool { return matches[i].ExpiresAt.Before(matches[j].ExpiresAt) })
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (store *MemoryMatchStore) SavePlayer(player MatchPlayerRecord) error {
	if store == nil {
		return errors.New("match store is not configured")
	}
	if player.JoinedAt.IsZero() {
		player.JoinedAt = time.Now().UTC()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	players := store.players[player.MatchID]
	replaced := false
	for i := range players {
		if players[i].UserID == player.UserID {
			players[i] = player
			replaced = true
			break
		}
	}
	if !replaced {
		players = append(players, player)
	}
	sort.SliceStable(players, func(i, j int) bool { return players[i].SeatIndex < players[j].SeatIndex })
	store.players[player.MatchID] = players
	return nil
}

func (store *MemoryMatchStore) ListPlayers(matchID string) ([]MatchPlayerRecord, error) {
	if store == nil {
		return nil, errors.New("match store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]MatchPlayerRecord{}, store.players[matchID]...), nil
}

func (store *MemoryMatchStore) SettlePlayer(matchID string, userID string, result string, score int, rankDelta int, at time.Time) error {
	if store == nil {
		return errors.New("match store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	players := store.players[matchID]
	for i := range players {
		if players[i].UserID == userID {
			players[i].Result = result
			players[i].Score = score
			players[i].RankDelta = rankDelta
			players[i].SettledAt = stamp
		}
	}
	store.players[matchID] = players
	return nil
}
