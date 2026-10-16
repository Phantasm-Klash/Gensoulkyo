package core

import (
	"time"

	phkv1 "github.com/phantasm-klash/phk-protocol/gen/go/phk/v1"

	"gensoulkyo/runtime/account"
)

const (
	ServerVersion                   = "0.1.0"
	ProtocolVersion                 = phkv1.ProtocolVersion
	BusinessAPIVersion              = phkv1.BusinessAPIVersion
	BattleAPIVersion                = phkv1.BattleAPIVersion
	RulesetVersion                  = phkv1.RulesetVersion
	TickRate                        = 60
	DefaultInputDelayTick           = 2
	DefaultMatchTicks               = 60 * 90
	ReconnectWindowSeconds          = 30
	BattleTicketTTLSeconds          = 60
	BattleServerHeartbeatTTLSeconds = 30
	DefaultBattleServerID           = "battle-local-dev"
	DefaultBattleEndpoint           = "127.0.0.1:7901"

	ServiceCallbackRuntimeModeKey             = "runtime_ctx_mode"
	ServiceCallbackOriginKey                  = "gensoulkyo_service_origin"
	ServiceCallbackFlagKey                    = "gensoulkyo_battle_callback"
	ServiceCallbackPlayerSessionContextKey    = "player_session_context_allowed"
	ServiceCallbackBusinessEnvelopeAllowedKey = "business_envelope_allowed"
	ServiceCallbackRuntimeModeRPC             = "rpc"
	ServiceCallbackOriginBattleServer         = "battle_server"
	ServiceCallbackRequiredValue              = "true"
	ServiceCallbackDisallowedValue            = "false"
)

type ModeConfig struct {
	ModeID             string `json:"mode_id"`
	MinPlayers         int    `json:"min_players"`
	MaxPlayers         int    `json:"max_players"`
	ModeRulesetVersion string `json:"mode_ruleset_version"`
	RewardTableID      string `json:"reward_table_id"`
}

type PlayerLoadout struct {
	StageID     string `json:"stage_id"`
	CharacterID string `json:"character_id"`
	RatingCode  string `json:"rating_code,omitempty"`
	// ShotType / BombType 是「自机选择」轴（任务「自机机制」）：shot_type 取自
	// PhK-BattleServer/src/danmaku/player_loadout.cpp 的 52 条 PlayerShotSpec.name
	// （如 "ReimuA1"），bomb_type 取自 13 条 PlayerBombSpec.name（如 "Reimu"）。
	// 两者 omitempty：客户端未提交时留空，C++/TS 侧走默认自机行为 —— 保证「缺省
	// 复现现状」逐字节成立；一旦提交则必须命中白名单（见 validateLoadout）。
	ShotType            string `json:"shot_type,omitempty"`
	BombType            string `json:"bomb_type,omitempty"`
	RulesetVersion      string `json:"ruleset_version"`
	ServerAuthoritative bool   `json:"server_authoritative"`
}

var ModeConfigs = map[string]ModeConfig{
	"certification": {
		ModeID:             "certification",
		MinPlayers:         2,
		MaxPlayers:         2,
		ModeRulesetVersion: "cert-s0",
		RewardTableID:      "cert_s0_rewards",
	},
	"pvp_duel": {
		ModeID:             "pvp_duel",
		MinPlayers:         2,
		MaxPlayers:         2,
		ModeRulesetVersion: "pvp-duel-s0",
		RewardTableID:      "pvp_duel_s0_rewards",
	},
	"battle_royale": {
		ModeID:             "battle_royale",
		MinPlayers:         5,
		MaxPlayers:         10,
		ModeRulesetVersion: "br-s0",
		RewardTableID:      "br_s0_rewards",
	},
	"world_boss": {
		ModeID:             "world_boss",
		MinPlayers:         4,
		MaxPlayers:         8,
		ModeRulesetVersion: "world-boss-s0",
		RewardTableID:      "world_boss_s0_rewards",
	},
	"instance_boss": {
		ModeID:             "instance_boss",
		MinPlayers:         4,
		MaxPlayers:         8,
		ModeRulesetVersion: "instance-boss-s0",
		RewardTableID:      "instance_boss_s0_rewards",
	},
	"mvp_boss_race": {
		ModeID:             "mvp_boss_race",
		MinPlayers:         2,
		MaxPlayers:         2,
		ModeRulesetVersion: "mvp-boss-race-s0",
		RewardTableID:      "mvp_boss_race_s0_rewards",
	},
}

type Clock func() time.Time

type Config struct {
	Clock                    Clock
	MatchDurationTicks       int
	BattleLifecycleAuditRepo BattleLifecycleAuditRepository
	LobbyLifecycleAuditRepo  LobbyLifecycleAuditRepository
	// DemRepository persists completed .dem v1 replay containers (thread B).
	// When nil the Service falls back to an in-memory store, mirroring the
	// optional-injection style of the audit repositories above.
	DemRepository DemRepository
	// OnSessionRevoked is invoked when a sign-in supersedes an existing session
	// token for the same identity (single sign-on). The second argument is the
	// *superseded* token, not the freshly issued one.
	//
	// Contract: it is called with Service.mu released, it may block, and the
	// implementation may take its own locks and call back into the Service (for
	// example to look up state). It must not assume any Service lock is held.
	OnSessionRevoked func(userID string, sessionToken string)
	// Accounts is the e-mail/password account subsystem. When nil the account
	// routes answer `accounts_unavailable` instead of panicking, which keeps the
	// anonymous/external sign-in paths usable without a database.
	Accounts *account.Service
	// CallbackSecret keys the HMAC that authenticates battle-server service
	// callbacks (POST /v1/battle/events, /v1/battle/results/submit,
	// /internal/battle/result). Each battle server derives its per-server
	// secret as hex(HMAC-SHA256(callbackSecret, battleServerID)) and signs every
	// callback body with it.
	//
	// It MUST be stable across restarts: a per-process random value rotates every
	// derived per-server secret, so in-flight battle servers get 403 until they
	// re-register. Deployments set it from GENSOULKYO_CALLBACK_SECRET
	// (cmd/gensoulkyo); when empty a random 32-byte hex value is generated per
	// process as a last resort.
	CallbackSecret string
}

type BattleLifecycleAuditStatus struct {
	OK                     bool      `json:"ok"`
	Configured             bool      `json:"configured"`
	ServerLifecycleRecords int       `json:"server_lifecycle_records"`
	AllocationRecords      int       `json:"allocation_records"`
	TicketRecords          int       `json:"ticket_records"`
	TicketExpiredRecords   int       `json:"ticket_expired_records"`
	TicketRevokedRecords   int       `json:"ticket_revoked_records"`
	TicketConsumedRecords  int       `json:"ticket_consumed_records"`
	TicketRejectedRecords  int       `json:"ticket_rejected_records"`
	ResultRecords          int       `json:"result_records"`
	ResultDuplicateRecords int       `json:"result_duplicate_records"`
	ResultRejectedRecords  int       `json:"result_rejected_records"`
	ReplayRecords          int       `json:"replay_records"`
	RejectedRecords        int       `json:"rejected_records"`
	LastSuccessOperation   string    `json:"last_success_operation,omitempty"`
	LastSuccessFingerprint string    `json:"last_success_fingerprint,omitempty"`
	LastSuccessAt          time.Time `json:"last_success_at,omitempty"`
	LastErrorOperation     string    `json:"last_error_operation,omitempty"`
	LastError              string    `json:"last_error,omitempty"`
	LastErrorAt            time.Time `json:"last_error_at,omitempty"`
	ServerAuthoritative    bool      `json:"server_authoritative"`
}

type BattleLifecycleAuditRepository interface {
	RecordMatchAllocationAudit(BattleAllocationAuditRecord) error
	RecordBattleTicketAudit(BattleTicketAuditRecord) error
	RecordBattleResultAudit(BattleResultAuditRecord) error
	RecordReplayAudit(ReplayAuditRecord) error
}

type LobbyLifecycleAuditStatus struct {
	OK                     bool      `json:"ok"`
	Configured             bool      `json:"configured"`
	AllowedActions         []string  `json:"allowed_actions"`
	RoomRecords            int       `json:"room_records"`
	RoomReadRecords        int       `json:"room_read_records"`
	RulesReadRecords       int       `json:"rules_read_records"`
	ReadyRecords           int       `json:"ready_records"`
	ConnectionRecords      int       `json:"connection_records"`
	MessageRecords         int       `json:"message_records"`
	RejectedRecords        int       `json:"rejected_records"`
	LastSuccessOperation   string    `json:"last_success_operation,omitempty"`
	LastSuccessFingerprint string    `json:"last_success_fingerprint,omitempty"`
	LastSuccessAt          time.Time `json:"last_success_at,omitempty"`
	LastErrorOperation     string    `json:"last_error_operation,omitempty"`
	LastError              string    `json:"last_error,omitempty"`
	LastErrorAt            time.Time `json:"last_error_at,omitempty"`
	ServerAuthoritative    bool      `json:"server_authoritative"`
}

type LobbyLifecycleAuditRepository interface {
	RecordLobbyRoomAudit(LobbyRoomAuditRecord) error
	RecordLobbyMessageAudit(LobbyMessageAuditRecord) error
}

type LobbyRoomAuditRecord struct {
	RoomCode            string    `json:"room_code"`
	Action              string    `json:"action"`
	ModeID              string    `json:"mode_id"`
	UserID              string    `json:"user_id"`
	TicketID            string    `json:"ticket_id"`
	MatchID             string    `json:"match_id"`
	RoomStatus          string    `json:"room_status"`
	HostUserID          string    `json:"host_user_id"`
	CurrentPlayers      int       `json:"current_players"`
	RequiredPlayers     int       `json:"required_players"`
	StageID             string    `json:"stage_id"`
	RulesetVersion      string    `json:"ruleset_version"`
	ModeRulesetVersion  string    `json:"mode_ruleset_version"`
	ModeConfigHash      string    `json:"mode_config_hash"`
	DeckSnapshotHash    string    `json:"deck_snapshot_hash"`
	CreatedAt           time.Time `json:"created_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type LobbyMessageAuditRecord struct {
	MessageID           string    `json:"message_id"`
	RoomCode            string    `json:"room_code"`
	ModeID              string    `json:"mode_id"`
	Kind                string    `json:"kind"`
	UserID              string    `json:"user_id"`
	Duplicate           bool      `json:"duplicate"`
	TextLength          int       `json:"text_length"`
	MetadataHash        string    `json:"metadata_hash"`
	CreatedAt           time.Time `json:"created_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type BattleAllocationAuditRecord struct {
	MatchID             string    `json:"match_id"`
	ModeID              string    `json:"mode_id"`
	BattleServerID      string    `json:"battle_server_id"`
	Endpoint            string    `json:"endpoint"`
	Region              string    `json:"region"`
	ProtocolVersion     string    `json:"protocol_version"`
	BusinessAPIVersion  string    `json:"business_api_version"`
	BattleAPIVersion    string    `json:"battle_api_version"`
	RulesetVersion      string    `json:"ruleset_version"`
	ModeConfigHash      string    `json:"mode_config_hash"`
	ServerSeedHash      string    `json:"server_seed_hash"`
	PlayerCount         int       `json:"player_count"`
	AllocationJSON      string    `json:"allocation_json"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type BattleTicketAuditRecord struct {
	TicketID            string    `json:"ticket_id"`
	MatchID             string    `json:"match_id"`
	UserID              string    `json:"user_id"`
	PlayerID            string    `json:"player_id"`
	BattleServerID      string    `json:"battle_server_id"`
	Endpoint            string    `json:"endpoint"`
	KeyID               string    `json:"key_id"`
	RulesetVersion      string    `json:"ruleset_version"`
	ProtocolVersion     string    `json:"protocol_version"`
	BusinessAPIVersion  string    `json:"business_api_version"`
	BattleAPIVersion    string    `json:"battle_api_version"`
	DeckSnapshotHash    string    `json:"deck_snapshot_hash"`
	ModeConfigHash      string    `json:"mode_config_hash"`
	Nonce               string    `json:"nonce"`
	SignaturePrefix     string    `json:"signature_prefix"`
	Status              string    `json:"status"`
	RejectReason        string    `json:"reject_reason,omitempty"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	ConsumedAt          time.Time `json:"consumed_at,omitempty"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type BattleResultAuditRecord struct {
	MatchID             string    `json:"match_id"`
	ModeID              string    `json:"mode_id"`
	BattleServerID      string    `json:"battle_server_id"`
	ResultHash          string    `json:"result_hash"`
	ReplayID            string    `json:"replay_id"`
	KeyID               string    `json:"key_id"`
	PlayerIDs           []string  `json:"player_ids"`
	SettlementKey       string    `json:"settlement_key"`
	Status              string    `json:"status"`
	RejectReason        string    `json:"reject_reason,omitempty"`
	VerifiedAt          time.Time `json:"verified_at"`
	SettledAt           time.Time `json:"settled_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type ReplayAuditRecord struct {
	ReplayID            string    `json:"replay_id"`
	MatchID             string    `json:"match_id"`
	UserID              string    `json:"user_id"`
	ModeID              string    `json:"mode_id"`
	RulesetVersion      string    `json:"ruleset_version"`
	ModeRulesetVersion  string    `json:"mode_ruleset_version"`
	StateHash           string    `json:"state_hash"`
	InputCount          int       `json:"input_count"`
	EventCount          int       `json:"event_count"`
	SettlementKey       string    `json:"settlement_key"`
	SettledAt           time.Time `json:"settled_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

type VersionStamp struct {
	ProtocolVersion    int    `json:"protocol_version"`
	BusinessAPIVersion string `json:"business_api_version,omitempty"`
	BattleAPIVersion   string `json:"battle_api_version,omitempty"`
	RulesetVersion     string `json:"ruleset_version"`
}

type RegisterBattleServerRequest struct {
	BattleServerID string   `json:"battle_server_id"`
	Endpoint       string   `json:"endpoint"`
	Region         string   `json:"region,omitempty"`
	BuildID        string   `json:"build_id,omitempty"`
	Capacity       int      `json:"capacity"`
	ActiveMatches  int      `json:"active_matches"`
	Load           float64  `json:"load"`
	Status         string   `json:"status,omitempty"`
	SupportedModes []string `json:"supported_modes"`
}

type BattleServerHeartbeatRequest struct {
	BattleServerID string   `json:"battle_server_id"`
	Endpoint       string   `json:"endpoint,omitempty"`
	Region         string   `json:"region,omitempty"`
	BuildID        string   `json:"build_id,omitempty"`
	Capacity       int      `json:"capacity"`
	ActiveMatches  int      `json:"active_matches"`
	Load           float64  `json:"load"`
	Status         string   `json:"status,omitempty"`
	SupportedModes []string `json:"supported_modes,omitempty"`
}

type BattleServerOfflineRequest struct {
	BattleServerID string `json:"battle_server_id"`
	Status         string `json:"status,omitempty"`
}

type BattleServerStatus struct {
	OK                  bool      `json:"ok"`
	BattleServerID      string    `json:"battle_server_id"`
	Endpoint            string    `json:"endpoint"`
	Region              string    `json:"region,omitempty"`
	BuildID             string    `json:"build_id,omitempty"`
	Capacity            int       `json:"capacity"`
	ActiveMatches       int       `json:"active_matches"`
	Load                float64   `json:"load"`
	Status              string    `json:"status"`
	SupportedModes      []string  `json:"supported_modes"`
	LastSeenAt          time.Time `json:"last_seen_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
	// CallbackSecret is the per-battle-server callback secret
	// (hex(HMAC-SHA256(callbackSecret, battle_server_id))). It is returned ONLY
	// by RegisterBattleServer; heartbeat, offline and the public server list
	// omit it (omitempty), so the derived secret never leaves the registration
	// response.
	CallbackSecret string `json:"callback_secret,omitempty"`
}

type BattleServerListResponse struct {
	OK                  bool                 `json:"ok"`
	Servers             []BattleServerStatus `json:"servers"`
	ServerTime          time.Time            `json:"server_time"`
	ServerAuthoritative bool                 `json:"server_authoritative"`
}

type BattleAllocationPlayer struct {
	UserID           string        `json:"user_id"`
	PlayerID         string        `json:"player_id"`
	DisplayName      string        `json:"display_name"`
	DeckSnapshotHash string        `json:"deck_snapshot_hash"`
	Loadout          PlayerLoadout `json:"loadout"`
}

type BattleServerAllocation struct {
	OK             bool         `json:"ok"`
	Version        VersionStamp `json:"version"`
	MatchID        string       `json:"match_id"`
	ModeID         string       `json:"mode_id"`
	BossID         string       `json:"boss_id,omitempty"`
	BattleServerID string       `json:"battle_server_id"`
	Endpoint       string       `json:"endpoint"`
	// EndpointReported marks an endpoint the out-of-process battle agent has
	// confirmed it actually bound. Until it is set the endpoint is only the
	// registry handle, and a client-facing consumer (the battle relay) must not
	// dial it yet: see HandleRelay's bounded wait.
	EndpointReported    bool                     `json:"endpoint_reported"`
	Players             []BattleAllocationPlayer `json:"players"`
	ServerSeed          int64                    `json:"server_seed"`
	ServerSeedHex       string                   `json:"server_seed_hex"`
	ModeConfigHash      string                   `json:"mode_config_hash"`
	AllocatedAt         time.Time                `json:"allocated_at"`
	ServerAuthoritative bool                     `json:"server_authoritative"`
}

// BattleAgentAssignment describes one live match an out-of-process battle
// agent must have a battle process running for. It is the agent-facing view of
// a BattleServerAllocation: endpoint + seed + roster, plus the ruleset the
// process must be started with.
type BattleAgentAssignment struct {
	MatchID        string   `json:"match_id"`
	ModeID         string   `json:"mode_id"`
	Ruleset        string   `json:"ruleset,omitempty"`
	Endpoint       string   `json:"endpoint"`
	ServerSeedHex  string   `json:"server_seed_hex"`
	ModeConfigHash string   `json:"mode_config_hash"`
	PlayerIDs      []string `json:"player_ids"`
	// BossID 是本局 boss（房间级，仅房主可选）；空串 = 默认 timeline（现状）。
	BossID string `json:"boss_id,omitempty"`
	// Loadouts 是每名玩家的自机 loadout；battle-agent 逐条转成
	// `--loadout <player_id>:<character_id>:<shot_type>:<bomb_type>`。
	Loadouts []BattleAgentLoadout `json:"loadouts"`
	// Players carries the server-authoritative player refs. PlayerID is the
	// value a battle result must echo back; UserID is the Nakama user.
	Players     []BattleAgentPlayer `json:"players"`
	AllocatedAt time.Time           `json:"allocated_at"`
}

// BattleAgentLoadout 是 agent 面向的每玩家自机 loadout 视图。字段名与 .dem
// header 的 players[].loadout{character_id,shot_type,bomb_type} 对齐。
type BattleAgentLoadout struct {
	PlayerID    string `json:"player_id"`
	CharacterID string `json:"character_id"`
	ShotType    string `json:"shot_type"`
	BombType    string `json:"bomb_type"`
}

// BattleAgentPlayer is the agent-facing view of an allocated player. The
// PlayerID is derived by core (p-<hash>) and is the identifier a submitted
// battle result must use; UserID is the underlying Nakama user.
type BattleAgentPlayer struct {
	UserID      string `json:"user_id"`
	PlayerID    string `json:"player_id"`
	DisplayName string `json:"display_name,omitempty"`
}

// BattleAgentEndpointReport is the payload of battle.agent.endpoint, the
// service-origin RPC an out-of-process battle agent calls right after it has
// spawned the battle process for a match.
//
// The agent picks the concrete UDP port at spawn time (a port the lobby cannot
// know in advance: it is the first free port at or above the agent's port
// floor), so the registered endpoint is only a stable handle. Without this
// report the lobby keeps handing clients the registered port and concurrent
// matches collide on one battle process.
type BattleAgentEndpointReport struct {
	BattleServerID string `json:"battle_server_id"`
	MatchID        string `json:"match_id"`
	Endpoint       string `json:"endpoint"`
}

// BattleAgentEndpointReportResponse is the reply to battle.agent.endpoint.
type BattleAgentEndpointReportResponse struct {
	OK                  bool                   `json:"ok"`
	Version             VersionStamp           `json:"version"`
	MatchID             string                 `json:"match_id"`
	BattleServerID      string                 `json:"battle_server_id"`
	Endpoint            string                 `json:"endpoint"`
	Allocation          BattleServerAllocation `json:"allocation"`
	Updated             bool                   `json:"updated"`
	ServerTime          time.Time              `json:"server_time"`
	ServerAuthoritative bool                   `json:"server_authoritative"`
}

// BattleAgentAssignmentsResponse is the reply to battle.agent.assignments.
type BattleAgentAssignmentsResponse struct {
	OK                  bool                    `json:"ok"`
	Version             VersionStamp            `json:"version"`
	BattleServerID      string                  `json:"battle_server_id"`
	Endpoint            string                  `json:"endpoint"`
	Assignments         []BattleAgentAssignment `json:"assignments"`
	ServerTime          time.Time               `json:"server_time"`
	ServerAuthoritative bool                    `json:"server_authoritative"`
}

type BattleTicket struct {
	Version           VersionStamp `json:"version"`
	TicketID          string       `json:"ticket_id"`
	MatchID           string       `json:"match_id"`
	UserID            string       `json:"user_id"`
	PlayerID          string       `json:"player_id"`
	ModeID            string       `json:"mode_id"`
	BattleServerID    string       `json:"battle_server_id"`
	Endpoint          string       `json:"endpoint"`
	DeckSnapshotHash  string       `json:"deck_snapshot_hash"`
	ModeConfigHash    string       `json:"mode_config_hash"`
	RulesetVersion    string       `json:"ruleset_version"`
	TicketNonceHex    string       `json:"ticket_nonce_hex"`
	IssuedAt          time.Time    `json:"issued_at"`
	ExpiresAt         time.Time    `json:"expires_at"`
	IssuedAtMS        int64        `json:"issued_at_ms"`
	ExpiresAtMS       int64        `json:"expires_at_ms"`
	BusinessSessionID string       `json:"business_session_id"`
	// W-1 opening loadout: hand_card_ids carries 0..cardHandLimit catalog card
	// ids drawn from the player's deck; initial_energy is the opening energy;
	// server_seed is the authoritative lobby seed (seedFrom) as DECIMAL TEXT so
	// seeds above 2^53 survive the JS client pass-through without narrowing.
	HandCardIDs []string `json:"hand_card_ids"`
	// DeckCardIDs is the lobby's authoritative ordered deck (W-1 phase 2):
	// the opening hand above is its first len(HandCardIDs) entries, so the
	// battle server can redraw after a cast from where the lobby left off.
	DeckCardIDs       []string `json:"deck_card_ids"`
	InitialEnergy     float64  `json:"initial_energy"`
	ServerSeedDecimal string   `json:"server_seed"`
	// 房间 boss 选择与本玩家自机 loadout（任务「boss 选择 + 自机机制」）。
	// 全部位于 ED25519 覆盖范围（签名对 json.Marshal(ticket) 计算），篡改即破签。
	BossID              string `json:"boss_id,omitempty"`
	CharacterID         string `json:"character_id,omitempty"`
	ShotType            string `json:"shot_type,omitempty"`
	BombType            string `json:"bomb_type,omitempty"`
	ServerAuthoritative bool   `json:"server_authoritative"`
}

type SignedBattleTicket struct {
	OK                  bool         `json:"ok"`
	Ticket              BattleTicket `json:"ticket"`
	SignatureAlg        string       `json:"signature_alg"`
	KeyID               string       `json:"key_id"`
	SignatureHex        string       `json:"signature_hex"`
	PublicKeyHex        string       `json:"public_key_hex"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

type BattleTicketConsumeRequest struct {
	Version        VersionStamp `json:"version"`
	TicketID       string       `json:"ticket_id"`
	MatchID        string       `json:"match_id"`
	UserID         string       `json:"user_id,omitempty"`
	PlayerID       string       `json:"player_id,omitempty"`
	BattleServerID string       `json:"battle_server_id"`
	ModeConfigHash string       `json:"mode_config_hash,omitempty"`
	TicketNonceHex string       `json:"ticket_nonce_hex"`
}

type BattleTicketConsumeResponse struct {
	OK                  bool         `json:"ok"`
	Version             VersionStamp `json:"version"`
	TicketID            string       `json:"ticket_id"`
	MatchID             string       `json:"match_id"`
	UserID              string       `json:"user_id"`
	PlayerID            string       `json:"player_id"`
	BattleServerID      string       `json:"battle_server_id"`
	ModeConfigHash      string       `json:"mode_config_hash"`
	IssuedAt            time.Time    `json:"issued_at"`
	ExpiresAt           time.Time    `json:"expires_at"`
	ConsumedAt          time.Time    `json:"consumed_at"`
	IssuedAtMS          int64        `json:"issued_at_ms"`
	ExpiresAtMS         int64        `json:"expires_at_ms"`
	ConsumedAtMS        int64        `json:"consumed_at_ms"`
	Consumed            bool         `json:"consumed"`
	Duplicate           bool         `json:"duplicate"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

type BattleResult struct {
	Version              VersionStamp `json:"version"`
	MatchID              string       `json:"match_id"`
	ModeID               string       `json:"mode_id"`
	ResultHash           string       `json:"result_hash"`
	ReplayID             string       `json:"replay_id"`
	PlayerIDs            []string     `json:"player_ids"`
	RewardProjectionJSON string       `json:"reward_projection_json,omitempty"`
	ModeResultJSON       string       `json:"mode_result_json,omitempty"`
	SettledAtMS          int64        `json:"settled_at_ms"`
}

type SignedBattleResult struct {
	OK                  bool         `json:"ok"`
	Result              BattleResult `json:"result"`
	SignatureAlg        string       `json:"signature_alg"`
	KeyID               string       `json:"key_id"`
	SignatureHex        string       `json:"signature_hex"`
	PublicKeyHex        string       `json:"public_key_hex,omitempty"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time,omitempty"`
}

type BattleResultSubmitRequest struct {
	SignedResult  SignedBattleResult       `json:"signed_result"`
	ReplaySummary ReplayInputStreamSummary `json:"replay_summary,omitempty"`
}

type ReplayInputStreamSummary struct {
	Version         VersionStamp `json:"version"`
	ReplayID        string       `json:"replay_id"`
	MatchID         string       `json:"match_id"`
	OwnerUserID     string       `json:"owner_user_id"`
	InputCount      int          `json:"input_count"`
	EventCount      int          `json:"event_count"`
	InputStreamHash string       `json:"input_stream_hash"`
	EventStreamHash string       `json:"event_stream_hash"`
	FinalStateHash  string       `json:"final_state_hash"`
	FinalTick       int          `json:"final_tick"`
}

type BattleResultSubmitResponse struct {
	OK                  bool         `json:"ok"`
	Version             VersionStamp `json:"version"`
	MatchID             string       `json:"match_id"`
	SettlementKey       string       `json:"settlement_key"`
	Accepted            bool         `json:"accepted"`
	Duplicate           bool         `json:"duplicate"`
	Error               string       `json:"error,omitempty"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

type AnonymousLoginRequest struct {
	DeviceID    string `json:"device_id"`
	DisplayName string `json:"display_name"`
}

type ExternalSessionRequest struct {
	UserID       string `json:"user_id"`
	SessionToken string `json:"session_token"`
	DisplayName  string `json:"display_name"`
	Provider     string `json:"provider"`
}

type AuthSession struct {
	UserID       string    `json:"user_id"`
	SessionToken string    `json:"session_token"`
	DisplayName  string    `json:"display_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// AccountEmailCodeRequest asks the lobby to mail a verification code to an
// address that is about to be registered.
type AccountEmailCodeRequest struct {
	Email string `json:"email"`
}

// AccountEmailCodeResponse reports when the code expires and when the same
// address may request another one, so the client can run its countdown without
// guessing the server's policy.
type AccountEmailCodeResponse struct {
	Email           string    `json:"email"`
	ExpiresAt       time.Time `json:"expires_at"`
	ResendAfter     time.Time `json:"resend_after"`
	ExpiresInSecond int       `json:"expires_in_seconds"`
	ResendInSecond  int       `json:"resend_in_seconds"`
	ServerTime      time.Time `json:"server_time"`
}

// AccountRegisterRequest is a self-service sign-up. `Code` is the e-mail
// verification code; registration is refused without a valid one.
type AccountRegisterRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Code        string `json:"code"`
	Platform    string `json:"platform"`
	ClientBuild string `json:"client_build"`
}

// AccountLoginRequest signs an existing account in with its e-mail + password.
type AccountLoginRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	Platform    string `json:"platform"`
	ClientBuild string `json:"client_build"`
}

type BootstrapSnapshot struct {
	UserID        string                    `json:"user_id"`
	SessionToken  string                    `json:"session_token"`
	DisplayName   string                    `json:"display_name"`
	ServerVersion string                    `json:"server_version"`
	Ruleset       string                    `json:"ruleset_version"`
	Modes         []ModeConfig              `json:"modes"`
	Wallet        map[string]int            `json:"wallet"`
	Inventory     InventorySnapshot         `json:"inventory"`
	Decks         DeckListResponse          `json:"decks"`
	Chests        ChestSnapshot             `json:"chests"`
	Tasks         map[string]TaskState      `json:"tasks"`
	Events        map[string]EventState     `json:"events"`
	Leaderboards  map[string]LeaderboardRow `json:"leaderboards"`
	Certification CertificationProfile      `json:"certification"`
	WorldBoss     WorldBossSnapshot         `json:"world_boss"`
}

type CertificationProfile struct {
	OK                        bool      `json:"ok"`
	UserID                    string    `json:"user_id,omitempty"`
	RatingCode                string    `json:"rating_code"`
	SeasonID                  string    `json:"season_id"`
	RankScore                 int       `json:"rank_score"`
	RankScoreFloor            int       `json:"rank_score_floor"`
	ChallengeStageID          string    `json:"challenge_stage_id"`
	Percentile                float64   `json:"percentile"`
	Top30Qualified            bool      `json:"top_30_qualified"`
	NextCertificationUnlocked bool      `json:"next_certification_unlocked"`
	LastRankScoreDelta        int       `json:"last_rank_score_delta"`
	LastResult                string    `json:"last_result,omitempty"`
	ServerAuthoritative       bool      `json:"server_authoritative"`
	ClientResultAuthoritative bool      `json:"client_result_authoritative"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type WorldBossSnapshot struct {
	OK                  bool       `json:"ok"`
	BossInstanceID      string     `json:"boss_instance_id"`
	SeasonID            string     `json:"season_id"`
	MaxHP               int        `json:"max_hp"`
	CurrentHP           int        `json:"current_hp"`
	DailyAttemptLimit   int        `json:"daily_attempt_limit"`
	DailyAttemptsUsed   int        `json:"daily_attempts_used"`
	DailyAttemptsLeft   int        `json:"daily_attempts_left"`
	StartsAt            time.Time  `json:"starts_at"`
	EndsAt              time.Time  `json:"ends_at"`
	DefeatedAt          *time.Time `json:"defeated_at,omitempty"`
	DefeatedByMatchID   string     `json:"defeated_by_match_id,omitempty"`
	DefeatedByUserID    string     `json:"defeated_by_user_id,omitempty"`
	AnnouncementEmitted bool       `json:"announcement_emitted"`
	ServerAuthoritative bool       `json:"server_authoritative"`
	ServerTime          time.Time  `json:"server_time"`
}

type CardInventoryEntry struct {
	CardID          string    `json:"card_id"`
	Copies          int       `json:"copies"`
	Level           int       `json:"level"`
	FirstObtainedAt time.Time `json:"first_obtained_at"`
}

type InventorySnapshot struct {
	OK                  bool                 `json:"ok"`
	UserID              string               `json:"user_id"`
	RulesetVersion      string               `json:"ruleset_version"`
	Items               []CardInventoryEntry `json:"items"`
	ServerAuthoritative bool                 `json:"server_authoritative"`
	ServerTime          time.Time            `json:"server_time"`
}

type CardUpgradeRequest struct {
	CardID                    string `json:"card_id"`
	TargetLevel               int    `json:"target_level"`
	ClientResultAuthoritative bool   `json:"client_result_authoritative"`
}

type CardUpgradeResponse struct {
	OK                        bool              `json:"ok"`
	UserID                    string            `json:"user_id"`
	CardID                    string            `json:"card_id"`
	Rarity                    string            `json:"rarity"`
	OldLevel                  int               `json:"old_level"`
	NewLevel                  int               `json:"new_level"`
	MaxLevel                  int               `json:"max_level"`
	Cost                      map[string]int    `json:"cost"`
	Wallet                    map[string]int    `json:"wallet"`
	Inventory                 InventorySnapshot `json:"inventory"`
	ServerAuthoritative       bool              `json:"server_authoritative"`
	ClientResultAuthoritative bool              `json:"client_result_authoritative"`
	ServerTime                time.Time         `json:"server_time"`
}

type DeckRecord struct {
	DeckID         string    `json:"deck_id"`
	Name           string    `json:"name"`
	Format         string    `json:"format"`
	RulesetVersion string    `json:"ruleset_version"`
	CardIDs        []string  `json:"card_ids"`
	Active         bool      `json:"active"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type DeckSnapshot struct {
	DeckID         string   `json:"deck_id"`
	Name           string   `json:"name"`
	RulesetVersion string   `json:"ruleset_version"`
	CardIDs        []string `json:"card_ids"`
}

type DeckListResponse struct {
	OK                  bool         `json:"ok"`
	UserID              string       `json:"user_id"`
	ActiveDeckID        string       `json:"active_deck_id"`
	RulesetVersion      string       `json:"ruleset_version"`
	Decks               []DeckRecord `json:"decks"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

type SaveDeckRequest struct {
	DeckID    string   `json:"deck_id"`
	Name      string   `json:"name"`
	Format    string   `json:"format"`
	CardIDs   []string `json:"card_ids"`
	Active    bool     `json:"active"`
	UpdatedAt string   `json:"updated_at"`
}

type SaveDeckResponse struct {
	OK                  bool       `json:"ok"`
	UserID              string     `json:"user_id"`
	Deck                DeckRecord `json:"deck"`
	ActiveDeckID        string     `json:"active_deck_id"`
	Validation          []string   `json:"validation"`
	ServerAuthoritative bool       `json:"server_authoritative"`
	ServerTime          time.Time  `json:"server_time"`
}

type ChestPityRules struct {
	RareEvery int  `json:"rare_every"`
	EpicEvery int  `json:"epic_every"`
	Inherit   bool `json:"inherit"`
}

type ChestPool struct {
	PoolID   string         `json:"pool_id"`
	SeasonID string         `json:"season_id"`
	Name     string         `json:"name"`
	NameKey  string         `json:"name_key"`
	Cost     map[string]int `json:"cost"`
	Weights  map[string]int `json:"weights"`
	Pity     ChestPityRules `json:"pity"`
	StartsAt string         `json:"starts_at"`
	EndsAt   string         `json:"ends_at"`
	Enabled  bool           `json:"enabled"`
}

type ChestPityState struct {
	RareCounter int `json:"rare_counter"`
	EpicCounter int `json:"epic_counter"`
}

type ChestSnapshot struct {
	OK                  bool                      `json:"ok"`
	UserID              string                    `json:"user_id"`
	RulesetVersion      string                    `json:"ruleset_version"`
	Wallet              map[string]int            `json:"wallet"`
	OwnedChests         map[string]int            `json:"owned_chests"`
	Pools               []ChestPool               `json:"pools"`
	PityCounters        map[string]ChestPityState `json:"pity_counters"`
	OpeningLog          []ChestOpeningRecord      `json:"opening_log"`
	LastResults         []ChestOpenResult         `json:"last_results"`
	ServerAuthoritative bool                      `json:"server_authoritative"`
	ServerTime          time.Time                 `json:"server_time"`
}

type ChestOpenRequest struct {
	PoolID                    string `json:"pool_id"`
	Count                     int    `json:"count"`
	ClientResultAuthoritative bool   `json:"client_result_authoritative"`
}

type ChestOpenResult struct {
	ID       string `json:"id"`
	CardID   string `json:"card_id"`
	NameKey  string `json:"name_key"`
	Rarity   string `json:"rarity"`
	Dust     int    `json:"dust"`
	Accepted int    `json:"accepted"`
	Overflow int    `json:"overflow"`
}

type ChestOpeningRecord struct {
	OpeningID  string            `json:"opening_id"`
	UserID     string            `json:"user_id"`
	PoolID     string            `json:"pool_id"`
	Count      int               `json:"count"`
	Cost       map[string]int    `json:"cost"`
	ServerSeed string            `json:"server_seed"`
	Results    []ChestOpenResult `json:"results"`
	OpenedAt   time.Time         `json:"opened_at"`
}

type ChestOpenResponse struct {
	OK                        bool                      `json:"ok"`
	UserID                    string                    `json:"user_id"`
	PoolID                    string                    `json:"pool_id"`
	Count                     int                       `json:"count"`
	Wallet                    map[string]int            `json:"wallet"`
	OwnedChests               map[string]int            `json:"owned_chests"`
	Inventory                 InventorySnapshot         `json:"inventory"`
	PityCounters              map[string]ChestPityState `json:"pity_counters"`
	Results                   []ChestOpenResult         `json:"results"`
	Audit                     ChestOpeningRecord        `json:"audit"`
	ServerAuthoritative       bool                      `json:"server_authoritative"`
	ClientResultAuthoritative bool                      `json:"client_result_authoritative"`
	ServerTime                time.Time                 `json:"server_time"`
}

type JoinQueueRequest struct {
	ModeID        string         `json:"mode_id"`
	ActiveDeckID  string         `json:"active_deck_id"`
	DeckSnapshot  DeckSnapshot   `json:"deck_snapshot"`
	ModeParams    map[string]any `json:"mode_params"`
	ClientVersion VersionStamp   `json:"client_version"`
}

type CreateRoomRequest struct {
	ModeID        string         `json:"mode_id"`
	ActiveDeckID  string         `json:"active_deck_id"`
	DeckSnapshot  DeckSnapshot   `json:"deck_snapshot"`
	ModeParams    map[string]any `json:"mode_params"`
	ClientVersion VersionStamp   `json:"client_version"`
	// BossID 是房间的 boss 选择（仅建房/房主可设）。合法值为 Go 侧 BossCatalog
	// 的 id（"tian"/"hui"）或空串（走默认 timeline）；非法值 invalid_request。
	BossID string `json:"boss_id,omitempty"`
}

// JoinRoomRequest 有意**不含** boss_id：boss 只能由房主在建房时设定，guest 加入
// 不得更改（房间级 boss 对双方生效）。
type JoinRoomRequest struct {
	ModeID        string         `json:"mode_id"`
	ActiveDeckID  string         `json:"active_deck_id"`
	DeckSnapshot  DeckSnapshot   `json:"deck_snapshot"`
	ModeParams    map[string]any `json:"mode_params"`
	ClientVersion VersionStamp   `json:"client_version"`
}

type RoomParticipantSnapshot struct {
	UserID           string        `json:"user_id"`
	DisplayName      string        `json:"display_name"`
	TicketID         string        `json:"ticket_id"`
	DeckSnapshotHash string        `json:"deck_snapshot_hash"`
	Loadout          PlayerLoadout `json:"loadout"`
	JoinedAt         time.Time     `json:"joined_at"`
	LastSeenAt       time.Time     `json:"last_seen_at"`
}

type LobbyMessageRequest struct {
	RoomCode  string         `json:"room_code"`
	MessageID string         `json:"message_id"`
	Kind      string         `json:"kind"`
	Text      string         `json:"text"`
	Metadata  map[string]any `json:"metadata"`
}

type LobbyMessage struct {
	MessageID           string         `json:"message_id"`
	RoomCode            string         `json:"room_code"`
	ModeID              string         `json:"mode_id"`
	Kind                string         `json:"kind"`
	UserID              string         `json:"user_id"`
	DisplayName         string         `json:"display_name"`
	Text                string         `json:"text"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	Duplicate           bool           `json:"duplicate"`
	ServerAuthoritative bool           `json:"server_authoritative"`
}

type RoomSnapshot struct {
	OK                  bool                      `json:"ok"`
	RoomCode            string                    `json:"room_code"`
	RoomStatus          string                    `json:"room_status"`
	ModeID              string                    `json:"mode_id"`
	HostUserID          string                    `json:"host_user_id"`
	RequiredPlayers     int                       `json:"required_players"`
	MaxPlayers          int                       `json:"max_players"`
	CurrentPlayers      int                       `json:"current_players"`
	MatchID             string                    `json:"match_id,omitempty"`
	BossID              string                    `json:"boss_id,omitempty"`
	StageID             string                    `json:"stage_id"`
	ModeRulesetVersion  string                    `json:"mode_ruleset_version"`
	RulesetVersion      string                    `json:"ruleset_version"`
	ModeConfigHash      string                    `json:"mode_config_hash"`
	ModeParams          map[string]any            `json:"mode_params,omitempty"`
	Participants        []RoomParticipantSnapshot `json:"participants"`
	Messages            []LobbyMessage            `json:"messages,omitempty"`
	CreatedAt           time.Time                 `json:"created_at"`
	ServerTime          time.Time                 `json:"server_time"`
	ServerAuthoritative bool                      `json:"server_authoritative"`
}

type RoomListResponse struct {
	OK                  bool           `json:"ok"`
	Rooms               []RoomSnapshot `json:"rooms"`
	ServerTime          time.Time      `json:"server_time"`
	ServerAuthoritative bool           `json:"server_authoritative"`
}

type BusinessNotificationTopic struct {
	Kind                           string   `json:"kind"`
	Topic                          string   `json:"topic"`
	Transport                      string   `json:"transport"`
	Delivery                       string   `json:"delivery"`
	State                          string   `json:"state"`
	ClientEventRequestOperation    string   `json:"client_event_request_operation"`
	ClientEventRequestKind         string   `json:"client_event_request_kind"`
	ClientRequestAuthority         string   `json:"client_request_authority"`
	ClientRequestFields            []string `json:"client_request_fields"`
	LookupKeyFields                []string `json:"lookup_key_fields"`
	ServerProjectionFields         []string `json:"server_projection_fields"`
	ForbiddenClientRequestFields   []string `json:"forbidden_client_request_fields"`
	ServerAuthoritativeProjection  bool     `json:"server_authoritative_projection"`
	ServerPush                     bool     `json:"server_push"`
	ServiceCallback                bool     `json:"service_callback"`
	HighFrequencyBattleTickAllowed bool     `json:"high_frequency_battle_tick_allowed"`
	ClientResultSubmitAllowed      bool     `json:"client_result_submit_allowed"`
}

type BusinessEventRequestContract struct {
	Kind                           string   `json:"kind"`
	ClientEventRequestOperation    string   `json:"client_event_request_operation"`
	ClientRPCOperation             string   `json:"client_rpc_operation"`
	ClientWSSOperation             string   `json:"client_wss_operation"`
	ClientRequestAuthority         string   `json:"client_request_authority"`
	ClientRequestFields            []string `json:"client_request_fields"`
	LookupKeyFields                []string `json:"lookup_key_fields"`
	ServerProjectionFields         []string `json:"server_projection_fields"`
	ForbiddenClientRequestFields   []string `json:"forbidden_client_request_fields"`
	BusinessEnvelopeRequired       bool     `json:"business_envelope_required"`
	ServerAuthoritativeProjection  bool     `json:"server_authoritative_projection"`
	ServiceCallback                bool     `json:"service_callback"`
	HighFrequencyBattleTickAllowed bool     `json:"high_frequency_battle_tick_allowed"`
	ClientResultSubmitAllowed      bool     `json:"client_result_submit_allowed"`
}

type ClientOperationContract struct {
	Operation                      string   `json:"operation"`
	Transports                     []string `json:"transports"`
	Authority                      string   `json:"authority"`
	ClientRequestFields            []string `json:"client_request_fields"`
	ServerProjectionFields         []string `json:"server_projection_fields"`
	BusinessEnvelopeRequired       bool     `json:"business_envelope_required"`
	ServiceCallback                bool     `json:"service_callback"`
	ServerAuthoritativeProjection  bool     `json:"server_authoritative_projection"`
	HighFrequencyBattleTickAllowed bool     `json:"high_frequency_battle_tick_allowed"`
	ClientResultSubmitAllowed      bool     `json:"client_result_submit_allowed"`
	ForbiddenClientRequestFields   []string `json:"forbidden_client_request_fields"`
}

type DisallowedClientOperationContract struct {
	Operation               string `json:"operation"`
	Reason                  string `json:"reason"`
	ServiceOnly             bool   `json:"service_only"`
	ServiceCallbackRequired bool   `json:"service_callback_required"`
	PlayerSessionAllowed    bool   `json:"player_session_allowed"`
	BusinessEnvelopeAllowed bool   `json:"business_envelope_allowed"`
	ClientRPCAllowed        bool   `json:"client_rpc_allowed"`
	ClientWSSAllowed        bool   `json:"client_wss_allowed"`
	HighFrequencyBattleTick bool   `json:"high_frequency_battle_tick"`
	ClientResultSubmit      bool   `json:"client_result_submit"`
	ServerAuthoritative     bool   `json:"server_authoritative"`
}

type BusinessContractSnapshot struct {
	OK                                 bool                                `json:"ok"`
	Version                            VersionStamp                        `json:"version"`
	BusinessTransports                 []string                            `json:"business_transports"`
	BattleTransports                   []string                            `json:"battle_transports"`
	ClientOperations                   []string                            `json:"client_operations"`
	ClientRPCOperations                []string                            `json:"client_rpc_operations"`
	ClientWSSOperations                []string                            `json:"client_wss_operations"`
	ClientOperationContracts           []ClientOperationContract           `json:"client_operation_contracts"`
	DisallowedClientOperations         []string                            `json:"disallowed_client_operations"`
	DisallowedClientOperationContracts []DisallowedClientOperationContract `json:"disallowed_client_operation_contracts"`
	ServiceOnlyOperations              []string                            `json:"service_only_operations"`
	ServiceCallbacks                   []string                            `json:"service_callbacks"`
	ServiceCallbackContext             map[string]string                   `json:"service_callback_context"`
	ServiceCallbackAcceptedValues      []string                            `json:"service_callback_accepted_values"`
	ServiceCallbackPlayerAllowed       bool                                `json:"service_callback_player_session_allowed"`
	ServiceCallbackEnvelopeAllowed     bool                                `json:"service_callback_business_envelope_allowed"`
	SettlementAuthority                string                              `json:"settlement_authority"`
	BusinessNotifications              []string                            `json:"business_notifications"`
	BusinessEventRequestKinds          []string                            `json:"business_event_request_kinds"`
	BusinessEventRequestContracts      []BusinessEventRequestContract      `json:"business_event_request_contracts"`
	BusinessNotificationTopics         []BusinessNotificationTopic         `json:"business_notification_topics"`
	ClientAuthority                    []string                            `json:"client_authority"`
	ServerAuthority                    []string                            `json:"server_authority"`
	ForbiddenFields                    []string                            `json:"forbidden_fields"`
	BusinessEnvelopeRequired           bool                                `json:"business_envelope_required"`
	ClientResultSubmitAllowed          bool                                `json:"client_result_submit_allowed"`
	HighFrequencyBattleTickAllowed     bool                                `json:"high_frequency_battle_tick_allowed"`
	ServerTime                         time.Time                           `json:"server_time"`
	ServerAuthoritative                bool                                `json:"server_authoritative"`
}

type RoomRulesSnapshot struct {
	OK                                 bool                                `json:"ok"`
	Version                            VersionStamp                        `json:"version"`
	Room                               RoomSnapshot                        `json:"room"`
	Mode                               ModeConfig                          `json:"mode"`
	TickRate                           int                                 `json:"tick_rate"`
	InputDelayTicks                    int                                 `json:"input_delay_ticks"`
	BattleTicketTTL                    int                                 `json:"battle_ticket_ttl_seconds"`
	BusinessTransports                 []string                            `json:"business_transports"`
	BattleTransports                   []string                            `json:"battle_transports"`
	ClientOperations                   []string                            `json:"client_operations"`
	ClientRPCOperations                []string                            `json:"client_rpc_operations"`
	ClientWSSOperations                []string                            `json:"client_wss_operations"`
	ClientOperationContracts           []ClientOperationContract           `json:"client_operation_contracts"`
	DisallowedClientOperations         []string                            `json:"disallowed_client_operations"`
	DisallowedClientOperationContracts []DisallowedClientOperationContract `json:"disallowed_client_operation_contracts"`
	ServiceOnlyOperations              []string                            `json:"service_only_operations"`
	ServiceCallbacks                   []string                            `json:"service_callbacks"`
	ServiceCallbackContext             map[string]string                   `json:"service_callback_context"`
	ServiceCallbackAcceptedValues      []string                            `json:"service_callback_accepted_values"`
	ServiceCallbackPlayerAllowed       bool                                `json:"service_callback_player_session_allowed"`
	ServiceCallbackEnvelopeAllowed     bool                                `json:"service_callback_business_envelope_allowed"`
	SettlementAuthority                string                              `json:"settlement_authority"`
	BusinessNotifications              []string                            `json:"business_notifications"`
	BusinessEventRequestKinds          []string                            `json:"business_event_request_kinds"`
	BusinessEventRequestContracts      []BusinessEventRequestContract      `json:"business_event_request_contracts"`
	BusinessNotificationTopics         []BusinessNotificationTopic         `json:"business_notification_topics"`
	ClientAuthority                    []string                            `json:"client_authority"`
	ServerAuthority                    []string                            `json:"server_authority"`
	ForbiddenFields                    []string                            `json:"forbidden_fields"`
	BusinessEnvelope                   bool                                `json:"business_envelope_required"`
	ClientResultSubmit                 bool                                `json:"client_result_submit_allowed"`
	HighFrequencyBattleTickAllowed     bool                                `json:"high_frequency_battle_tick_allowed"`
	ServerTime                         time.Time                           `json:"server_time"`
	ServerAuthoritative                bool                                `json:"server_authoritative"`
}

type QueueResponse struct {
	OK               bool                    `json:"ok"`
	QueueStatus      string                  `json:"queue_status"`
	TicketID         string                  `json:"ticket_id"`
	MatchID          string                  `json:"match_id,omitempty"`
	ModeID           string                  `json:"mode_id"`
	Loadout          PlayerLoadout           `json:"loadout"`
	BossID           string                  `json:"boss_id,omitempty"`
	RoomCode         string                  `json:"room_code,omitempty"`
	RoomStatus       string                  `json:"room_status,omitempty"`
	RequiredPlayers  int                     `json:"required_players"`
	CurrentPlayers   int                     `json:"current_players"`
	MatchStart       *MatchStartEvent        `json:"match_start,omitempty"`
	BattleAllocation *BattleServerAllocation `json:"battle_allocation,omitempty"`
	BattleTicket     *SignedBattleTicket     `json:"battle_ticket,omitempty"`
}

type PresenceHeartbeatRequest struct {
	TicketID        string `json:"ticket_id"`
	MatchID         string `json:"match_id"`
	ClientTick      int    `json:"client_tick"`
	LastEventCursor int    `json:"last_event_cursor"`
}

type PresenceHeartbeatResponse struct {
	OK                   bool                    `json:"ok"`
	UserID               string                  `json:"user_id"`
	PresenceStatus       string                  `json:"presence_status"`
	SessionStatus        string                  `json:"session_status"`
	TicketID             string                  `json:"ticket_id,omitempty"`
	QueueStatus          string                  `json:"queue_status,omitempty"`
	RoomCode             string                  `json:"room_code,omitempty"`
	RoomStatus           string                  `json:"room_status,omitempty"`
	RequiredPlayers      int                     `json:"required_players,omitempty"`
	CurrentPlayers       int                     `json:"current_players,omitempty"`
	ModeID               string                  `json:"mode_id,omitempty"`
	Loadout              PlayerLoadout           `json:"loadout,omitempty"`
	MatchID              string                  `json:"match_id,omitempty"`
	MatchStatus          string                  `json:"match_status,omitempty"`
	MatchTick            int                     `json:"match_tick"`
	LastClientTick       int                     `json:"last_client_tick"`
	Connected            bool                    `json:"connected"`
	Ready                bool                    `json:"ready"`
	ReconnectSecondsLeft int                     `json:"reconnect_seconds_left"`
	LastEventCursor      int                     `json:"last_event_cursor"`
	LatestEventCursor    int                     `json:"latest_event_cursor"`
	OldestEventCursor    int                     `json:"oldest_event_cursor"`
	BattleAllocation     *BattleServerAllocation `json:"battle_allocation,omitempty"`
	BattleTicket         *SignedBattleTicket     `json:"battle_ticket,omitempty"`
	ServerTime           time.Time               `json:"server_time"`
	ServerAuthoritative  bool                    `json:"server_authoritative"`
}

type ReadyResponse struct {
	OK              bool                `json:"ok"`
	MatchID         string              `json:"match_id"`
	ReadyStatus     string              `json:"ready_status"`
	ReadyCount      int                 `json:"ready_count"`
	RequiredPlayers int                 `json:"required_players"`
	MatchStart      *MatchStartEvent    `json:"match_start,omitempty"`
	BattleTicket    *SignedBattleTicket `json:"battle_ticket,omitempty"`
}

type BusinessEventRequest struct {
	Kind     string `json:"kind"`
	TicketID string `json:"ticket_id,omitempty"`
	RoomCode string `json:"room_code,omitempty"`
	MatchID  string `json:"match_id,omitempty"`
}

type BusinessEvent struct {
	OK                                 bool                                `json:"ok"`
	Version                            VersionStamp                        `json:"version"`
	Kind                               string                              `json:"kind"`
	Topic                              string                              `json:"topic"`
	Delivery                           string                              `json:"delivery"`
	State                              string                              `json:"state"`
	ClientEventRequestOperation        string                              `json:"client_event_request_operation"`
	ClientRequestAuthority             string                              `json:"client_request_authority"`
	ClientRequestFields                []string                            `json:"client_request_fields"`
	LookupKeyFields                    []string                            `json:"lookup_key_fields"`
	ForbiddenClientRequestFields       []string                            `json:"forbidden_client_request_fields"`
	UserID                             string                              `json:"user_id"`
	ModeID                             string                              `json:"mode_id,omitempty"`
	RoomCode                           string                              `json:"room_code,omitempty"`
	TicketID                           string                              `json:"ticket_id,omitempty"`
	MatchID                            string                              `json:"match_id,omitempty"`
	QueueStatus                        string                              `json:"queue_status,omitempty"`
	RoomStatus                         string                              `json:"room_status,omitempty"`
	MatchStatus                        string                              `json:"match_status,omitempty"`
	ReadyCount                         int                                 `json:"ready_count,omitempty"`
	RequiredPlayers                    int                                 `json:"required_players,omitempty"`
	CurrentPlayers                     int                                 `json:"current_players,omitempty"`
	Room                               *RoomSnapshot                       `json:"room,omitempty"`
	Queue                              *QueueResponse                      `json:"queue,omitempty"`
	Ready                              *ReadyResponse                      `json:"ready,omitempty"`
	BattleAllocation                   *BattleServerAllocation             `json:"battle_allocation,omitempty"`
	BattleTicket                       *SignedBattleTicket                 `json:"battle_ticket,omitempty"`
	Settlement                         *MatchEndEvent                      `json:"settlement,omitempty"`
	Activity                           *ActivitySnapshot                   `json:"activity,omitempty"`
	AllowedClientOperations            []string                            `json:"allowed_client_operations"`
	AllowedClientRPCOperations         []string                            `json:"allowed_client_rpc_operations"`
	AllowedClientWSSOperations         []string                            `json:"allowed_client_wss_operations"`
	ClientOperationContracts           []ClientOperationContract           `json:"client_operation_contracts"`
	DisallowedClientOperations         []string                            `json:"disallowed_client_operations"`
	DisallowedClientOperationContracts []DisallowedClientOperationContract `json:"disallowed_client_operation_contracts"`
	ServiceOnlyOperations              []string                            `json:"service_only_operations"`
	ServiceCallbacks                   []string                            `json:"service_callbacks"`
	ServiceCallbackContext             map[string]string                   `json:"service_callback_context"`
	ServiceCallbackAcceptedValues      []string                            `json:"service_callback_accepted_values"`
	ServiceCallbackPlayerAllowed       bool                                `json:"service_callback_player_session_allowed"`
	ServiceCallbackEnvelopeAllowed     bool                                `json:"service_callback_business_envelope_allowed"`
	SettlementAuthority                string                              `json:"settlement_authority"`
	BusinessNotifications              []string                            `json:"business_notifications"`
	BusinessEventRequestKinds          []string                            `json:"business_event_request_kinds"`
	BusinessEventRequestContracts      []BusinessEventRequestContract      `json:"business_event_request_contracts"`
	BusinessNotificationTopics         []BusinessNotificationTopic         `json:"business_notification_topics"`
	BusinessEnvelopeRequired           bool                                `json:"business_envelope_required"`
	ForbiddenFields                    []string                            `json:"forbidden_fields"`
	HighFrequencyBattleTickAllowed     bool                                `json:"high_frequency_battle_tick_allowed"`
	ClientResultSubmitAllowed          bool                                `json:"client_result_submit_allowed"`
	ServerTime                         time.Time                           `json:"server_time"`
	ServerAuthoritative                bool                                `json:"server_authoritative"`
}

type MatchStartEvent struct {
	Type               string                  `json:"type"`
	MatchID            string                  `json:"match_id"`
	ModeID             string                  `json:"mode_id"`
	StageID            string                  `json:"stage_id"`
	RulesetVersion     string                  `json:"ruleset_version"`
	ModeRulesetVersion string                  `json:"mode_ruleset_version"`
	ServerSeed         int64                   `json:"server_seed"`
	InputDelayTicks    int                     `json:"input_delay_ticks"`
	TickRate           int                     `json:"tick_rate"`
	Players            []PlayerIdentity        `json:"players"`
	ModeState          map[string]any          `json:"mode_state"`
	BattleAllocation   *BattleServerAllocation `json:"battle_allocation,omitempty"`
}

type PlayerIdentity struct {
	PlayerID    string        `json:"player_id"`
	UserID      string        `json:"user_id"`
	DisplayName string        `json:"display_name"`
	Loadout     PlayerLoadout `json:"loadout"`
}

type InputPacket struct {
	Tick     int  `json:"tick"`
	Seq      int  `json:"seq"`
	Dir      int  `json:"dir"`
	Slow     bool `json:"slow"`
	Shoot    bool `json:"shoot"`
	Bomb     bool `json:"bomb"`
	CardSlot int  `json:"card_slot"`
}

type InputResponse struct {
	OK       bool        `json:"ok"`
	Accepted bool        `json:"accepted"`
	Reason   string      `json:"reason"`
	Packet   InputPacket `json:"packet"`
	Snapshot Snapshot    `json:"snapshot"`
}

type ReconnectResponse struct {
	OK                bool                `json:"ok"`
	MatchID           string              `json:"match_id"`
	UserID            string              `json:"user_id"`
	ReconnectStatus   string              `json:"reconnect_status"`
	Connected         bool                `json:"connected"`
	SecondsLeft       int                 `json:"seconds_left"`
	MatchStart        *MatchStartEvent    `json:"match_start,omitempty"`
	Snapshot          Snapshot            `json:"snapshot"`
	ServerTime        time.Time           `json:"server_time"`
	ReconnectDeadline time.Time           `json:"reconnect_deadline,omitempty"`
	BattleTicket      *SignedBattleTicket `json:"battle_ticket,omitempty"`
	// Tick is the match tick at the moment of the call: a reconnecting client
	// resyncs to it and respawns (T10 rule 4).
	Tick int `json:"tick"`
}

type EventStreamResponse struct {
	OK           bool         `json:"ok"`
	MatchID      string       `json:"match_id"`
	After        int          `json:"after"`
	Cursor       int          `json:"cursor"`
	LatestCursor int          `json:"latest_cursor"`
	OldestCursor int          `json:"oldest_cursor"`
	HasMore      bool         `json:"has_more"`
	Events       []MatchEvent `json:"events"`
	SnapshotTick int          `json:"snapshot_tick"`
	ServerTime   time.Time    `json:"server_time"`
}

type RematchResponse struct {
	OK                  bool             `json:"ok"`
	MatchID             string           `json:"match_id"`
	NewMatchID          string           `json:"new_match_id,omitempty"`
	RematchStatus       string           `json:"rematch_status"`
	AcceptedCount       int              `json:"accepted_count"`
	RequiredPlayers     int              `json:"required_players"`
	ModeID              string           `json:"mode_id"`
	StageID             string           `json:"stage_id"`
	Loadout             PlayerLoadout    `json:"loadout"`
	MatchStart          *MatchStartEvent `json:"match_start,omitempty"`
	ServerAuthoritative bool             `json:"server_authoritative"`
	ServerTime          time.Time        `json:"server_time"`
}

type ModeActionRequest struct {
	ModeID                    string         `json:"mode_id"`
	ActionType                string         `json:"action_type"`
	Payload                   map[string]any `json:"payload"`
	ClientResultAuthoritative bool           `json:"client_result_authoritative"`
}

type ModeActionResponse struct {
	OK                        bool           `json:"ok"`
	Accepted                  bool           `json:"accepted"`
	Reason                    string         `json:"reason"`
	MatchID                   string         `json:"match_id"`
	ModeID                    string         `json:"mode_id"`
	UserID                    string         `json:"user_id"`
	ActionID                  string         `json:"action_id"`
	ActionType                string         `json:"action_type"`
	Status                    string         `json:"status"`
	Payload                   map[string]any `json:"payload"`
	ModeState                 map[string]any `json:"mode_state"`
	Event                     MatchEvent     `json:"event"`
	ServerAuthoritative       bool           `json:"server_authoritative"`
	ClientResultAuthoritative bool           `json:"client_result_authoritative"`
	ServerTime                time.Time      `json:"server_time"`
}

type Snapshot struct {
	MatchID      string               `json:"match_id"`
	Tick         int                  `json:"tick"`
	Full         bool                 `json:"full"`
	StateHash    string               `json:"state_hash"`
	StageID      string               `json:"stage_id"`
	Players      []PlayerSnapshot     `json:"players"`
	BulletsDelta []BulletDelta        `json:"bullets_delta"`
	Score        []ScoreSnapshot      `json:"score"`
	ActiveCards  []ActiveCardSnapshot `json:"active_cards"`
	ModeState    map[string]any       `json:"mode_state"`
	Events       []MatchEvent         `json:"events"`
}

type PlayerSnapshot struct {
	UserID               string        `json:"user_id"`
	X                    float64       `json:"x"`
	Y                    float64       `json:"y"`
	Loadout              PlayerLoadout `json:"loadout"`
	Ready                bool          `json:"ready"`
	Connected            bool          `json:"connected"`
	LastTick             int           `json:"last_tick"`
	LastSeq              int           `json:"last_seq"`
	CardPlays            int           `json:"card_plays"`
	BombUses             int           `json:"bomb_uses"`
	GrazeCount           int           `json:"graze_count"`
	HitCount             int           `json:"hit_count"`
	DamageDealt          int           `json:"damage_dealt"`
	Energy               float64       `json:"energy,omitempty"`
	HandSize             int           `json:"hand_size,omitempty"`
	ReconnectSecondsLeft int           `json:"reconnect_seconds_left,omitempty"`
}

type ScoreSnapshot struct {
	UserID string `json:"user_id"`
	Score  int    `json:"score"`
}

type BulletDelta struct {
	Op        string  `json:"op"`
	BulletID  string  `json:"bullet_id"`
	PatternID string  `json:"pattern_id"`
	Kind      string  `json:"kind"`
	Tick      int     `json:"tick"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	VX        float64 `json:"vx,omitempty"`
	VY        float64 `json:"vy,omitempty"`
	Radius    float64 `json:"radius,omitempty"`
	Damage    int     `json:"damage,omitempty"`
	Color     string  `json:"color,omitempty"`
}

type ActiveCardSnapshot struct {
	ActivationID  string  `json:"activation_id"`
	UserID        string  `json:"user_id"`
	CardID        string  `json:"card_id"`
	Slot          int     `json:"slot"`
	StartedTick   int     `json:"started_tick"`
	ExpiresTick   int     `json:"expires_tick"`
	EffectKind    string  `json:"effect_kind"`
	Cost          float64 `json:"cost"`
	Damage        int     `json:"damage,omitempty"`
	CooldownUntil int     `json:"cooldown_until,omitempty"`
	PatternID     string  `json:"pattern_id,omitempty"`
	PurgeRadius   float64 `json:"purge_radius,omitempty"`
	PurgeCount    int     `json:"purge_count,omitempty"`
	TargetUserID  string  `json:"target_user_id,omitempty"`
}

type MatchEvent struct {
	Seq          int     `json:"seq,omitempty"`
	Type         string  `json:"type"`
	Tick         int     `json:"tick"`
	UserID       string  `json:"user_id,omitempty"`
	ActionID     string  `json:"action_id,omitempty"`
	ActionType   string  `json:"action_type,omitempty"`
	BulletID     string  `json:"bullet_id,omitempty"`
	CardID       string  `json:"card_id,omitempty"`
	NewMatchID   string  `json:"new_match_id,omitempty"`
	FromUserID   string  `json:"from_user_id,omitempty"`
	ToUserID     string  `json:"to_user_id,omitempty"`
	Slot         int     `json:"slot,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	Status       string  `json:"status,omitempty"`
	RoundIndex   int     `json:"round_index,omitempty"`
	ExpiresTick  int     `json:"expires_tick,omitempty"`
	Value        int     `json:"value,omitempty"`
	X            float64 `json:"x,omitempty"`
	Y            float64 `json:"y,omitempty"`
	TargetUserID string  `json:"target_user_id,omitempty"`
}

type MatchEndEvent struct {
	Type                 string           `json:"type"`
	OK                   bool             `json:"ok"`
	Duplicate            bool             `json:"duplicate"`
	MatchID              string           `json:"match_id"`
	UserID               string           `json:"user_id"`
	Mode                 string           `json:"mode"`
	StageID              string           `json:"stage_id"`
	Loadout              PlayerLoadout    `json:"loadout"`
	RulesetVersion       string           `json:"ruleset_version"`
	ModeRulesetVersion   string           `json:"mode_ruleset_version"`
	ServerSeed           int64            `json:"server_seed"`
	Status               string           `json:"status"`
	Result               string           `json:"result"`
	Score                int              `json:"score"`
	GrazeCount           int              `json:"graze_count"`
	HitCount             int              `json:"hit_count"`
	ReplayID             string           `json:"replay_id"`
	FinalResult          map[string]any   `json:"final_result"`
	RewardJSON           []RewardItem     `json:"reward_json"`
	TaskProgress         []TaskProgress   `json:"task_progress"`
	EventPoints          map[string]int   `json:"event_points"`
	LeaderboardUpdates   []LeaderboardRow `json:"leaderboard_updates"`
	ModeResult           map[string]any   `json:"mode_result"`
	ServerAuthoritative  bool             `json:"server_authoritative"`
	ClientAuthoredReward bool             `json:"client_authored_reward"`
	SettlementKey        string           `json:"settlement_key"`
	SettledAt            time.Time        `json:"settled_at"`
}

type ReplayRecord struct {
	OK                  bool           `json:"ok"`
	ReplayID            string         `json:"replay_id"`
	MatchID             string         `json:"match_id"`
	UserID              string         `json:"user_id"`
	ModeID              string         `json:"mode_id"`
	StageID             string         `json:"stage_id"`
	Loadout             PlayerLoadout  `json:"loadout"`
	RulesetVersion      string         `json:"ruleset_version"`
	ModeRulesetVersion  string         `json:"mode_ruleset_version"`
	ServerSeed          int64          `json:"server_seed"`
	TickRate            int            `json:"tick_rate"`
	StartedAt           time.Time      `json:"started_at,omitempty"`
	EndedAt             time.Time      `json:"ended_at,omitempty"`
	SettledAt           time.Time      `json:"settled_at"`
	ServerAuthoritative bool           `json:"server_authoritative"`
	StateHash           string         `json:"state_hash"`
	FinalResult         map[string]any `json:"final_result"`
	ModeResult          map[string]any `json:"mode_result"`
	InputCount          int            `json:"input_count"`
	EventCount          int            `json:"event_count"`
	Events              []MatchEvent   `json:"events"`
	Settlement          MatchEndEvent  `json:"settlement"`
	// HasDem / DemSize 是 thread B 追加字段：该 replay 是否已有可下载的
	// .dem 录制，以及容器字节数。仅追加，不改动既有字段语义。
	HasDem  bool  `json:"has_dem"`
	DemSize int64 `json:"dem_size,omitempty"`
}

type RewardItem struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id,omitempty"`
	Amount int    `json:"amount"`
	Source string `json:"source"`
}

type TaskProgress struct {
	TaskID   string `json:"task_id"`
	LabelKey string `json:"label_key"`
	Progress int    `json:"progress"`
	Target   int    `json:"target"`
	Claimed  bool   `json:"claimed"`
}

type TaskState struct {
	LabelKey string `json:"label_key"`
	Progress int    `json:"progress"`
	Target   int    `json:"target"`
	Claimed  bool   `json:"claimed"`
}

type EventState struct {
	LabelKey     string `json:"label_key"`
	StartsAt     string `json:"starts_at"`
	EndsAt       string `json:"ends_at"`
	Points       int    `json:"points"`
	RewardStatus string `json:"reward_status"`
}

type LeaderboardRow struct {
	LeaderboardID string  `json:"leaderboard_id"`
	LabelKey      string  `json:"label_key"`
	Score         int     `json:"score"`
	Rank          int     `json:"rank"`
	Percentile    float64 `json:"percentile"`
	SeasonID      string  `json:"season_id"`
	RewardStatus  string  `json:"reward_status,omitempty"`
}

type ActivitySnapshot struct {
	OK                    bool                      `json:"ok"`
	UserID                string                    `json:"user_id"`
	Tasks                 map[string]TaskState      `json:"tasks"`
	Events                map[string]EventState     `json:"events"`
	Leaderboards          map[string]LeaderboardRow `json:"leaderboards"`
	ClaimableTasks        int                       `json:"claimable_tasks"`
	ClaimableEvents       int                       `json:"claimable_events"`
	ClaimableLeaderboards int                       `json:"claimable_leaderboards"`
	ServerAuthoritative   bool                      `json:"server_authoritative"`
}

type ActivityClaimRequest struct {
	ClaimKind string `json:"claim_kind"`
	ClaimID   string `json:"claim_id"`
}

type ActivityClaimResult struct {
	OK                  bool         `json:"ok"`
	Duplicate           bool         `json:"duplicate"`
	Reason              string       `json:"reason"`
	ClaimKind           string       `json:"claim_kind"`
	ClaimID             string       `json:"claim_id"`
	UserID              string       `json:"user_id"`
	RewardJSON          []RewardItem `json:"reward_json"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	Claimed             bool         `json:"claimed"`
	RewardStatus        string       `json:"reward_status"`
	SettlementKey       string       `json:"settlement_key"`
	SettledAt           time.Time    `json:"settled_at"`
}

// ---- 签到 Check-in / 商城 Shop ----

// CheckinDayState 描述签到周期中某一天的状态。
type CheckinDayState struct {
	Day       int           `json:"day"`
	Reward    CheckinReward `json:"reward"`
	Claimed   bool          `json:"claimed"`
	Claimable bool          `json:"claimable"`
}

// CheckinReward 描述签到奖励（gold/ticket 为钱包货币键）。
type CheckinReward struct {
	Gold   int `json:"gold,omitempty"`
	Ticket int `json:"ticket,omitempty"`
}

// CheckinView 是 GET /v1/checkin 的响应。
type CheckinView struct {
	OK               bool              `json:"ok"`
	UserID           string            `json:"user_id"`
	CycleID          string            `json:"cycle_id"`
	Days             []CheckinDayState `json:"days"`
	Streak           int               `json:"streak"`
	NextClaimableDay int               `json:"next_claimable_day"`
	ServerTimeMs     int64             `json:"server_time_ms"`
}

// CheckinClaimView 是 POST /v1/checkin/claim 的响应。
type CheckinClaimView struct {
	OK               bool           `json:"ok"`
	Day              int            `json:"day"`
	Reward           CheckinReward  `json:"reward"`
	Wallet           map[string]int `json:"wallet"`
	Streak           int            `json:"streak"`
	NextClaimableDay int            `json:"next_claimable_day"`
	ServerTimeMs     int64          `json:"server_time_ms"`
}

// ShopItemView 描述商城中的一个商品。
type ShopItemView struct {
	ItemID      string         `json:"item_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Price       map[string]int `json:"price"`
	Grants      ShopGrant      `json:"grants"`
	Stock       int            `json:"stock"`
	Purchased   int            `json:"purchased"`
	Purchasable bool           `json:"purchasable"`
}

// ShopGrant 描述购买后发放的内容。
type ShopGrant struct {
	Item  string `json:"item"`
	Count int    `json:"count"`
}

// ShopView 是 GET /v1/shop 的响应。
type ShopView struct {
	OK           bool           `json:"ok"`
	Currency     string         `json:"currency"`
	Wallet       map[string]int `json:"wallet"`
	Items        []ShopItemView `json:"items"`
	ServerTimeMs int64          `json:"server_time_ms"`
}

// LegacyShopPurchaseRequest 是旧版 POST /v1/shop/purchase 的请求体。
// Nakama 的 shop.purchase 使用 runtime/core/shop.go 中的 ShopPurchaseRequest。
type LegacyShopPurchaseRequest struct {
	ItemID string `json:"item_id"`
	Count  int    `json:"count"`
}

// ShopPurchaseView 是 POST /v1/shop/purchase 的响应。
type ShopPurchaseView struct {
	OK           bool           `json:"ok"`
	ItemID       string         `json:"item_id"`
	Count        int            `json:"count"`
	Spent        map[string]int `json:"spent"`
	Wallet       map[string]int `json:"wallet"`
	Inventory    map[string]int `json:"inventory"`
	ServerTimeMs int64          `json:"server_time_ms"`
}
