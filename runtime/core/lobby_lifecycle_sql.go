package core

import (
	"database/sql"
	"errors"
)

const insertLobbyRoomAuditSQL = `
INSERT INTO lobby_room_audits (
    room_code,
    action,
    mode_id,
    user_id,
    ticket_id,
    match_id,
    room_status,
    host_user_id,
    current_players,
    required_players,
    stage_id,
    ruleset_version,
    mode_ruleset_version,
    mode_config_hash,
    deck_snapshot_hash,
    created_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
) ON CONFLICT DO NOTHING`

const insertLobbyMessageAuditSQL = `
INSERT INTO lobby_message_audits (
    message_id,
    room_code,
    mode_id,
    kind,
    user_id,
    duplicate,
    text_length,
    metadata_hash,
    created_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) ON CONFLICT (message_id, room_code, user_id, duplicate) DO NOTHING`

type SQLLobbyLifecycleAuditRepository struct {
	db               *sql.DB
	insertRoomSQL    string
	insertMessageSQL string
}

type SQLLobbyLifecycleAuditRepositoryOption func(*SQLLobbyLifecycleAuditRepository)

func NewSQLLobbyLifecycleAuditRepository(db *sql.DB, options ...SQLLobbyLifecycleAuditRepositoryOption) (*SQLLobbyLifecycleAuditRepository, error) {
	if db == nil {
		return nil, errors.New("lobby lifecycle audit repository requires db")
	}
	repo := &SQLLobbyLifecycleAuditRepository{
		db:               db,
		insertRoomSQL:    insertLobbyRoomAuditSQL,
		insertMessageSQL: insertLobbyMessageAuditSQL,
	}
	for _, option := range options {
		option(repo)
	}
	return repo, nil
}

func withSQLLobbyLifecycleAuditStatements(roomSQL string, messageSQL string) SQLLobbyLifecycleAuditRepositoryOption {
	return func(repo *SQLLobbyLifecycleAuditRepository) {
		if roomSQL != "" {
			repo.insertRoomSQL = roomSQL
		}
		if messageSQL != "" {
			repo.insertMessageSQL = messageSQL
		}
	}
}

func (repo *SQLLobbyLifecycleAuditRepository) RecordLobbyRoomAudit(record LobbyRoomAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("lobby lifecycle audit repository is not configured")
	}
	_, err := repo.db.Exec(
		repo.insertRoomSQL,
		record.RoomCode,
		firstNonEmptyCore(record.Action, "unknown"),
		record.ModeID,
		record.UserID,
		record.TicketID,
		record.MatchID,
		record.RoomStatus,
		record.HostUserID,
		record.CurrentPlayers,
		record.RequiredPlayers,
		record.StageID,
		record.RulesetVersion,
		record.ModeRulesetVersion,
		record.ModeConfigHash,
		record.DeckSnapshotHash,
		record.CreatedAt,
		record.ServerAuthoritative,
	)
	return err
}

func (repo *SQLLobbyLifecycleAuditRepository) RecordLobbyMessageAudit(record LobbyMessageAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("lobby lifecycle audit repository is not configured")
	}
	_, err := repo.db.Exec(
		repo.insertMessageSQL,
		record.MessageID,
		record.RoomCode,
		record.ModeID,
		record.Kind,
		record.UserID,
		record.Duplicate,
		record.TextLength,
		record.MetadataHash,
		record.CreatedAt,
		record.ServerAuthoritative,
	)
	return err
}
