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

// RoomStore is the persistence seam for lobby rooms.
//
// Service.rooms is an in-process map: a lobby restart drops every open room and
// a room whose status reached "found" is never reaped because the lobby has no
// background ticker. Wiring Service to this interface is what removes both
// problems; the SQL implementation is the system of record and Redis keeps the
// hot mirror.
//
// Nothing in Service has been switched over yet — this file only delivers the
// store plus its tests. See the wiring checklist in the handover notes.
type RoomStore interface {
	// SaveRoom inserts or updates a room.
	SaveRoom(room RoomRecord) error
	// LoadRoom returns the room, and false when it does not exist.
	LoadRoom(roomCode string) (RoomRecord, bool, error)
	// MarkRoomFound records that the room produced a match.
	MarkRoomFound(roomCode string, matchID string, at time.Time) error
	// CloseRoom moves the room to a terminal status (closed/cancelled/expired).
	CloseRoom(roomCode string, status string, at time.Time) error
	// ListRoomsByStatus returns rooms in any of the given statuses.
	ListRoomsByStatus(statuses []string, limit int) ([]RoomRecord, error)
	// ListExpiringRooms returns non-terminal rooms whose expires_at is before
	// the given instant. This is the query the zombie sweep needs.
	ListExpiringRooms(before time.Time, limit int) ([]RoomRecord, error)
	// SaveMember inserts or updates a seated player.
	SaveMember(member RoomMemberRecord) error
	ListMembers(roomCode string) ([]RoomMemberRecord, error)
	// RemoveMember stamps left_at instead of deleting, so a departure stays
	// auditable for the settlement path.
	RemoveMember(roomCode string, userID string, at time.Time) error
}

// RoomStatus values accepted by the lobby_rooms check constraint.
const (
	RoomStatusWaiting   = "waiting"
	RoomStatusFound     = "found"
	RoomStatusCancelled = "cancelled"
	RoomStatusClosed    = "closed"
	RoomStatusExpired   = "expired"
)

// RoomRecord is the durable copy of the in-process roomState.
type RoomRecord struct {
	RoomCode    string
	ModeID      string
	HostUserID  string
	Status      string
	BossID      string
	StageID     string
	CoopPlayers int
	MatchID     string
	ModeParams  map[string]any
	Messages    []LobbyMessage
	TicketIDs   []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
	ClosedAt    time.Time

	// Members is a transient hydration of lobby_room_members. The load path
	// fills it and SaveRoom ignores it (the upsert names its columns), so it is
	// never written back. It exists because the seat list is the only thing that
	// can rebuild a room's participants after a restart: the ticket ids survive
	// in lobby_rooms, but the in-process tickets they point at do not.
	Members []RoomMemberRecord
}

// RoomMemberRecord is the durable copy of one seat in a room.
type RoomMemberRecord struct {
	RoomCode     string
	UserID       string
	TicketID     string
	SeatIndex    int
	IsHost       bool
	DeckSnapshot DeckSnapshot
	Loadout      PlayerLoadout
	BossID       string
	JoinedAt     time.Time
	LeftAt       time.Time
}

// --- SQL implementation ------------------------------------------------------

const insertRoomSQL = `
INSERT INTO lobby_rooms (
    room_code,
    mode_id,
    host_user_id,
    status,
    boss_id,
    stage_id,
    coop_players,
    match_id,
    mode_params_json,
    messages_json,
    ticket_ids_json,
    created_at,
    updated_at,
    expires_at,
    closed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12, $13, $14, $15
) ON CONFLICT (room_code) DO UPDATE SET
    mode_id = EXCLUDED.mode_id,
    host_user_id = EXCLUDED.host_user_id,
    status = EXCLUDED.status,
    boss_id = EXCLUDED.boss_id,
    stage_id = EXCLUDED.stage_id,
    coop_players = EXCLUDED.coop_players,
    match_id = EXCLUDED.match_id,
    mode_params_json = EXCLUDED.mode_params_json,
    messages_json = EXCLUDED.messages_json,
    ticket_ids_json = EXCLUDED.ticket_ids_json,
    updated_at = EXCLUDED.updated_at,
    expires_at = EXCLUDED.expires_at,
    closed_at = EXCLUDED.closed_at`

const selectRoomSQL = `
SELECT room_code, mode_id, host_user_id, status, boss_id, stage_id, coop_players,
       match_id, mode_params_json, messages_json, ticket_ids_json,
       created_at, updated_at, expires_at, closed_at
  FROM lobby_rooms
 WHERE room_code = $1`

const markRoomFoundSQL = `
UPDATE lobby_rooms
   SET status = $2, match_id = $3, updated_at = $4
 WHERE room_code = $1`

const closeRoomSQL = `
UPDATE lobby_rooms
   SET status = $2, closed_at = $3, updated_at = $3
 WHERE room_code = $1`

const listRoomsByStatusSQL = `
SELECT room_code, mode_id, host_user_id, status, boss_id, stage_id, coop_players,
       match_id, mode_params_json, messages_json, ticket_ids_json,
       created_at, updated_at, expires_at, closed_at
  FROM lobby_rooms
 WHERE status = ANY($1)
 ORDER BY updated_at ASC
 LIMIT $2`

const listExpiringRoomsSQL = `
SELECT room_code, mode_id, host_user_id, status, boss_id, stage_id, coop_players,
       match_id, mode_params_json, messages_json, ticket_ids_json,
       created_at, updated_at, expires_at, closed_at
  FROM lobby_rooms
 WHERE expires_at IS NOT NULL AND expires_at < $1
   AND status IN ('waiting', 'found')
 ORDER BY expires_at ASC
 LIMIT $2`

const upsertRoomMemberSQL = `
INSERT INTO lobby_room_members (
    room_code, user_id, ticket_id, seat_index, is_host,
    deck_snapshot_json, loadout_json, boss_id, joined_at, left_at
) VALUES (
    $1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10
) ON CONFLICT (room_code, user_id) DO UPDATE SET
    ticket_id = EXCLUDED.ticket_id,
    seat_index = EXCLUDED.seat_index,
    is_host = EXCLUDED.is_host,
    deck_snapshot_json = EXCLUDED.deck_snapshot_json,
    loadout_json = EXCLUDED.loadout_json,
    boss_id = EXCLUDED.boss_id,
    left_at = EXCLUDED.left_at`

const listRoomMembersSQL = `
SELECT room_code, user_id, ticket_id, seat_index, is_host,
       deck_snapshot_json, loadout_json, boss_id, joined_at, left_at
  FROM lobby_room_members
 WHERE room_code = $1
 ORDER BY seat_index ASC`

const removeRoomMemberSQL = `
UPDATE lobby_room_members
   SET left_at = $3
 WHERE room_code = $1 AND user_id = $2`

// SQLRoomStore persists rooms in Postgres.
type SQLRoomStore struct {
	db         *sql.DB
	statements roomStoreStatements
}

type roomStoreStatements struct {
	insertRoom        string
	selectRoom        string
	markRoomFound     string
	closeRoom         string
	listRoomsByStatus string
	listExpiringRooms string
	upsertMember      string
	listMembers       string
	removeMember      string
}

// RoomStoreOption rewrites the statements a store executes. Tests use it to
// capture SQL without a live database.
type RoomStoreOption func(*SQLRoomStore)

// NewSQLRoomStore returns a store backed by db.
func NewSQLRoomStore(db *sql.DB, options ...RoomStoreOption) (*SQLRoomStore, error) {
	if db == nil {
		return nil, errors.New("room store requires db")
	}
	store := &SQLRoomStore{db: db, statements: defaultRoomStoreStatements()}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

func defaultRoomStoreStatements() roomStoreStatements {
	return roomStoreStatements{
		insertRoom:        insertRoomSQL,
		selectRoom:        selectRoomSQL,
		markRoomFound:     markRoomFoundSQL,
		closeRoom:         closeRoomSQL,
		listRoomsByStatus: listRoomsByStatusSQL,
		listExpiringRooms: listExpiringRoomsSQL,
		upsertMember:      upsertRoomMemberSQL,
		listMembers:       listRoomMembersSQL,
		removeMember:      removeRoomMemberSQL,
	}
}

func withRoomStoreStatements(statements roomStoreStatements) RoomStoreOption {
	return func(store *SQLRoomStore) { store.statements = statements }
}

// NewRoomStore picks the SQL store when a database is configured and falls back
// to the process-local store otherwise, so the lobby keeps working without
// Postgres exactly as it does today.
func NewRoomStore(db *sql.DB) (RoomStore, error) {
	if db == nil {
		return NewMemoryRoomStore(), nil
	}
	return NewSQLRoomStore(db)
}

func (store *SQLRoomStore) SaveRoom(room RoomRecord) error {
	if store == nil || store.db == nil {
		return errors.New("room store is not configured")
	}
	modeParams, err := marshalRoomJSON(room.ModeParams)
	if err != nil {
		return err
	}
	messages, err := marshalRoomJSON(room.Messages)
	if err != nil {
		return err
	}
	ticketIDs, err := marshalRoomJSON(room.TicketIDs)
	if err != nil {
		return err
	}
	createdAt := room.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := room.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err = store.db.Exec(
		store.statements.insertRoom,
		room.RoomCode,
		room.ModeID,
		room.HostUserID,
		firstNonEmptyCore(room.Status, RoomStatusWaiting),
		room.BossID,
		room.StageID,
		room.CoopPlayers,
		room.MatchID,
		modeParams,
		messages,
		ticketIDs,
		createdAt,
		updatedAt,
		nullableTime(room.ExpiresAt),
		nullableTime(room.ClosedAt),
	)
	return err
}

func (store *SQLRoomStore) LoadRoom(roomCode string) (RoomRecord, bool, error) {
	if store == nil || store.db == nil {
		return RoomRecord{}, false, errors.New("room store is not configured")
	}
	row := store.db.QueryRow(store.statements.selectRoom, roomCode)
	room, err := scanRoomRecord(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RoomRecord{}, false, nil
		}
		return RoomRecord{}, false, err
	}
	return room, true, nil
}

func (store *SQLRoomStore) MarkRoomFound(roomCode string, matchID string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("room store is not configured")
	}
	stamp := at
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	_, err := store.db.Exec(store.statements.markRoomFound, roomCode, RoomStatusFound, matchID, stamp)
	return err
}

func (store *SQLRoomStore) CloseRoom(roomCode string, status string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("room store is not configured")
	}
	stamp := at
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	_, err := store.db.Exec(store.statements.closeRoom, roomCode, firstNonEmptyCore(status, RoomStatusClosed), stamp)
	return err
}

func (store *SQLRoomStore) ListRoomsByStatus(statuses []string, limit int) ([]RoomRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("room store is not configured")
	}
	rows, err := store.db.Query(store.statements.listRoomsByStatus, statusListForSQL(statuses), normalizeRoomLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRoomRecords(rows)
}

func (store *SQLRoomStore) ListExpiringRooms(before time.Time, limit int) ([]RoomRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("room store is not configured")
	}
	rows, err := store.db.Query(store.statements.listExpiringRooms, before, normalizeRoomLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRoomRecords(rows)
}

func (store *SQLRoomStore) SaveMember(member RoomMemberRecord) error {
	if store == nil || store.db == nil {
		return errors.New("room store is not configured")
	}
	deck, err := marshalRoomJSON(member.DeckSnapshot)
	if err != nil {
		return err
	}
	loadout, err := marshalRoomJSON(member.Loadout)
	if err != nil {
		return err
	}
	joinedAt := member.JoinedAt
	if joinedAt.IsZero() {
		joinedAt = time.Now().UTC()
	}
	_, err = store.db.Exec(
		store.statements.upsertMember,
		member.RoomCode,
		member.UserID,
		member.TicketID,
		member.SeatIndex,
		member.IsHost,
		deck,
		loadout,
		member.BossID,
		joinedAt,
		nullableTime(member.LeftAt),
	)
	return err
}

func (store *SQLRoomStore) ListMembers(roomCode string) ([]RoomMemberRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("room store is not configured")
	}
	rows, err := store.db.Query(store.statements.listMembers, roomCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []RoomMemberRecord{}
	for rows.Next() {
		member, err := scanRoomMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (store *SQLRoomStore) RemoveMember(roomCode string, userID string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("room store is not configured")
	}
	stamp := at
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	_, err := store.db.Exec(store.statements.removeMember, roomCode, userID, stamp)
	return err
}

type roomRowScanner interface {
	Scan(dest ...any) error
}

func collectRoomRecords(rows *sql.Rows) ([]RoomRecord, error) {
	rooms := []RoomRecord{}
	for rows.Next() {
		room, err := scanRoomRecord(rows)
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}

func scanRoomRecord(scanner roomRowScanner) (RoomRecord, error) {
	var (
		room       RoomRecord
		modeParams []byte
		messages   []byte
		ticketIDs  []byte
		expiresAt  sql.NullTime
		closedAt   sql.NullTime
	)
	err := scanner.Scan(
		&room.RoomCode,
		&room.ModeID,
		&room.HostUserID,
		&room.Status,
		&room.BossID,
		&room.StageID,
		&room.CoopPlayers,
		&room.MatchID,
		&modeParams,
		&messages,
		&ticketIDs,
		&room.CreatedAt,
		&room.UpdatedAt,
		&expiresAt,
		&closedAt,
	)
	if err != nil {
		return RoomRecord{}, err
	}
	room.ModeParams = map[string]any{}
	if len(modeParams) > 0 {
		if err := json.Unmarshal(modeParams, &room.ModeParams); err != nil {
			return RoomRecord{}, err
		}
	}
	if len(messages) > 0 {
		if err := json.Unmarshal(messages, &room.Messages); err != nil {
			return RoomRecord{}, err
		}
	}
	if len(ticketIDs) > 0 {
		if err := json.Unmarshal(ticketIDs, &room.TicketIDs); err != nil {
			return RoomRecord{}, err
		}
	}
	if expiresAt.Valid {
		room.ExpiresAt = expiresAt.Time
	}
	if closedAt.Valid {
		room.ClosedAt = closedAt.Time
	}
	return room, nil
}

func scanRoomMember(scanner roomRowScanner) (RoomMemberRecord, error) {
	var (
		member  RoomMemberRecord
		deck    []byte
		loadout []byte
		leftAt  sql.NullTime
	)
	err := scanner.Scan(
		&member.RoomCode,
		&member.UserID,
		&member.TicketID,
		&member.SeatIndex,
		&member.IsHost,
		&deck,
		&loadout,
		&member.BossID,
		&member.JoinedAt,
		&leftAt,
	)
	if err != nil {
		return RoomMemberRecord{}, err
	}
	if len(deck) > 0 {
		if err := json.Unmarshal(deck, &member.DeckSnapshot); err != nil {
			return RoomMemberRecord{}, err
		}
	}
	if len(loadout) > 0 {
		if err := json.Unmarshal(loadout, &member.Loadout); err != nil {
			return RoomMemberRecord{}, err
		}
	}
	if leftAt.Valid {
		member.LeftAt = leftAt.Time
	}
	return member, nil
}

func marshalRoomJSON(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(encoded) == 0 {
		return "{}", nil
	}
	return string(encoded), nil
}

func normalizeRoomLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	return limit
}

// statusListForSQL renders a status filter as a PostgreSQL array literal.
//
// pgx's database/sql driver cannot bind a Go []string as a query argument
// ("unsupported type []string, a slice of string"), so the filter travels as
// the array literal `{'waiting','found'}` and the statement casts it with
// `$1::text[]`. An empty filter returns nil, which the statement treats as
// "no filter" — the same semantics the in-memory store has, where an empty
// wanted-set matches every record.
func statusListForSQL(statuses []string) any {
	filtered := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if trimmed := strings.TrimSpace(status); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	// pgx's database/sql driver cannot bind a Go []string as a query argument
	// ("unsupported type []string, a slice of string"), so the filter travels as
	// a PostgreSQL array literal instead. The driver sends the argument with an
	// unknown type, so the server infers text[] from `= ANY($1)` on a text
	// column and parses the literal itself. An empty filter yields an empty
	// array, which matches no row.
	//
	// Elements are double-quoted on purpose. The value crosses the wire as one
	// string, so single quotes inside it are SQL string delimiters rather than
	// part of an array literal: `{'waiting'}` parses as something that matches
	// no row at all, which silently turns every status query into an empty
	// result. `{"waiting"}` is the array-literal form Postgres expects.
	// Backslashes and double quotes inside an element are escaped so a status
	// containing either cannot break out of the literal.
	parts := make([]string, 0, len(filtered))
	for _, status := range filtered {
		escaped := strings.ReplaceAll(status, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		parts = append(parts, "\""+escaped+"\"")
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// --- in-memory implementation ------------------------------------------------

// MemoryRoomStore keeps rooms in the process. It exists so the lobby can run
// without Postgres (and so tests can exercise the interface without a database);
// it is not a substitute for the SQL store.
type MemoryRoomStore struct {
	mu      sync.Mutex
	rooms   map[string]RoomRecord
	members map[string][]RoomMemberRecord
}

// NewMemoryRoomStore returns an empty process-local store.
func NewMemoryRoomStore() *MemoryRoomStore {
	return &MemoryRoomStore{
		rooms:   map[string]RoomRecord{},
		members: map[string][]RoomMemberRecord{},
	}
}

func (store *MemoryRoomStore) SaveRoom(room RoomRecord) error {
	if store == nil {
		return errors.New("room store is not configured")
	}
	if room.CreatedAt.IsZero() {
		room.CreatedAt = time.Now().UTC()
	}
	if room.UpdatedAt.IsZero() {
		room.UpdatedAt = room.CreatedAt
	}
	if room.Status == "" {
		room.Status = RoomStatusWaiting
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.rooms[room.RoomCode]; ok {
		room.CreatedAt = existing.CreatedAt
	}
	store.rooms[room.RoomCode] = room
	return nil
}

func (store *MemoryRoomStore) LoadRoom(roomCode string) (RoomRecord, bool, error) {
	if store == nil {
		return RoomRecord{}, false, errors.New("room store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	room, ok := store.rooms[roomCode]
	return room, ok, nil
}

func (store *MemoryRoomStore) MarkRoomFound(roomCode string, matchID string, at time.Time) error {
	if store == nil {
		return errors.New("room store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	room, ok := store.rooms[roomCode]
	if !ok {
		return nil
	}
	room.Status = RoomStatusFound
	room.MatchID = matchID
	room.UpdatedAt = stampOrNow(at)
	store.rooms[roomCode] = room
	return nil
}

func (store *MemoryRoomStore) CloseRoom(roomCode string, status string, at time.Time) error {
	if store == nil {
		return errors.New("room store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	room, ok := store.rooms[roomCode]
	if !ok {
		return nil
	}
	room.Status = firstNonEmptyCore(status, RoomStatusClosed)
	room.ClosedAt = stampOrNow(at)
	room.UpdatedAt = room.ClosedAt
	store.rooms[roomCode] = room
	return nil
}

func (store *MemoryRoomStore) ListRoomsByStatus(statuses []string, limit int) ([]RoomRecord, error) {
	if store == nil {
		return nil, errors.New("room store is not configured")
	}
	wanted := map[string]bool{}
	for _, status := range statuses {
		if trimmed := strings.TrimSpace(status); trimmed != "" {
			wanted[trimmed] = true
		}
	}
	store.mu.Lock()
	rooms := make([]RoomRecord, 0, len(store.rooms))
	for _, room := range store.rooms {
		if len(wanted) == 0 || wanted[room.Status] {
			rooms = append(rooms, room)
		}
	}
	store.mu.Unlock()
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].UpdatedAt.Before(rooms[j].UpdatedAt) })
	return limitRoomRecords(rooms, limit), nil
}

func (store *MemoryRoomStore) ListExpiringRooms(before time.Time, limit int) ([]RoomRecord, error) {
	if store == nil {
		return nil, errors.New("room store is not configured")
	}
	store.mu.Lock()
	rooms := []RoomRecord{}
	for _, room := range store.rooms {
		if room.ExpiresAt.IsZero() || !room.ExpiresAt.Before(before) {
			continue
		}
		if room.Status != RoomStatusWaiting && room.Status != RoomStatusFound {
			continue
		}
		rooms = append(rooms, room)
	}
	store.mu.Unlock()
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ExpiresAt.Before(rooms[j].ExpiresAt) })
	return limitRoomRecords(rooms, limit), nil
}

func (store *MemoryRoomStore) SaveMember(member RoomMemberRecord) error {
	if store == nil {
		return errors.New("room store is not configured")
	}
	if member.JoinedAt.IsZero() {
		member.JoinedAt = time.Now().UTC()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	members := store.members[member.RoomCode]
	replaced := false
	for i := range members {
		if members[i].UserID == member.UserID {
			members[i] = member
			replaced = true
			break
		}
	}
	if !replaced {
		members = append(members, member)
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].SeatIndex < members[j].SeatIndex })
	store.members[member.RoomCode] = members
	return nil
}

func (store *MemoryRoomStore) ListMembers(roomCode string) ([]RoomMemberRecord, error) {
	if store == nil {
		return nil, errors.New("room store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	members := append([]RoomMemberRecord{}, store.members[roomCode]...)
	return members, nil
}

func (store *MemoryRoomStore) RemoveMember(roomCode string, userID string, at time.Time) error {
	if store == nil {
		return errors.New("room store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	members := store.members[roomCode]
	for i := range members {
		if members[i].UserID == userID {
			members[i].LeftAt = stamp
		}
	}
	store.members[roomCode] = members
	return nil
}

func limitRoomRecords(rooms []RoomRecord, limit int) []RoomRecord {
	if limit > 0 && len(rooms) > limit {
		return rooms[:limit]
	}
	return rooms
}

func stampOrNow(stamp time.Time) time.Time {
	if stamp.IsZero() {
		return time.Now().UTC()
	}
	return stamp
}
