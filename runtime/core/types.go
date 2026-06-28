package core

import (
	"time"

	phkv1 "github.com/phantasm-klash/phk-protocol/gen/go/phk/v1"
)

const (
	ServerVersion          = "0.1.0"
	ProtocolVersion        = phkv1.ProtocolVersion
	BusinessAPIVersion     = phkv1.BusinessAPIVersion
	BattleAPIVersion       = phkv1.BattleAPIVersion
	RulesetVersion         = phkv1.RulesetVersion
	TickRate               = 60
	DefaultInputDelayTick  = 2
	DefaultMatchTicks      = 60 * 90
	ReconnectWindowSeconds = 30
	BattleTicketTTLSeconds = 60
	DefaultBattleServerID  = "battle-local-dev"
	DefaultBattleEndpoint  = "127.0.0.1:7901"
)

type ModeConfig struct {
	ModeID             string `json:"mode_id"`
	MinPlayers         int    `json:"min_players"`
	MaxPlayers         int    `json:"max_players"`
	ModeRulesetVersion string `json:"mode_ruleset_version"`
	RewardTableID      string `json:"reward_table_id"`
}

type PlayerLoadout struct {
	StageID             string `json:"stage_id"`
	CharacterID         string `json:"character_id"`
	RatingCode          string `json:"rating_code,omitempty"`
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
}

type Clock func() time.Time

type Config struct {
	Clock              Clock
	MatchDurationTicks int
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
	OK                  bool                     `json:"ok"`
	Version             VersionStamp             `json:"version"`
	MatchID             string                   `json:"match_id"`
	ModeID              string                   `json:"mode_id"`
	BattleServerID      string                   `json:"battle_server_id"`
	Endpoint            string                   `json:"endpoint"`
	Players             []BattleAllocationPlayer `json:"players"`
	ServerSeed          int64                    `json:"server_seed"`
	ServerSeedHex       string                   `json:"server_seed_hex"`
	ModeConfigHash      string                   `json:"mode_config_hash"`
	AllocatedAt         time.Time                `json:"allocated_at"`
	ServerAuthoritative bool                     `json:"server_authoritative"`
}

type BattleTicket struct {
	Version             VersionStamp `json:"version"`
	TicketID            string       `json:"ticket_id"`
	MatchID             string       `json:"match_id"`
	UserID              string       `json:"user_id"`
	PlayerID            string       `json:"player_id"`
	ModeID              string       `json:"mode_id"`
	BattleServerID      string       `json:"battle_server_id"`
	Endpoint            string       `json:"endpoint"`
	DeckSnapshotHash    string       `json:"deck_snapshot_hash"`
	RulesetVersion      string       `json:"ruleset_version"`
	TicketNonceHex      string       `json:"ticket_nonce_hex"`
	IssuedAt            time.Time    `json:"issued_at"`
	ExpiresAt           time.Time    `json:"expires_at"`
	IssuedAtMS          int64        `json:"issued_at_ms"`
	ExpiresAtMS         int64        `json:"expires_at_ms"`
	BusinessSessionID   string       `json:"business_session_id"`
	ServerAuthoritative bool         `json:"server_authoritative"`
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
	SignedResult SignedBattleResult `json:"signed_result"`
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
	ModeID       string         `json:"mode_id"`
	ActiveDeckID string         `json:"active_deck_id"`
	DeckSnapshot DeckSnapshot   `json:"deck_snapshot"`
	ModeParams   map[string]any `json:"mode_params"`
}

type CreateRoomRequest struct {
	ModeID       string         `json:"mode_id"`
	ActiveDeckID string         `json:"active_deck_id"`
	DeckSnapshot DeckSnapshot   `json:"deck_snapshot"`
	ModeParams   map[string]any `json:"mode_params"`
}

type JoinRoomRequest struct {
	ModeID       string         `json:"mode_id"`
	ActiveDeckID string         `json:"active_deck_id"`
	DeckSnapshot DeckSnapshot   `json:"deck_snapshot"`
	ModeParams   map[string]any `json:"mode_params"`
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
	StageID             string                    `json:"stage_id"`
	ModeRulesetVersion  string                    `json:"mode_ruleset_version"`
	RulesetVersion      string                    `json:"ruleset_version"`
	ModeConfigHash      string                    `json:"mode_config_hash"`
	ModeParams          map[string]any            `json:"mode_params,omitempty"`
	Participants        []RoomParticipantSnapshot `json:"participants"`
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

type RoomRulesSnapshot struct {
	OK                  bool         `json:"ok"`
	Version             VersionStamp `json:"version"`
	Room                RoomSnapshot `json:"room"`
	Mode                ModeConfig   `json:"mode"`
	TickRate            int          `json:"tick_rate"`
	InputDelayTicks     int          `json:"input_delay_ticks"`
	BattleTicketTTL     int          `json:"battle_ticket_ttl_seconds"`
	ClientAuthority     []string     `json:"client_authority"`
	ServerAuthority     []string     `json:"server_authority"`
	ForbiddenFields     []string     `json:"forbidden_fields"`
	ServerTime          time.Time    `json:"server_time"`
	ServerAuthoritative bool         `json:"server_authoritative"`
}

type QueueResponse struct {
	OK               bool                    `json:"ok"`
	QueueStatus      string                  `json:"queue_status"`
	TicketID         string                  `json:"ticket_id"`
	MatchID          string                  `json:"match_id,omitempty"`
	ModeID           string                  `json:"mode_id"`
	Loadout          PlayerLoadout           `json:"loadout"`
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
	OK                   bool          `json:"ok"`
	UserID               string        `json:"user_id"`
	PresenceStatus       string        `json:"presence_status"`
	SessionStatus        string        `json:"session_status"`
	TicketID             string        `json:"ticket_id,omitempty"`
	QueueStatus          string        `json:"queue_status,omitempty"`
	RoomCode             string        `json:"room_code,omitempty"`
	RoomStatus           string        `json:"room_status,omitempty"`
	RequiredPlayers      int           `json:"required_players,omitempty"`
	CurrentPlayers       int           `json:"current_players,omitempty"`
	ModeID               string        `json:"mode_id,omitempty"`
	Loadout              PlayerLoadout `json:"loadout,omitempty"`
	MatchID              string        `json:"match_id,omitempty"`
	MatchStatus          string        `json:"match_status,omitempty"`
	MatchTick            int           `json:"match_tick"`
	LastClientTick       int           `json:"last_client_tick"`
	Connected            bool          `json:"connected"`
	Ready                bool          `json:"ready"`
	ReconnectSecondsLeft int           `json:"reconnect_seconds_left"`
	LastEventCursor      int           `json:"last_event_cursor"`
	LatestEventCursor    int           `json:"latest_event_cursor"`
	OldestEventCursor    int           `json:"oldest_event_cursor"`
	ServerTime           time.Time     `json:"server_time"`
	ServerAuthoritative  bool          `json:"server_authoritative"`
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
}

type MatchEvent struct {
	Seq         int     `json:"seq,omitempty"`
	Type        string  `json:"type"`
	Tick        int     `json:"tick"`
	UserID      string  `json:"user_id,omitempty"`
	ActionID    string  `json:"action_id,omitempty"`
	ActionType  string  `json:"action_type,omitempty"`
	BulletID    string  `json:"bullet_id,omitempty"`
	CardID      string  `json:"card_id,omitempty"`
	NewMatchID  string  `json:"new_match_id,omitempty"`
	FromUserID  string  `json:"from_user_id,omitempty"`
	ToUserID    string  `json:"to_user_id,omitempty"`
	Slot        int     `json:"slot,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	Status      string  `json:"status,omitempty"`
	RoundIndex  int     `json:"round_index,omitempty"`
	ExpiresTick int     `json:"expires_tick,omitempty"`
	Value       int     `json:"value,omitempty"`
	X           float64 `json:"x,omitempty"`
	Y           float64 `json:"y,omitempty"`
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
