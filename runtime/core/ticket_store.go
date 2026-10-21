package core

import (
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"
)

// Ticket errors. Consume only succeeds while the ticket is still 'issued', so a
// replayed consume over a restarted lobby is rejected instead of silently
// re-using the ticket.
var (
	// ErrTicketNotFound is returned when a ticket id was never issued.
	ErrTicketNotFound = errors.New("battle ticket not found")
	// ErrTicketNotIssued is returned when the ticket exists but is no longer in
	// the 'issued' state (already consumed, expired, revoked or rejected).
	ErrTicketNotIssued = errors.New("battle ticket is no longer issuable")
)

// BattleTicketStore is the mutable counterpart of the battle_ticket_audits
// audit trail: it is what lets a restarted lobby still consume a ticket exactly
// once.
type BattleTicketStore interface {
	Issue(ticket BattleTicketRecord) error
	Load(ticketID string) (BattleTicketRecord, bool, error)
	// Consume moves an issued ticket to 'consumed'. It fails when the ticket
	// was already consumed or never existed.
	Consume(ticketID string, at time.Time) error
	// Reject records a refused consume attempt with its reason.
	Reject(ticketID string, reason string, at time.Time) error
	// Revoke retires a still-issued ticket because its battle allocation
	// changed (for example the endpoint moved after the agent bound a
	// different port). Unlike Reject it marks the ticket 'revoked' rather
	// than 'rejected', keeping "the client asked wrong" and "the lobby
	// invalidated it" distinguishable in the audit trail.
	Revoke(ticketID string, reason string, at time.Time) error
	// Expire marks tickets past their TTL. Returns the number expired.
	Expire(before time.Time, limit int) (int, error)
	ListByMatch(matchID string) ([]BattleTicketRecord, error)
}

// Battle ticket status values accepted by the battle_tickets check constraint.
const (
	TicketStatusIssued   = "issued"
	TicketStatusConsumed = "consumed"
	TicketStatusExpired  = "expired"
	TicketStatusRevoked  = "revoked"
	TicketStatusRejected = "rejected"
)

// BattleTicketRecord is the durable state of one signed battle ticket.
type BattleTicketRecord struct {
	TicketID           string
	MatchID            string
	UserID             string
	PlayerID           string
	ModeID             string
	BattleServerID     string
	Endpoint           string
	KeyID              string
	SignatureHex       string
	NonceHex           string
	RulesetVersion     string
	ProtocolVersion    string
	BusinessAPIVersion string
	BattleAPIVersion   string
	ModeConfigHash     string
	DeckSnapshotHash   string
	// TicketJSON is the exact signed envelope handed to the battle server, kept
	// so a restarted lobby can re-verify a late callback.
	TicketJSON          string
	Status              string
	RejectReason        string
	IssuedAt            time.Time
	ExpiresAt           time.Time
	ConsumedAt          time.Time
	UpdatedAt           time.Time
	ServerAuthoritative bool
}

// --- SQL implementation ------------------------------------------------------

const insertBattleTicketSQL = `
INSERT INTO battle_tickets (
    ticket_id,
    match_id,
    user_id,
    player_id,
    mode_id,
    battle_server_id,
    endpoint,
    key_id,
    signature_hex,
    nonce_hex,
    ruleset_version,
    protocol_version,
    business_api_version,
    battle_api_version,
    mode_config_hash,
    deck_snapshot_hash,
    ticket_json,
    status,
    reject_reason,
    issued_at,
    expires_at,
    consumed_at,
    updated_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17::jsonb, $18, $19, $20, $21, $22, $23, $24
) ON CONFLICT (ticket_id) DO UPDATE SET
    status = EXCLUDED.status,
    reject_reason = EXCLUDED.reject_reason,
    battle_server_id = EXCLUDED.battle_server_id,
    endpoint = EXCLUDED.endpoint,
    expires_at = EXCLUDED.expires_at,
    consumed_at = COALESCE(EXCLUDED.consumed_at, battle_tickets.consumed_at),
    updated_at = EXCLUDED.updated_at`

const battleTicketColumnsSQL = `ticket_id, match_id, user_id, player_id, mode_id,
       battle_server_id, endpoint, key_id, signature_hex, nonce_hex,
       ruleset_version, protocol_version, business_api_version, battle_api_version,
       mode_config_hash, deck_snapshot_hash, ticket_json, status, reject_reason,
       issued_at, expires_at, consumed_at, updated_at, server_authoritative`

const selectBattleTicketSQL = `
SELECT ` + battleTicketColumnsSQL + `
  FROM battle_tickets
 WHERE ticket_id = $1`

// The status guard is the whole point: a concurrent or replayed consume loses.
const consumeBattleTicketSQL = `
UPDATE battle_tickets
   SET status = $2, consumed_at = $3, updated_at = $3
 WHERE ticket_id = $1 AND status = 'issued'`

const rejectBattleTicketSQL = `
UPDATE battle_tickets
   SET status = $2, reject_reason = $3, updated_at = $4
 WHERE ticket_id = $1`

const revokeBattleTicketSQL = `
UPDATE battle_tickets
   SET status = $2, reject_reason = $3, updated_at = $4
 WHERE ticket_id = $1 AND status = 'issued'`

const expireBattleTicketsSQL = `
UPDATE battle_tickets
   SET status = $2, updated_at = $1
 WHERE ticket_id IN (
       SELECT ticket_id FROM battle_tickets
        WHERE status = 'issued' AND expires_at < $1
        ORDER BY expires_at ASC
        LIMIT $3
   )`

const listBattleTicketsByMatchSQL = `
SELECT ` + battleTicketColumnsSQL + `
  FROM battle_tickets
 WHERE match_id = $1
 ORDER BY issued_at ASC`

// SQLBattleTicketStore persists battle tickets in Postgres.
type SQLBattleTicketStore struct {
	db         *sql.DB
	statements ticketStoreStatements
}

type ticketStoreStatements struct {
	insertTicket  string
	selectTicket  string
	consumeTicket string
	rejectTicket  string
	revokeTicket  string
	expireTickets string
	listByMatch   string
}

// TicketStoreOption rewrites the statements a store executes.
type TicketStoreOption func(*SQLBattleTicketStore)

// NewSQLBattleTicketStore returns a store backed by db.
func NewSQLBattleTicketStore(db *sql.DB, options ...TicketStoreOption) (*SQLBattleTicketStore, error) {
	if db == nil {
		return nil, errors.New("battle ticket store requires db")
	}
	store := &SQLBattleTicketStore{db: db, statements: defaultTicketStoreStatements()}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

func defaultTicketStoreStatements() ticketStoreStatements {
	return ticketStoreStatements{
		insertTicket:  insertBattleTicketSQL,
		selectTicket:  selectBattleTicketSQL,
		consumeTicket: consumeBattleTicketSQL,
		rejectTicket:  rejectBattleTicketSQL,
		revokeTicket:  revokeBattleTicketSQL,
		expireTickets: expireBattleTicketsSQL,
		listByMatch:   listBattleTicketsByMatchSQL,
	}
}

func withTicketStoreStatements(statements ticketStoreStatements) TicketStoreOption {
	return func(store *SQLBattleTicketStore) { store.statements = statements }
}

// NewBattleTicketStore picks the SQL store when a database is configured and the
// process-local store otherwise.
func NewBattleTicketStore(db *sql.DB) (BattleTicketStore, error) {
	if db == nil {
		return NewMemoryBattleTicketStore(), nil
	}
	return NewSQLBattleTicketStore(db)
}

func (store *SQLBattleTicketStore) Issue(ticket BattleTicketRecord) error {
	if store == nil || store.db == nil {
		return errors.New("battle ticket store is not configured")
	}
	issuedAt := ticket.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = time.Now().UTC()
	}
	updatedAt := ticket.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = issuedAt
	}
	_, err := store.db.Exec(
		store.statements.insertTicket,
		ticket.TicketID,
		ticket.MatchID,
		ticket.UserID,
		ticket.PlayerID,
		ticket.ModeID,
		ticket.BattleServerID,
		ticket.Endpoint,
		ticket.KeyID,
		ticket.SignatureHex,
		ticket.NonceHex,
		ticket.RulesetVersion,
		ticket.ProtocolVersion,
		ticket.BusinessAPIVersion,
		ticket.BattleAPIVersion,
		ticket.ModeConfigHash,
		ticket.DeckSnapshotHash,
		firstNonEmptyCore(ticket.TicketJSON, "{}"),
		firstNonEmptyCore(ticket.Status, TicketStatusIssued),
		ticket.RejectReason,
		issuedAt,
		ticket.ExpiresAt,
		nullableTime(ticket.ConsumedAt),
		updatedAt,
		ticket.ServerAuthoritative,
	)
	return err
}

func (store *SQLBattleTicketStore) Load(ticketID string) (BattleTicketRecord, bool, error) {
	if store == nil || store.db == nil {
		return BattleTicketRecord{}, false, errors.New("battle ticket store is not configured")
	}
	ticket, err := scanBattleTicket(store.db.QueryRow(store.statements.selectTicket, ticketID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BattleTicketRecord{}, false, nil
		}
		return BattleTicketRecord{}, false, err
	}
	return ticket, true, nil
}

func (store *SQLBattleTicketStore) Consume(ticketID string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("battle ticket store is not configured")
	}
	stamp := stampOrNow(at)
	result, err := store.db.Exec(store.statements.consumeTicket, ticketID, TicketStatusConsumed, stamp)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	// Distinguish "never issued" from "already consumed" on the failure path
	// only, so callers can log the real reason.
	if _, ok, err := store.Load(ticketID); err == nil && !ok {
		return ErrTicketNotFound
	}
	return ErrTicketNotIssued
}

func (store *SQLBattleTicketStore) Reject(ticketID string, reason string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("battle ticket store is not configured")
	}
	_, err := store.db.Exec(store.statements.rejectTicket, ticketID, TicketStatusRejected, reason, stampOrNow(at))
	return err
}

func (store *SQLBattleTicketStore) Revoke(ticketID string, reason string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("battle ticket store is not configured")
	}
	_, err := store.db.Exec(store.statements.revokeTicket, ticketID, TicketStatusRevoked, reason, stampOrNow(at))
	return err
}

func (store *SQLBattleTicketStore) Expire(before time.Time, limit int) (int, error) {
	if store == nil || store.db == nil {
		return 0, errors.New("battle ticket store is not configured")
	}
	result, err := store.db.Exec(store.statements.expireTickets, before, TicketStatusExpired, normalizeRoomLimit(limit))
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (store *SQLBattleTicketStore) ListByMatch(matchID string) ([]BattleTicketRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("battle ticket store is not configured")
	}
	rows, err := store.db.Query(store.statements.listByMatch, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := []BattleTicketRecord{}
	for rows.Next() {
		ticket, err := scanBattleTicket(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

func scanBattleTicket(scanner roomRowScanner) (BattleTicketRecord, error) {
	var (
		ticket     BattleTicketRecord
		consumedAt sql.NullTime
	)
	err := scanner.Scan(
		&ticket.TicketID,
		&ticket.MatchID,
		&ticket.UserID,
		&ticket.PlayerID,
		&ticket.ModeID,
		&ticket.BattleServerID,
		&ticket.Endpoint,
		&ticket.KeyID,
		&ticket.SignatureHex,
		&ticket.NonceHex,
		&ticket.RulesetVersion,
		&ticket.ProtocolVersion,
		&ticket.BusinessAPIVersion,
		&ticket.BattleAPIVersion,
		&ticket.ModeConfigHash,
		&ticket.DeckSnapshotHash,
		&ticket.TicketJSON,
		&ticket.Status,
		&ticket.RejectReason,
		&ticket.IssuedAt,
		&ticket.ExpiresAt,
		&consumedAt,
		&ticket.UpdatedAt,
		&ticket.ServerAuthoritative,
	)
	if err != nil {
		return BattleTicketRecord{}, err
	}
	if consumedAt.Valid {
		ticket.ConsumedAt = consumedAt.Time
	}
	return ticket, nil
}

// --- in-memory implementation ------------------------------------------------

// MemoryBattleTicketStore keeps tickets in the process.
type MemoryBattleTicketStore struct {
	mu      sync.Mutex
	tickets map[string]BattleTicketRecord
}

// NewMemoryBattleTicketStore returns an empty process-local store.
func NewMemoryBattleTicketStore() *MemoryBattleTicketStore {
	return &MemoryBattleTicketStore{tickets: map[string]BattleTicketRecord{}}
}

func (store *MemoryBattleTicketStore) Issue(ticket BattleTicketRecord) error {
	if store == nil {
		return errors.New("battle ticket store is not configured")
	}
	if ticket.IssuedAt.IsZero() {
		ticket.IssuedAt = time.Now().UTC()
	}
	if ticket.UpdatedAt.IsZero() {
		ticket.UpdatedAt = ticket.IssuedAt
	}
	if ticket.Status == "" {
		ticket.Status = TicketStatusIssued
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.tickets[ticket.TicketID]; ok && !existing.ConsumedAt.IsZero() {
		ticket.ConsumedAt = existing.ConsumedAt
	}
	store.tickets[ticket.TicketID] = ticket
	return nil
}

func (store *MemoryBattleTicketStore) Load(ticketID string) (BattleTicketRecord, bool, error) {
	if store == nil {
		return BattleTicketRecord{}, false, errors.New("battle ticket store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ticket, ok := store.tickets[ticketID]
	return ticket, ok, nil
}

func (store *MemoryBattleTicketStore) Consume(ticketID string, at time.Time) error {
	if store == nil {
		return errors.New("battle ticket store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	ticket, ok := store.tickets[ticketID]
	if !ok {
		return ErrTicketNotFound
	}
	if ticket.Status != TicketStatusIssued {
		return ErrTicketNotIssued
	}
	ticket.Status = TicketStatusConsumed
	ticket.ConsumedAt = stamp
	ticket.UpdatedAt = stamp
	store.tickets[ticketID] = ticket
	return nil
}

func (store *MemoryBattleTicketStore) Reject(ticketID string, reason string, at time.Time) error {
	if store == nil {
		return errors.New("battle ticket store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	ticket, ok := store.tickets[ticketID]
	if !ok {
		return ErrTicketNotFound
	}
	ticket.Status = TicketStatusRejected
	ticket.RejectReason = reason
	ticket.UpdatedAt = stamp
	store.tickets[ticketID] = ticket
	return nil
}

func (store *MemoryBattleTicketStore) Revoke(ticketID string, reason string, at time.Time) error {
	if store == nil {
		return errors.New("battle ticket store is not configured")
	}
	stamp := stampOrNow(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	ticket, ok := store.tickets[ticketID]
	if !ok {
		return ErrTicketNotFound
	}
	if ticket.Status != TicketStatusIssued {
		return nil
	}
	ticket.Status = TicketStatusRevoked
	ticket.RejectReason = reason
	ticket.UpdatedAt = stamp
	store.tickets[ticketID] = ticket
	return nil
}

func (store *MemoryBattleTicketStore) Expire(before time.Time, limit int) (int, error) {
	if store == nil {
		return 0, errors.New("battle ticket store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	expired := 0
	for id, ticket := range store.tickets {
		if ticket.Status != TicketStatusIssued || ticket.ExpiresAt.IsZero() || !ticket.ExpiresAt.Before(before) {
			continue
		}
		if limit > 0 && expired >= limit {
			break
		}
		ticket.Status = TicketStatusExpired
		ticket.UpdatedAt = before
		store.tickets[id] = ticket
		expired++
	}
	return expired, nil
}

func (store *MemoryBattleTicketStore) ListByMatch(matchID string) ([]BattleTicketRecord, error) {
	if store == nil {
		return nil, errors.New("battle ticket store is not configured")
	}
	store.mu.Lock()
	tickets := []BattleTicketRecord{}
	for _, ticket := range store.tickets {
		if ticket.MatchID == matchID {
			tickets = append(tickets, ticket)
		}
	}
	store.mu.Unlock()
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].IssuedAt.Before(tickets[j].IssuedAt) })
	return tickets, nil
}

// --- idempotency -------------------------------------------------------------

// IdempotencyStore marks a write so it can happen exactly once even across a
// restart. Redis carries the hot path; this is the durable copy.
type IdempotencyStore interface {
	// Claim returns true when this caller owns the operation.
	Claim(record IdempotencyRecord) (bool, error)
	// Complete stores the cached response.
	Complete(scope string, key string, response string, at time.Time) error
	Load(scope string, key string) (IdempotencyRecord, bool, error)
}

// Idempotency status values accepted by the idempotency_keys check constraint.
const (
	IdempotencyInProgress = "in_progress"
	IdempotencyCompleted  = "completed"
)

// IdempotencyRecord is one durable exactly-once marker.
type IdempotencyRecord struct {
	Scope              string
	Key                string
	RequestFingerprint string
	Response           string
	Status             string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	CompletedAt        time.Time
}

const insertIdempotencySQL = `
INSERT INTO idempotency_keys (
    scope, idempotency_key, request_fingerprint, response_json, status,
    created_at, expires_at, completed_at
) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
ON CONFLICT (scope, idempotency_key) DO NOTHING`

const completeIdempotencySQL = `
UPDATE idempotency_keys
   SET status = $3, response_json = $4::jsonb, completed_at = $5
 WHERE scope = $1 AND idempotency_key = $2 AND status = 'in_progress'`

const selectIdempotencySQL = `
SELECT scope, idempotency_key, request_fingerprint, response_json, status,
       created_at, expires_at, completed_at
  FROM idempotency_keys
 WHERE scope = $1 AND idempotency_key = $2`

// SQLIdempotencyStore persists idempotency markers in Postgres.
type SQLIdempotencyStore struct {
	db *sql.DB
}

// NewSQLIdempotencyStore returns a store backed by db.
func NewSQLIdempotencyStore(db *sql.DB) (*SQLIdempotencyStore, error) {
	if db == nil {
		return nil, errors.New("idempotency store requires db")
	}
	return &SQLIdempotencyStore{db: db}, nil
}

// NewIdempotencyStore picks the SQL store when a database is configured and the
// process-local store otherwise.
func NewIdempotencyStore(db *sql.DB) (IdempotencyStore, error) {
	if db == nil {
		return NewMemoryIdempotencyStore(), nil
	}
	return NewSQLIdempotencyStore(db)
}

func (store *SQLIdempotencyStore) Claim(record IdempotencyRecord) (bool, error) {
	if store == nil || store.db == nil {
		return false, errors.New("idempotency store is not configured")
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	result, err := store.db.Exec(
		insertIdempotencySQL,
		record.Scope,
		record.Key,
		record.RequestFingerprint,
		firstNonEmptyCore(record.Response, "{}"),
		firstNonEmptyCore(record.Status, IdempotencyInProgress),
		createdAt,
		record.ExpiresAt,
		nullableTime(record.CompletedAt),
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (store *SQLIdempotencyStore) Complete(scope string, key string, response string, at time.Time) error {
	if store == nil || store.db == nil {
		return errors.New("idempotency store is not configured")
	}
	_, err := store.db.Exec(completeIdempotencySQL, scope, key, IdempotencyCompleted, firstNonEmptyCore(response, "{}"), stampOrNow(at))
	return err
}

func (store *SQLIdempotencyStore) Load(scope string, key string) (IdempotencyRecord, bool, error) {
	if store == nil || store.db == nil {
		return IdempotencyRecord{}, false, errors.New("idempotency store is not configured")
	}
	var (
		record      IdempotencyRecord
		completedAt sql.NullTime
	)
	err := store.db.QueryRow(selectIdempotencySQL, scope, key).Scan(
		&record.Scope,
		&record.Key,
		&record.RequestFingerprint,
		&record.Response,
		&record.Status,
		&record.CreatedAt,
		&record.ExpiresAt,
		&completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdempotencyRecord{}, false, nil
		}
		return IdempotencyRecord{}, false, err
	}
	if completedAt.Valid {
		record.CompletedAt = completedAt.Time
	}
	return record, true, nil
}

// MemoryIdempotencyStore keeps idempotency markers in the process.
type MemoryIdempotencyStore struct {
	mu      sync.Mutex
	records map[string]IdempotencyRecord
}

// NewMemoryIdempotencyStore returns an empty process-local store.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{records: map[string]IdempotencyRecord{}}
}

func (store *MemoryIdempotencyStore) Claim(record IdempotencyRecord) (bool, error) {
	if store == nil {
		return false, errors.New("idempotency store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := record.Scope + "\x00" + record.Key
	if _, ok := store.records[id]; ok {
		return false, nil
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.Status == "" {
		record.Status = IdempotencyInProgress
	}
	store.records[id] = record
	return true, nil
}

func (store *MemoryIdempotencyStore) Complete(scope string, key string, response string, at time.Time) error {
	if store == nil {
		return errors.New("idempotency store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := scope + "\x00" + key
	record, ok := store.records[id]
	if !ok {
		return errors.New("idempotency key was never claimed")
	}
	record.Status = IdempotencyCompleted
	record.Response = response
	record.CompletedAt = stampOrNow(at)
	store.records[id] = record
	return nil
}

func (store *MemoryIdempotencyStore) Load(scope string, key string) (IdempotencyRecord, bool, error) {
	if store == nil {
		return IdempotencyRecord{}, false, errors.New("idempotency store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[scope+"\x00"+key]
	return record, ok, nil
}
