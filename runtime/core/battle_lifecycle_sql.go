package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const insertMatchAllocationAuditSQL = `
INSERT INTO match_allocation_audits (
    match_id,
    mode_id,
    battle_server_id,
    endpoint,
    region,
    protocol_version,
    ruleset_version,
    mode_config_hash,
    server_seed_hash,
    player_count,
    allocation_json,
    status,
    server_authoritative,
    created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14
) ON CONFLICT DO NOTHING`

const insertBattleTicketAuditSQL = `
INSERT INTO battle_ticket_audits (
    ticket_id,
    issued_at,
    expires_at,
    user_id,
    match_id,
    player_id,
    battle_server_id,
    endpoint,
    key_id,
    ruleset_version,
    protocol_version,
    deck_snapshot_hash,
    mode_config_hash,
    nonce,
    signature_prefix,
    status,
    server_authoritative,
    consumed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
) ON CONFLICT (ticket_id) DO UPDATE SET
    status = EXCLUDED.status,
    consumed_at = COALESCE(EXCLUDED.consumed_at, battle_ticket_audits.consumed_at),
    server_authoritative = EXCLUDED.server_authoritative
WHERE battle_ticket_audits.status = 'issued' AND EXCLUDED.status IN ('consumed', 'expired', 'revoked')`

const insertBattleResultAuditSQL = `
INSERT INTO battle_result_audits (
    match_id,
    mode_id,
    battle_server_id,
    result_hash,
    replay_id,
    key_id,
    player_ids_json,
    settlement_key,
    status,
    reject_reason,
    verified_at,
    settled_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13
) ON CONFLICT DO NOTHING`

const insertReplayAuditSQL = `
INSERT INTO replay_audits (
    replay_id,
    match_id,
    user_id,
    mode_id,
    ruleset_version,
    mode_ruleset_version,
    state_hash,
    input_count,
    event_count,
    settlement_key,
    settled_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
) ON CONFLICT (replay_id) DO NOTHING`

type SQLBattleLifecycleAuditRepository struct {
	db                  *sql.DB
	insertAllocationSQL string
	insertTicketSQL     string
	insertResultSQL     string
	insertReplaySQL     string
}

type SQLBattleLifecycleAuditRepositoryOption func(*SQLBattleLifecycleAuditRepository)

func NewSQLBattleLifecycleAuditRepository(db *sql.DB, options ...SQLBattleLifecycleAuditRepositoryOption) (*SQLBattleLifecycleAuditRepository, error) {
	if db == nil {
		return nil, errors.New("battle lifecycle audit repository requires db")
	}
	repo := &SQLBattleLifecycleAuditRepository{
		db:                  db,
		insertAllocationSQL: insertMatchAllocationAuditSQL,
		insertTicketSQL:     insertBattleTicketAuditSQL,
		insertResultSQL:     insertBattleResultAuditSQL,
		insertReplaySQL:     insertReplayAuditSQL,
	}
	for _, option := range options {
		option(repo)
	}
	return repo, nil
}

func withSQLBattleLifecycleAuditStatements(allocationSQL string, ticketSQL string, resultSQL string, replaySQL string) SQLBattleLifecycleAuditRepositoryOption {
	return func(repo *SQLBattleLifecycleAuditRepository) {
		if allocationSQL != "" {
			repo.insertAllocationSQL = allocationSQL
		}
		if ticketSQL != "" {
			repo.insertTicketSQL = ticketSQL
		}
		if resultSQL != "" {
			repo.insertResultSQL = resultSQL
		}
		if replaySQL != "" {
			repo.insertReplaySQL = replaySQL
		}
	}
}

func (repo *SQLBattleLifecycleAuditRepository) RecordMatchAllocationAudit(record BattleAllocationAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("battle lifecycle audit repository is not configured")
	}
	_, err := repo.db.Exec(
		repo.insertAllocationSQL,
		record.MatchID,
		record.ModeID,
		record.BattleServerID,
		record.Endpoint,
		record.Region,
		record.ProtocolVersion,
		record.RulesetVersion,
		record.ModeConfigHash,
		record.ServerSeedHash,
		record.PlayerCount,
		firstNonEmptyCore(record.AllocationJSON, "{}"),
		firstNonEmptyCore(record.Status, "allocated"),
		record.ServerAuthoritative,
		record.CreatedAt,
	)
	return err
}

func (repo *SQLBattleLifecycleAuditRepository) RecordBattleTicketAudit(record BattleTicketAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("battle lifecycle audit repository is not configured")
	}
	_, err := repo.db.Exec(
		repo.insertTicketSQL,
		record.TicketID,
		record.IssuedAt,
		record.ExpiresAt,
		record.UserID,
		record.MatchID,
		record.PlayerID,
		record.BattleServerID,
		record.Endpoint,
		record.KeyID,
		record.RulesetVersion,
		record.ProtocolVersion,
		record.DeckSnapshotHash,
		record.ModeConfigHash,
		record.Nonce,
		record.SignaturePrefix,
		firstNonEmptyCore(record.Status, "issued"),
		record.ServerAuthoritative,
		nullableTime(record.ConsumedAt),
	)
	return err
}

func (repo *SQLBattleLifecycleAuditRepository) RecordBattleResultAudit(record BattleResultAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("battle lifecycle audit repository is not configured")
	}
	playerIDsJSON, err := json.Marshal(record.PlayerIDs)
	if err != nil {
		return err
	}
	_, err = repo.db.Exec(
		repo.insertResultSQL,
		record.MatchID,
		record.ModeID,
		record.BattleServerID,
		record.ResultHash,
		record.ReplayID,
		record.KeyID,
		string(playerIDsJSON),
		record.SettlementKey,
		firstNonEmptyCore(record.Status, "accepted"),
		record.RejectReason,
		record.VerifiedAt,
		record.SettledAt,
		record.ServerAuthoritative,
	)
	return err
}

func (repo *SQLBattleLifecycleAuditRepository) RecordReplayAudit(record ReplayAuditRecord) error {
	if repo == nil || repo.db == nil {
		return errors.New("battle lifecycle audit repository is not configured")
	}
	_, err := repo.db.Exec(
		repo.insertReplaySQL,
		record.ReplayID,
		record.MatchID,
		record.UserID,
		record.ModeID,
		record.RulesetVersion,
		record.ModeRulesetVersion,
		record.StateHash,
		record.InputCount,
		record.EventCount,
		record.SettlementKey,
		record.SettledAt,
		record.ServerAuthoritative,
	)
	return err
}

func firstNonEmptyCore(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
