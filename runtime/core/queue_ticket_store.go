package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// QueueTicketRecord is the durable form of a lobby queue ticket.
//
// It is the lobby's own ticket -- the seat, deck, loadout and room binding a
// player holds while waiting -- as opposed to BattleTicketRecord, which is the
// signed ticket a battle server consumes exactly once. The two were easy to
// conflate because both are called "tickets"; they are not the same object and
// they have different lifetimes.
type QueueTicketRecord struct {
	TicketID     string
	UserID       string
	ModeID       string
	QueueKey     string
	RoomCode     string
	RoomStatus   string
	MatchID      string
	Status       string
	BossID       string
	CoopPlayers  int
	DeckSnapshot DeckSnapshot
	Loadout      PlayerLoadout
	CreatedAt    time.Time
	LastSeenAt   time.Time
}

// Queue ticket status values accepted by the queue_tickets check constraint.
const (
	QueueTicketStatusQueued    = "queued"
	QueueTicketStatusFound     = "found"
	QueueTicketStatusCancelled = "cancelled"
)

// QueueTicketStore is the persistent home of lobby queue tickets.
//
// The lobby keeps an in-process cache in front of it; the store is what makes a
// ticket outlive the process, so a player who reconnects after a restart is
// recognised as the same seat instead of queueing twice.
type QueueTicketStore interface {
	// Save inserts or updates a ticket. An empty Status defaults to 'queued'.
	Save(record QueueTicketRecord) error
	Load(ticketID string) (QueueTicketRecord, bool, error)
	ListByRoom(roomCode string) ([]QueueTicketRecord, error)
	ListByMatch(matchID string) ([]QueueTicketRecord, error)
	// ListLiveByUser returns the user's queued ticket, if any. The join path
	// asks this so a rejoin after a restart lands on the existing seat.
	ListLiveByUser(userID string) (QueueTicketRecord, bool, error)
	// ListActive returns non-cancelled tickets, oldest first, for sweeps.
	ListActive(limit int) ([]QueueTicketRecord, error)
	Delete(ticketID string) error
}

// --- SQL implementation ------------------------------------------------------

const insertQueueTicketSQL = `
INSERT INTO queue_tickets (
    ticket_id, user_id, mode_id, queue_key, room_code, room_status,
    match_id, status, boss_id, coop_players,
    deck_snapshot_json, loadout_json, created_at, last_seen_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    $11::jsonb, $12::jsonb, $13, $14, now()
) ON CONFLICT (ticket_id) DO UPDATE SET
    mode_id = EXCLUDED.mode_id,
    queue_key = EXCLUDED.queue_key,
    room_code = EXCLUDED.room_code,
    room_status = EXCLUDED.room_status,
    match_id = EXCLUDED.match_id,
    status = EXCLUDED.status,
    boss_id = EXCLUDED.boss_id,
    coop_players = EXCLUDED.coop_players,
    deck_snapshot_json = EXCLUDED.deck_snapshot_json,
    loadout_json = EXCLUDED.loadout_json,
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at = now()`

const selectQueueTicketSQL = `
SELECT ticket_id, user_id, mode_id, queue_key, room_code, room_status,
       match_id, status, boss_id, coop_players,
       deck_snapshot_json, loadout_json, created_at, last_seen_at
  FROM queue_tickets
 WHERE ticket_id = $1`

const queueTicketColumns = `
SELECT ticket_id, user_id, mode_id, queue_key, room_code, room_status,
       match_id, status, boss_id, coop_players,
       deck_snapshot_json, loadout_json, created_at, last_seen_at
  FROM queue_tickets`

const listQueueTicketsByRoomSQL = queueTicketColumns + `
 WHERE room_code = $1
 ORDER BY created_at ASC`

const listQueueTicketsByMatchSQL = queueTicketColumns + `
 WHERE match_id = $1
 ORDER BY created_at ASC`

const listLiveQueueTicketByUserSQL = queueTicketColumns + `
 WHERE user_id = $1 AND status = 'queued'
 ORDER BY created_at DESC
 LIMIT 1`

const listActiveQueueTicketsSQL = queueTicketColumns + `
 WHERE status <> 'cancelled'
 ORDER BY created_at ASC
 LIMIT $1`

const deleteQueueTicketSQL = `DELETE FROM queue_tickets WHERE ticket_id = $1`

// SQLQueueTicketStore persists queue tickets in Postgres.
type SQLQueueTicketStore struct {
	db         *sql.DB
	statements queueTicketStatements
}

type queueTicketStatements struct {
	upsert     string
	selectOne  string
	byRoom     string
	byMatch    string
	liveByUser string
	active     string
	delete     string
}

func NewQueueTicketStore(db *sql.DB, options ...func(*queueTicketStatements)) (*SQLQueueTicketStore, error) {
	if db == nil {
		return nil, errors.New("queue ticket store requires db")
	}
	statements := queueTicketStatements{
		upsert:     insertQueueTicketSQL,
		selectOne:  selectQueueTicketSQL,
		byRoom:     listQueueTicketsByRoomSQL,
		byMatch:    listQueueTicketsByMatchSQL,
		liveByUser: listLiveQueueTicketByUserSQL,
		active:     listActiveQueueTicketsSQL,
		delete:     deleteQueueTicketSQL,
	}
	for _, option := range options {
		if option != nil {
			option(&statements)
		}
	}
	return &SQLQueueTicketStore{db: db, statements: statements}, nil
}

func (store *SQLQueueTicketStore) Save(record QueueTicketRecord) error {
	if store == nil || store.db == nil {
		return errors.New("queue ticket store is not configured")
	}
	if record.Status == "" {
		record.Status = QueueTicketStatusQueued
	}
	deck, err := marshalRoomJSON(record.DeckSnapshot)
	if err != nil {
		return err
	}
	loadout, err := marshalRoomJSON(record.Loadout)
	if err != nil {
		return err
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	lastSeenAt := record.LastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = createdAt
	}
	_, err = store.db.Exec(
		store.statements.upsert,
		record.TicketID,
		record.UserID,
		record.ModeID,
		record.QueueKey,
		record.RoomCode,
		record.RoomStatus,
		record.MatchID,
		record.Status,
		record.BossID,
		record.CoopPlayers,
		deck,
		loadout,
		createdAt,
		lastSeenAt,
	)
	return err
}

func (store *SQLQueueTicketStore) Load(ticketID string) (QueueTicketRecord, bool, error) {
	if store == nil || store.db == nil {
		return QueueTicketRecord{}, false, errors.New("queue ticket store is not configured")
	}
	row := store.db.QueryRow(store.statements.selectOne, ticketID)
	record, err := scanQueueTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueTicketRecord{}, false, nil
	}
	if err != nil {
		return QueueTicketRecord{}, false, err
	}
	return record, true, nil
}

func (store *SQLQueueTicketStore) ListByRoom(roomCode string) ([]QueueTicketRecord, error) {
	return store.query(store.statements.byRoom, roomCode)
}

func (store *SQLQueueTicketStore) ListByMatch(matchID string) ([]QueueTicketRecord, error) {
	return store.query(store.statements.byMatch, matchID)
}

func (store *SQLQueueTicketStore) ListLiveByUser(userID string) (QueueTicketRecord, bool, error) {
	if store == nil || store.db == nil {
		return QueueTicketRecord{}, false, errors.New("queue ticket store is not configured")
	}
	row := store.db.QueryRow(store.statements.liveByUser, userID)
	record, err := scanQueueTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueTicketRecord{}, false, nil
	}
	if err != nil {
		return QueueTicketRecord{}, false, err
	}
	return record, true, nil
}

func (store *SQLQueueTicketStore) ListActive(limit int) ([]QueueTicketRecord, error) {
	if limit <= 0 {
		limit = DefaultSweepLimit
	}
	return store.query(store.statements.active, limit)
}

func (store *SQLQueueTicketStore) Delete(ticketID string) error {
	if store == nil || store.db == nil {
		return errors.New("queue ticket store is not configured")
	}
	_, err := store.db.Exec(store.statements.delete, ticketID)
	return err
}

func (store *SQLQueueTicketStore) query(statement string, argument any) ([]QueueTicketRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("queue ticket store is not configured")
	}
	rows, err := store.db.Query(statement, argument)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]QueueTicketRecord, 0, 8)
	for rows.Next() {
		record, err := scanQueueTicket(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type queueTicketScanner interface {
	Scan(dest ...any) error
}

func scanQueueTicket(scanner queueTicketScanner) (QueueTicketRecord, error) {
	var record QueueTicketRecord
	var deckJSON []byte
	var loadoutJSON []byte
	err := scanner.Scan(
		&record.TicketID,
		&record.UserID,
		&record.ModeID,
		&record.QueueKey,
		&record.RoomCode,
		&record.RoomStatus,
		&record.MatchID,
		&record.Status,
		&record.BossID,
		&record.CoopPlayers,
		&deckJSON,
		&loadoutJSON,
		&record.CreatedAt,
		&record.LastSeenAt,
	)
	if err != nil {
		return QueueTicketRecord{}, err
	}
	if len(deckJSON) > 0 {
		if err := json.Unmarshal(deckJSON, &record.DeckSnapshot); err != nil {
			return QueueTicketRecord{}, err
		}
	}
	if len(loadoutJSON) > 0 {
		if err := json.Unmarshal(loadoutJSON, &record.Loadout); err != nil {
			return QueueTicketRecord{}, err
		}
	}
	return record, nil
}

// --- in-memory implementation ----------------------------------------------

// MemoryQueueTicketStore is the fallback used when no database is configured.
// It behaves like the pre-persistence map so tests and the standalone lobby keep
// working unchanged.
type MemoryQueueTicketStore struct {
	mu      sync.RWMutex
	tickets map[string]QueueTicketRecord
}

func NewMemoryQueueTicketStore() *MemoryQueueTicketStore {
	return &MemoryQueueTicketStore{tickets: map[string]QueueTicketRecord{}}
}

func (store *MemoryQueueTicketStore) Save(record QueueTicketRecord) error {
	if store == nil {
		return errors.New("queue ticket store is not configured")
	}
	if record.Status == "" {
		record.Status = QueueTicketStatusQueued
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = record.CreatedAt
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.tickets[record.TicketID] = record
	return nil
}

func (store *MemoryQueueTicketStore) Load(ticketID string) (QueueTicketRecord, bool, error) {
	if store == nil {
		return QueueTicketRecord{}, false, errors.New("queue ticket store is not configured")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.tickets[ticketID]
	return record, ok, nil
}

func (store *MemoryQueueTicketStore) ListByRoom(roomCode string) ([]QueueTicketRecord, error) {
	return store.filter(func(record QueueTicketRecord) bool { return record.RoomCode == roomCode })
}

func (store *MemoryQueueTicketStore) ListByMatch(matchID string) ([]QueueTicketRecord, error) {
	return store.filter(func(record QueueTicketRecord) bool { return record.MatchID == matchID })
}

func (store *MemoryQueueTicketStore) ListLiveByUser(userID string) (QueueTicketRecord, bool, error) {
	records, err := store.filter(func(record QueueTicketRecord) bool {
		return record.UserID == userID && record.Status == QueueTicketStatusQueued
	})
	if err != nil || len(records) == 0 {
		return QueueTicketRecord{}, false, err
	}
	return records[len(records)-1], true, nil
}

func (store *MemoryQueueTicketStore) ListActive(limit int) ([]QueueTicketRecord, error) {
	records, err := store.filter(func(record QueueTicketRecord) bool {
		return record.Status != QueueTicketStatusCancelled
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (store *MemoryQueueTicketStore) Delete(ticketID string) error {
	if store == nil {
		return errors.New("queue ticket store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.tickets, ticketID)
	return nil
}

func (store *MemoryQueueTicketStore) filter(match func(QueueTicketRecord) bool) ([]QueueTicketRecord, error) {
	if store == nil {
		return nil, errors.New("queue ticket store is not configured")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]QueueTicketRecord, 0, len(store.tickets))
	for _, record := range store.tickets {
		if match(record) {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].TicketID < records[j].TicketID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

// --- conversion -------------------------------------------------------------

// queueTicketRecordFromTicket projects the in-process ticket into its durable
// form. Every field the lobby needs to recognise a returning seat travels here.
func queueTicketRecordFromTicket(ticket *queueTicket) QueueTicketRecord {
	if ticket == nil {
		return QueueTicketRecord{}
	}
	return QueueTicketRecord{
		TicketID:     ticket.TicketID,
		UserID:       ticket.UserID,
		ModeID:       ticket.ModeID,
		QueueKey:     ticket.QueueKey,
		RoomCode:     ticket.RoomCode,
		RoomStatus:   ticket.RoomStatus,
		MatchID:      ticket.MatchID,
		Status:       ticket.Status,
		BossID:       ticket.BossID,
		CoopPlayers:  ticket.CoopPlayers,
		DeckSnapshot: ticket.DeckSnapshot,
		Loadout:      ticket.Loadout,
		CreatedAt:    ticket.CreatedAt,
		LastSeenAt:   ticket.LastSeenAt,
	}
}

func (record QueueTicketRecord) toQueueTicket() queueTicket {
	return queueTicket{
		TicketID:     record.TicketID,
		UserID:       record.UserID,
		ModeID:       record.ModeID,
		QueueKey:     record.QueueKey,
		RoomCode:     record.RoomCode,
		RoomStatus:   record.RoomStatus,
		MatchID:      record.MatchID,
		Status:       record.Status,
		BossID:       record.BossID,
		CoopPlayers:  record.CoopPlayers,
		DeckSnapshot: record.DeckSnapshot,
		Loadout:      record.Loadout,
		CreatedAt:    record.CreatedAt,
		LastSeenAt:   record.LastSeenAt,
	}
}
