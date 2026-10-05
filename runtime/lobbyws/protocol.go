package lobbyws

import (
	"encoding/hex"
	"encoding/json"
	"time"

	"gensoulkyo/runtime/core"
)

// LobbyMessageType mirrors phk.v1.LobbyMessageType. Messages are JSON-encoded
// envelopes: {"type": <int>, "seq": <int>, "payload": {...}} where the payload
// field names follow lobby.proto.
const (
	TypeAuthRequest         = 1
	TypeAuthResponse        = 2
	TypeBootstrapRequest    = 3
	TypeBootstrapResponse   = 4
	TypeRoomCreateRequest   = 5
	TypeRoomCreateResponse  = 6
	TypeRoomJoinRequest     = 7
	TypeRoomJoinResponse    = 8
	TypeRoomLeaveRequest    = 9
	TypeRoomState           = 10
	TypeMatchStart          = 11
	TypeMatchResult         = 12
	// TypeError is an extension (not in lobby.proto) used for transport-level
	// failures that do not correspond to a specific response message.
	TypeError = 100
)

// VersionStamp mirrors phk.v1.VersionStamp.
type VersionStamp struct {
	ProtocolVersion    int    `json:"protocol_version,omitempty"`
	BusinessAPIVersion string `json:"business_api_version,omitempty"`
	BattleAPIVersion   string `json:"battle_api_version,omitempty"`
	RulesetVersion     string `json:"ruleset_version,omitempty"`
	RulesetHash        string `json:"ruleset_hash,omitempty"`
}

// ErrorStatus mirrors phk.v1.ErrorStatus.
type ErrorStatus struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// DeckSnapshotRef mirrors phk.v1.DeckSnapshotRef.
type DeckSnapshotRef struct {
	DeckID          string   `json:"deck_id"`
	DeckSnapshotHash string  `json:"deck_snapshot_hash"`
	RulesetVersion  string   `json:"ruleset_version"`
	CardIDs         []string `json:"card_ids"`
}

// LoadoutRef mirrors phk.v1.LoadoutRef.
type LoadoutRef struct {
	UserID      string           `json:"user_id"`
	PlayerID    string           `json:"player_id"`
	CharacterID string           `json:"character_id"`
	StageID     string           `json:"stage_id"`
	RatingCode  string           `json:"rating_code"`
	Deck        *DeckSnapshotRef `json:"deck,omitempty"`
}

// LobbyPlayerProfile mirrors phk.v1.LobbyPlayerProfile.
type LobbyPlayerProfile struct {
	UserID      string     `json:"user_id"`
	PlayerID    string     `json:"player_id"`
	DisplayName string     `json:"display_name"`
	CharacterID string     `json:"character_id"`
	Level       int        `json:"level"`
	RatingCode  string     `json:"rating_code"`
	Loadout     LoadoutRef `json:"loadout"`
}

// LobbyPlayer mirrors phk.v1.LobbyPlayer.
type LobbyPlayer struct {
	UserID      string     `json:"user_id"`
	PlayerID    string     `json:"player_id"`
	DisplayName string     `json:"display_name"`
	Ready       bool       `json:"ready"`
	Host        bool       `json:"host"`
	Connected   bool       `json:"connected"`
	CharacterID string     `json:"character_id"`
	Loadout     LoadoutRef `json:"loadout"`
}

// LobbyAuthRequest mirrors phk.v1.LobbyAuthRequest.
type LobbyAuthRequest struct {
	Version      VersionStamp `json:"version"`
	SessionToken string       `json:"session_token"`
	UserID       string       `json:"user_id"`
	Platform     string       `json:"platform"`
	ClientBuild  string       `json:"client_build"`
}

// LobbyAuthResponse mirrors phk.v1.LobbyAuthResponse.
type LobbyAuthResponse struct {
	Version      VersionStamp `json:"version"`
	SessionToken string       `json:"session_token"`
	UserID       string       `json:"user_id"`
	PlayerID     string       `json:"player_id"`
	IssuedAtMS   int64        `json:"issued_at_ms"`
	ExpiresAtMS  int64        `json:"expires_at_ms"`
	Error        *ErrorStatus `json:"error,omitempty"`
}

// LobbyBootstrapRequest mirrors phk.v1.LobbyBootstrapRequest.
type LobbyBootstrapRequest struct {
	Version              VersionStamp `json:"version"`
	SessionToken         string       `json:"session_token"`
	UserID               string       `json:"user_id"`
	KnownRulesetVersion  string       `json:"known_ruleset_version"`
}

// LobbyBootstrapResponse mirrors phk.v1.LobbyBootstrapResponse.
type LobbyBootstrapResponse struct {
	Version              VersionStamp       `json:"version"`
	Profile              LobbyPlayerProfile `json:"profile"`
	RulesetVersion       string             `json:"ruleset_version"`
	UnlockedCharacterIDs []string           `json:"unlocked_character_ids"`
	ServerFlags          map[string]string  `json:"server_flags"`
	Error                *ErrorStatus       `json:"error,omitempty"`
}

// RoomCreateRequest mirrors phk.v1.RoomCreateRequest.
type RoomCreateRequest struct {
	Version    VersionStamp      `json:"version"`
	RoomCode   string            `json:"room_code"`
	ModeID     string            `json:"mode_id"`
	HostUserID string            `json:"host_user_id"`
	Loadout    LoadoutRef        `json:"loadout"`
	ModeParams map[string]string `json:"mode_params"`
}

// RoomCreateResponse mirrors phk.v1.RoomCreateResponse.
type RoomCreateResponse struct {
	Version    VersionStamp     `json:"version"`
	RoomCode   string           `json:"room_code"`
	ModeID     string           `json:"mode_id"`
	HostUserID string           `json:"host_user_id"`
	Room       *RoomStateMessage `json:"room,omitempty"`
	Error      *ErrorStatus     `json:"error,omitempty"`
}

// RoomJoinRequest mirrors phk.v1.RoomJoinRequest.
type RoomJoinRequest struct {
	Version  VersionStamp `json:"version"`
	RoomCode string       `json:"room_code"`
	UserID   string       `json:"user_id"`
	PlayerID string       `json:"player_id"`
	Loadout  LoadoutRef   `json:"loadout"`
}

// RoomJoinResponse mirrors phk.v1.RoomJoinResponse.
type RoomJoinResponse struct {
	Version    VersionStamp      `json:"version"`
	RoomCode   string            `json:"room_code"`
	ModeID     string            `json:"mode_id"`
	HostUserID string            `json:"host_user_id"`
	Players    []LobbyPlayer     `json:"players"`
	Room       *RoomStateMessage `json:"room,omitempty"`
	Error      *ErrorStatus      `json:"error,omitempty"`
}

// RoomLeaveRequest mirrors phk.v1.RoomLeaveRequest.
type RoomLeaveRequest struct {
	Version  VersionStamp `json:"version"`
	RoomCode string       `json:"room_code"`
	UserID   string       `json:"user_id"`
	PlayerID string       `json:"player_id"`
	Reason   string       `json:"reason"`
}

// RoomStateMessage mirrors phk.v1.RoomStateMessage.
type RoomStateMessage struct {
	Version        VersionStamp     `json:"version"`
	RoomCode       string           `json:"room_code"`
	HostUserID     string           `json:"host_user_id"`
	Players        []LobbyPlayer    `json:"players"`
	ModeID         string           `json:"mode_id"`
	AllReady       bool             `json:"all_ready"`
	RulesetVersion string           `json:"ruleset_version"`
	ModeParams     map[string]string `json:"mode_params"`
}

// BattleTicket mirrors the phk.v1.BattleTicket fields carried inside a signed
// battle ticket.
type BattleTicket struct {
	Version          VersionStamp `json:"version"`
	TicketID         string       `json:"ticket_id"`
	MatchID          string       `json:"match_id"`
	UserID           string       `json:"user_id"`
	PlayerID         string       `json:"player_id"`
	ModeID           string       `json:"mode_id"`
	BattleServerID   string       `json:"battle_server_id"`
	Endpoint         string       `json:"endpoint"`
	DeckSnapshotHash string       `json:"deck_snapshot_hash"`
	RulesetVersion   string       `json:"ruleset_version"`
	TicketNonce      string       `json:"ticket_nonce"`
	IssuedAtMS       int64        `json:"issued_at_ms"`
	ExpiresAtMS      int64        `json:"expires_at_ms"`
	BusinessSessionID string      `json:"business_session_id"`
}

// SignedBattleTicket mirrors phk.v1.SignedBattleTicket. The signature is
// hex-encoded in JSON.
type SignedBattleTicket struct {
	Ticket       BattleTicket `json:"ticket"`
	SignatureAlg string       `json:"signature_alg"`
	KeyID        string       `json:"key_id"`
	Signature    string       `json:"signature"`
	PublicKeyHex string       `json:"public_key_hex,omitempty"`
}

// MatchStartMessage mirrors phk.v1.MatchStartMessage.
//
// NOTE: the proto declares server_seed as bytes; in this JSON transport the seed
// is a number for client convenience, with server_seed_hex as an equivalent
// hexadecimal representation.
type MatchStartMessage struct {
	Version            VersionStamp       `json:"version"`
	MatchID            string             `json:"match_id"`
	ServerSeed         int64              `json:"server_seed"`
	ServerSeedHex      string             `json:"server_seed_hex"`
	BattleServerID     string             `json:"battle_server_id"`
	Endpoint           string             `json:"endpoint"`
	SignedBattleTicket *SignedBattleTicket `json:"signed_battle_ticket,omitempty"`
	RulesetVersion     string             `json:"ruleset_version"`
	ModeID             string             `json:"mode_id"`
	PlayerIDs          []string           `json:"player_ids"`
	StartedAtMS        int64              `json:"started_at_ms"`
}

// MatchResultMessage mirrors phk.v1.MatchResultMessage.
type MatchResultMessage struct {
	Version             VersionStamp   `json:"version"`
	MatchID             string         `json:"match_id"`
	WinnerPlayerID      string         `json:"winner_player_id"`
	Points              map[string]int `json:"points"`
	ReplayID            string         `json:"replay_id"`
	ServerAuthoritative bool           `json:"server_authoritative"`
	ModeID              string         `json:"mode_id"`
	SettledAtMS         int64          `json:"settled_at_ms"`
}

// envelope is the JSON wrapper for every lobby message.
type envelope struct {
	Type    int             `json:"type"`
	Seq     int64           `json:"seq,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func versionStampFromCore(stamp core.VersionStamp) VersionStamp {
	return VersionStamp{
		ProtocolVersion:    stamp.ProtocolVersion,
		BusinessAPIVersion: stamp.BusinessAPIVersion,
		BattleAPIVersion:   stamp.BattleAPIVersion,
		RulesetVersion:     stamp.RulesetVersion,
	}
}

func loadoutRefFromCore(loadout core.PlayerLoadout, userID string, playerID string) LoadoutRef {
	return LoadoutRef{
		UserID:      userID,
		PlayerID:    playerID,
		CharacterID: loadout.CharacterID,
		StageID:     loadout.StageID,
		RatingCode:  loadout.RatingCode,
	}
}

func signedBattleTicketFromCore(signed *core.SignedBattleTicket) *SignedBattleTicket {
	if signed == nil {
		return nil
	}
	ticket := signed.Ticket
	return &SignedBattleTicket{
		Ticket: BattleTicket{
			Version:           versionStampFromCore(ticket.Version),
			TicketID:          ticket.TicketID,
			MatchID:           ticket.MatchID,
			UserID:            ticket.UserID,
			PlayerID:          ticket.PlayerID,
			ModeID:            ticket.ModeID,
			BattleServerID:    ticket.BattleServerID,
			Endpoint:          ticket.Endpoint,
			DeckSnapshotHash:  ticket.DeckSnapshotHash,
			RulesetVersion:    ticket.RulesetVersion,
			TicketNonce:       ticket.TicketNonceHex,
			IssuedAtMS:        ticket.IssuedAtMS,
			ExpiresAtMS:       ticket.ExpiresAtMS,
			BusinessSessionID: ticket.BusinessSessionID,
		},
		SignatureAlg: signed.SignatureAlg,
		KeyID:        signed.KeyID,
		Signature:    signed.SignatureHex,
		PublicKeyHex: signed.PublicKeyHex,
	}
}

func roomStateMessageFromSnapshot(snapshot core.RoomSnapshot) RoomStateMessage {
	players := make([]LobbyPlayer, 0, len(snapshot.Participants))
	for _, participant := range snapshot.Participants {
		// Before a match exists there is no battle player id yet; the user id is
		// used as a stable placeholder so the field is never empty.
		playerID := participant.UserID
		players = append(players, LobbyPlayer{
			UserID:      participant.UserID,
			PlayerID:    playerID,
			DisplayName: participant.DisplayName,
			Host:        participant.UserID == snapshot.HostUserID,
			Connected:   true,
			CharacterID: participant.Loadout.CharacterID,
			Loadout:     loadoutRefFromCore(participant.Loadout, participant.UserID, playerID),
		})
	}
	modeParams := map[string]string{}
	for key, value := range snapshot.ModeParams {
		if text, ok := value.(string); ok {
			modeParams[key] = text
		}
	}
	return RoomStateMessage{
		Version:        coreVersionStamp(snapshot.RulesetVersion),
		RoomCode:       snapshot.RoomCode,
		HostUserID:     snapshot.HostUserID,
		Players:        players,
		ModeID:         snapshot.ModeID,
		AllReady:       snapshot.RoomStatus == "found",
		RulesetVersion: snapshot.RulesetVersion,
		ModeParams:     modeParams,
	}
}

func coreVersionStamp(rulesetVersion string) VersionStamp {
	if rulesetVersion == "" {
		rulesetVersion = core.RulesetVersion
	}
	return VersionStamp{
		ProtocolVersion:    core.ProtocolVersion,
		BusinessAPIVersion: core.BusinessAPIVersion,
		BattleAPIVersion:   core.BattleAPIVersion,
		RulesetVersion:     rulesetVersion,
	}
}

func matchStartMessageFromEvent(event core.MatchStartEvent, signed *core.SignedBattleTicket, allocation *core.BattleServerAllocation) MatchStartMessage {
	battleServerID := ""
	endpoint := ""
	if allocation != nil {
		battleServerID = allocation.BattleServerID
		endpoint = allocation.Endpoint
	} else if event.BattleAllocation != nil {
		battleServerID = event.BattleAllocation.BattleServerID
		endpoint = event.BattleAllocation.Endpoint
	}
	playerIDs := make([]string, 0, len(event.Players))
	for _, player := range event.Players {
		playerIDs = append(playerIDs, player.PlayerID)
	}
	return MatchStartMessage{
		Version:            coreVersionStamp(event.RulesetVersion),
		MatchID:            event.MatchID,
		ServerSeed:         event.ServerSeed,
		ServerSeedHex:      seedHex(event.ServerSeed),
		BattleServerID:     battleServerID,
		Endpoint:           endpoint,
		SignedBattleTicket: signedBattleTicketFromCore(signed),
		RulesetVersion:     event.RulesetVersion,
		ModeID:             event.ModeID,
		PlayerIDs:          playerIDs,
		StartedAtMS:        time.Now().UTC().UnixMilli(),
	}
}

func seedHex(seed int64) string {
	value := uint64(seed)
	buf := []byte{
		byte(value >> 56), byte(value >> 48), byte(value >> 40), byte(value >> 32),
		byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value),
	}
	return hex.EncodeToString(buf)
}
