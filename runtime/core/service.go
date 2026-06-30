package core

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	codeUnauthorized     = "unauthorized"
	codeNotFound         = "not_found"
	codeInvalidRequest   = "invalid_request"
	codeInvalidDeck      = "deck_invalid"
	codeInvalidMode      = "mode_invalid"
	codeForbiddenField   = "forbidden_field"
	codeInvalidInput     = "input_invalid"
	codeMatchState       = "match_state_invalid"
	codeClaimIneligible  = "claim_ineligible"
	codeRoomUnavailable  = "room_unavailable"
	codeReconnectExpired = "reconnect_expired"
	codeModeAction       = "mode_action_invalid"
	codeBattleServer     = "battle_server_unavailable"
	startX               = 480.0
	startY               = 600.0
	playfieldMinX        = 160.0
	playfieldMaxX        = 800.0
	playfieldMinY        = 48.0
	playfieldMaxY        = 672.0
	cardHandLimit        = 4
	cardStartingEnergy   = 2.0
	cardMaxEnergy        = 10.0
	cardEnergyPerTick    = 0.0025
	deckSize             = 20
	maxCopiesPerCard     = 2
	maxHighRareCards     = 6
	maxInterferenceCards = 4
	defaultDeckID        = "local_default"
	defaultDeckName      = "Local Practice"
	defaultDeckFormat    = "local_practice"
	defaultChestPoolID   = "local_basic"
	maxChestOpenCount    = 10
	maxCardLevel         = 5
	maxEventLogEntries   = 256
	defaultSeasonID      = "local_s0"
	defaultRatingCode    = "copper"
	defaultRankScore     = 1000
	top30Threshold       = 0.30
	worldBossInstanceID  = "world_boss_local_s0_001"
	worldBossMaxHP       = 100000
	worldBossDailyLimit  = 3
)

var forbiddenClientFields = map[string]struct{}{
	"score":                  {},
	"graze":                  {},
	"hit":                    {},
	"hits":                   {},
	"damage":                 {},
	"position":               {},
	"reward":                 {},
	"reward_json":            {},
	"drop":                   {},
	"rank_score":             {},
	"boss_hp":                {},
	"active_cards":           {},
	"card_id":                {},
	"cooldown":               {},
	"cooldowns":              {},
	"energy":                 {},
	"hand":                   {},
	"mode_state":             {},
	"server_authoritative":   {},
	"client_authored_reward": {},
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func ErrorCode(err error) string {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return "internal"
}

func newError(code string, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Service struct {
	mu                    sync.Mutex
	clock                 Clock
	matchDurationTicks    int
	battleAuditRepo       BattleLifecycleAuditRepository
	battleAuditStatus     BattleLifecycleAuditStatus
	lobbyAuditRepo        LobbyLifecycleAuditRepository
	lobbyAuditStatus      LobbyLifecycleAuditStatus
	signingKeyID          string
	signingPublicKey      ed25519.PublicKey
	signingPrivateKey     ed25519.PrivateKey
	nextSeq               int64
	users                 map[string]*userState
	sessionToUser         map[string]string
	queues                map[string][]string
	tickets               map[string]*queueTicket
	rooms                 map[string]*roomState
	matches               map[string]*matchState
	rematches             map[string]*rematchState
	settlements           map[string]*MatchEndEvent
	replays               map[string]*ReplayRecord
	activityClaims        map[string]*ActivityClaimResult
	worldBoss             *worldBossState
	worldBossAttempts     map[string]map[string]int
	battleServers         map[string]*battleServerState
	battleAllocations     map[string]*BattleServerAllocation
	battleTickets         map[string]*SignedBattleTicket
	battleTicketsByID     map[string]*SignedBattleTicket
	consumedBattleTickets map[string]time.Time
}

type userState struct {
	UserID           string
	SessionToken     string
	DeviceID         string
	DisplayName      string
	CreatedAt        time.Time
	LastSeenAt       time.Time
	Wallet           map[string]int
	Inventory        map[string]CardInventoryEntry
	Decks            map[string]DeckRecord
	ActiveDeckID     string
	Chests           map[string]int
	ChestPity        map[string]ChestPityState
	ChestOpenings    []ChestOpeningRecord
	LastChestResults []ChestOpenResult
	Tasks            map[string]TaskState
	Events           map[string]EventState
	Leaderboards     map[string]LeaderboardRow
	Certification    CertificationProfile
}

type queueTicket struct {
	TicketID     string
	UserID       string
	ModeID       string
	QueueKey     string
	DeckSnapshot DeckSnapshot
	Loadout      PlayerLoadout
	CreatedAt    time.Time
	MatchID      string
	Status       string
	RoomCode     string
	RoomStatus   string
	LastSeenAt   time.Time
}

type roomState struct {
	RoomCode   string
	ModeID     string
	HostUserID string
	TicketIDs  []string
	CreatedAt  time.Time
	MatchID    string
	Status     string
	StageID    string
	ModeParams map[string]any
	Messages   []LobbyMessage
}

type matchParticipantSpec struct {
	UserID       string
	DeckSnapshot DeckSnapshot
	Loadout      PlayerLoadout
}

type rematchState struct {
	MatchID    string
	NewMatchID string
	Accepted   map[string]time.Time
	CreatedAt  time.Time
}

type matchState struct {
	MatchID            string
	ModeID             string
	RulesetVersion     string
	ModeRulesetVersion string
	RewardTableID      string
	ServerSeed         int64
	Status             string
	StageID            string
	Tick               int
	LastSimulatedTick  int
	CreatedAt          time.Time
	StartedAt          time.Time
	EndedAt            time.Time
	PlayerIDs          []string
	Players            map[string]*playerState
	BossHP             int
	BossMaxHP          int
	NextBulletSeq      int
	Bullets            map[string]*bulletState
	LastBulletDeltas   []BulletDelta
	LastEvents         []MatchEvent
	EventLog           []MatchEvent
	NextEventSeq       int
	ActiveCards        map[string]*activeCardState
	NextCardSeq        int
	NextModeActionSeq  int
	ModeActions        map[string]ModeActionResponse
	TransferredCards   map[string]string
	ModeState          map[string]any
	WorldBossDamage    int
	WorldBossApplied   bool
	WorldBossDefeated  bool
	BattleAllocation   *BattleServerAllocation
	BattleResultHash   string
	BattleResultReplay string
	BattleResultKeyID  string
	BattleResultAt     time.Time
}

type battleServerState struct {
	BattleServerID string
	Endpoint       string
	Region         string
	BuildID        string
	Capacity       int
	ActiveMatches  int
	Load           float64
	Status         string
	SupportedModes []string
	LastSeenAt     time.Time
}

type worldBossState struct {
	BossInstanceID      string
	SeasonID            string
	MaxHP               int
	CurrentHP           int
	StartsAt            time.Time
	EndsAt              time.Time
	DefeatedAt          time.Time
	DefeatedByMatchID   string
	DefeatedByUserID    string
	AnnouncementEmitted bool
}

type playerState struct {
	UserID            string
	DisplayName       string
	Ready             bool
	Connected         bool
	DisconnectedAt    time.Time
	LastSeenAt        time.Time
	X                 float64
	Y                 float64
	LastTick          int
	LastSeq           int
	Score             int
	GrazeCount        int
	HitCount          int
	BombUses          int
	CardPlays         int
	DamageDealt       int
	Energy            float64
	Hand              []string
	DrawCursor        int
	Cooldowns         map[string]int
	InvulnerableUntil int
	DeckSnapshot      DeckSnapshot
	Loadout           PlayerLoadout
	Inputs            []InputPacket
	GrazedBullets     map[string]struct{}
}

type activeCardState struct {
	ActivationID  string
	UserID        string
	CardID        string
	Slot          int
	StartedTick   int
	ExpiresTick   int
	EffectKind    string
	Cost          float64
	Damage        int
	CooldownUntil int
}

type serverCardDefinition struct {
	CardID        string
	EffectKind    string
	Cost          float64
	CooldownTicks int
	DurationTicks int
	Damage        int
	ScoreBonus    int
	EnergyGain    float64
	DrawCards     int
}

type serverCardActivation struct {
	ActivationID  string
	CardID        string
	EffectKind    string
	ExpiresTick   int
	CooldownUntil int
	Damage        int
	ScoreBonus    int
	Cost          float64
	EnergyGain    float64
	DrawCards     int
}

var serverCardCatalog = map[string]serverCardDefinition{
	"focus_lens":     {CardID: "focus_lens", EffectKind: "self", Cost: 2, CooldownTicks: 240, DurationTicks: 360, Damage: 12, ScoreBonus: 18},
	"hitbox_charm":   {CardID: "hitbox_charm", EffectKind: "self", Cost: 3, CooldownTicks: 420, DurationTicks: 300, Damage: 14, ScoreBonus: 20},
	"density_surge":  {CardID: "density_surge", EffectKind: "pattern", Cost: 4, CooldownTicks: 540, DurationTicks: 420, Damage: 30, ScoreBonus: 32},
	"tempo_break":    {CardID: "tempo_break", EffectKind: "pattern", Cost: 3, CooldownTicks: 480, DurationTicks: 300, Damage: 22, ScoreBonus: 26},
	"bomb_amplifier": {CardID: "bomb_amplifier", EffectKind: "self", Cost: 2, CooldownTicks: 360, DurationTicks: 480, Damage: 16, ScoreBonus: 18},
	"guard_seal":     {CardID: "guard_seal", EffectKind: "self", Cost: 5, CooldownTicks: 720, DurationTicks: 900, Damage: 18, ScoreBonus: 30},
	"graze_engine":   {CardID: "graze_engine", EffectKind: "economy", Cost: 2, CooldownTicks: 300, DurationTicks: 360, Damage: 10, ScoreBonus: 20},
	"draw_sigil":     {CardID: "draw_sigil", EffectKind: "economy", Cost: 1, CooldownTicks: 420, DurationTicks: 1, Damage: 0, ScoreBonus: 12, EnergyGain: 0.75, DrawCards: 1},
	"aim_baffle":     {CardID: "aim_baffle", EffectKind: "pattern", Cost: 4, CooldownTicks: 600, DurationTicks: 360, Damage: 28, ScoreBonus: 32},
	"purge_charm":    {CardID: "purge_charm", EffectKind: "self", Cost: 3, CooldownTicks: 540, DurationTicks: 480, Damage: 18, ScoreBonus: 24},
	"curve_prism":    {CardID: "curve_prism", EffectKind: "pattern", Cost: 3, CooldownTicks: 500, DurationTicks: 360, Damage: 24, ScoreBonus: 28},
	"last_arc":       {CardID: "last_arc", EffectKind: "finisher", Cost: 6, CooldownTicks: 900, DurationTicks: 420, Damage: 48, ScoreBonus: 60},
}

var serverCardRarities = map[string]string{
	"focus_lens":     "common",
	"hitbox_charm":   "uncommon",
	"density_surge":  "rare",
	"tempo_break":    "uncommon",
	"bomb_amplifier": "common",
	"guard_seal":     "rare",
	"graze_engine":   "common",
	"draw_sigil":     "common",
	"aim_baffle":     "rare",
	"purge_charm":    "uncommon",
	"curve_prism":    "uncommon",
	"last_arc":       "epic",
}

var serverStrongInterferenceCards = map[string]struct{}{
	"density_surge": {},
	"aim_baffle":    {},
}

var serverRankedBannedCards = map[string]struct{}{
	"last_arc": {},
}

var allowedStageIDs = map[string]struct{}{
	"starlit_lanes":   {},
	"misty_crossfire": {},
	"clockwork_bloom": {},
	"lunar_maze":      {},
}

var allowedCharacterIDs = map[string]struct{}{
	"balanced":    {},
	"precision":   {},
	"wide":        {},
	"spell_power": {},
}

func NewService(config Config) *Service {
	clock := config.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	matchTicks := config.MatchDurationTicks
	if matchTicks <= 0 {
		matchTicks = DefaultMatchTicks
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(nil)
	service := &Service{
		clock:              clock,
		matchDurationTicks: matchTicks,
		battleAuditRepo:    config.BattleLifecycleAuditRepo,
		battleAuditStatus: BattleLifecycleAuditStatus{
			OK:                  true,
			Configured:          config.BattleLifecycleAuditRepo != nil,
			ServerAuthoritative: true,
		},
		lobbyAuditRepo: config.LobbyLifecycleAuditRepo,
		lobbyAuditStatus: LobbyLifecycleAuditStatus{
			OK:                  true,
			Configured:          config.LobbyLifecycleAuditRepo != nil,
			ServerAuthoritative: true,
		},
		signingKeyID:          "dev-ed25519-0",
		signingPublicKey:      publicKey,
		signingPrivateKey:     privateKey,
		users:                 map[string]*userState{},
		sessionToUser:         map[string]string{},
		queues:                map[string][]string{},
		tickets:               map[string]*queueTicket{},
		rooms:                 map[string]*roomState{},
		matches:               map[string]*matchState{},
		rematches:             map[string]*rematchState{},
		settlements:           map[string]*MatchEndEvent{},
		replays:               map[string]*ReplayRecord{},
		activityClaims:        map[string]*ActivityClaimResult{},
		worldBossAttempts:     map[string]map[string]int{},
		battleServers:         map[string]*battleServerState{},
		battleAllocations:     map[string]*BattleServerAllocation{},
		battleTickets:         map[string]*SignedBattleTicket{},
		battleTicketsByID:     map[string]*SignedBattleTicket{},
		consumedBattleTickets: map[string]time.Time{},
	}
	service.registerDefaultBattleServerLocked()
	return service
}

func (s *Service) BattleLifecycleAuditStatus() BattleLifecycleAuditStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := s.battleAuditStatus
	status.Configured = s.battleAuditRepo != nil
	status.OK = status.Configured && status.LastError == ""
	status.ServerAuthoritative = true
	return status
}

func (s *Service) LobbyLifecycleAuditStatus() LobbyLifecycleAuditStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := s.lobbyAuditStatus
	status.Configured = s.lobbyAuditRepo != nil
	status.OK = status.Configured && status.LastError == ""
	status.ServerAuthoritative = true
	return status
}

func (s *Service) LoginAnonymous(req AnonymousLoginRequest) (*AuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = "Anonymous"
	}
	userID := s.nextIDLocked("user")
	token := s.nextIDLocked("session")
	now := s.clock()
	user := &userState{
		UserID:           userID,
		SessionToken:     token,
		DeviceID:         strings.TrimSpace(req.DeviceID),
		DisplayName:      displayName,
		CreatedAt:        now,
		LastSeenAt:       now,
		Wallet:           map[string]int{"points": 0, "card_dust": 0, "chest_keys": 1},
		Inventory:        defaultInventory(now),
		Decks:            map[string]DeckRecord{},
		ActiveDeckID:     defaultDeckID,
		Chests:           defaultChests(),
		ChestPity:        map[string]ChestPityState{},
		ChestOpenings:    []ChestOpeningRecord{},
		LastChestResults: []ChestOpenResult{},
		Tasks:            defaultTasks(),
		Events:           defaultEvents(),
		Leaderboards:     defaultLeaderboards(),
		Certification:    defaultCertificationProfile("", now),
	}
	user.Certification.UserID = user.UserID
	defaultDeck := defaultDeckRecord(now)
	user.Decks[defaultDeck.DeckID] = defaultDeck
	s.users[userID] = user
	s.sessionToUser[token] = userID
	return &AuthSession{UserID: userID, SessionToken: token, DisplayName: displayName, CreatedAt: now}, nil
}

func (s *Service) LoginExternal(req ExternalSessionRequest) (*AuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	userID := strings.TrimSpace(req.UserID)
	sessionToken := strings.TrimSpace(req.SessionToken)
	if userID == "" {
		return nil, newError(codeInvalidRequest, "external user id is required")
	}
	if sessionToken == "" {
		return nil, newError(codeUnauthorized, "external session token is required")
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		if provider := strings.TrimSpace(req.Provider); provider != "" {
			displayName = provider + " Player"
		} else {
			displayName = "External Player"
		}
	}
	now := s.clock()
	user := s.users[userID]
	if user == nil {
		deviceID := strings.TrimSpace(req.Provider)
		if deviceID == "" {
			deviceID = "external"
		}
		user = &userState{
			UserID:           userID,
			SessionToken:     sessionToken,
			DeviceID:         deviceID,
			DisplayName:      displayName,
			CreatedAt:        now,
			LastSeenAt:       now,
			Wallet:           map[string]int{"points": 0, "card_dust": 0, "chest_keys": 1},
			Inventory:        defaultInventory(now),
			Decks:            map[string]DeckRecord{},
			ActiveDeckID:     defaultDeckID,
			Chests:           defaultChests(),
			ChestPity:        map[string]ChestPityState{},
			ChestOpenings:    []ChestOpeningRecord{},
			LastChestResults: []ChestOpenResult{},
			Tasks:            defaultTasks(),
			Events:           defaultEvents(),
			Leaderboards:     defaultLeaderboards(),
			Certification:    defaultCertificationProfile("", now),
		}
		user.Certification.UserID = user.UserID
		defaultDeck := defaultDeckRecord(now)
		user.Decks[defaultDeck.DeckID] = defaultDeck
		s.users[userID] = user
	} else {
		delete(s.sessionToUser, user.SessionToken)
		user.SessionToken = sessionToken
		user.DisplayName = displayName
		user.LastSeenAt = now
	}
	s.sessionToUser[sessionToken] = userID
	return &AuthSession{UserID: user.UserID, SessionToken: user.SessionToken, DisplayName: user.DisplayName, CreatedAt: user.CreatedAt}, nil
}

func (s *Service) Bootstrap(sessionToken string) (*BootstrapSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	return &BootstrapSnapshot{
		UserID:        user.UserID,
		SessionToken:  user.SessionToken,
		DisplayName:   user.DisplayName,
		ServerVersion: ServerVersion,
		Ruleset:       RulesetVersion,
		Modes:         modeConfigList(),
		Wallet:        copyIntMap(user.Wallet),
		Inventory:     s.inventorySnapshotLocked(user),
		Decks:         s.deckListLocked(user),
		Chests:        s.chestSnapshotLocked(user),
		Tasks:         copyTasks(user.Tasks),
		Events:        copyEvents(user.Events),
		Leaderboards:  copyLeaderboards(user.Leaderboards),
		Certification: copyCertificationProfile(user.Certification),
		WorldBoss:     s.worldBossSnapshotLocked(user),
	}, nil
}

func (s *Service) Inventory(sessionToken string) (*InventorySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	snapshot := s.inventorySnapshotLocked(user)
	return &snapshot, nil
}

func (s *Service) UpgradeCard(sessionToken string, req CardUpgradeRequest) (*CardUpgradeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	if req.ClientResultAuthoritative {
		return nil, newError(codeForbiddenField, "client cannot submit client_result_authoritative")
	}
	cardID := strings.TrimSpace(req.CardID)
	if cardID == "" {
		return nil, newError(codeInvalidRequest, "card_id is required")
	}
	if _, ok := serverCardCatalog[cardID]; !ok {
		return nil, newError(codeNotFound, "card not found")
	}
	if user.Inventory == nil {
		user.Inventory = map[string]CardInventoryEntry{}
	}
	entry := user.Inventory[cardID]
	if entry.CardID == "" || entry.Copies <= 0 {
		return nil, newError(codeInvalidRequest, "card is not owned")
	}
	if entry.Level <= 0 {
		entry.Level = 1
	}
	targetLevel := req.TargetLevel
	if targetLevel <= 0 {
		targetLevel = entry.Level + 1
	}
	if targetLevel != entry.Level+1 {
		return nil, newError(codeInvalidRequest, "target_level must be current level + 1")
	}
	if targetLevel > maxCardLevel {
		return nil, newError(codeInvalidRequest, "card is already max level")
	}
	cost := cardUpgradeCost(cardID, targetLevel)
	if user.Wallet == nil {
		user.Wallet = map[string]int{}
	}
	for key, amount := range cost {
		if user.Wallet[key] < amount {
			return nil, newError(codeInvalidRequest, "not enough %s", key)
		}
	}
	for key, amount := range cost {
		user.Wallet[key] -= amount
	}
	oldLevel := entry.Level
	entry.Level = targetLevel
	if entry.FirstObtainedAt.IsZero() {
		entry.FirstObtainedAt = s.clock()
	}
	user.Inventory[cardID] = entry
	now := s.clock()
	return &CardUpgradeResponse{
		OK:                        true,
		UserID:                    user.UserID,
		CardID:                    cardID,
		Rarity:                    serverCardRarities[cardID],
		OldLevel:                  oldLevel,
		NewLevel:                  entry.Level,
		MaxLevel:                  maxCardLevel,
		Cost:                      copyIntMap(cost),
		Wallet:                    copyIntMap(user.Wallet),
		Inventory:                 s.inventorySnapshotLocked(user),
		ServerAuthoritative:       true,
		ClientResultAuthoritative: false,
		ServerTime:                now,
	}, nil
}

func (s *Service) Decks(sessionToken string) (*DeckListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	response := s.deckListLocked(user)
	return &response, nil
}

func (s *Service) Chests(sessionToken string) (*ChestSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	snapshot := s.chestSnapshotLocked(user)
	return &snapshot, nil
}

func (s *Service) OpenChest(sessionToken string, req ChestOpenRequest) (*ChestOpenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	if req.ClientResultAuthoritative {
		return nil, newError(codeForbiddenField, "client cannot submit client_result_authoritative")
	}
	poolID := strings.TrimSpace(req.PoolID)
	if poolID == "" {
		poolID = defaultChestPoolID
	}
	count := req.Count
	if count <= 0 {
		count = 1
	}
	if count > maxChestOpenCount {
		return nil, newError(codeInvalidRequest, "count must be <= %d", maxChestOpenCount)
	}
	pool, ok := chestPoolByID(poolID)
	if !ok || !pool.Enabled {
		return nil, newError(codeNotFound, "chest pool not found")
	}
	if user.Chests == nil {
		user.Chests = defaultChests()
	}
	if user.ChestPity == nil {
		user.ChestPity = map[string]ChestPityState{}
	}
	if user.Wallet == nil {
		user.Wallet = map[string]int{}
	}
	if user.Chests[poolID] < count {
		return nil, newError(codeInvalidRequest, "not enough chests")
	}
	cost := multipliedCost(pool.Cost, count)
	for key, amount := range cost {
		if user.Wallet[key] < amount {
			return nil, newError(codeInvalidRequest, "not enough %s", key)
		}
	}
	for key, amount := range cost {
		user.Wallet[key] -= amount
	}
	user.Chests[poolID] -= count
	openingID := s.nextIDLocked("chest_opening")
	seed := chestOpeningSeed(user.UserID, poolID, openingID, len(user.ChestOpenings), count)
	results := make([]ChestOpenResult, 0, count)
	for index := 0; index < count; index++ {
		results = append(results, s.rollChestResultLocked(user, pool, seed, index, openingID))
	}
	now := s.clock()
	audit := ChestOpeningRecord{
		OpeningID:  openingID,
		UserID:     user.UserID,
		PoolID:     poolID,
		Count:      count,
		Cost:       copyIntMap(cost),
		ServerSeed: seed,
		Results:    copyChestOpenResults(results),
		OpenedAt:   now,
	}
	user.ChestOpenings = append(user.ChestOpenings, audit)
	user.LastChestResults = copyChestOpenResults(results)
	return &ChestOpenResponse{
		OK:                        true,
		UserID:                    user.UserID,
		PoolID:                    poolID,
		Count:                     count,
		Wallet:                    copyIntMap(user.Wallet),
		OwnedChests:               copyIntMap(user.Chests),
		Inventory:                 s.inventorySnapshotLocked(user),
		PityCounters:              copyChestPity(user.ChestPity),
		Results:                   copyChestOpenResults(results),
		Audit:                     audit,
		ServerAuthoritative:       true,
		ClientResultAuthoritative: false,
		ServerTime:                now,
	}, nil
}

func (s *Service) SaveDeck(sessionToken string, req SaveDeckRequest) (*SaveDeckResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	deckID := strings.TrimSpace(req.DeckID)
	if deckID == "" {
		deckID = s.nextIDLocked("deck")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Saved Deck"
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = defaultDeckFormat
	}
	now := s.clock()
	record := DeckRecord{
		DeckID:         deckID,
		Name:           name,
		Format:         format,
		RulesetVersion: RulesetVersion,
		CardIDs:        copyStringSlice(req.CardIDs),
		Active:         req.Active,
		UpdatedAt:      now,
	}
	if err := validateDeckRecordForUser(user, record); err != nil {
		return nil, err
	}
	if record.Active {
		for id, existing := range user.Decks {
			existing.Active = false
			user.Decks[id] = existing
		}
		user.ActiveDeckID = record.DeckID
	} else if user.ActiveDeckID == "" {
		record.Active = true
		user.ActiveDeckID = record.DeckID
	}
	user.Decks[record.DeckID] = record
	return &SaveDeckResponse{
		OK:                  true,
		UserID:              user.UserID,
		Deck:                copyDeckRecord(record),
		ActiveDeckID:        user.ActiveDeckID,
		Validation:          []string{},
		ServerAuthoritative: true,
		ServerTime:          now,
	}, nil
}

func (s *Service) JoinQueue(sessionToken string, req JoinQueueRequest) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	modeID := strings.TrimSpace(req.ModeID)
	if modeID == "" {
		modeID = "certification"
	}
	mode, ok := ModeConfigs[modeID]
	if !ok {
		return nil, newError(codeInvalidMode, "unsupported mode %q", modeID)
	}
	deck, err := s.resolveDeckForMatchLocked(user, req.ActiveDeckID, req.DeckSnapshot)
	if err != nil {
		return nil, err
	}
	loadout, err := validateLoadout(modeID, req.ModeParams, user.Certification)
	if err != nil {
		return nil, err
	}
	if err := s.validateModeEntryLocked(user, modeID); err != nil {
		return nil, err
	}
	queueKey := queueKeyFor(modeID, loadout)
	ticketID := s.nextIDLocked("ticket")
	ticket := &queueTicket{
		TicketID:     ticketID,
		UserID:       user.UserID,
		ModeID:       modeID,
		QueueKey:     queueKey,
		DeckSnapshot: deck,
		Loadout:      loadout,
		CreatedAt:    s.clock(),
		Status:       "queued",
	}
	ticket.LastSeenAt = ticket.CreatedAt
	s.tickets[ticketID] = ticket
	s.queues[queueKey] = append(s.queues[queueKey], ticketID)

	if len(s.queues[queueKey]) >= mode.MinPlayers {
		groupIDs := append([]string{}, s.queues[queueKey][:mode.MinPlayers]...)
		s.queues[queueKey] = s.queues[queueKey][mode.MinPlayers:]
		match := s.createMatchLocked(mode, groupIDs)
		return s.queueResponseLocked(ticket, mode, len(s.queues[queueKey]), match), nil
	}
	return s.queueResponseLocked(ticket, mode, len(s.queues[queueKey]), nil), nil
}

func (s *Service) CreateRoom(sessionToken string, req CreateRoomRequest) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	modeID := strings.TrimSpace(req.ModeID)
	if modeID == "" {
		modeID = "certification"
	}
	mode, ok := ModeConfigs[modeID]
	if !ok {
		return nil, newError(codeInvalidMode, "unsupported mode %q", modeID)
	}
	deck, err := s.resolveDeckForMatchLocked(user, req.ActiveDeckID, req.DeckSnapshot)
	if err != nil {
		return nil, err
	}
	loadout, err := validateLoadout(modeID, req.ModeParams, user.Certification)
	if err != nil {
		return nil, err
	}
	if err := s.validateModeEntryLocked(user, modeID); err != nil {
		return nil, err
	}
	if ticket, room := s.waitingRoomTicketForUserLocked(user.UserID); ticket != nil && room != nil {
		existingMode := ModeConfigs[ticket.ModeID]
		s.recordLobbyRoomAuditLocked(room, ticket, user.UserID, "create_retry", s.clock())
		return s.queueResponseLocked(ticket, existingMode, s.ticketDepthLocked(ticket), nil), nil
	}
	roomCode := s.nextRoomCodeLocked()
	ticketID := s.nextIDLocked("ticket")
	ticket := &queueTicket{
		TicketID:     ticketID,
		UserID:       user.UserID,
		ModeID:       modeID,
		QueueKey:     queueKeyFor(modeID, loadout),
		DeckSnapshot: deck,
		Loadout:      loadout,
		CreatedAt:    s.clock(),
		Status:       "queued",
		RoomCode:     roomCode,
		RoomStatus:   "waiting",
	}
	ticket.LastSeenAt = ticket.CreatedAt
	room := &roomState{
		RoomCode:   roomCode,
		ModeID:     modeID,
		HostUserID: user.UserID,
		TicketIDs:  []string{ticketID},
		CreatedAt:  ticket.CreatedAt,
		Status:     "waiting",
		StageID:    loadout.StageID,
		ModeParams: copyAnyMap(req.ModeParams),
	}
	s.tickets[ticketID] = ticket
	s.rooms[roomCode] = room
	s.recordLobbyRoomAuditLocked(room, ticket, user.UserID, "created", ticket.CreatedAt)
	return s.queueResponseLocked(ticket, mode, len(room.TicketIDs), nil), nil
}

func (s *Service) JoinRoom(sessionToken string, roomCode string, req JoinRoomRequest) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	normalizedRoomCode := normalizeRoomCode(roomCode)
	if normalizedRoomCode == "" {
		return nil, newError(codeInvalidRequest, "room_code is required")
	}
	room, ok := s.rooms[normalizedRoomCode]
	if !ok {
		return nil, newError(codeNotFound, "room not found")
	}
	mode, ok := ModeConfigs[room.ModeID]
	if !ok {
		return nil, newError(codeInvalidMode, "unsupported mode %q", room.ModeID)
	}
	if room.Status != "waiting" {
		for _, ticketID := range room.TicketIDs {
			ticket := s.tickets[ticketID]
			if ticket != nil && ticket.UserID == user.UserID {
				var match *matchState
				if ticket.MatchID != "" {
					match = s.matches[ticket.MatchID]
				}
				return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), match), nil
			}
		}
		return nil, newError(codeRoomUnavailable, "room is %s", room.Status)
	}
	for _, ticketID := range room.TicketIDs {
		ticket := s.tickets[ticketID]
		if ticket != nil && ticket.UserID == user.UserID {
			s.recordLobbyRoomAuditLocked(room, ticket, user.UserID, "join_retry", s.clock())
			return s.queueResponseLocked(ticket, mode, len(room.TicketIDs), nil), nil
		}
	}
	modeID := strings.TrimSpace(req.ModeID)
	if modeID != "" && modeID != room.ModeID {
		return nil, newError(codeInvalidMode, "room mode is %q, got %q", room.ModeID, modeID)
	}
	if len(room.TicketIDs) >= mode.MaxPlayers {
		return nil, newError(codeRoomUnavailable, "room is full")
	}
	deck, err := s.resolveDeckForMatchLocked(user, req.ActiveDeckID, req.DeckSnapshot)
	if err != nil {
		return nil, err
	}
	loadout, err := validateLoadout(room.ModeID, req.ModeParams, user.Certification)
	if err != nil {
		return nil, err
	}
	if !hasLoadoutStageParam(req.ModeParams) {
		loadout.StageID = room.StageID
	}
	if loadout.StageID != room.StageID {
		return nil, newError(codeInvalidMode, "room stage is %q, got %q", room.StageID, loadout.StageID)
	}
	if err := s.validateModeEntryLocked(user, room.ModeID); err != nil {
		return nil, err
	}
	ticketID := s.nextIDLocked("ticket")
	ticket := &queueTicket{
		TicketID:     ticketID,
		UserID:       user.UserID,
		ModeID:       room.ModeID,
		QueueKey:     queueKeyFor(room.ModeID, loadout),
		DeckSnapshot: deck,
		Loadout:      loadout,
		CreatedAt:    s.clock(),
		Status:       "queued",
		RoomCode:     normalizedRoomCode,
		RoomStatus:   "waiting",
	}
	ticket.LastSeenAt = ticket.CreatedAt
	s.tickets[ticketID] = ticket
	room.TicketIDs = append(room.TicketIDs, ticketID)
	if len(room.TicketIDs) >= mode.MinPlayers {
		groupIDs := append([]string{}, room.TicketIDs[:mode.MinPlayers]...)
		match := s.createMatchLocked(mode, groupIDs)
		room.MatchID = match.MatchID
		room.Status = "found"
		s.recordLobbyRoomAuditLocked(room, ticket, user.UserID, "matched", s.clock())
		return s.queueResponseLocked(ticket, mode, len(room.TicketIDs), match), nil
	}
	s.recordLobbyRoomAuditLocked(room, ticket, user.UserID, "joined", ticket.CreatedAt)
	return s.queueResponseLocked(ticket, mode, len(room.TicketIDs), nil), nil
}

func (s *Service) ListRooms(sessionToken string) (*RoomListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	codes := make([]string, 0, len(s.rooms))
	for roomCode, room := range s.rooms {
		if room == nil || room.Status != "waiting" {
			continue
		}
		codes = append(codes, roomCode)
	}
	sort.Strings(codes)
	rooms := make([]RoomSnapshot, 0, len(codes))
	for _, roomCode := range codes {
		room := s.rooms[roomCode]
		if snapshot := s.roomSnapshotLocked(room, now); snapshot.OK {
			rooms = append(rooms, snapshot)
			s.recordLobbyRoomAuditLocked(room, nil, user.UserID, "listed", now)
		}
	}
	return &RoomListResponse{
		OK:                  true,
		Rooms:               rooms,
		ServerTime:          now,
		ServerAuthoritative: true,
	}, nil
}

func (s *Service) Room(sessionToken string, roomCode string) (*RoomSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	room, err := s.roomByCodeLocked(roomCode)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	snapshot := s.roomSnapshotLocked(room, now)
	s.recordLobbyRoomAuditLocked(room, nil, user.UserID, "snapshot_read", now)
	return &snapshot, nil
}

func (s *Service) RoomRules(sessionToken string, roomCode string) (*RoomRulesSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	room, err := s.roomByCodeLocked(roomCode)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	snapshot := s.roomSnapshotLocked(room, now)
	mode := ModeConfigs[room.ModeID]
	forbiddenFields := make([]string, 0, len(forbiddenClientFields))
	for field := range forbiddenClientFields {
		forbiddenFields = append(forbiddenFields, field)
	}
	sort.Strings(forbiddenFields)
	rules := &RoomRulesSnapshot{
		OK:              true,
		Version:         currentVersionStamp(),
		Room:            snapshot,
		Mode:            mode,
		TickRate:        TickRate,
		InputDelayTicks: DefaultInputDelayTick,
		BattleTicketTTL: BattleTicketTTLSeconds,
		ClientAuthority: []string{
			"input_packet",
			"cast_card_request",
			"mode_action_request",
			"ready",
			"reconnect_request",
		},
		ServerAuthority: []string{
			"battle_ticket",
			"deck_snapshot_hash",
			"match_seed",
			"state_snapshot",
			"score",
			"graze_count",
			"hit_count",
			"damage",
			"reward",
		},
		ForbiddenFields:     forbiddenFields,
		ServerTime:          now,
		ServerAuthoritative: true,
	}
	s.recordLobbyRoomAuditLocked(room, nil, user.UserID, "rules_read", now)
	return rules, nil
}

func (s *Service) LeaveRoom(sessionToken string, roomCode string) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	room, err := s.roomByCodeLocked(roomCode)
	if err != nil {
		if ErrorCode(err) == codeNotFound {
			if ticket := s.roomTicketForUserLocked(normalizeRoomCode(roomCode), user.UserID); ticket != nil && ticket.Status == "cancelled" {
				mode := ModeConfigs[ticket.ModeID]
				return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), nil), nil
			}
		}
		return nil, err
	}
	mode := ModeConfigs[room.ModeID]
	var ticket *queueTicket
	for _, ticketID := range room.TicketIDs {
		candidate := s.tickets[ticketID]
		if candidate != nil && candidate.UserID == user.UserID {
			ticket = candidate
			break
		}
	}
	if ticket == nil {
		return nil, newError(codeNotFound, "room ticket not found")
	}
	if ticket.MatchID != "" || room.MatchID != "" {
		return nil, newError(codeMatchState, "room already matched")
	}
	if ticket.Status == "cancelled" {
		return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), nil), nil
	}
	if ticket.Status != "queued" {
		return nil, newError(codeMatchState, "ticket is %s", ticket.Status)
	}
	roomAudit := s.lobbyRoomAuditRecordLocked(room, ticket, user.UserID, "left", s.clock())
	s.cancelRoomTicketLocked(ticket, user.UserID)
	roomAudit.RoomStatus = ticket.RoomStatus
	roomAudit.CurrentPlayers = s.ticketDepthLocked(ticket)
	if updated := s.rooms[normalizeRoomCode(roomCode)]; updated != nil {
		roomAudit.RoomStatus = updated.Status
		roomAudit.HostUserID = updated.HostUserID
		roomAudit.CurrentPlayers = len(updated.TicketIDs)
	}
	s.recordLobbyRoomAuditRecordLocked(roomAudit)
	return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), nil), nil
}

func (s *Service) LobbyMessage(sessionToken string, req LobbyMessageRequest) (*LobbyMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	room, err := s.roomByCodeLocked(req.RoomCode)
	if err != nil {
		return nil, err
	}
	if room.Status != "waiting" {
		return nil, newError(codeRoomUnavailable, "room is %s", room.Status)
	}
	if !s.roomHasUserLocked(room, user.UserID) {
		return nil, newError(codeUnauthorized, "user is not in room")
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "chat"
	}
	if kind != "chat" && kind != "announcement" {
		return nil, newError(codeInvalidRequest, "message kind must be chat or announcement")
	}
	if kind == "announcement" && room.HostUserID != user.UserID {
		return nil, newError(codeUnauthorized, "only room host can publish announcements")
	}
	messageID := strings.TrimSpace(req.MessageID)
	if messageID == "" {
		return nil, newError(codeInvalidRequest, "message_id is required")
	}
	if existing := roomMessageByID(room, messageID, user.UserID); existing != nil {
		duplicate := copyLobbyMessage(*existing)
		duplicate.Duplicate = true
		s.recordLobbyMessageAuditLocked(duplicate)
		return &duplicate, nil
	}
	if field := firstForbiddenFieldDeep(req.Metadata); field != "" {
		return nil, newError(codeForbiddenField, "client cannot author %s", field)
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && kind == "chat" {
		return nil, newError(codeInvalidRequest, "chat text is required")
	}
	if len(text) > 240 {
		text = text[:240]
	}
	message := LobbyMessage{
		MessageID:           messageID,
		RoomCode:            room.RoomCode,
		ModeID:              room.ModeID,
		Kind:                kind,
		UserID:              user.UserID,
		DisplayName:         user.DisplayName,
		Text:                text,
		Metadata:            sanitizedLobbyMetadata(req.Metadata),
		CreatedAt:           s.clock(),
		ServerAuthoritative: true,
	}
	room.Messages = append(room.Messages, message)
	if len(room.Messages) > 32 {
		room.Messages = append([]LobbyMessage{}, room.Messages[len(room.Messages)-32:]...)
	}
	s.recordLobbyMessageAuditLocked(message)
	return &message, nil
}

func (s *Service) QueueTicket(sessionToken string, ticketID string) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	ticket, ok := s.tickets[ticketID]
	if !ok || ticket.UserID != user.UserID {
		return nil, newError(codeNotFound, "ticket not found")
	}
	mode := ModeConfigs[ticket.ModeID]
	var match *matchState
	if ticket.MatchID != "" {
		match = s.matches[ticket.MatchID]
	}
	if ticket.RoomCode != "" {
		record := s.lobbyRoomAuditRecordLocked(s.rooms[normalizeRoomCode(ticket.RoomCode)], ticket, user.UserID, "ticket_read", s.clock())
		record.CurrentPlayers = s.ticketDepthLocked(ticket)
		s.recordLobbyRoomAuditRecordLocked(record)
	}
	return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), match), nil
}

func (s *Service) CancelTicket(sessionToken string, ticketID string) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	ticket, ok := s.tickets[ticketID]
	if !ok || ticket.UserID != user.UserID {
		return nil, newError(codeNotFound, "ticket not found")
	}
	mode := ModeConfigs[ticket.ModeID]
	if ticket.MatchID != "" {
		return nil, newError(codeMatchState, "ticket already matched")
	}
	if ticket.Status == "cancelled" {
		return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), nil), nil
	}
	if ticket.Status != "queued" {
		return nil, newError(codeMatchState, "ticket is %s", ticket.Status)
	}
	if ticket.RoomCode != "" {
		var roomAudit LobbyRoomAuditRecord
		if room := s.rooms[normalizeRoomCode(ticket.RoomCode)]; room != nil {
			roomAudit = s.lobbyRoomAuditRecordLocked(room, ticket, user.UserID, "cancelled", s.clock())
		}
		s.cancelRoomTicketLocked(ticket, user.UserID)
		if roomAudit.RoomCode != "" {
			roomAudit.RoomStatus = ticket.RoomStatus
			roomAudit.CurrentPlayers = s.ticketDepthLocked(ticket)
			if updated := s.rooms[normalizeRoomCode(ticket.RoomCode)]; updated != nil {
				roomAudit.RoomStatus = updated.Status
				roomAudit.HostUserID = updated.HostUserID
				roomAudit.CurrentPlayers = len(updated.TicketIDs)
			}
			s.recordLobbyRoomAuditRecordLocked(roomAudit)
		}
	} else {
		s.queues[ticket.QueueKey] = removeString(s.queues[ticket.QueueKey], ticket.TicketID)
		ticket.Status = "cancelled"
		ticket.RoomStatus = "none"
	}
	return s.queueResponseLocked(ticket, mode, s.ticketDepthLocked(ticket), nil), nil
}

func (s *Service) Heartbeat(sessionToken string, req PresenceHeartbeatRequest) (*PresenceHeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	user.LastSeenAt = now
	ticketID := strings.TrimSpace(req.TicketID)
	matchID := strings.TrimSpace(req.MatchID)
	if matchID == "" && ticketID != "" {
		ticket, ok := s.tickets[ticketID]
		if !ok || ticket.UserID != user.UserID {
			return nil, newError(codeNotFound, "ticket not found")
		}
		ticket.LastSeenAt = now
		matchID = ticket.MatchID
		if matchID == "" {
			s.recordLobbyHeartbeatAuditLocked(user.UserID, ticket, nil, now)
			return s.heartbeatTicketResponseLocked(user, ticket, req, now), nil
		}
	}
	if matchID != "" {
		match, player, err := s.matchPlayerLocked(user.UserID, matchID)
		if err != nil {
			return nil, err
		}
		player.LastSeenAt = now
		if player.Connected {
			player.DisconnectedAt = time.Time{}
		}
		s.recordLobbyHeartbeatAuditLocked(user.UserID, s.tickets[ticketID], match, now)
		return s.heartbeatMatchResponseLocked(user, match, player, ticketID, req, now), nil
	}
	return &PresenceHeartbeatResponse{
		OK:                  true,
		UserID:              user.UserID,
		PresenceStatus:      "online",
		SessionStatus:       "authenticated",
		QueueStatus:         "none",
		LastClientTick:      req.ClientTick,
		Connected:           true,
		ServerTime:          now,
		ServerAuthoritative: true,
	}, nil
}

func (s *Service) ReadyMatch(sessionToken string, matchID string) (*ReadyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status == "ended" {
		return nil, newError(codeMatchState, "match already ended")
	}
	wasReady := player.Ready
	wasRunning := match.Status == "running"
	player.Ready = true
	if !wasReady {
		appendMatchEventLocked(match, MatchEvent{Type: "player_ready", Tick: match.Tick, UserID: user.UserID})
	}
	readyCount := s.readyCountLocked(match)
	if readyCount == len(match.PlayerIDs) && match.Status != "running" {
		match.Status = "running"
		match.StartedAt = s.clock()
	}
	if !wasRunning && match.Status == "running" {
		appendMatchEventLocked(match, MatchEvent{Type: "match_started", Tick: match.Tick})
	}
	if !wasReady {
		s.recordLobbyReadyAuditLocked(match, user.UserID)
	}
	var start *MatchStartEvent
	if match.Status == "running" {
		event := s.matchStartLocked(match)
		start = &event
	}
	var ticket *SignedBattleTicket
	if match.Status == "running" {
		signed, err := s.signedBattleTicketLocked(match, user)
		if err != nil {
			return nil, err
		}
		ticket = signed
	}
	return &ReadyResponse{
		OK:              true,
		MatchID:         match.MatchID,
		ReadyStatus:     match.Status,
		ReadyCount:      readyCount,
		RequiredPlayers: len(match.PlayerIDs),
		MatchStart:      start,
		BattleTicket:    ticket,
	}, nil
}

func (s *Service) SubmitInput(sessionToken string, matchID string, raw map[string]any) (*InputResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status != "running" {
		return nil, newError(codeMatchState, "match is %s", match.Status)
	}
	if !player.Connected {
		return nil, newError(codeMatchState, "player is disconnected")
	}
	if field := firstForbiddenField(raw); field != "" {
		return nil, newError(codeForbiddenField, "client cannot submit %s", field)
	}
	packet, err := inputPacketFromMap(raw)
	if err != nil {
		return nil, err
	}
	if err := validateInputPacket(packet, player, match); err != nil {
		return nil, err
	}
	match.LastBulletDeltas = []BulletDelta{}
	match.LastEvents = []MatchEvent{}
	s.applyInputLocked(match, player, packet)
	s.advanceSimulationLocked(match, packet.Tick)
	if match.ModeID == "world_boss" {
		s.applyWorldBossModeStateLocked(match)
	}
	snapshot := s.snapshotLocked(match, false)
	return &InputResponse{OK: true, Accepted: true, Reason: "none", Packet: packet, Snapshot: snapshot}, nil
}

func (s *Service) DisconnectMatch(sessionToken string, matchID string) (*ReconnectResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status == "ended" {
		return nil, newError(codeMatchState, "match already ended")
	}
	now := s.clock()
	wasConnected := player.Connected
	player.Connected = false
	player.DisconnectedAt = now
	match.LastEvents = []MatchEvent{}
	appendMatchEventLocked(match, MatchEvent{Type: "player_disconnected", Tick: match.Tick, UserID: user.UserID})
	if wasConnected {
		s.recordLobbyConnectionAuditLocked(match, user.UserID, "disconnected")
	}
	snapshot := s.snapshotLocked(match, true)
	return &ReconnectResponse{
		OK:                true,
		MatchID:           match.MatchID,
		UserID:            user.UserID,
		ReconnectStatus:   "disconnected",
		Connected:         false,
		SecondsLeft:       ReconnectWindowSeconds,
		Snapshot:          snapshot,
		ServerTime:        now,
		ReconnectDeadline: now.Add(time.Duration(ReconnectWindowSeconds) * time.Second),
	}, nil
}

func (s *Service) ReconnectMatch(sessionToken string, matchID string) (*ReconnectResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status == "ended" {
		return nil, newError(codeMatchState, "match already ended")
	}
	now := s.clock()
	secondsLeft := ReconnectWindowSeconds
	if !player.DisconnectedAt.IsZero() {
		elapsed := int(now.Sub(player.DisconnectedAt).Seconds())
		secondsLeft = max(0, ReconnectWindowSeconds-elapsed)
		if secondsLeft <= 0 {
			return nil, newError(codeReconnectExpired, "reconnect window expired")
		}
	}
	wasDisconnected := !player.Connected
	player.Connected = true
	player.DisconnectedAt = time.Time{}
	match.LastEvents = []MatchEvent{}
	appendMatchEventLocked(match, MatchEvent{Type: "player_reconnected", Tick: match.Tick, UserID: user.UserID})
	if wasDisconnected {
		s.recordLobbyConnectionAuditLocked(match, user.UserID, "reconnected")
	}
	snapshot := s.snapshotLocked(match, true)
	var start *MatchStartEvent
	if match.Status == "running" {
		event := s.matchStartLocked(match)
		start = &event
	}
	var ticket *SignedBattleTicket
	if match.Status == "running" {
		signed, err := s.signedBattleTicketLocked(match, user)
		if err != nil {
			return nil, err
		}
		ticket = signed
	}
	return &ReconnectResponse{
		OK:              true,
		MatchID:         match.MatchID,
		UserID:          user.UserID,
		ReconnectStatus: "restored",
		Connected:       true,
		SecondsLeft:     secondsLeft,
		MatchStart:      start,
		Snapshot:        snapshot,
		ServerTime:      now,
		BattleTicket:    ticket,
	}, nil
}

func (s *Service) Snapshot(sessionToken string, matchID string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	snapshot := s.snapshotLocked(match, true)
	return &snapshot, nil
}

func (s *Service) MatchEvents(sessionToken string, matchID string, after int, limit int) (*EventStreamResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if after < 0 {
		after = 0
	}
	if limit <= 0 || limit > 64 {
		limit = 64
	}
	events := []MatchEvent{}
	latestCursor := 0
	oldestCursor := 0
	if len(match.EventLog) > 0 {
		oldestCursor = match.EventLog[0].Seq
		latestCursor = match.EventLog[len(match.EventLog)-1].Seq
	}
	for _, event := range match.EventLog {
		if event.Seq <= after {
			continue
		}
		if len(events) >= limit {
			break
		}
		events = append(events, event)
	}
	cursor := after
	if len(events) > 0 {
		cursor = events[len(events)-1].Seq
	}
	return &EventStreamResponse{
		OK:           true,
		MatchID:      match.MatchID,
		After:        after,
		Cursor:       cursor,
		LatestCursor: latestCursor,
		OldestCursor: oldestCursor,
		HasMore:      cursor < latestCursor,
		Events:       copyMatchEvents(events),
		SnapshotTick: match.Tick,
		ServerTime:   s.clock(),
	}, nil
}

func (s *Service) SubmitModeAction(sessionToken string, matchID string, raw map[string]any) (*ModeActionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	if field := firstForbiddenField(raw); field != "" {
		return nil, newError(codeForbiddenField, "client cannot submit %s", field)
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status == "ended" {
		return nil, newError(codeMatchState, "match already ended")
	}
	if !player.Connected {
		return nil, newError(codeMatchState, "player is disconnected")
	}
	req := modeActionRequestFromMap(raw)
	if req.ClientResultAuthoritative {
		return nil, newError(codeForbiddenField, "client cannot submit client_result_authoritative")
	}
	if req.ModeID == "" {
		req.ModeID = match.ModeID
	}
	if req.ModeID != match.ModeID {
		return nil, newError(codeInvalidMode, "match mode is %q, got %q", match.ModeID, req.ModeID)
	}
	if req.ActionType == "" {
		return nil, newError(codeInvalidRequest, "action_type is required")
	}
	response := s.applyModeActionLocked(match, player, req)
	return &response, nil
}

func (s *Service) SettleMatch(sessionToken string, matchID string, raw map[string]any) (*MatchEndEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	if field := firstForbiddenField(raw); field != "" {
		return nil, newError(codeForbiddenField, "client cannot submit %s", field)
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	duplicate := match.Status == "ended"
	if match.Status != "ended" {
		s.settleMatchLocked(match)
	}
	key := settlementKey(matchID, user.UserID)
	settlement, ok := s.settlements[key]
	if !ok {
		return nil, newError(codeNotFound, "settlement not found")
	}
	copy := *settlement
	copy.Duplicate = duplicate
	return &copy, nil
}

func (s *Service) RequestRematch(sessionToken string, matchID string) (*RematchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, player, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	if match.Status != "ended" {
		return nil, newError(codeMatchState, "match is %s", match.Status)
	}
	if _, ok := s.settlements[settlementKey(match.MatchID, user.UserID)]; !ok {
		return nil, newError(codeMatchState, "match is not settled for user")
	}
	for _, playerID := range match.PlayerIDs {
		if _, ok := s.settlements[settlementKey(match.MatchID, playerID)]; !ok {
			return nil, newError(codeMatchState, "match settlement is incomplete")
		}
	}
	state := s.rematches[match.MatchID]
	if state == nil {
		state = &rematchState{
			MatchID:   match.MatchID,
			Accepted:  map[string]time.Time{},
			CreatedAt: s.clock(),
		}
		s.rematches[match.MatchID] = state
	}
	if _, accepted := state.Accepted[user.UserID]; !accepted {
		state.Accepted[user.UserID] = s.clock()
		appendMatchEventLocked(match, MatchEvent{Type: "rematch_requested", Tick: match.Tick, UserID: user.UserID, Status: "waiting"})
	}
	status := "waiting"
	var newMatch *matchState
	if state.NewMatchID != "" {
		status = "found"
		newMatch = s.matches[state.NewMatchID]
	} else if len(state.Accepted) >= len(match.PlayerIDs) {
		mode, ok := ModeConfigs[match.ModeID]
		if !ok {
			return nil, newError(codeInvalidMode, "unsupported mode %q", match.ModeID)
		}
		newMatch = s.createMatchFromParticipantsLocked(mode, rematchParticipantsFromMatch(match))
		state.NewMatchID = newMatch.MatchID
		status = "found"
		appendMatchEventLocked(match, MatchEvent{Type: "rematch_found", Tick: match.Tick, UserID: user.UserID, NewMatchID: newMatch.MatchID, Status: "found"})
	}
	var start *MatchStartEvent
	if newMatch != nil && newMatch.Status == "running" {
		event := s.matchStartLocked(newMatch)
		start = &event
	}
	return &RematchResponse{
		OK:                  true,
		MatchID:             match.MatchID,
		NewMatchID:          state.NewMatchID,
		RematchStatus:       status,
		AcceptedCount:       len(state.Accepted),
		RequiredPlayers:     len(match.PlayerIDs),
		ModeID:              match.ModeID,
		StageID:             match.StageID,
		Loadout:             player.Loadout,
		MatchStart:          start,
		ServerAuthoritative: true,
		ServerTime:          s.clock(),
	}, nil
}

func (s *Service) Replay(sessionToken string, replayID string) (*ReplayRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	replayID = strings.TrimSpace(replayID)
	if replayID == "" {
		return nil, newError(codeInvalidRequest, "replay_id is required")
	}
	replay, ok := s.replays[replayID]
	if !ok {
		return nil, newError(codeNotFound, "replay not found")
	}
	if replay.UserID != user.UserID {
		return nil, newError(codeUnauthorized, "user cannot read replay")
	}
	copy := copyReplayRecord(replay)
	return &copy, nil
}

func (s *Service) ClaimActivity(sessionToken string, raw map[string]any) (*ActivityClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	if field := firstForbiddenField(raw); field != "" {
		return nil, newError(codeForbiddenField, "client cannot submit %s", field)
	}
	req := ActivityClaimRequest{
		ClaimKind: strings.TrimSpace(asString(raw["claim_kind"])),
		ClaimID:   strings.TrimSpace(asString(raw["claim_id"])),
	}
	if req.ClaimKind == "" || req.ClaimID == "" {
		return nil, newError(codeInvalidRequest, "claim_kind and claim_id are required")
	}
	if !isValidClaimKind(req.ClaimKind) {
		return nil, newError(codeInvalidRequest, "invalid claim_kind %q", req.ClaimKind)
	}
	key := activityClaimKey(req.ClaimKind, req.ClaimID, user.UserID)
	if existing, ok := s.activityClaims[key]; ok {
		copy := *existing
		copy.Duplicate = true
		return &copy, nil
	}
	if err := claimEligibility(user, req); err != nil {
		return nil, err
	}
	result := s.buildActivityClaimResultLocked(user, req)
	s.applyRewardsLocked(user, "activity:"+req.ClaimKind+":"+req.ClaimID, result.RewardJSON)
	s.applyActivityProjectionLocked(user, result)
	s.activityClaims[key] = result
	copy := *result
	return &copy, nil
}

func (s *Service) RegisterBattleServer(req RegisterBattleServerRequest) (*BattleServerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	server, err := s.upsertBattleServerLocked(BattleServerHeartbeatRequest{
		BattleServerID: req.BattleServerID,
		Endpoint:       req.Endpoint,
		Region:         req.Region,
		BuildID:        req.BuildID,
		Capacity:       req.Capacity,
		ActiveMatches:  req.ActiveMatches,
		Load:           req.Load,
		Status:         req.Status,
		SupportedModes: req.SupportedModes,
	})
	if err != nil {
		return nil, err
	}
	s.recordBattleServerLifecycleAuditLocked(server, "server_registered")
	status := battleServerStatusFromState(server)
	return &status, nil
}

func (s *Service) BattleServerHeartbeat(req BattleServerHeartbeatRequest) (*BattleServerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	server, err := s.upsertBattleServerLocked(req)
	if err != nil {
		return nil, err
	}
	s.recordBattleServerLifecycleAuditLocked(server, "server_heartbeat")
	status := battleServerStatusFromState(server)
	return &status, nil
}

func (s *Service) BattleServerOffline(req BattleServerOfflineRequest) (*BattleServerStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	serverID := strings.TrimSpace(req.BattleServerID)
	if serverID == "" {
		return nil, newError(codeInvalidRequest, "battle_server_id is required")
	}
	server, ok := s.battleServers[serverID]
	if !ok || server == nil {
		return nil, newError(codeNotFound, "battle server %q not found", serverID)
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "offline"
	}
	server.Status = status
	server.Load = 0
	server.LastSeenAt = s.clock()
	s.recordBattleServerLifecycleAuditLocked(server, "server_offline")
	out := battleServerStatusFromState(server)
	return &out, nil
}

func (s *Service) BattleServers() *BattleServerListResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := make([]string, 0, len(s.battleServers))
	for id := range s.battleServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	servers := make([]BattleServerStatus, 0, len(ids))
	for _, id := range ids {
		servers = append(servers, battleServerStatusFromState(s.battleServers[id]))
	}
	return &BattleServerListResponse{
		OK:                  true,
		Servers:             servers,
		ServerTime:          s.clock(),
		ServerAuthoritative: true,
	}
}

func (s *Service) BattleAllocation(sessionToken string, matchID string) (*BattleServerAllocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	alloc := s.ensureBattleAllocationLocked(match)
	copy := copyBattleAllocation(alloc)
	return &copy, nil
}

func (s *Service) BattleTicket(sessionToken string, matchID string) (*SignedBattleTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, err
	}
	ticket, err := s.signedBattleTicketLocked(match, user)
	if err != nil {
		return nil, err
	}
	copy := copySignedBattleTicket(ticket)
	return &copy, nil
}

func (s *Service) ConsumeBattleTicket(req BattleTicketConsumeRequest) (*BattleTicketConsumeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	ticketID := strings.TrimSpace(req.TicketID)
	matchID := strings.TrimSpace(req.MatchID)
	battleServerID := strings.TrimSpace(req.BattleServerID)
	nonce := strings.TrimSpace(req.TicketNonceHex)
	if ticketID == "" || matchID == "" || battleServerID == "" || nonce == "" {
		return nil, newError(codeInvalidRequest, "ticket_id, match_id, battle_server_id, and ticket_nonce_hex are required")
	}
	signed := s.signedBattleTicketByIDLocked(ticketID)
	if signed == nil {
		return nil, newError(codeNotFound, "battle ticket not found")
	}
	ticket := signed.Ticket
	if ticket.MatchID != matchID {
		return nil, newError(codeInvalidRequest, "battle ticket match mismatch")
	}
	if ticket.BattleServerID != battleServerID {
		return nil, newError(codeBattleServer, "battle ticket server mismatch")
	}
	if ticket.TicketNonceHex != nonce {
		return nil, newError(codeInvalidRequest, "battle ticket nonce mismatch")
	}
	if req.UserID != "" && strings.TrimSpace(req.UserID) != ticket.UserID {
		return nil, newError(codeUnauthorized, "battle ticket user mismatch")
	}
	if req.PlayerID != "" && strings.TrimSpace(req.PlayerID) != ticket.PlayerID {
		return nil, newError(codeUnauthorized, "battle ticket player mismatch")
	}
	if now.After(ticket.ExpiresAt) {
		s.recordBattleTicketExpiredAuditLocked(signed, now)
		return nil, newError(codeMatchState, "battle ticket expired")
	}
	if consumedAt, ok := s.consumedBattleTickets[ticketID]; ok {
		return &BattleTicketConsumeResponse{
			OK:                  true,
			Version:             currentVersionStamp(),
			TicketID:            ticket.TicketID,
			MatchID:             ticket.MatchID,
			UserID:              ticket.UserID,
			PlayerID:            ticket.PlayerID,
			BattleServerID:      ticket.BattleServerID,
			Consumed:            true,
			Duplicate:           true,
			ServerAuthoritative: true,
			ServerTime:          consumedAt,
		}, nil
	}
	s.consumedBattleTickets[ticketID] = now
	s.recordBattleTicketConsumedAuditLocked(signed, now)
	return &BattleTicketConsumeResponse{
		OK:                  true,
		Version:             currentVersionStamp(),
		TicketID:            ticket.TicketID,
		MatchID:             ticket.MatchID,
		UserID:              ticket.UserID,
		PlayerID:            ticket.PlayerID,
		BattleServerID:      ticket.BattleServerID,
		Consumed:            true,
		Duplicate:           false,
		ServerAuthoritative: true,
		ServerTime:          now,
	}, nil
}

func (s *Service) SubmitBattleResult(req BattleResultSubmitRequest) (*BattleResultSubmitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	signed := req.SignedResult
	if err := validateSignedBattleResultShape(signed, now); err != nil {
		return nil, err
	}
	result := signed.Result
	match, ok := s.matches[result.MatchID]
	if !ok {
		return nil, newError(codeNotFound, "match not found")
	}
	if result.ModeID != match.ModeID {
		return nil, newError(codeInvalidMode, "battle result mode is %q, match mode is %q", result.ModeID, match.ModeID)
	}
	if result.Version.ProtocolVersion != ProtocolVersion || result.Version.RulesetVersion != match.RulesetVersion {
		return nil, newError(codeInvalidRequest, "battle result version mismatch")
	}
	allocation := s.ensureBattleAllocationLocked(match)
	if allocation == nil {
		return nil, newError(codeBattleServer, "battle allocation unavailable")
	}
	if signed.KeyID != allocation.BattleServerID {
		return nil, newError(codeBattleServer, "battle result key id %q does not match battle server %q", signed.KeyID, allocation.BattleServerID)
	}
	if !sameStringSet(result.PlayerIDs, allocationPlayerIDs(allocation)) {
		return nil, newError(codeInvalidRequest, "battle result players do not match allocation")
	}
	if match.Status == "ended" {
		if match.BattleResultHash != "" && match.BattleResultHash == result.ResultHash {
			s.recordBattleResultDuplicateAuditLocked(match, allocation, signed, now)
			return &BattleResultSubmitResponse{
				OK:                  true,
				Version:             currentVersionStamp(),
				MatchID:             match.MatchID,
				SettlementKey:       battleResultSettlementKey(match.MatchID),
				Accepted:            true,
				Duplicate:           true,
				ServerAuthoritative: true,
				ServerTime:          now,
			}, nil
		}
		return nil, newError(codeMatchState, "match is already ended")
	}

	match.BattleResultHash = result.ResultHash
	match.BattleResultReplay = result.ReplayID
	match.BattleResultKeyID = signed.KeyID
	match.BattleResultAt = time.UnixMilli(result.SettledAtMS).UTC()
	if match.BattleResultAt.IsZero() {
		match.BattleResultAt = now
	}
	match.ModeState["battle_result_hash"] = result.ResultHash
	match.ModeState["battle_result_replay_id"] = result.ReplayID
	match.ModeState["battle_result_key_id"] = signed.KeyID
	match.ModeState["battle_result_verified"] = true
	appendMatchEventLocked(match, MatchEvent{Type: "battle_result_verified", Tick: match.Tick, Status: "accepted"})
	s.settleMatchLocked(match)
	s.recordBattleResultAuditLocked(match, allocation, signed, now)
	return &BattleResultSubmitResponse{
		OK:                  true,
		Version:             currentVersionStamp(),
		MatchID:             match.MatchID,
		SettlementKey:       battleResultSettlementKey(match.MatchID),
		Accepted:            true,
		Duplicate:           false,
		ServerAuthoritative: true,
		ServerTime:          now,
	}, nil
}

func (s *Service) createMatchLocked(mode ModeConfig, ticketIDs []string) *matchState {
	participants := make([]matchParticipantSpec, 0, len(ticketIDs))
	for _, ticketID := range ticketIDs {
		ticket := s.tickets[ticketID]
		if ticket == nil {
			continue
		}
		participants = append(participants, matchParticipantSpec{
			UserID:       ticket.UserID,
			DeckSnapshot: copyDeckSnapshot(ticket.DeckSnapshot),
			Loadout:      ticket.Loadout,
		})
	}
	match := s.createMatchFromParticipantsLocked(mode, participants)
	for _, ticketID := range ticketIDs {
		ticket := s.tickets[ticketID]
		if ticket == nil {
			continue
		}
		ticket.MatchID = match.MatchID
		ticket.Status = "found"
		if ticket.RoomCode != "" {
			ticket.RoomStatus = "found"
		}
	}
	return match
}

func rematchParticipantsFromMatch(match *matchState) []matchParticipantSpec {
	participants := make([]matchParticipantSpec, 0, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		if player == nil {
			continue
		}
		participants = append(participants, matchParticipantSpec{
			UserID:       player.UserID,
			DeckSnapshot: copyDeckSnapshot(player.DeckSnapshot),
			Loadout:      player.Loadout,
		})
	}
	return participants
}

func (s *Service) createMatchFromParticipantsLocked(mode ModeConfig, participants []matchParticipantSpec) *matchState {
	matchID := s.nextIDLocked("match")
	now := s.clock()
	match := &matchState{
		MatchID:            matchID,
		ModeID:             mode.ModeID,
		RulesetVersion:     RulesetVersion,
		ModeRulesetVersion: mode.ModeRulesetVersion,
		RewardTableID:      mode.RewardTableID,
		ServerSeed:         seedFrom(matchID),
		Status:             "loading",
		StageID:            "starlit_lanes",
		CreatedAt:          now,
		Players:            map[string]*playerState{},
		BossMaxHP:          bossMaxHPForMode(mode.ModeID),
		Bullets:            map[string]*bulletState{},
		ActiveCards:        map[string]*activeCardState{},
		ModeActions:        map[string]ModeActionResponse{},
		TransferredCards:   map[string]string{},
		ModeState:          defaultModeState(mode.ModeID),
	}
	if mode.ModeID == "world_boss" {
		boss := s.ensureWorldBossLocked()
		match.BossMaxHP = max(0, boss.CurrentHP)
	}
	match.BossHP = match.BossMaxHP
	if len(participants) > 0 && participants[0].Loadout.StageID != "" {
		match.StageID = participants[0].Loadout.StageID
	}
	match.ModeState["stage_id"] = match.StageID
	for _, participant := range participants {
		user := s.users[participant.UserID]
		if user == nil {
			continue
		}
		match.PlayerIDs = append(match.PlayerIDs, user.UserID)
		deck := copyDeckSnapshot(participant.DeckSnapshot)
		hand, drawCursor := initialHand(deck)
		match.Players[user.UserID] = &playerState{
			UserID:        user.UserID,
			DisplayName:   user.DisplayName,
			Connected:     true,
			LastSeenAt:    now,
			X:             startX,
			Y:             startY,
			LastTick:      -1,
			LastSeq:       0,
			Energy:        cardStartingEnergy,
			Hand:          hand,
			DrawCursor:    drawCursor,
			Cooldowns:     map[string]int{},
			DeckSnapshot:  deck,
			Loadout:       participant.Loadout,
			GrazedBullets: map[string]struct{}{},
		}
	}
	s.consumeModeEntryLocked(match)
	updateModeStateLocked(match)
	if match.ModeID == "world_boss" {
		s.applyWorldBossModeStateLocked(match)
	}
	s.matches[matchID] = match
	if match.ModeID == "battle_royale" {
		match.ModeState["candidate_cards"] = battleRoyaleCandidates(match, 0)
		match.ModeState["choice_deadline_tick"] = TickRate * 30
		match.ModeState["public_pool_hash"] = shortHash(fmt.Sprintf("%d:%s:%d", match.ServerSeed, match.ModeID, 0))
	}
	match.BattleAllocation = s.ensureBattleAllocationLocked(match)
	return match
}

func (s *Service) queueResponseLocked(ticket *queueTicket, mode ModeConfig, queueDepth int, match *matchState) *QueueResponse {
	resp := &QueueResponse{
		OK:              true,
		QueueStatus:     ticket.Status,
		TicketID:        ticket.TicketID,
		ModeID:          ticket.ModeID,
		Loadout:         ticket.Loadout,
		RoomCode:        ticket.RoomCode,
		RoomStatus:      ticket.RoomStatus,
		RequiredPlayers: mode.MinPlayers,
		CurrentPlayers:  queueDepth,
	}
	if ticket.MatchID != "" {
		resp.MatchID = ticket.MatchID
		resp.CurrentPlayers = mode.MinPlayers
	}
	if match != nil {
		alloc := s.ensureBattleAllocationLocked(match)
		resp.BattleAllocation = alloc
		if user := s.users[ticket.UserID]; user != nil {
			if signed, err := s.signedBattleTicketLocked(match, user); err == nil {
				resp.BattleTicket = signed
			}
		}
		if match.Status == "running" {
			event := s.matchStartLocked(match)
			resp.MatchStart = &event
		}
	}
	return resp
}

func (s *Service) heartbeatTicketResponseLocked(user *userState, ticket *queueTicket, req PresenceHeartbeatRequest, now time.Time) *PresenceHeartbeatResponse {
	mode := ModeConfigs[ticket.ModeID]
	presenceStatus := "queue_waiting"
	if ticket.RoomCode != "" {
		presenceStatus = "room_waiting"
	}
	if ticket.Status == "cancelled" {
		presenceStatus = "queue_cancelled"
	}
	if ticket.Status == "found" {
		presenceStatus = "match_found"
	}
	return &PresenceHeartbeatResponse{
		OK:                  true,
		UserID:              user.UserID,
		PresenceStatus:      presenceStatus,
		SessionStatus:       "authenticated",
		TicketID:            ticket.TicketID,
		QueueStatus:         ticket.Status,
		RoomCode:            ticket.RoomCode,
		RoomStatus:          ticket.RoomStatus,
		RequiredPlayers:     mode.MinPlayers,
		CurrentPlayers:      s.ticketDepthLocked(ticket),
		ModeID:              ticket.ModeID,
		Loadout:             ticket.Loadout,
		MatchID:             ticket.MatchID,
		LastClientTick:      req.ClientTick,
		Connected:           true,
		LastEventCursor:     normalizedCursor(req.LastEventCursor),
		ServerTime:          now,
		ServerAuthoritative: true,
	}
}

func (s *Service) heartbeatMatchResponseLocked(user *userState, match *matchState, player *playerState, ticketID string, req PresenceHeartbeatRequest, now time.Time) *PresenceHeartbeatResponse {
	if ticketID == "" {
		ticketID = s.ticketIDForMatchUserLocked(match.MatchID, user.UserID)
	}
	queueStatus := "found"
	roomCode := ""
	roomStatus := ""
	requiredPlayers := len(match.PlayerIDs)
	currentPlayers := connectedPlayerCount(match)
	if ticket := s.tickets[ticketID]; ticket != nil {
		queueStatus = ticket.Status
		roomCode = ticket.RoomCode
		roomStatus = ticket.RoomStatus
		if mode, ok := ModeConfigs[ticket.ModeID]; ok {
			requiredPlayers = mode.MinPlayers
		}
	}
	oldestCursor, latestCursor := eventCursorBounds(match)
	presenceStatus := "in_match"
	if match.Status == "ended" {
		presenceStatus = "ended"
	} else if !player.Connected {
		presenceStatus = "disconnected"
	}
	return &PresenceHeartbeatResponse{
		OK:                   true,
		UserID:               user.UserID,
		PresenceStatus:       presenceStatus,
		SessionStatus:        "authenticated",
		TicketID:             ticketID,
		QueueStatus:          queueStatus,
		RoomCode:             roomCode,
		RoomStatus:           roomStatus,
		RequiredPlayers:      requiredPlayers,
		CurrentPlayers:       currentPlayers,
		ModeID:               match.ModeID,
		Loadout:              player.Loadout,
		MatchID:              match.MatchID,
		MatchStatus:          match.Status,
		MatchTick:            match.Tick,
		LastClientTick:       req.ClientTick,
		Connected:            player.Connected,
		Ready:                player.Ready,
		ReconnectSecondsLeft: reconnectSecondsLeftForPlayer(player, now),
		LastEventCursor:      normalizedCursor(req.LastEventCursor),
		LatestEventCursor:    latestCursor,
		OldestEventCursor:    oldestCursor,
		ServerTime:           now,
		ServerAuthoritative:  true,
	}
}

func (s *Service) roomByCodeLocked(roomCode string) (*roomState, error) {
	normalizedRoomCode := normalizeRoomCode(roomCode)
	if normalizedRoomCode == "" {
		return nil, newError(codeInvalidRequest, "room_code is required")
	}
	room, ok := s.rooms[normalizedRoomCode]
	if !ok || room == nil {
		return nil, newError(codeNotFound, "room not found")
	}
	return room, nil
}

func (s *Service) roomSnapshotLocked(room *roomState, now time.Time) RoomSnapshot {
	if room == nil {
		return RoomSnapshot{}
	}
	mode := ModeConfigs[room.ModeID]
	participants := make([]RoomParticipantSnapshot, 0, len(room.TicketIDs))
	for _, ticketID := range room.TicketIDs {
		ticket := s.tickets[ticketID]
		if ticket == nil || ticket.Status != "queued" {
			continue
		}
		user := s.users[ticket.UserID]
		displayName := ""
		if user != nil {
			displayName = user.DisplayName
		}
		participants = append(participants, RoomParticipantSnapshot{
			UserID:           ticket.UserID,
			DisplayName:      displayName,
			TicketID:         ticket.TicketID,
			DeckSnapshotHash: deckSnapshotHash(ticket.DeckSnapshot),
			Loadout:          ticket.Loadout,
			JoinedAt:         ticket.CreatedAt,
			LastSeenAt:       ticket.LastSeenAt,
		})
	}
	return RoomSnapshot{
		OK:                  true,
		RoomCode:            room.RoomCode,
		RoomStatus:          room.Status,
		ModeID:              room.ModeID,
		HostUserID:          room.HostUserID,
		RequiredPlayers:     mode.MinPlayers,
		MaxPlayers:          mode.MaxPlayers,
		CurrentPlayers:      len(participants),
		MatchID:             room.MatchID,
		StageID:             room.StageID,
		ModeRulesetVersion:  mode.ModeRulesetVersion,
		RulesetVersion:      RulesetVersion,
		ModeConfigHash:      modeConfigHash(room.ModeID),
		ModeParams:          copyAnyMap(room.ModeParams),
		Participants:        participants,
		Messages:            copyLobbyMessages(room.Messages),
		CreatedAt:           room.CreatedAt,
		ServerTime:          now,
		ServerAuthoritative: true,
	}
}

func (s *Service) roomHasUserLocked(room *roomState, userID string) bool {
	if room == nil {
		return false
	}
	for _, ticketID := range room.TicketIDs {
		ticket := s.tickets[ticketID]
		if ticket != nil && ticket.UserID == userID && ticket.Status == "queued" {
			return true
		}
	}
	return false
}

func (s *Service) waitingRoomTicketForUserLocked(userID string) (*queueTicket, *roomState) {
	for _, room := range s.rooms {
		if room == nil || room.Status != "waiting" {
			continue
		}
		for _, ticketID := range room.TicketIDs {
			ticket := s.tickets[ticketID]
			if ticket != nil && ticket.UserID == userID && ticket.Status == "queued" && ticket.MatchID == "" {
				return ticket, room
			}
		}
	}
	return nil, nil
}

func (s *Service) roomTicketForUserLocked(roomCode string, userID string) *queueTicket {
	roomCode = normalizeRoomCode(roomCode)
	if roomCode == "" {
		return nil
	}
	for _, ticket := range s.tickets {
		if ticket != nil && ticket.RoomCode == roomCode && ticket.UserID == userID {
			return ticket
		}
	}
	return nil
}

func roomMessageByID(room *roomState, messageID string, userID string) *LobbyMessage {
	if room == nil || messageID == "" || userID == "" {
		return nil
	}
	for index := range room.Messages {
		if room.Messages[index].MessageID == messageID && room.Messages[index].UserID == userID {
			return &room.Messages[index]
		}
	}
	return nil
}

func (s *Service) ticketDepthLocked(ticket *queueTicket) int {
	if ticket == nil {
		return 0
	}
	if ticket.RoomCode != "" {
		room := s.rooms[ticket.RoomCode]
		if room == nil || room.Status == "cancelled" || room.Status == "closed" {
			return 0
		}
		count := 0
		for _, ticketID := range room.TicketIDs {
			if candidate := s.tickets[ticketID]; candidate != nil && candidate.Status == "queued" {
				count++
			}
		}
		return count
	}
	return len(s.queues[ticket.QueueKey])
}

func (s *Service) ticketIDForMatchUserLocked(matchID string, userID string) string {
	for _, ticket := range s.tickets {
		if ticket != nil && ticket.MatchID == matchID && ticket.UserID == userID {
			return ticket.TicketID
		}
	}
	return ""
}

func (s *Service) cancelRoomTicketLocked(ticket *queueTicket, userID string) {
	room := s.rooms[ticket.RoomCode]
	ticket.Status = "cancelled"
	ticket.RoomStatus = "cancelled"
	if room == nil || room.Status != "waiting" {
		return
	}
	room.TicketIDs = removeString(room.TicketIDs, ticket.TicketID)
	if len(room.TicketIDs) == 0 {
		delete(s.rooms, room.RoomCode)
		room.TicketIDs = []string{}
		return
	}
	for _, ticketID := range room.TicketIDs {
		if remaining := s.tickets[ticketID]; remaining != nil && remaining.Status == "queued" {
			if room.HostUserID == userID {
				room.HostUserID = remaining.UserID
			}
			remaining.RoomStatus = room.Status
		}
	}
}

func (s *Service) matchStartLocked(match *matchState) MatchStartEvent {
	players := make([]PlayerIdentity, 0, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		players = append(players, PlayerIdentity{PlayerID: playerIDForUser(match.MatchID, userID), UserID: userID, DisplayName: player.DisplayName, Loadout: player.Loadout})
	}
	allocation := s.ensureBattleAllocationLocked(match)
	return MatchStartEvent{
		Type:               "match_start",
		MatchID:            match.MatchID,
		ModeID:             match.ModeID,
		StageID:            match.StageID,
		RulesetVersion:     match.RulesetVersion,
		ModeRulesetVersion: match.ModeRulesetVersion,
		ServerSeed:         match.ServerSeed,
		InputDelayTicks:    DefaultInputDelayTick,
		TickRate:           TickRate,
		Players:            players,
		ModeState:          copyAnyMap(match.ModeState),
		BattleAllocation:   allocation,
	}
}

func (s *Service) applyInputLocked(match *matchState, player *playerState, packet InputPacket) {
	accruePlayerResources(player, max(1, packet.Tick-player.LastTick))
	player.LastTick = packet.Tick
	player.LastSeq = packet.Seq
	player.Inputs = append(player.Inputs, packet)
	if packet.Tick > match.Tick {
		match.Tick = packet.Tick
	}
	speed := 330.0
	if packet.Slow {
		speed = 145.0
		player.GrazeCount++
		player.Score += 3
	}
	dx, dy := directionFromBits(packet.Dir)
	player.X = clamp(player.X+dx*speed/float64(TickRate), playfieldMinX, playfieldMaxX)
	player.Y = clamp(player.Y+dy*speed/float64(TickRate), playfieldMinY, playfieldMaxY)
	if packet.Shoot {
		player.Score += 5
	}
	if packet.Bomb {
		player.BombUses++
		player.Score += 2
		player.InvulnerableUntil = max(player.InvulnerableUntil, packet.Tick+90)
		if match.BossHP > 0 {
			damage := 35
			player.DamageDealt += damage
			match.BossHP = max(0, match.BossHP-damage)
			appendMatchEventLocked(match, MatchEvent{Type: "bomb_damage", Tick: packet.Tick, UserID: player.UserID, Value: damage, X: round(player.X, 3), Y: round(player.Y, 3)})
		}
	}
	if packet.CardSlot >= 0 {
		s.applyCardRequestLocked(match, player, packet)
	}
	player.Score += 1
	if packet.Shoot && match.BossHP > 0 {
		damage := 4
		player.DamageDealt += damage
		match.BossHP = max(0, match.BossHP-damage)
	}
	updateModeStateLocked(match)
}

func (s *Service) applyCardRequestLocked(match *matchState, player *playerState, packet InputPacket) {
	activation, err := s.validateAndActivateCardLocked(match, player, packet.CardSlot, packet.Tick)
	if err != nil {
		appendMatchEventLocked(match, MatchEvent{Type: "card_rejected", Tick: packet.Tick, UserID: player.UserID, Slot: packet.CardSlot, Reason: ErrorCode(err)})
		return
	}
	player.CardPlays++
	player.Score += activation.ScoreBonus
	if activation.EnergyGain > 0 {
		player.Energy = math.Min(cardMaxEnergy, player.Energy+activation.EnergyGain)
	}
	match.ActiveCards[activation.ActivationID] = &activeCardState{
		ActivationID:  activation.ActivationID,
		UserID:        player.UserID,
		CardID:        activation.CardID,
		Slot:          packet.CardSlot,
		StartedTick:   packet.Tick,
		ExpiresTick:   activation.ExpiresTick,
		EffectKind:    activation.EffectKind,
		Cost:          activation.Cost,
		Damage:        activation.Damage,
		CooldownUntil: activation.CooldownUntil,
	}
	if activation.Damage > 0 && match.BossHP > 0 {
		player.DamageDealt += activation.Damage
		match.BossHP = max(0, match.BossHP-activation.Damage)
	}
	appendMatchEventLocked(match, MatchEvent{
		Type:        "card_accepted",
		Tick:        packet.Tick,
		UserID:      player.UserID,
		CardID:      activation.CardID,
		Slot:        packet.CardSlot,
		Value:       activation.Damage,
		ExpiresTick: activation.ExpiresTick,
	})
	for i := 0; i < activation.DrawCards; i++ {
		drawServerCard(player)
	}
}

func (s *Service) applyModeActionLocked(match *matchState, player *playerState, req ModeActionRequest) ModeActionResponse {
	match.NextModeActionSeq++
	actionID := fmt.Sprintf("%s_a%06d", match.MatchID, match.NextModeActionSeq)
	response := ModeActionResponse{
		OK:                        true,
		Accepted:                  false,
		Reason:                    "none",
		MatchID:                   match.MatchID,
		ModeID:                    match.ModeID,
		UserID:                    player.UserID,
		ActionID:                  actionID,
		ActionType:                req.ActionType,
		Status:                    "rejected",
		Payload:                   copyAnyMap(req.Payload),
		ModeState:                 copyAnyMap(match.ModeState),
		ServerAuthoritative:       true,
		ClientResultAuthoritative: false,
		ServerTime:                s.clock(),
	}
	var event MatchEvent
	var err error
	switch req.ActionType {
	case "select_round_card":
		event, err = applyBattleRoyaleSelectionLocked(match, player, actionID, req.Payload)
	case "transfer_card":
		event, err = applyBossTransferLocked(match, player, actionID, req.Payload)
	default:
		err = newError(codeModeAction, "unsupported mode action %q", req.ActionType)
	}
	if err != nil {
		response.Reason = ErrorCode(err)
		response.Status = "rejected"
		event = MatchEvent{Type: "mode_action_rejected", Tick: match.Tick, UserID: player.UserID, ActionID: actionID, ActionType: req.ActionType, Reason: response.Reason, Status: response.Status}
		appendMatchEventLocked(match, event)
		response.Event = lastMatchEvent(match)
		response.ModeState = copyAnyMap(match.ModeState)
		match.ModeActions[actionID] = response
		return response
	}
	response.Accepted = true
	response.Status = "accepted"
	response.Reason = "none"
	appendMatchEventLocked(match, event)
	response.Event = lastMatchEvent(match)
	response.ModeState = copyAnyMap(match.ModeState)
	match.ModeActions[actionID] = response
	return response
}

func (s *Service) validateAndActivateCardLocked(match *matchState, player *playerState, slot int, tick int) (serverCardActivation, error) {
	if slot < 0 || slot >= cardHandLimit {
		return serverCardActivation{}, newError(codeInvalidInput, "card slot is outside hand")
	}
	if slot >= len(player.Hand) {
		return serverCardActivation{}, newError(codeInvalidInput, "card slot is empty")
	}
	cardID := strings.TrimSpace(player.Hand[slot])
	definition, ok := serverCardCatalog[cardID]
	if !ok {
		return serverCardActivation{}, newError(codeInvalidDeck, "unknown card %q", cardID)
	}
	if tick < player.Cooldowns[cardID] {
		return serverCardActivation{}, newError(codeInvalidInput, "card is on cooldown")
	}
	if player.Energy+0.0001 < definition.Cost {
		return serverCardActivation{}, newError(codeInvalidInput, "not enough energy")
	}
	player.Energy = math.Max(0, player.Energy-definition.Cost)
	player.Cooldowns[cardID] = tick + definition.CooldownTicks
	player.Hand = append(player.Hand[:slot], player.Hand[slot+1:]...)
	drawServerCard(player)
	match.NextCardSeq++
	return serverCardActivation{
		ActivationID:  fmt.Sprintf("%s_c%06d", match.MatchID, match.NextCardSeq),
		CardID:        cardID,
		EffectKind:    definition.EffectKind,
		ExpiresTick:   tick + definition.DurationTicks,
		CooldownUntil: tick + definition.CooldownTicks,
		Damage:        definition.Damage,
		ScoreBonus:    definition.ScoreBonus,
		Cost:          definition.Cost,
		EnergyGain:    definition.EnergyGain,
		DrawCards:     definition.DrawCards,
	}, nil
}

func (s *Service) snapshotLocked(match *matchState, full bool) Snapshot {
	players := make([]PlayerSnapshot, 0, len(match.PlayerIDs))
	score := make([]ScoreSnapshot, 0, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		reconnectSecondsLeft := reconnectSecondsLeftForPlayer(player, s.clock())
		players = append(players, PlayerSnapshot{
			UserID:               userID,
			X:                    round(player.X, 3),
			Y:                    round(player.Y, 3),
			Loadout:              player.Loadout,
			Ready:                player.Ready,
			Connected:            player.Connected,
			LastTick:             player.LastTick,
			LastSeq:              player.LastSeq,
			CardPlays:            player.CardPlays,
			BombUses:             player.BombUses,
			GrazeCount:           player.GrazeCount,
			HitCount:             player.HitCount,
			DamageDealt:          player.DamageDealt,
			Energy:               round(player.Energy, 3),
			HandSize:             len(player.Hand),
			ReconnectSecondsLeft: reconnectSecondsLeft,
		})
		score = append(score, ScoreSnapshot{UserID: userID, Score: player.Score})
	}
	snapshot := Snapshot{
		MatchID:      match.MatchID,
		Tick:         match.Tick,
		Full:         full,
		StageID:      match.StageID,
		Players:      players,
		BulletsDelta: snapshotBulletDeltas(match, full),
		Score:        score,
		ActiveCards:  activeCardSnapshots(match),
		ModeState:    copyAnyMap(match.ModeState),
		Events:       copyMatchEvents(match.LastEvents),
	}
	snapshot.StateHash = stateHash(snapshot)
	return snapshot
}

func (s *Service) settleMatchLocked(match *matchState) {
	match.Status = "ended"
	match.EndedAt = s.clock()
	s.finalizeModeResultLocked(match)
	appendMatchEventLocked(match, MatchEvent{Type: "match_ended", Tick: match.Tick})
	winnerID := ""
	winnerScore := math.MinInt
	draw := false
	for _, userID := range match.PlayerIDs {
		score := match.Players[userID].Score
		if score > winnerScore {
			winnerScore = score
			winnerID = userID
			draw = false
		} else if score == winnerScore {
			draw = true
		}
	}
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		user := s.users[userID]
		result := "loss"
		if match.ModeID == "instance_boss" {
			if match.BossHP == 0 && connectedPlayerCount(match) > 0 {
				result = "win"
			}
		} else if match.ModeID == "world_boss" {
			if player.DamageDealt > 0 {
				result = "win"
			}
		} else {
			if draw {
				result = "draw"
			} else if userID == winnerID {
				result = "win"
			}
		}
		settlement := s.buildSettlementLocked(match, player, user, result)
		key := settlementKey(match.MatchID, userID)
		s.settlements[key] = settlement
		replay := s.buildReplayRecordLocked(match, player, settlement)
		s.replays[settlement.ReplayID] = replay
		s.recordReplayAuditLocked(replay)
		s.applyRewardsLocked(user, match.MatchID, settlement.RewardJSON)
		s.applyProgressLocked(user, settlement)
	}
}

func (s *Service) buildReplayRecordLocked(match *matchState, player *playerState, settlement *MatchEndEvent) *ReplayRecord {
	finalSnapshot := s.snapshotLocked(match, true)
	inputCount := 0
	for _, userID := range match.PlayerIDs {
		inputCount += len(match.Players[userID].Inputs)
	}
	return &ReplayRecord{
		OK:                  true,
		ReplayID:            settlement.ReplayID,
		MatchID:             match.MatchID,
		UserID:              player.UserID,
		ModeID:              match.ModeID,
		StageID:             match.StageID,
		Loadout:             player.Loadout,
		RulesetVersion:      match.RulesetVersion,
		ModeRulesetVersion:  match.ModeRulesetVersion,
		ServerSeed:          match.ServerSeed,
		TickRate:            TickRate,
		StartedAt:           match.StartedAt,
		EndedAt:             match.EndedAt,
		SettledAt:           settlement.SettledAt,
		ServerAuthoritative: true,
		StateHash:           finalSnapshot.StateHash,
		FinalResult:         copyAnyMap(settlement.FinalResult),
		ModeResult:          copyAnyMap(settlement.ModeResult),
		InputCount:          inputCount,
		EventCount:          len(match.EventLog),
		Events:              copyMatchEvents(match.EventLog),
		Settlement:          *settlement,
	}
}

func (s *Service) ensureWorldBossLocked() *worldBossState {
	if s.worldBoss != nil {
		return s.worldBoss
	}
	now := s.clock()
	s.worldBoss = &worldBossState{
		BossInstanceID: worldBossInstanceID,
		SeasonID:       defaultSeasonID,
		MaxHP:          worldBossMaxHP,
		CurrentHP:      worldBossMaxHP,
		StartsAt:       time.Date(now.UTC().Year(), 1, 1, 0, 0, 0, 0, time.UTC),
		EndsAt:         time.Date(now.UTC().Year(), 12, 31, 23, 59, 59, 0, time.UTC),
	}
	return s.worldBoss
}

func (s *Service) worldBossSnapshotLocked(user *userState) WorldBossSnapshot {
	boss := s.ensureWorldBossLocked()
	now := s.clock()
	attemptsUsed := 0
	attemptsLeft := worldBossDailyLimit
	if user != nil {
		attemptsUsed = s.worldBossAttemptsUsedLocked(user.UserID)
		attemptsLeft = s.worldBossAttemptsLeftLocked(user.UserID)
	}
	var defeatedAt *time.Time
	if !boss.DefeatedAt.IsZero() {
		value := boss.DefeatedAt
		defeatedAt = &value
	}
	return WorldBossSnapshot{
		OK:                  true,
		BossInstanceID:      boss.BossInstanceID,
		SeasonID:            boss.SeasonID,
		MaxHP:               boss.MaxHP,
		CurrentHP:           boss.CurrentHP,
		DailyAttemptLimit:   worldBossDailyLimit,
		DailyAttemptsUsed:   attemptsUsed,
		DailyAttemptsLeft:   attemptsLeft,
		StartsAt:            boss.StartsAt,
		EndsAt:              boss.EndsAt,
		DefeatedAt:          defeatedAt,
		DefeatedByMatchID:   boss.DefeatedByMatchID,
		DefeatedByUserID:    boss.DefeatedByUserID,
		AnnouncementEmitted: boss.AnnouncementEmitted,
		ServerAuthoritative: true,
		ServerTime:          now,
	}
}

func (s *Service) validateModeEntryLocked(user *userState, modeID string) error {
	if modeID != "world_boss" {
		return nil
	}
	boss := s.ensureWorldBossLocked()
	if boss.CurrentHP <= 0 {
		return newError(codeMatchState, "world boss is already defeated")
	}
	if s.worldBossAttemptsLeftLocked(user.UserID) <= 0 {
		return newError(codeMatchState, "world boss daily attempts exhausted")
	}
	return nil
}

func (s *Service) consumeModeEntryLocked(match *matchState) {
	if match.ModeID != "world_boss" {
		return
	}
	for _, userID := range match.PlayerIDs {
		s.consumeWorldBossAttemptLocked(userID)
	}
}

func (s *Service) consumeWorldBossAttemptLocked(userID string) {
	day := s.worldBossAttemptDayLocked()
	if s.worldBossAttempts[day] == nil {
		s.worldBossAttempts[day] = map[string]int{}
	}
	s.worldBossAttempts[day][userID]++
}

func (s *Service) worldBossAttemptsUsedLocked(userID string) int {
	day := s.worldBossAttemptDayLocked()
	return s.worldBossAttempts[day][userID]
}

func (s *Service) worldBossAttemptsLeftLocked(userID string) int {
	return max(0, worldBossDailyLimit-s.worldBossAttemptsUsedLocked(userID))
}

func (s *Service) worldBossAttemptDayLocked() string {
	return s.clock().UTC().Format("2006-01-02")
}

func (s *Service) applyWorldBossModeStateLocked(match *matchState) {
	if match.ModeID != "world_boss" {
		return
	}
	boss := s.ensureWorldBossLocked()
	leftByUser := map[string]any{}
	minLeft := worldBossDailyLimit
	for _, userID := range match.PlayerIDs {
		left := s.worldBossAttemptsLeftLocked(userID)
		leftByUser[userID] = left
		minLeft = min(minLeft, left)
	}
	if len(match.PlayerIDs) == 0 {
		minLeft = worldBossDailyLimit
	}
	match.ModeState["boss_instance_id"] = boss.BossInstanceID
	match.ModeState["season_id"] = boss.SeasonID
	match.ModeState["boss_hp_preview"] = match.BossHP
	match.ModeState["boss_max_hp"] = match.BossMaxHP
	match.ModeState["boss_hp_global"] = boss.CurrentHP
	match.ModeState["boss_hp_global_max"] = boss.MaxHP
	match.ModeState["daily_attempt_limit"] = worldBossDailyLimit
	match.ModeState["daily_attempts_left"] = minLeft
	match.ModeState["daily_attempts_left_by_user"] = leftByUser
	match.ModeState["active_bullets"] = len(match.Bullets)
	match.ModeState["world_announcement_emitted"] = boss.AnnouncementEmitted
	match.ModeState["defeated_by_match_id"] = boss.DefeatedByMatchID
	match.ModeState["defeated_by_user_id"] = boss.DefeatedByUserID
}

func (s *Service) finalizeModeResultLocked(match *matchState) {
	switch match.ModeID {
	case "world_boss":
		if match.WorldBossApplied {
			return
		}
		damage := min(totalDamageDealt(match), match.BossMaxHP)
		match.WorldBossDamage = damage
		boss := s.ensureWorldBossLocked()
		before := boss.CurrentHP
		applied := min(max(0, before), damage)
		boss.CurrentHP = max(0, boss.CurrentHP-applied)
		if before > 0 && boss.CurrentHP == 0 && boss.DefeatedAt.IsZero() {
			now := s.clock()
			boss.DefeatedAt = now
			boss.DefeatedByMatchID = match.MatchID
			boss.DefeatedByUserID = topDamagePlayerID(match)
			boss.AnnouncementEmitted = true
			match.WorldBossDefeated = true
			appendMatchEventLocked(match, MatchEvent{Type: "world_boss_defeated", Tick: match.Tick, UserID: boss.DefeatedByUserID, Value: applied, Status: "announced"})
		}
		match.WorldBossApplied = true
		match.ModeState["boss_hp_before_global"] = before
		match.ModeState["boss_hp_after_global"] = boss.CurrentHP
		match.ModeState["global_damage_applied"] = applied
		s.applyWorldBossModeStateLocked(match)
	case "instance_boss":
		if match.BossHP == 0 {
			match.ModeState["party_status"] = "cleared"
		} else {
			match.ModeState["party_status"] = "failed"
		}
		updateModeStateLocked(match)
	}
}

func (s *Service) buildSettlementLocked(match *matchState, player *playerState, user *userState, result string) *MatchEndEvent {
	now := s.clock()
	replayID := "replay_" + match.MatchID + "_" + shortHash(fmt.Sprintf("%s:%d:%d", player.UserID, player.LastSeq, player.Score))
	certification := normalizedCertificationProfile(user.Certification)
	rankDelta := 0
	rankScoreBefore := certification.RankScore
	rankScoreAfter := rankScoreBefore
	percentileAfter := certification.Percentile
	qualifiedTop30 := certification.Top30Qualified
	nextCertificationUnlocked := certification.NextCertificationUnlocked
	if match.ModeID == "certification" {
		rankDelta = certificationRankDelta(result, player)
		rankScoreAfter = max(0, rankScoreBefore+rankDelta)
		percentileAfter = certificationPercentile(rankScoreAfter, result)
		qualifiedTop30 = percentileAfter <= top30Threshold
		nextCertificationUnlocked = qualifiedTop30
	}
	rewards := []RewardItem{
		{Type: "points", Amount: 100 + player.Score/10, Source: "match"},
	}
	if result == "win" {
		rewards = append(rewards, RewardItem{Type: "chest_keys", Amount: 1, Source: "win"})
	}
	if player.GrazeCount >= 10 {
		rewards = append(rewards, RewardItem{Type: "card_dust", Amount: player.GrazeCount / 10, Source: "graze"})
	}
	taskProgress := []TaskProgress{
		{TaskID: "daily_complete_match", LabelKey: "task.daily.complete_match", Progress: 1, Target: 1, Claimed: false},
		{TaskID: "daily_graze", LabelKey: "task.daily.graze", Progress: player.GrazeCount, Target: 500, Claimed: false},
	}
	eventPoints := map[string]int{"local_s0": max(1, player.Score/50)}
	leaderboards := []LeaderboardRow{
		{LeaderboardID: "single_score", LabelKey: "leaderboard.single_score", Score: player.Score, Rank: rankFromPercentile(percentileFor(result)), Percentile: percentileFor(result), SeasonID: defaultSeasonID},
		{LeaderboardID: "rank_score", LabelKey: "leaderboard.rank_score", Score: rankScoreAfter, Rank: rankFromPercentile(percentileAfter), Percentile: percentileAfter, SeasonID: certification.SeasonID},
	}
	modeResult := map[string]any{
		"rating_code":                 certification.RatingCode,
		"rank_score_before":           rankScoreBefore,
		"rank_score_delta":            rankDelta,
		"rank_score_after":            rankScoreAfter,
		"rank_score_floor":            certification.RankScoreFloor,
		"percentile_after":            percentileAfter,
		"qualified_top_30":            qualifiedTop30,
		"top_30_threshold":            top30Threshold,
		"next_certification_unlocked": nextCertificationUnlocked,
		"challenge_stage_id":          certification.ChallengeStageID,
		"damage_dealt":                player.DamageDealt,
		"card_plays":                  player.CardPlays,
		"stage_id":                    match.StageID,
		"character_id":                player.Loadout.CharacterID,
		"boss_hp_after":               match.BossHP,
		"boss_max_hp":                 match.BossMaxHP,
		"server_authority":            true,
		"mode_result_owner":           "server",
	}
	if match.BattleResultHash != "" {
		modeResult["battle_result_hash"] = match.BattleResultHash
		modeResult["battle_result_replay_id"] = match.BattleResultReplay
		modeResult["battle_result_key_id"] = match.BattleResultKeyID
		modeResult["battle_result_verified"] = true
	}
	if match.ModeID == "world_boss" {
		boss := s.ensureWorldBossLocked()
		before := intFromAnyValue(match.ModeState["boss_hp_before_global"])
		if before <= 0 {
			before = boss.CurrentHP
		}
		after, hasAfter := asInt(match.ModeState["boss_hp_after_global"])
		if !hasAfter {
			after = boss.CurrentHP
		}
		applied := intFromAnyValue(match.ModeState["global_damage_applied"])
		modeResult["boss_instance_id"] = boss.BossInstanceID
		modeResult["season_id"] = boss.SeasonID
		modeResult["team_damage"] = match.WorldBossDamage
		modeResult["global_damage_applied"] = applied
		modeResult["boss_hp_before_global"] = before
		modeResult["boss_hp_after_global"] = after
		modeResult["boss_defeated"] = !boss.DefeatedAt.IsZero()
		modeResult["defeated_by_match_id"] = boss.DefeatedByMatchID
		modeResult["defeated_by_user_id"] = boss.DefeatedByUserID
		modeResult["world_announcement_emitted"] = boss.AnnouncementEmitted
		modeResult["daily_attempts_left"] = s.worldBossAttemptsLeftLocked(player.UserID)
	}
	if match.ModeID == "instance_boss" {
		cleared := match.BossHP == 0 && connectedPlayerCount(match) > 0
		modeResult["instance_cleared"] = cleared
		modeResult["clear_condition"] = "defeat_boss"
		modeResult["team_damage"] = totalDamageDealt(match)
		modeResult["party_status"] = asString(match.ModeState["party_status"])
	}
	return &MatchEndEvent{
		Type:                 "match_end",
		OK:                   true,
		Duplicate:            false,
		MatchID:              match.MatchID,
		UserID:               player.UserID,
		Mode:                 match.ModeID,
		StageID:              match.StageID,
		Loadout:              player.Loadout,
		RulesetVersion:       match.RulesetVersion,
		ModeRulesetVersion:   match.ModeRulesetVersion,
		ServerSeed:           match.ServerSeed,
		Status:               "completed",
		Result:               result,
		Score:                player.Score,
		GrazeCount:           player.GrazeCount,
		HitCount:             player.HitCount,
		ReplayID:             replayID,
		FinalResult:          map[string]any{"result": result, "winner": result, "score": player.Score, "damage_dealt": player.DamageDealt, "boss_hp_after": match.BossHP, "stage_id": match.StageID, "character_id": player.Loadout.CharacterID, "battle_result_hash": match.BattleResultHash, "battle_result_replay_id": match.BattleResultReplay},
		RewardJSON:           rewards,
		TaskProgress:         taskProgress,
		EventPoints:          eventPoints,
		LeaderboardUpdates:   leaderboards,
		ModeResult:           modeResult,
		ServerAuthoritative:  true,
		ClientAuthoredReward: false,
		SettlementKey:        settlementKey(match.MatchID, player.UserID),
		SettledAt:            now,
	}
}

func (s *Service) applyRewardsLocked(user *userState, sourceID string, rewards []RewardItem) {
	_ = sourceID
	for _, reward := range rewards {
		if reward.Amount <= 0 {
			continue
		}
		switch reward.Type {
		case "points", "card_dust", "chest_keys":
			user.Wallet[reward.Type] += reward.Amount
		case "chest":
			chestID := strings.TrimSpace(reward.ItemID)
			if chestID == "" {
				chestID = defaultChestPoolID
			}
			if user.Chests == nil {
				user.Chests = defaultChests()
			}
			user.Chests[chestID] += reward.Amount
		}
	}
}

func (s *Service) applyProgressLocked(user *userState, settlement *MatchEndEvent) {
	for _, update := range settlement.TaskProgress {
		task := user.Tasks[update.TaskID]
		if task.LabelKey == "" {
			task.LabelKey = update.LabelKey
		}
		task.Progress = max(task.Progress, update.Progress)
		task.Target = max(1, update.Target)
		task.Claimed = task.Claimed || update.Claimed
		user.Tasks[update.TaskID] = task
	}
	for eventID, points := range settlement.EventPoints {
		event := user.Events[eventID]
		if event.LabelKey == "" {
			event = defaultEvents()[eventID]
		}
		event.Points += points
		user.Events[eventID] = event
	}
	for _, update := range settlement.LeaderboardUpdates {
		current := user.Leaderboards[update.LeaderboardID]
		if current.LabelKey == "" || update.Score >= current.Score {
			user.Leaderboards[update.LeaderboardID] = update
		}
	}
	if settlement.Mode == "certification" {
		user.Certification = certificationProfileFromSettlement(user, settlement)
	}
}

func (s *Service) buildActivityClaimResultLocked(user *userState, req ActivityClaimRequest) *ActivityClaimResult {
	rewards := []RewardItem{}
	rewardStatus := "claimed"
	switch req.ClaimKind {
	case "task":
		rewards = append(rewards, RewardItem{Type: "points", Amount: 25, Source: "task"})
	case "event":
		event := user.Events[req.ClaimID]
		rewards = append(rewards, RewardItem{Type: "points", Amount: clampInt(event.Points*5, 10, 150), Source: "event"})
	case "leaderboard":
		rewards = append(rewards, RewardItem{Type: "points", Amount: 100, Source: "leaderboard"})
		rewards = append(rewards, RewardItem{Type: "chest_keys", Amount: 1, Source: "leaderboard"})
	}
	now := s.clock()
	return &ActivityClaimResult{
		OK:                  true,
		Duplicate:           false,
		Reason:              "none",
		ClaimKind:           req.ClaimKind,
		ClaimID:             req.ClaimID,
		UserID:              user.UserID,
		RewardJSON:          rewards,
		ServerAuthoritative: true,
		Claimed:             true,
		RewardStatus:        rewardStatus,
		SettlementKey:       activityClaimKey(req.ClaimKind, req.ClaimID, user.UserID),
		SettledAt:           now,
	}
}

func (s *Service) applyActivityProjectionLocked(user *userState, result *ActivityClaimResult) {
	switch result.ClaimKind {
	case "task":
		task := user.Tasks[result.ClaimID]
		task.Claimed = true
		user.Tasks[result.ClaimID] = task
	case "event":
		event := user.Events[result.ClaimID]
		event.RewardStatus = result.RewardStatus
		user.Events[result.ClaimID] = event
	case "leaderboard":
		board := user.Leaderboards[result.ClaimID]
		board.RewardStatus = result.RewardStatus
		user.Leaderboards[result.ClaimID] = board
	}
}

func (s *Service) matchPlayerLocked(userID string, matchID string) (*matchState, *playerState, error) {
	match, ok := s.matches[matchID]
	if !ok {
		return nil, nil, newError(codeNotFound, "match not found")
	}
	player, ok := match.Players[userID]
	if !ok {
		return nil, nil, newError(codeUnauthorized, "user is not in match")
	}
	return match, player, nil
}

func (s *Service) readyCountLocked(match *matchState) int {
	count := 0
	for _, player := range match.Players {
		if player.Ready {
			count++
		}
	}
	return count
}

func (s *Service) recordLobbyReadyAuditLocked(match *matchState, userID string) {
	if s.lobbyAuditRepo == nil || match == nil || userID == "" {
		return
	}
	ticketID := s.ticketIDForMatchUserLocked(match.MatchID, userID)
	if ticketID == "" {
		return
	}
	ticket := s.tickets[ticketID]
	if ticket == nil || ticket.RoomCode == "" {
		return
	}
	room := s.rooms[normalizeRoomCode(ticket.RoomCode)]
	if room == nil {
		return
	}
	record := s.lobbyRoomAuditRecordLocked(room, ticket, userID, "ready", s.clock())
	record.MatchID = match.MatchID
	record.CurrentPlayers = s.readyCountLocked(match)
	record.RequiredPlayers = len(match.PlayerIDs)
	s.recordLobbyRoomAuditRecordLocked(record)
}

func (s *Service) recordLobbyConnectionAuditLocked(match *matchState, userID string, action string) {
	if s.lobbyAuditRepo == nil || match == nil || userID == "" {
		return
	}
	ticketID := s.ticketIDForMatchUserLocked(match.MatchID, userID)
	if ticketID == "" {
		return
	}
	ticket := s.tickets[ticketID]
	if ticket == nil || ticket.RoomCode == "" {
		return
	}
	room := s.rooms[normalizeRoomCode(ticket.RoomCode)]
	if room == nil {
		return
	}
	record := s.lobbyRoomAuditRecordLocked(room, ticket, userID, action, s.clock())
	record.MatchID = match.MatchID
	record.CurrentPlayers = connectedPlayerCountLocked(match)
	record.RequiredPlayers = len(match.PlayerIDs)
	s.recordLobbyRoomAuditRecordLocked(record)
}

func connectedPlayerCountLocked(match *matchState) int {
	if match == nil {
		return 0
	}
	count := 0
	for _, player := range match.Players {
		if player.Connected {
			count++
		}
	}
	return count
}

func (s *Service) userBySessionLocked(sessionToken string) (*userState, error) {
	token := strings.TrimSpace(strings.TrimPrefix(sessionToken, "Bearer "))
	if token == "" {
		return nil, newError(codeUnauthorized, "missing session token")
	}
	userID, ok := s.sessionToUser[token]
	if !ok {
		return nil, newError(codeUnauthorized, "invalid session token")
	}
	return s.users[userID], nil
}

func (s *Service) nextIDLocked(prefix string) string {
	s.nextSeq++
	return fmt.Sprintf("%s_%06d", prefix, s.nextSeq)
}

func (s *Service) nextRoomCodeLocked() string {
	for {
		raw := s.nextIDLocked("room")
		code := "R" + strings.ToUpper(shortHash(raw))
		if _, exists := s.rooms[code]; !exists {
			return code
		}
	}
}

func normalizeRoomCode(roomCode string) string {
	return strings.ToUpper(strings.TrimSpace(roomCode))
}

func queueKeyFor(modeID string, loadout PlayerLoadout) string {
	modeID = strings.TrimSpace(modeID)
	stageID := strings.TrimSpace(loadout.StageID)
	if modeID == "certification" {
		ratingCode := strings.TrimSpace(loadout.RatingCode)
		if ratingCode == "" {
			ratingCode = defaultRatingCode
		}
		return modeID + ":" + ratingCode + ":" + stageID
	}
	return modeID + ":" + stageID
}

func validateLoadout(modeID string, modeParams map[string]any, profile CertificationProfile) (PlayerLoadout, error) {
	if modeParams == nil {
		modeParams = map[string]any{}
	}
	if field := firstForbiddenFieldDeep(modeParams); field != "" {
		return PlayerLoadout{}, newError(codeForbiddenField, "client cannot submit %s", field)
	}
	stageID := strings.TrimSpace(asString(modeParams["stage_id"]))
	if stageID == "" || stageID == "<nil>" {
		stageID = "starlit_lanes"
	}
	if _, ok := allowedStageIDs[stageID]; !ok {
		return PlayerLoadout{}, newError(codeInvalidMode, "unsupported stage %q", stageID)
	}
	characterID := strings.TrimSpace(asString(modeParams["character_id"]))
	if characterID == "" || characterID == "<nil>" {
		characterID = "balanced"
	}
	if _, ok := allowedCharacterIDs[characterID]; !ok {
		return PlayerLoadout{}, newError(codeInvalidMode, "unsupported character %q", characterID)
	}
	ratingCode := ""
	if modeID == "certification" {
		profile = normalizedCertificationProfile(profile)
		ratingCode = strings.TrimSpace(asString(modeParams["rating_code"]))
		if ratingCode == "" || ratingCode == "<nil>" {
			ratingCode = profile.RatingCode
		}
		if ratingCode != profile.RatingCode {
			return PlayerLoadout{}, newError(codeInvalidMode, "rating %q is not unlocked", ratingCode)
		}
	}
	return PlayerLoadout{
		StageID:             stageID,
		CharacterID:         characterID,
		RatingCode:          ratingCode,
		RulesetVersion:      RulesetVersion,
		ServerAuthoritative: true,
	}, nil
}

func hasLoadoutStageParam(modeParams map[string]any) bool {
	if modeParams == nil {
		return false
	}
	stageID := strings.TrimSpace(asString(modeParams["stage_id"]))
	return stageID != "" && stageID != "<nil>"
}

func initialHand(deck DeckSnapshot) ([]string, int) {
	hand := []string{}
	drawCursor := 0
	for drawCursor < len(deck.CardIDs) && len(hand) < cardHandLimit {
		cardID := strings.TrimSpace(deck.CardIDs[drawCursor])
		drawCursor++
		if cardID != "" {
			hand = append(hand, cardID)
		}
	}
	return hand, drawCursor
}

func drawServerCard(player *playerState) bool {
	if player == nil || len(player.Hand) >= cardHandLimit || len(player.DeckSnapshot.CardIDs) == 0 {
		return false
	}
	cardID := strings.TrimSpace(player.DeckSnapshot.CardIDs[player.DrawCursor%len(player.DeckSnapshot.CardIDs)])
	player.DrawCursor++
	if cardID == "" {
		return false
	}
	player.Hand = append(player.Hand, cardID)
	return true
}

func accruePlayerResources(player *playerState, ticks int) {
	if player == nil || ticks <= 0 {
		return
	}
	player.Energy = math.Min(cardMaxEnergy, player.Energy+float64(ticks)*cardEnergyPerTick)
}

func reconnectSecondsLeftForPlayer(player *playerState, now time.Time) int {
	if player == nil || player.Connected || player.DisconnectedAt.IsZero() {
		return 0
	}
	elapsed := int(now.Sub(player.DisconnectedAt).Seconds())
	return max(0, ReconnectWindowSeconds-elapsed)
}

func validateDeckSnapshot(deck DeckSnapshot) error {
	if strings.TrimSpace(deck.DeckID) == "" {
		return newError(codeInvalidDeck, "deck_id is required")
	}
	if deck.RulesetVersion != "" && deck.RulesetVersion != RulesetVersion {
		return newError(codeInvalidDeck, "ruleset mismatch")
	}
	if len(deck.CardIDs) != deckSize {
		return newError(codeInvalidDeck, "deck must have 20 cards")
	}
	counts := map[string]int{}
	for _, cardID := range deck.CardIDs {
		cardID = strings.TrimSpace(cardID)
		if cardID == "" {
			return newError(codeInvalidDeck, "card id is empty")
		}
		if _, ok := serverCardCatalog[cardID]; !ok {
			return newError(codeInvalidDeck, "unknown card %q", cardID)
		}
		counts[cardID]++
		if counts[cardID] > maxCopiesPerCard {
			return newError(codeInvalidDeck, "card %s exceeds two-copy limit", cardID)
		}
	}
	return nil
}

func validateDeckRecordForUser(user *userState, deck DeckRecord) error {
	if strings.TrimSpace(deck.DeckID) == "" {
		return newError(codeInvalidDeck, "deck_id is required")
	}
	if deck.RulesetVersion != "" && deck.RulesetVersion != RulesetVersion {
		return newError(codeInvalidDeck, "ruleset mismatch")
	}
	format := strings.TrimSpace(deck.Format)
	if format == "" {
		return newError(codeInvalidDeck, "format is required")
	}
	if len(deck.CardIDs) != deckSize {
		return newError(codeInvalidDeck, "deck must have 20 cards")
	}
	counts := map[string]int{}
	highRareCount := 0
	interferenceCount := 0
	for _, rawID := range deck.CardIDs {
		cardID := strings.TrimSpace(rawID)
		if cardID == "" {
			return newError(codeInvalidDeck, "card id is empty")
		}
		if _, ok := serverCardCatalog[cardID]; !ok {
			return newError(codeInvalidDeck, "unknown card %q", cardID)
		}
		counts[cardID]++
		if counts[cardID] > maxCopiesPerCard {
			return newError(codeInvalidDeck, "card %s exceeds two-copy limit", cardID)
		}
		if counts[cardID] > inventoryCopies(user, cardID) {
			return newError(codeInvalidDeck, "card %s is not owned enough", cardID)
		}
		if isHighRareCard(cardID) {
			highRareCount++
		}
		if _, ok := serverStrongInterferenceCards[cardID]; ok {
			interferenceCount++
		}
		if format == "ranked" {
			if _, ok := serverRankedBannedCards[cardID]; ok {
				return newError(codeInvalidDeck, "card %s is banned in ranked", cardID)
			}
		}
	}
	if highRareCount > maxHighRareCards {
		return newError(codeInvalidDeck, "high rarity card limit exceeded")
	}
	if interferenceCount > maxInterferenceCards {
		return newError(codeInvalidDeck, "strong interference card limit exceeded")
	}
	return nil
}

func (s *Service) resolveDeckForMatchLocked(user *userState, activeDeckID string, submitted DeckSnapshot) (DeckSnapshot, error) {
	if user == nil {
		return DeckSnapshot{}, newError(codeUnauthorized, "user not found")
	}
	deckID := strings.TrimSpace(activeDeckID)
	if deckID == "" {
		deckID = strings.TrimSpace(submitted.DeckID)
	}
	if deckID == "" {
		deckID = user.ActiveDeckID
	}
	if deckID != "" {
		if record, ok := user.Decks[deckID]; ok {
			if err := validateDeckRecordForUser(user, record); err != nil {
				return DeckSnapshot{}, err
			}
			return snapshotFromDeckRecord(record), nil
		}
	}
	if submitted.DeckID == "" && deckID != "" {
		submitted.DeckID = deckID
	}
	if err := validateDeckSnapshot(submitted); err != nil {
		return DeckSnapshot{}, err
	}
	record := DeckRecord{
		DeckID:         submitted.DeckID,
		Name:           submitted.Name,
		Format:         defaultDeckFormat,
		RulesetVersion: submitted.RulesetVersion,
		CardIDs:        copyStringSlice(submitted.CardIDs),
		Active:         false,
		UpdatedAt:      s.clock(),
	}
	if err := validateDeckRecordForUser(user, record); err != nil {
		return DeckSnapshot{}, err
	}
	return copyDeckSnapshot(submitted), nil
}

func (s *Service) inventorySnapshotLocked(user *userState) InventorySnapshot {
	return InventorySnapshot{
		OK:                  true,
		UserID:              user.UserID,
		RulesetVersion:      RulesetVersion,
		Items:               inventoryItems(user.Inventory),
		ServerAuthoritative: true,
		ServerTime:          s.clock(),
	}
}

func (s *Service) deckListLocked(user *userState) DeckListResponse {
	return DeckListResponse{
		OK:                  true,
		UserID:              user.UserID,
		ActiveDeckID:        user.ActiveDeckID,
		RulesetVersion:      RulesetVersion,
		Decks:               deckRecords(user.Decks),
		ServerAuthoritative: true,
		ServerTime:          s.clock(),
	}
}

func (s *Service) chestSnapshotLocked(user *userState) ChestSnapshot {
	if user.Chests == nil {
		user.Chests = defaultChests()
	}
	if user.ChestPity == nil {
		user.ChestPity = map[string]ChestPityState{}
	}
	return ChestSnapshot{
		OK:                  true,
		UserID:              user.UserID,
		RulesetVersion:      RulesetVersion,
		Wallet:              copyIntMap(user.Wallet),
		OwnedChests:         copyIntMap(user.Chests),
		Pools:               chestPools(),
		PityCounters:        copyChestPity(user.ChestPity),
		OpeningLog:          copyChestOpenings(user.ChestOpenings),
		LastResults:         copyChestOpenResults(user.LastChestResults),
		ServerAuthoritative: true,
		ServerTime:          s.clock(),
	}
}

func (s *Service) rollChestResultLocked(user *userState, pool ChestPool, seed string, index int, openingID string) ChestOpenResult {
	rolledRarity := rollChestRarity(pool.Weights, seed, index)
	rarity := applyChestPity(user.ChestPity, pool, rolledRarity)
	cardID := pickChestCardForRarity(rarity, seed, index)
	accepted, overflow, dust := grantCardToUserLocked(user, cardID, 1, s.clock())
	return ChestOpenResult{
		ID:       fmt.Sprintf("%s_%02d", openingID, index+1),
		CardID:   cardID,
		NameKey:  cardNameKey(cardID),
		Rarity:   rarity,
		Dust:     dust,
		Accepted: accepted,
		Overflow: overflow,
	}
}

func defaultInventory(now time.Time) map[string]CardInventoryEntry {
	items := map[string]CardInventoryEntry{}
	for _, cardID := range sortedCardCatalogIDs() {
		items[cardID] = CardInventoryEntry{CardID: cardID, Copies: maxCopiesPerCard, Level: 1, FirstObtainedAt: now}
	}
	return items
}

func defaultChests() map[string]int {
	return map[string]int{defaultChestPoolID: 1}
}

func chestPools() []ChestPool {
	return []ChestPool{
		{
			PoolID:   defaultChestPoolID,
			SeasonID: "local_s0",
			Name:     "Local Basic",
			NameKey:  "screen.chest.local_basic",
			Cost:     map[string]int{"chest_keys": 1},
			Weights:  map[string]int{"common": 70, "uncommon": 20, "rare": 8, "epic": 2},
			Pity:     ChestPityRules{RareEvery: 10, EpicEvery: 60, Inherit: false},
			StartsAt: "2026-01-01T00:00:00Z",
			EndsAt:   "",
			Enabled:  true,
		},
	}
}

func chestPoolByID(poolID string) (ChestPool, bool) {
	for _, pool := range chestPools() {
		if pool.PoolID == poolID {
			return pool, true
		}
	}
	return ChestPool{}, false
}

func multipliedCost(cost map[string]int, count int) map[string]int {
	out := map[string]int{}
	for key, value := range cost {
		if value > 0 {
			out[key] = value * count
		}
	}
	return out
}

func chestOpeningSeed(userID string, poolID string, openingID string, openingCount int, count int) string {
	return shortHash(fmt.Sprintf("chest:%s:%s:%s:%d:%d", userID, poolID, openingID, openingCount, count))
}

func rollChestRarity(weights map[string]int, seed string, index int) string {
	total := 0
	for _, value := range weights {
		if value > 0 {
			total += value
		}
	}
	if total <= 0 {
		return "common"
	}
	roll := int(deterministicUnit(int64(index+1), index+1, "chest:"+seed, 0) * float64(total))
	cursor := 0
	for _, rarity := range []string{"common", "uncommon", "rare", "epic", "legendary"} {
		value := weights[rarity]
		if value <= 0 {
			continue
		}
		cursor += value
		if roll < cursor {
			return rarity
		}
	}
	return "common"
}

func applyChestPity(counters map[string]ChestPityState, pool ChestPool, rolledRarity string) string {
	state := counters[pool.PoolID]
	state.RareCounter++
	state.EpicCounter++
	result := rolledRarity
	if pool.Pity.EpicEvery > 0 && state.EpicCounter >= pool.Pity.EpicEvery && rarityRank(result) < rarityRank("epic") {
		result = "epic"
	}
	if pool.Pity.RareEvery > 0 && state.RareCounter >= pool.Pity.RareEvery && rarityRank(result) < rarityRank("rare") {
		result = "rare"
	}
	if rarityRank(result) >= rarityRank("rare") {
		state.RareCounter = 0
	}
	if rarityRank(result) >= rarityRank("epic") {
		state.EpicCounter = 0
	}
	counters[pool.PoolID] = state
	return result
}

func pickChestCardForRarity(rarity string, seed string, index int) string {
	candidates := []string{}
	for _, cardID := range sortedCardCatalogIDs() {
		if serverCardRarities[cardID] == rarity {
			candidates = append(candidates, cardID)
		}
	}
	if len(candidates) == 0 {
		candidates = sortedCardCatalogIDs()
	}
	if len(candidates) == 0 {
		return ""
	}
	offset := int(deterministicUnit(int64(index+17), index+3, "chest_card:"+seed+":"+rarity, 0) * float64(len(candidates)))
	if offset >= len(candidates) {
		offset = len(candidates) - 1
	}
	return candidates[offset]
}

func grantCardToUserLocked(user *userState, cardID string, copies int, now time.Time) (int, int, int) {
	if copies <= 0 {
		return 0, 0, 0
	}
	if _, ok := serverCardCatalog[cardID]; !ok {
		return 0, copies, 0
	}
	entry := user.Inventory[cardID]
	if entry.CardID == "" {
		entry = CardInventoryEntry{CardID: cardID, Level: 1, FirstObtainedAt: now}
	}
	accepted := min(copies, max(0, maxCopiesPerCard-entry.Copies))
	overflow := copies - accepted
	if accepted > 0 {
		entry.Copies += accepted
		if entry.Level <= 0 {
			entry.Level = 1
		}
		if entry.FirstObtainedAt.IsZero() {
			entry.FirstObtainedAt = now
		}
		user.Inventory[cardID] = entry
	}
	dust := overflow * dustValueForCard(cardID)
	if dust > 0 {
		user.Wallet["card_dust"] += dust
	}
	return accepted, overflow, dust
}

func dustValueForCard(cardID string) int {
	switch serverCardRarities[cardID] {
	case "legendary":
		return 100
	case "epic":
		return 60
	case "rare":
		return 25
	case "uncommon":
		return 10
	default:
		return 5
	}
}

func cardUpgradeCost(cardID string, targetLevel int) map[string]int {
	levelStep := targetLevel - 1
	if levelStep < 1 {
		levelStep = 1
	}
	return map[string]int{"card_dust": dustValueForCard(cardID) * levelStep}
}

func rarityRank(rarity string) int {
	switch rarity {
	case "legendary":
		return 4
	case "epic":
		return 3
	case "rare":
		return 2
	case "uncommon":
		return 1
	default:
		return 0
	}
}

func cardNameKey(cardID string) string {
	if cardID == "" {
		return ""
	}
	return "card." + cardID + ".name"
}

func defaultDeckRecord(now time.Time) DeckRecord {
	return DeckRecord{
		DeckID:         defaultDeckID,
		Name:           defaultDeckName,
		Format:         defaultDeckFormat,
		RulesetVersion: RulesetVersion,
		CardIDs:        defaultDeckCardIDs(),
		Active:         true,
		UpdatedAt:      now,
	}
}

func defaultDeckCardIDs() []string {
	return []string{
		"focus_lens",
		"hitbox_charm",
		"density_surge",
		"tempo_break",
		"bomb_amplifier",
		"guard_seal",
		"graze_engine",
		"draw_sigil",
		"aim_baffle",
		"purge_charm",
		"focus_lens",
		"hitbox_charm",
		"density_surge",
		"tempo_break",
		"bomb_amplifier",
		"guard_seal",
		"graze_engine",
		"draw_sigil",
		"aim_baffle",
		"purge_charm",
	}
}

func snapshotFromDeckRecord(record DeckRecord) DeckSnapshot {
	return DeckSnapshot{
		DeckID:         record.DeckID,
		Name:           record.Name,
		RulesetVersion: record.RulesetVersion,
		CardIDs:        copyStringSlice(record.CardIDs),
	}
}

func inventoryCopies(user *userState, cardID string) int {
	if user == nil {
		return 0
	}
	entry, ok := user.Inventory[cardID]
	if !ok {
		return 0
	}
	return entry.Copies
}

func isHighRareCard(cardID string) bool {
	switch serverCardRarities[cardID] {
	case "rare", "epic", "legendary":
		return true
	default:
		return false
	}
}

func inputPacketFromMap(raw map[string]any) (InputPacket, error) {
	tick, ok := asInt(raw["tick"])
	if !ok {
		return InputPacket{}, newError(codeInvalidInput, "tick is required")
	}
	seq, ok := asInt(raw["seq"])
	if !ok {
		return InputPacket{}, newError(codeInvalidInput, "seq is required")
	}
	dir, ok := asInt(raw["dir"])
	if !ok {
		return InputPacket{}, newError(codeInvalidInput, "dir is required")
	}
	cardSlot := -1
	if value, exists := raw["card_slot"]; exists {
		parsed, ok := asInt(value)
		if !ok {
			return InputPacket{}, newError(codeInvalidInput, "card_slot is invalid")
		}
		cardSlot = parsed
	}
	return InputPacket{
		Tick:     tick,
		Seq:      seq,
		Dir:      dir,
		Slow:     asBool(raw["slow"]),
		Shoot:    asBool(raw["shoot"]),
		Bomb:     asBool(raw["bomb"]),
		CardSlot: cardSlot,
	}, nil
}

func modeActionRequestFromMap(raw map[string]any) ModeActionRequest {
	payload := map[string]any{}
	if payloadValue, ok := raw["payload"]; ok {
		if parsed, ok := payloadValue.(map[string]any); ok {
			payload = copyAnyMap(parsed)
		}
	}
	return ModeActionRequest{
		ModeID:                    strings.TrimSpace(asString(raw["mode_id"])),
		ActionType:                strings.TrimSpace(asString(raw["action_type"])),
		Payload:                   payload,
		ClientResultAuthoritative: asBool(raw["client_result_authoritative"]),
	}
}

func validateInputPacket(packet InputPacket, player *playerState, match *matchState) error {
	if packet.Tick <= player.LastTick {
		return newError(codeInvalidInput, "tick must be monotonic")
	}
	if packet.Seq != player.LastSeq+1 {
		return newError(codeInvalidInput, "seq must be %d", player.LastSeq+1)
	}
	if packet.Dir < 0 || packet.Dir > 15 {
		return newError(codeInvalidInput, "dir must be 0..15")
	}
	if packet.CardSlot < -1 || packet.CardSlot >= cardHandLimit {
		return newError(codeInvalidInput, "card_slot must be -1..3 or 1..4")
	}
	if packet.Tick+4 < match.Tick {
		return newError(codeInvalidInput, "input too late")
	}
	baselineTick := max(player.LastTick, match.LastSimulatedTick)
	if baselineTick < 0 {
		baselineTick = 0
	}
	if packet.Tick > baselineTick+TickRate*5 {
		return newError(codeInvalidInput, "tick jump is too large")
	}
	return nil
}

func applyBattleRoyaleSelectionLocked(match *matchState, player *playerState, actionID string, payload map[string]any) (MatchEvent, error) {
	if match.ModeID != "battle_royale" {
		return MatchEvent{}, newError(codeModeAction, "select_round_card requires battle_royale")
	}
	roundIndex, ok := asInt(payload["round_index"])
	if !ok {
		roundIndex = max(0, match.Tick/(TickRate*30))
	}
	cardID := strings.TrimSpace(asString(payload["card_id"]))
	if cardID == "" {
		return MatchEvent{}, newError(codeModeAction, "card_id is required")
	}
	candidates := battleRoyaleCandidates(match, roundIndex)
	if !stringSliceContains(candidates, cardID) {
		return MatchEvent{}, newError(codeModeAction, "card is not a server candidate")
	}
	selections := anyMapFrom(match.ModeState["round_selections"])
	selectionKey := fmt.Sprintf("%d:%s", roundIndex, player.UserID)
	if _, exists := selections[selectionKey]; exists {
		return MatchEvent{}, newError(codeModeAction, "round selection already submitted")
	}
	selection := map[string]any{
		"user_id":     player.UserID,
		"card_id":     cardID,
		"round_index": roundIndex,
		"action_id":   actionID,
	}
	selections[selectionKey] = selection
	match.ModeState["round_selections"] = selections
	match.ModeState["round_index"] = roundIndex
	match.ModeState["candidate_cards"] = candidates
	match.ModeState["choice_deadline_tick"] = (roundIndex + 1) * TickRate * 30
	match.ModeState["zero_round_order"] = sortedPlayerIDs(match)
	match.ModeState["public_pool_hash"] = shortHash(fmt.Sprintf("%d:%s:%d", match.ServerSeed, match.ModeID, roundIndex))
	return MatchEvent{
		Type:       "mode_action_accepted",
		Tick:       match.Tick,
		UserID:     player.UserID,
		ActionID:   actionID,
		ActionType: "select_round_card",
		CardID:     cardID,
		RoundIndex: roundIndex,
		Status:     "accepted",
	}, nil
}

func applyBossTransferLocked(match *matchState, player *playerState, actionID string, payload map[string]any) (MatchEvent, error) {
	if match.ModeID != "world_boss" && match.ModeID != "instance_boss" {
		return MatchEvent{}, newError(codeModeAction, "transfer_card requires boss mode")
	}
	fromUserID := strings.TrimSpace(asString(payload["from_player_id"]))
	toUserID := strings.TrimSpace(asString(payload["to_player_id"]))
	cardID := strings.TrimSpace(asString(payload["card_id"]))
	if fromUserID == "" {
		fromUserID = player.UserID
	}
	if fromUserID != player.UserID {
		return MatchEvent{}, newError(codeModeAction, "cannot transfer another player's card")
	}
	if toUserID == "" || cardID == "" {
		return MatchEvent{}, newError(codeModeAction, "to_player_id and card_id are required")
	}
	fromPlayer := match.Players[fromUserID]
	toPlayer := match.Players[toUserID]
	if fromPlayer == nil || toPlayer == nil {
		return MatchEvent{}, newError(codeModeAction, "transfer players must be in match")
	}
	if fromUserID == toUserID {
		return MatchEvent{}, newError(codeModeAction, "cannot transfer to self")
	}
	if !deckContainsCard(fromPlayer.DeckSnapshot, cardID) {
		return MatchEvent{}, newError(codeModeAction, "card is not in source deck")
	}
	if _, exists := match.TransferredCards[cardID]; exists {
		return MatchEvent{}, newError(codeModeAction, "card already transferred")
	}
	transfer := map[string]any{
		"action_id":      actionID,
		"from_player_id": fromUserID,
		"to_player_id":   toUserID,
		"card_id":        cardID,
		"status":         "accepted",
	}
	match.TransferredCards[cardID] = actionID
	requests := anySliceFrom(match.ModeState["transfer_requests"])
	requests = append(requests, transfer)
	match.ModeState["transfer_requests"] = requests
	match.ModeState["party_status"] = "engaged"
	match.ModeState["transferred_card_count"] = len(match.TransferredCards)
	return MatchEvent{
		Type:       "mode_action_accepted",
		Tick:       match.Tick,
		UserID:     player.UserID,
		ActionID:   actionID,
		ActionType: "transfer_card",
		CardID:     cardID,
		FromUserID: fromUserID,
		ToUserID:   toUserID,
		Status:     "accepted",
	}, nil
}

func firstForbiddenField(raw map[string]any) string {
	for key := range raw {
		if _, ok := forbiddenClientFields[key]; ok {
			return key
		}
	}
	return ""
}

func firstForbiddenFieldDeep(raw map[string]any) string {
	for key, value := range raw {
		if _, ok := forbiddenClientFields[key]; ok {
			return key
		}
		switch typed := value.(type) {
		case map[string]any:
			if nested := firstForbiddenFieldDeep(typed); nested != "" {
				return nested
			}
		case []any:
			for _, item := range typed {
				if nestedMap, ok := item.(map[string]any); ok {
					if nested := firstForbiddenFieldDeep(nestedMap); nested != "" {
						return nested
					}
				}
			}
		}
	}
	return ""
}

func sanitizedLobbyMetadata(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range raw {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, forbidden := forbiddenClientFields[key]; forbidden {
			continue
		}
		switch typed := value.(type) {
		case string:
			out[key] = strings.TrimSpace(typed)
		case bool, int, int32, int64, float64:
			out[key] = typed
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func claimEligibility(user *userState, req ActivityClaimRequest) error {
	switch req.ClaimKind {
	case "task":
		task, ok := user.Tasks[req.ClaimID]
		if !ok {
			return newError(codeClaimIneligible, "task not found")
		}
		if task.Claimed {
			return newError(codeClaimIneligible, "task already claimed")
		}
		if task.Progress < task.Target {
			return newError(codeClaimIneligible, "task incomplete")
		}
	case "event":
		event, ok := user.Events[req.ClaimID]
		if !ok {
			return newError(codeClaimIneligible, "event not found")
		}
		if event.Points <= 0 {
			return newError(codeClaimIneligible, "event has no points")
		}
		if event.RewardStatus != "pending" && event.RewardStatus != "available" {
			return newError(codeClaimIneligible, "event reward is %s", event.RewardStatus)
		}
	case "leaderboard":
		board, ok := user.Leaderboards[req.ClaimID]
		if !ok {
			return newError(codeClaimIneligible, "leaderboard not found")
		}
		if board.Rank <= 0 || board.Percentile > 0.30 {
			return newError(codeClaimIneligible, "leaderboard threshold not reached")
		}
	default:
		return newError(codeInvalidRequest, "invalid claim kind")
	}
	return nil
}

func isValidClaimKind(kind string) bool {
	return kind == "task" || kind == "event" || kind == "leaderboard"
}

func defaultTasks() map[string]TaskState {
	return map[string]TaskState{
		"daily_complete_match": {LabelKey: "task.daily.complete_match", Progress: 0, Target: 1, Claimed: false},
		"daily_graze":          {LabelKey: "task.daily.graze", Progress: 0, Target: 500, Claimed: false},
		"weekly_replay_review": {LabelKey: "task.weekly.replay_review", Progress: 0, Target: 1, Claimed: false},
	}
}

func defaultEvents() map[string]EventState {
	return map[string]EventState{
		"local_s0": {LabelKey: "event.local_s0.name", StartsAt: "2026-01-01T00:00:00Z", EndsAt: "2026-12-31T23:59:59Z", Points: 0, RewardStatus: "pending"},
	}
}

func defaultLeaderboards() map[string]LeaderboardRow {
	return map[string]LeaderboardRow{
		"rank_score":        {LeaderboardID: "rank_score", LabelKey: "leaderboard.rank_score", Score: defaultRankScore, Rank: rankFromPercentile(1.0), Percentile: 1.0, SeasonID: defaultSeasonID},
		"single_score":      {LeaderboardID: "single_score", LabelKey: "leaderboard.single_score", Score: 0, Rank: 0, Percentile: 1.0, SeasonID: defaultSeasonID},
		"world_boss_damage": {LeaderboardID: "world_boss_damage", LabelKey: "leaderboard.world_boss_damage", Score: 0, Rank: 0, Percentile: 1.0, SeasonID: defaultSeasonID},
	}
}

func defaultCertificationProfile(userID string, now time.Time) CertificationProfile {
	return CertificationProfile{
		OK:                        true,
		UserID:                    userID,
		RatingCode:                defaultRatingCode,
		SeasonID:                  defaultSeasonID,
		RankScore:                 defaultRankScore,
		RankScoreFloor:            defaultRankScore,
		ChallengeStageID:          "starlit_lanes",
		Percentile:                1.0,
		Top30Qualified:            false,
		NextCertificationUnlocked: false,
		LastRankScoreDelta:        0,
		ServerAuthoritative:       true,
		ClientResultAuthoritative: false,
		UpdatedAt:                 now,
	}
}

func normalizedCertificationProfile(profile CertificationProfile) CertificationProfile {
	if profile.RatingCode == "" {
		profile = defaultCertificationProfile(profile.UserID, profile.UpdatedAt)
	}
	if profile.SeasonID == "" {
		profile.SeasonID = defaultSeasonID
	}
	if profile.RankScore <= 0 {
		profile.RankScore = defaultRankScore
	}
	if profile.RankScoreFloor <= 0 {
		profile.RankScoreFloor = defaultRankScore
	}
	if profile.ChallengeStageID == "" {
		profile.ChallengeStageID = "starlit_lanes"
	}
	if profile.Percentile <= 0 {
		profile.Percentile = 1.0
	}
	profile.OK = true
	profile.ServerAuthoritative = true
	profile.ClientResultAuthoritative = false
	return profile
}

func certificationProfileFromSettlement(user *userState, settlement *MatchEndEvent) CertificationProfile {
	profile := normalizedCertificationProfile(user.Certification)
	profile.UserID = user.UserID
	profile.RatingCode = strings.TrimSpace(asString(settlement.ModeResult["rating_code"]))
	if profile.RatingCode == "" || profile.RatingCode == "<nil>" {
		profile.RatingCode = defaultRatingCode
	}
	profile.RankScore = max(profile.RankScoreFloor, intFromAnyValue(settlement.ModeResult["rank_score_after"]))
	profile.LastRankScoreDelta = intFromAnyValue(settlement.ModeResult["rank_score_delta"])
	profile.Percentile = floatFromAnyValue(settlement.ModeResult["percentile_after"], profile.Percentile)
	profile.Top30Qualified = asBool(settlement.ModeResult["qualified_top_30"])
	profile.NextCertificationUnlocked = asBool(settlement.ModeResult["next_certification_unlocked"])
	profile.LastResult = settlement.Result
	profile.UpdatedAt = settlement.SettledAt
	return profile
}

func certificationRankDelta(result string, player *playerState) int {
	performance := player.Score/35 + player.DamageDealt/20 + player.GrazeCount/30 - player.HitCount*4
	switch result {
	case "win":
		return clampInt(28+performance, 15, 90)
	case "draw":
		return clampInt(8+performance/2, -8, 35)
	default:
		return clampInt(-22+performance/3, -60, 12)
	}
}

func certificationPercentile(rankScore int, result string) float64 {
	base := 1.0 - float64(rankScore-defaultRankScore)/360.0
	switch result {
	case "win":
		base -= 0.08
	case "draw":
		base -= 0.02
	}
	return round(clamp(base, 0.05, 0.95), 3)
}

func rankFromPercentile(percentile float64) int {
	return max(1, int(math.Ceil(clamp(percentile, 0.01, 1.0)*100.0)))
}

func defaultModeState(modeID string) map[string]any {
	switch modeID {
	case "certification":
		return map[string]any{"rating_code": "copper", "rank_score_preview": 0, "challenge_progress": 0.0}
	case "pvp_duel":
		return map[string]any{"duel_round": 1, "duel_score_limit": 1, "duel_status": "loading"}
	case "battle_royale":
		return map[string]any{"round_index": 0, "choice_deadline_tick": TickRate * 30, "public_pool_hash": "", "zero_round_order": []string{}, "candidate_cards": []string{"focus_lens", "density_surge", "tempo_break"}, "round_selections": map[string]any{}}
	case "world_boss":
		return map[string]any{"boss_hp_preview": 100000, "daily_attempts_left": 3, "transfer_requests": []any{}}
	case "instance_boss":
		return map[string]any{"boss_phase": "loading", "party_status": "forming", "clear_conditions": []string{"defeat_boss"}}
	default:
		return map[string]any{}
	}
}

func modeConfigList() []ModeConfig {
	keys := make([]string, 0, len(ModeConfigs))
	for key := range ModeConfigs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ModeConfig, 0, len(keys))
	for _, key := range keys {
		out = append(out, ModeConfigs[key])
	}
	return out
}

func battleRoyaleCandidates(match *matchState, roundIndex int) []string {
	catalog := sortedCardCatalogIDs()
	if len(catalog) == 0 {
		return []string{}
	}
	candidates := []string{}
	offset := int(deterministicUnit(match.ServerSeed, roundIndex+1, "br_candidates", 0) * float64(len(catalog)))
	step := 3
	for len(candidates) < 3 && len(candidates) < len(catalog) {
		cardID := catalog[(offset+len(candidates)*step)%len(catalog)]
		if !stringSliceContains(candidates, cardID) {
			candidates = append(candidates, cardID)
		}
		step += 2
	}
	return candidates
}

func sortedCardCatalogIDs() []string {
	ids := make([]string, 0, len(serverCardCatalog))
	for cardID := range serverCardCatalog {
		ids = append(ids, cardID)
	}
	sort.Strings(ids)
	return ids
}

func sortedPlayerIDs(match *matchState) []string {
	out := append([]string{}, match.PlayerIDs...)
	sort.Strings(out)
	return out
}

func (s *Service) registerDefaultBattleServerLocked() {
	_, _ = s.upsertBattleServerLocked(BattleServerHeartbeatRequest{
		BattleServerID: DefaultBattleServerID,
		Endpoint:       DefaultBattleEndpoint,
		Region:         "local",
		BuildID:        "dev",
		Capacity:       128,
		ActiveMatches:  0,
		Load:           0,
		Status:         "online",
		SupportedModes: modeConfigKeys(),
	})
}

func (s *Service) upsertBattleServerLocked(req BattleServerHeartbeatRequest) (*battleServerState, error) {
	serverID := strings.TrimSpace(req.BattleServerID)
	if serverID == "" {
		return nil, newError(codeInvalidRequest, "battle_server_id is required")
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	existing := s.battleServers[serverID]
	if endpoint == "" && existing != nil {
		endpoint = existing.Endpoint
	}
	if endpoint == "" {
		return nil, newError(codeInvalidRequest, "endpoint is required")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "online"
	}
	capacity := req.Capacity
	if capacity <= 0 && existing != nil {
		capacity = existing.Capacity
	}
	if capacity <= 0 {
		capacity = 1
	}
	supportedModes := append([]string{}, req.SupportedModes...)
	if len(supportedModes) == 0 && existing != nil {
		supportedModes = append([]string{}, existing.SupportedModes...)
	}
	if len(supportedModes) == 0 {
		supportedModes = modeConfigKeys()
	}
	sort.Strings(supportedModes)
	server := &battleServerState{
		BattleServerID: serverID,
		Endpoint:       endpoint,
		Region:         strings.TrimSpace(req.Region),
		BuildID:        strings.TrimSpace(req.BuildID),
		Capacity:       capacity,
		ActiveMatches:  max(0, req.ActiveMatches),
		Load:           clamp(req.Load, 0, 1),
		Status:         status,
		SupportedModes: supportedModes,
		LastSeenAt:     s.clock(),
	}
	if server.Region == "" && existing != nil {
		server.Region = existing.Region
	}
	if server.BuildID == "" && existing != nil {
		server.BuildID = existing.BuildID
	}
	s.battleServers[serverID] = server
	return server, nil
}

func battleServerStatusFromState(server *battleServerState) BattleServerStatus {
	if server == nil {
		return BattleServerStatus{}
	}
	return BattleServerStatus{
		OK:                  true,
		BattleServerID:      server.BattleServerID,
		Endpoint:            server.Endpoint,
		Region:              server.Region,
		BuildID:             server.BuildID,
		Capacity:            server.Capacity,
		ActiveMatches:       server.ActiveMatches,
		Load:                server.Load,
		Status:              server.Status,
		SupportedModes:      append([]string{}, server.SupportedModes...),
		LastSeenAt:          server.LastSeenAt,
		ServerAuthoritative: true,
	}
}

func (s *Service) ensureBattleAllocationLocked(match *matchState) *BattleServerAllocation {
	if match == nil {
		return nil
	}
	if existing := s.battleAllocations[match.MatchID]; existing != nil {
		match.BattleAllocation = existing
		return existing
	}
	server := s.selectBattleServerLocked(match.ModeID)
	players := make([]BattleAllocationPlayer, 0, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		if player == nil {
			continue
		}
		players = append(players, BattleAllocationPlayer{
			UserID:           userID,
			PlayerID:         playerIDForUser(match.MatchID, userID),
			DisplayName:      player.DisplayName,
			DeckSnapshotHash: deckSnapshotHash(player.DeckSnapshot),
			Loadout:          player.Loadout,
		})
	}
	alloc := &BattleServerAllocation{
		OK:                  true,
		Version:             currentVersionStamp(),
		MatchID:             match.MatchID,
		ModeID:              match.ModeID,
		BattleServerID:      server.BattleServerID,
		Endpoint:            server.Endpoint,
		Players:             players,
		ServerSeed:          match.ServerSeed,
		ServerSeedHex:       seedHex(match.ServerSeed),
		ModeConfigHash:      modeConfigHash(match.ModeID),
		AllocatedAt:         s.clock(),
		ServerAuthoritative: true,
	}
	s.battleAllocations[match.MatchID] = alloc
	match.BattleAllocation = alloc
	s.recordMatchAllocationAuditLocked(alloc, server)
	return alloc
}

func (s *Service) selectBattleServerLocked(modeID string) *battleServerState {
	if len(s.battleServers) == 0 {
		s.registerDefaultBattleServerLocked()
	}
	ids := make([]string, 0, len(s.battleServers))
	for id := range s.battleServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var best *battleServerState
	bestScore := math.MaxFloat64
	bestModeBreadth := math.MaxInt
	for _, id := range ids {
		server := s.battleServers[id]
		if server == nil || !s.battleServerEligibleForAllocationLocked(server, modeID) {
			continue
		}
		if server.Capacity > 0 && server.ActiveMatches >= server.Capacity {
			continue
		}
		score := server.Load + float64(server.ActiveMatches)/float64(max(1, server.Capacity))
		modeBreadth := battleServerModeBreadth(server)
		if best == nil || score < bestScore || (score == bestScore && modeBreadth < bestModeBreadth) {
			best = server
			bestScore = score
			bestModeBreadth = modeBreadth
		}
	}
	if best != nil {
		s.accountBattleServerAllocationLocked(best)
		return best
	}
	fallback := s.battleServers[DefaultBattleServerID]
	if fallback == nil {
		s.registerDefaultBattleServerLocked()
		fallback = s.battleServers[DefaultBattleServerID]
	}
	s.accountBattleServerAllocationLocked(fallback)
	return fallback
}

func (s *Service) battleServerEligibleForAllocationLocked(server *battleServerState, modeID string) bool {
	if server == nil || server.Status != "online" || !serverSupportsMode(server, modeID) {
		return false
	}
	if server.BattleServerID == DefaultBattleServerID {
		return true
	}
	lastSeen := server.LastSeenAt
	if lastSeen.IsZero() {
		return false
	}
	return !s.clock().After(lastSeen.Add(time.Duration(BattleServerHeartbeatTTLSeconds) * time.Second))
}

func (s *Service) accountBattleServerAllocationLocked(server *battleServerState) {
	if server == nil {
		return
	}
	server.ActiveMatches++
	server.Load = clamp(float64(server.ActiveMatches)/float64(max(1, server.Capacity)), server.Load, 1)
}

func battleServerModeBreadth(server *battleServerState) int {
	if server == nil || len(server.SupportedModes) == 0 {
		return math.MaxInt
	}
	if stringSliceContains(server.SupportedModes, "*") {
		return math.MaxInt - 1
	}
	return len(server.SupportedModes)
}

func serverSupportsMode(server *battleServerState, modeID string) bool {
	if server == nil {
		return false
	}
	if len(server.SupportedModes) == 0 {
		return true
	}
	for _, supported := range server.SupportedModes {
		if supported == modeID || supported == "*" {
			return true
		}
	}
	return false
}

func (s *Service) signedBattleTicketLocked(match *matchState, user *userState) (*SignedBattleTicket, error) {
	if match == nil || user == nil {
		return nil, newError(codeInvalidRequest, "match and user are required")
	}
	player := match.Players[user.UserID]
	if player == nil {
		return nil, newError(codeUnauthorized, "user is not in match")
	}
	allocation := s.ensureBattleAllocationLocked(match)
	if allocation == nil {
		return nil, newError(codeBattleServer, "battle server allocation unavailable")
	}
	cacheKey := battleTicketCacheKey(match.MatchID, user.UserID)
	now := s.clock()
	if existing := s.battleTickets[cacheKey]; existing != nil {
		if _, consumed := s.consumedBattleTickets[existing.Ticket.TicketID]; !consumed && existing.Ticket.ExpiresAt.After(now) {
			return existing, nil
		}
		if _, consumed := s.consumedBattleTickets[existing.Ticket.TicketID]; !consumed {
			s.recordBattleTicketExpiredAuditLocked(existing, now)
		}
	}
	ticketID := s.nextIDLocked("battle_ticket")
	issuedAt := now
	expiresAt := issuedAt.Add(time.Duration(BattleTicketTTLSeconds) * time.Second)
	ticket := BattleTicket{
		Version:             currentVersionStamp(),
		TicketID:            ticketID,
		MatchID:             match.MatchID,
		UserID:              user.UserID,
		PlayerID:            playerIDForUser(match.MatchID, user.UserID),
		ModeID:              match.ModeID,
		BattleServerID:      allocation.BattleServerID,
		Endpoint:            allocation.Endpoint,
		DeckSnapshotHash:    deckSnapshotHash(player.DeckSnapshot),
		RulesetVersion:      match.RulesetVersion,
		TicketNonceHex:      shortHash(fmt.Sprintf("%s:%s:%s:%d", ticketID, match.MatchID, user.UserID, issuedAt.UnixNano())) + shortHash(user.SessionToken),
		IssuedAt:            issuedAt,
		ExpiresAt:           expiresAt,
		IssuedAtMS:          issuedAt.UnixMilli(),
		ExpiresAtMS:         expiresAt.UnixMilli(),
		BusinessSessionID:   businessSessionRef(user.SessionToken),
		ServerAuthoritative: true,
	}
	payload, _ := json.Marshal(ticket)
	signature := ed25519.Sign(s.signingPrivateKey, payload)
	signed := &SignedBattleTicket{
		OK:                  true,
		Ticket:              ticket,
		SignatureAlg:        "ED25519",
		KeyID:               s.signingKeyID,
		SignatureHex:        hex.EncodeToString(signature),
		PublicKeyHex:        hex.EncodeToString(s.signingPublicKey),
		ServerAuthoritative: true,
		ServerTime:          now,
	}
	s.battleTickets[cacheKey] = signed
	s.battleTicketsByID[ticket.TicketID] = signed
	s.recordBattleTicketAuditLocked(signed)
	return signed, nil
}

func (s *Service) signedBattleTicketByIDLocked(ticketID string) *SignedBattleTicket {
	if ticketID == "" {
		return nil
	}
	return s.battleTicketsByID[ticketID]
}

func (s *Service) recordMatchAllocationAuditLocked(allocation *BattleServerAllocation, server *battleServerState) {
	if s.battleAuditRepo == nil || allocation == nil {
		return
	}
	allocationJSON := ""
	if encoded, err := json.Marshal(copyBattleAllocation(allocation)); err == nil {
		allocationJSON = string(encoded)
	}
	region := ""
	if server != nil {
		region = server.Region
	}
	err := s.battleAuditRepo.RecordMatchAllocationAudit(BattleAllocationAuditRecord{
		MatchID:             allocation.MatchID,
		ModeID:              allocation.ModeID,
		BattleServerID:      allocation.BattleServerID,
		Endpoint:            allocation.Endpoint,
		Region:              region,
		ProtocolVersion:     fmt.Sprintf("%d", allocation.Version.ProtocolVersion),
		RulesetVersion:      allocation.Version.RulesetVersion,
		ModeConfigHash:      allocation.ModeConfigHash,
		ServerSeedHash:      "sha256:" + shortHash(allocation.ServerSeedHex),
		PlayerCount:         len(allocation.Players),
		AllocationJSON:      allocationJSON,
		Status:              "allocated",
		CreatedAt:           allocation.AllocatedAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:allocation", allocation.MatchID, allocation.ModeID, allocation.BattleServerID, allocation.ModeConfigHash, fmt.Sprintf("%d", len(allocation.Players)))
	s.recordBattleAuditOutcomeLocked("match_allocation", fingerprint, allocation.AllocatedAt, err)
}

func (s *Service) recordBattleServerLifecycleAuditLocked(server *battleServerState, status string) {
	if s.battleAuditRepo == nil || server == nil || server.BattleServerID == "" {
		return
	}
	now := s.clock()
	metadata := map[string]any{
		"battle_server_id": server.BattleServerID,
		"endpoint":         server.Endpoint,
		"region":           server.Region,
		"build_id":         server.BuildID,
		"capacity":         server.Capacity,
		"active_matches":   server.ActiveMatches,
		"load":             server.Load,
		"status":           server.Status,
		"supported_modes":  append([]string{}, server.SupportedModes...),
		"last_seen_at":     server.LastSeenAt,
	}
	metadataJSON := "{}"
	if encoded, err := json.Marshal(metadata); err == nil {
		metadataJSON = string(encoded)
	}
	err := s.battleAuditRepo.RecordMatchAllocationAudit(BattleAllocationAuditRecord{
		MatchID:             "battle-server:" + server.BattleServerID,
		ModeID:              "battle_server_lifecycle",
		BattleServerID:      server.BattleServerID,
		Endpoint:            server.Endpoint,
		Region:              server.Region,
		ProtocolVersion:     fmt.Sprintf("%d", ProtocolVersion),
		RulesetVersion:      RulesetVersion,
		ModeConfigHash:      "sha256:" + shortHash(strings.Join(server.SupportedModes, ",")),
		ServerSeedHash:      "sha256:" + shortHash(server.BuildID+"|"+server.Endpoint+"|"+server.Region),
		PlayerCount:         server.ActiveMatches,
		AllocationJSON:      metadataJSON,
		Status:              status,
		CreatedAt:           now,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:server", status, server.BattleServerID, server.Endpoint, server.BuildID, fmt.Sprintf("%d", server.ActiveMatches), fmt.Sprintf("%.3f", server.Load))
	s.recordBattleAuditOutcomeLocked(status, fingerprint, now, err)
}

func (s *Service) recordBattleTicketAuditLocked(signed *SignedBattleTicket) {
	if s.battleAuditRepo == nil || signed == nil || signed.Ticket.TicketID == "" {
		return
	}
	ticket := signed.Ticket
	err := s.battleAuditRepo.RecordBattleTicketAudit(BattleTicketAuditRecord{
		TicketID:            ticket.TicketID,
		MatchID:             ticket.MatchID,
		UserID:              ticket.UserID,
		PlayerID:            ticket.PlayerID,
		BattleServerID:      ticket.BattleServerID,
		Endpoint:            ticket.Endpoint,
		KeyID:               signed.KeyID,
		RulesetVersion:      ticket.RulesetVersion,
		ProtocolVersion:     fmt.Sprintf("%d", ticket.Version.ProtocolVersion),
		DeckSnapshotHash:    ticket.DeckSnapshotHash,
		ModeConfigHash:      modeConfigHash(ticket.ModeID),
		Nonce:               ticket.TicketNonceHex,
		SignaturePrefix:     prefixString(signed.SignatureHex, 16),
		Status:              "issued",
		IssuedAt:            ticket.IssuedAt,
		ExpiresAt:           ticket.ExpiresAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:ticket", ticket.TicketID, ticket.MatchID, ticket.UserID, ticket.PlayerID, ticket.DeckSnapshotHash, ticket.TicketNonceHex)
	s.recordBattleAuditOutcomeLocked("battle_ticket", fingerprint, ticket.IssuedAt, err)
}

func (s *Service) recordBattleTicketExpiredAuditLocked(signed *SignedBattleTicket, expiredAt time.Time) {
	if s.battleAuditRepo == nil || signed == nil || signed.Ticket.TicketID == "" {
		return
	}
	ticket := signed.Ticket
	if expiredAt.IsZero() {
		expiredAt = s.clock()
	}
	err := s.battleAuditRepo.RecordBattleTicketAudit(BattleTicketAuditRecord{
		TicketID:            ticket.TicketID,
		MatchID:             ticket.MatchID,
		UserID:              ticket.UserID,
		PlayerID:            ticket.PlayerID,
		BattleServerID:      ticket.BattleServerID,
		Endpoint:            ticket.Endpoint,
		KeyID:               signed.KeyID,
		RulesetVersion:      ticket.RulesetVersion,
		ProtocolVersion:     fmt.Sprintf("%d", ticket.Version.ProtocolVersion),
		DeckSnapshotHash:    ticket.DeckSnapshotHash,
		ModeConfigHash:      modeConfigHash(ticket.ModeID),
		Nonce:               ticket.TicketNonceHex,
		SignaturePrefix:     prefixString(signed.SignatureHex, 16),
		Status:              "expired",
		IssuedAt:            ticket.IssuedAt,
		ExpiresAt:           ticket.ExpiresAt,
		ConsumedAt:          expiredAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:ticket:expired", ticket.TicketID, ticket.MatchID, ticket.UserID, ticket.PlayerID, ticket.DeckSnapshotHash, ticket.TicketNonceHex)
	s.recordBattleAuditOutcomeLocked("battle_ticket_expired", fingerprint, expiredAt, err)
}

func (s *Service) recordBattleTicketConsumedAuditLocked(signed *SignedBattleTicket, consumedAt time.Time) {
	if s.battleAuditRepo == nil || signed == nil || signed.Ticket.TicketID == "" {
		return
	}
	ticket := signed.Ticket
	if consumedAt.IsZero() {
		consumedAt = s.clock()
	}
	err := s.battleAuditRepo.RecordBattleTicketAudit(BattleTicketAuditRecord{
		TicketID:            ticket.TicketID,
		MatchID:             ticket.MatchID,
		UserID:              ticket.UserID,
		PlayerID:            ticket.PlayerID,
		BattleServerID:      ticket.BattleServerID,
		Endpoint:            ticket.Endpoint,
		KeyID:               signed.KeyID,
		RulesetVersion:      ticket.RulesetVersion,
		ProtocolVersion:     fmt.Sprintf("%d", ticket.Version.ProtocolVersion),
		DeckSnapshotHash:    ticket.DeckSnapshotHash,
		ModeConfigHash:      modeConfigHash(ticket.ModeID),
		Nonce:               ticket.TicketNonceHex,
		SignaturePrefix:     prefixString(signed.SignatureHex, 16),
		Status:              "consumed",
		IssuedAt:            ticket.IssuedAt,
		ExpiresAt:           ticket.ExpiresAt,
		ConsumedAt:          consumedAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:ticket:consumed", ticket.TicketID, ticket.MatchID, ticket.UserID, ticket.PlayerID, ticket.DeckSnapshotHash, ticket.TicketNonceHex)
	s.recordBattleAuditOutcomeLocked("battle_ticket_consumed", fingerprint, consumedAt, err)
}

func (s *Service) recordLobbyRoomAuditLocked(room *roomState, ticket *queueTicket, userID string, action string, createdAt time.Time) {
	s.recordLobbyRoomAuditRecordLocked(s.lobbyRoomAuditRecordLocked(room, ticket, userID, action, createdAt))
}

func (s *Service) lobbyRoomAuditRecordLocked(room *roomState, ticket *queueTicket, userID string, action string, createdAt time.Time) LobbyRoomAuditRecord {
	if createdAt.IsZero() {
		createdAt = s.clock()
	}
	modeID := ""
	roomCode := ""
	roomStatus := ""
	hostUserID := ""
	stageID := ""
	matchID := ""
	currentPlayers := 0
	requiredPlayers := 0
	modeRulesetVersion := ""
	modeConfigHashValue := ""
	if room != nil {
		modeID = room.ModeID
		roomCode = room.RoomCode
		roomStatus = room.Status
		hostUserID = room.HostUserID
		stageID = room.StageID
		matchID = room.MatchID
		currentPlayers = len(room.TicketIDs)
		if mode, ok := ModeConfigs[room.ModeID]; ok {
			requiredPlayers = mode.MinPlayers
			modeRulesetVersion = mode.ModeRulesetVersion
		}
		modeConfigHashValue = modeConfigHash(room.ModeID)
	}
	ticketID := ""
	deckHash := ""
	if ticket != nil {
		ticketID = ticket.TicketID
		if userID == "" {
			userID = ticket.UserID
		}
		if modeID == "" {
			modeID = ticket.ModeID
		}
		if roomCode == "" {
			roomCode = ticket.RoomCode
		}
		if roomStatus == "" {
			roomStatus = ticket.RoomStatus
		}
		if matchID == "" {
			matchID = ticket.MatchID
		}
		if stageID == "" {
			stageID = ticket.Loadout.StageID
		}
		deckHash = deckSnapshotHash(ticket.DeckSnapshot)
		if requiredPlayers == 0 {
			if mode, ok := ModeConfigs[ticket.ModeID]; ok {
				requiredPlayers = mode.MinPlayers
				modeRulesetVersion = mode.ModeRulesetVersion
			}
		}
		if modeConfigHashValue == "" {
			modeConfigHashValue = modeConfigHash(ticket.ModeID)
		}
	}
	return LobbyRoomAuditRecord{
		RoomCode:            roomCode,
		Action:              action,
		ModeID:              modeID,
		UserID:              userID,
		TicketID:            ticketID,
		MatchID:             matchID,
		RoomStatus:          roomStatus,
		HostUserID:          hostUserID,
		CurrentPlayers:      currentPlayers,
		RequiredPlayers:     requiredPlayers,
		StageID:             stageID,
		RulesetVersion:      RulesetVersion,
		ModeRulesetVersion:  modeRulesetVersion,
		ModeConfigHash:      modeConfigHashValue,
		DeckSnapshotHash:    deckHash,
		CreatedAt:           createdAt,
		ServerAuthoritative: true,
	}
}

func (s *Service) recordLobbyRoomAuditRecordLocked(record LobbyRoomAuditRecord) {
	if s.lobbyAuditRepo == nil || record.RoomCode == "" || record.Action == "" {
		return
	}
	err := s.lobbyAuditRepo.RecordLobbyRoomAudit(record)
	fingerprint := lifecycleFingerprint("lobby:room", record.Action, record.RoomCode, record.UserID, record.TicketID, record.MatchID, record.RoomStatus, record.DeckSnapshotHash)
	s.recordLobbyAuditOutcomeLocked(record.Action, fingerprint, record.CreatedAt, err)
}

func (s *Service) recordLobbyMessageAuditLocked(message LobbyMessage) {
	if s.lobbyAuditRepo == nil || message.MessageID == "" {
		return
	}
	metadataHash := ""
	if len(message.Metadata) > 0 {
		if encoded, err := json.Marshal(message.Metadata); err == nil {
			metadataHash = "sha256:" + shortHash(string(encoded))
		}
	}
	err := s.lobbyAuditRepo.RecordLobbyMessageAudit(LobbyMessageAuditRecord{
		MessageID:           message.MessageID,
		RoomCode:            message.RoomCode,
		ModeID:              message.ModeID,
		Kind:                message.Kind,
		UserID:              message.UserID,
		Duplicate:           message.Duplicate,
		TextLength:          len(message.Text),
		MetadataHash:        metadataHash,
		CreatedAt:           message.CreatedAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("lobby:message", message.MessageID, message.RoomCode, message.UserID, message.Kind, fmt.Sprintf("%t", message.Duplicate), metadataHash)
	s.recordLobbyAuditOutcomeLocked("message", fingerprint, message.CreatedAt, err)
}

func (s *Service) recordLobbyHeartbeatAuditLocked(userID string, ticket *queueTicket, match *matchState, occurredAt time.Time) {
	if ticket == nil && match != nil {
		if ticketID := s.ticketIDForMatchUserLocked(match.MatchID, userID); ticketID != "" {
			ticket = s.tickets[ticketID]
		}
	}
	if ticket == nil || ticket.RoomCode == "" {
		return
	}
	room := s.rooms[normalizeRoomCode(ticket.RoomCode)]
	record := s.lobbyRoomAuditRecordLocked(room, ticket, userID, "heartbeat", occurredAt)
	record.CurrentPlayers = s.ticketDepthLocked(ticket)
	if match != nil {
		record.MatchID = match.MatchID
		record.CurrentPlayers = connectedPlayerCount(match)
	}
	s.recordLobbyRoomAuditRecordLocked(record)
}

func (s *Service) recordLobbyAuditOutcomeLocked(operation string, fingerprint string, occurredAt time.Time, err error) {
	s.lobbyAuditStatus.Configured = s.lobbyAuditRepo != nil
	s.lobbyAuditStatus.ServerAuthoritative = true
	if err != nil {
		s.lobbyAuditStatus.OK = false
		s.lobbyAuditStatus.RejectedRecords++
		s.lobbyAuditStatus.LastErrorOperation = operation
		s.lobbyAuditStatus.LastError = err.Error()
		s.lobbyAuditStatus.LastErrorAt = s.clock()
		return
	}
	switch operation {
	case "listed", "snapshot_read", "ticket_read":
		s.lobbyAuditStatus.RoomReadRecords++
	case "rules_read":
		s.lobbyAuditStatus.RulesReadRecords++
	case "message":
		s.lobbyAuditStatus.MessageRecords++
	case "create_retry", "join_retry":
		s.lobbyAuditStatus.RoomReadRecords++
	case "ready":
		s.lobbyAuditStatus.ReadyRecords++
	case "disconnected", "reconnected", "heartbeat":
		s.lobbyAuditStatus.ConnectionRecords++
	default:
		s.lobbyAuditStatus.RoomRecords++
	}
	s.lobbyAuditStatus.LastSuccessOperation = operation
	s.lobbyAuditStatus.LastSuccessFingerprint = fingerprint
	if occurredAt.IsZero() {
		occurredAt = s.clock()
	}
	s.lobbyAuditStatus.LastSuccessAt = occurredAt
	if s.lobbyAuditStatus.Configured {
		s.lobbyAuditStatus.OK = s.lobbyAuditStatus.LastError == ""
	}
}

func (s *Service) recordBattleResultAuditLocked(match *matchState, allocation *BattleServerAllocation, signed SignedBattleResult, verifiedAt time.Time) {
	if s.battleAuditRepo == nil || match == nil {
		return
	}
	battleServerID := signed.KeyID
	if allocation != nil && allocation.BattleServerID != "" {
		battleServerID = allocation.BattleServerID
	}
	err := s.battleAuditRepo.RecordBattleResultAudit(BattleResultAuditRecord{
		MatchID:             match.MatchID,
		ModeID:              match.ModeID,
		BattleServerID:      battleServerID,
		ResultHash:          signed.Result.ResultHash,
		ReplayID:            signed.Result.ReplayID,
		KeyID:               signed.KeyID,
		PlayerIDs:           copyStringSlice(signed.Result.PlayerIDs),
		SettlementKey:       battleResultSettlementKey(match.MatchID),
		Status:              "accepted",
		VerifiedAt:          verifiedAt,
		SettledAt:           match.BattleResultAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:result", match.MatchID, match.ModeID, battleServerID, signed.Result.ResultHash, signed.Result.ReplayID, fmt.Sprintf("%d", signed.Result.SettledAtMS))
	s.recordBattleAuditOutcomeLocked("battle_result", fingerprint, verifiedAt, err)
}

func (s *Service) recordBattleResultDuplicateAuditLocked(match *matchState, allocation *BattleServerAllocation, signed SignedBattleResult, verifiedAt time.Time) {
	if s.battleAuditRepo == nil || match == nil {
		return
	}
	battleServerID := signed.KeyID
	if allocation != nil && allocation.BattleServerID != "" {
		battleServerID = allocation.BattleServerID
	}
	err := s.battleAuditRepo.RecordBattleResultAudit(BattleResultAuditRecord{
		MatchID:             match.MatchID,
		ModeID:              match.ModeID,
		BattleServerID:      battleServerID,
		ResultHash:          signed.Result.ResultHash,
		ReplayID:            signed.Result.ReplayID,
		KeyID:               signed.KeyID,
		PlayerIDs:           copyStringSlice(signed.Result.PlayerIDs),
		SettlementKey:       battleResultSettlementKey(match.MatchID),
		Status:              "duplicate",
		VerifiedAt:          verifiedAt,
		SettledAt:           match.BattleResultAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:result:duplicate", match.MatchID, match.ModeID, battleServerID, signed.Result.ResultHash, signed.Result.ReplayID, fmt.Sprintf("%d", signed.Result.SettledAtMS))
	s.recordBattleAuditOutcomeLocked("battle_result_duplicate", fingerprint, verifiedAt, err)
}

func (s *Service) recordReplayAuditLocked(replay *ReplayRecord) {
	if s.battleAuditRepo == nil || replay == nil || replay.ReplayID == "" {
		return
	}
	err := s.battleAuditRepo.RecordReplayAudit(ReplayAuditRecord{
		ReplayID:            replay.ReplayID,
		MatchID:             replay.MatchID,
		UserID:              replay.UserID,
		ModeID:              replay.ModeID,
		RulesetVersion:      replay.RulesetVersion,
		ModeRulesetVersion:  replay.ModeRulesetVersion,
		StateHash:           replay.StateHash,
		InputCount:          replay.InputCount,
		EventCount:          replay.EventCount,
		SettlementKey:       replay.Settlement.SettlementKey,
		SettledAt:           replay.SettledAt,
		ServerAuthoritative: true,
	})
	fingerprint := lifecycleFingerprint("battle:replay", replay.ReplayID, replay.MatchID, replay.UserID, replay.StateHash, replay.Settlement.SettlementKey)
	s.recordBattleAuditOutcomeLocked("replay", fingerprint, replay.SettledAt, err)
}

func (s *Service) recordBattleAuditOutcomeLocked(operation string, fingerprint string, occurredAt time.Time, err error) {
	s.battleAuditStatus.Configured = s.battleAuditRepo != nil
	s.battleAuditStatus.ServerAuthoritative = true
	if err != nil {
		s.battleAuditStatus.OK = false
		s.battleAuditStatus.RejectedRecords++
		s.battleAuditStatus.LastErrorOperation = operation
		s.battleAuditStatus.LastError = err.Error()
		s.battleAuditStatus.LastErrorAt = s.clock()
		return
	}
	switch operation {
	case "server_registered", "server_heartbeat", "server_offline":
		s.battleAuditStatus.ServerLifecycleRecords++
	case "match_allocation":
		s.battleAuditStatus.AllocationRecords++
	case "battle_ticket":
		s.battleAuditStatus.TicketRecords++
	case "battle_ticket_expired":
		s.battleAuditStatus.TicketExpiredRecords++
	case "battle_ticket_consumed":
		s.battleAuditStatus.TicketConsumedRecords++
	case "battle_result":
		s.battleAuditStatus.ResultRecords++
	case "battle_result_duplicate":
		s.battleAuditStatus.ResultDuplicateRecords++
	case "replay":
		s.battleAuditStatus.ReplayRecords++
	}
	s.battleAuditStatus.LastSuccessOperation = operation
	s.battleAuditStatus.LastSuccessFingerprint = fingerprint
	if occurredAt.IsZero() {
		occurredAt = s.clock()
	}
	s.battleAuditStatus.LastSuccessAt = occurredAt
	if s.battleAuditStatus.Configured {
		s.battleAuditStatus.OK = s.battleAuditStatus.LastError == ""
	}
}

func validateSignedBattleResultShape(signed SignedBattleResult, now time.Time) error {
	result := signed.Result
	if !versionStampCompatible(result.Version) {
		return newError(codeInvalidRequest, "battle result version is incompatible")
	}
	if strings.TrimSpace(result.MatchID) == "" || strings.TrimSpace(result.ModeID) == "" {
		return newError(codeInvalidRequest, "battle result match_id and mode_id are required")
	}
	if !looksLikeSha256Ref(result.ResultHash) {
		return newError(codeInvalidRequest, "battle result hash is invalid")
	}
	if strings.TrimSpace(result.ReplayID) == "" {
		return newError(codeInvalidRequest, "battle result replay_id is required")
	}
	if len(result.PlayerIDs) == 0 {
		return newError(codeInvalidRequest, "battle result player_ids are required")
	}
	if result.SettledAtMS <= 0 {
		return newError(codeInvalidRequest, "battle result settled_at_ms is required")
	}
	settledAt := time.UnixMilli(result.SettledAtMS)
	if settledAt.After(now.Add(5 * time.Minute)) {
		return newError(codeInvalidRequest, "battle result settled_at_ms is in the future")
	}
	if signed.SignatureAlg != "ED25519" {
		return newError(codeInvalidRequest, "battle result signature_alg is unsupported")
	}
	if strings.TrimSpace(signed.KeyID) == "" || strings.TrimSpace(signed.SignatureHex) == "" {
		return newError(codeInvalidRequest, "battle result signature fields are required")
	}
	if len(signed.SignatureHex) != 128 || !isHexString(signed.SignatureHex) {
		return newError(codeInvalidRequest, "battle result signature shape is invalid")
	}
	if signed.PublicKeyHex != "" && (len(signed.PublicKeyHex) != 64 || !isHexString(signed.PublicKeyHex)) {
		return newError(codeInvalidRequest, "battle result public key shape is invalid")
	}
	if !signed.ServerAuthoritative {
		return newError(codeInvalidRequest, "battle result must be server authoritative")
	}
	if strings.TrimSpace(result.RewardProjectionJSON) != "" && !json.Valid([]byte(result.RewardProjectionJSON)) {
		return newError(codeInvalidRequest, "battle result reward projection json is invalid")
	}
	if strings.TrimSpace(result.ModeResultJSON) != "" && !json.Valid([]byte(result.ModeResultJSON)) {
		return newError(codeInvalidRequest, "battle result mode result json is invalid")
	}
	return nil
}

func allocationPlayerIDs(allocation *BattleServerAllocation) []string {
	if allocation == nil {
		return []string{}
	}
	out := make([]string, 0, len(allocation.Players))
	for _, player := range allocation.Players {
		out = append(out, player.PlayerID)
	}
	return out
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[string]int{}
	for _, value := range left {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		seen[value]++
	}
	for _, value := range right {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func looksLikeSha256Ref(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	return len(digest) >= 3 && isHexString(digest)
}

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func currentVersionStamp() VersionStamp {
	return VersionStamp{
		ProtocolVersion:    ProtocolVersion,
		BusinessAPIVersion: BusinessAPIVersion,
		BattleAPIVersion:   BattleAPIVersion,
		RulesetVersion:     RulesetVersion,
	}
}

func versionStampCompatible(version VersionStamp) bool {
	return version.ProtocolVersion == ProtocolVersion &&
		version.BattleAPIVersion == BattleAPIVersion
}

func playerIDForUser(matchID string, userID string) string {
	return "p-" + shortHash(matchID+":"+userID)
}

func deckSnapshotHash(deck DeckSnapshot) string {
	payload, _ := json.Marshal(deck)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func modeConfigHash(modeID string) string {
	mode := ModeConfigs[modeID]
	payload, _ := json.Marshal(mode)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func seedHex(seed int64) string {
	return fmt.Sprintf("%016x", uint64(seed))
}

func battleTicketCacheKey(matchID string, userID string) string {
	return matchID + ":" + userID
}

func businessSessionRef(sessionToken string) string {
	return "session-ref:" + shortHash(sessionToken)
}

func modeConfigKeys() []string {
	keys := make([]string, 0, len(ModeConfigs))
	for key := range ModeConfigs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func deckContainsCard(deck DeckSnapshot, cardID string) bool {
	for _, id := range deck.CardIDs {
		if strings.TrimSpace(id) == cardID {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func removeString(values []string, needle string) []string {
	out := values[:0]
	for _, value := range values {
		if value != needle {
			out = append(out, value)
		}
	}
	return out
}

func directionFromBits(bits int) (float64, float64) {
	dx, dy := 0.0, 0.0
	if bits&1 != 0 {
		dx -= 1
	}
	if bits&2 != 0 {
		dx += 1
	}
	if bits&4 != 0 {
		dy -= 1
	}
	if bits&8 != 0 {
		dy += 1
	}
	length := math.Sqrt(dx*dx + dy*dy)
	if length > 1 {
		return dx / length, dy / length
	}
	return dx, dy
}

func stateHash(snapshot Snapshot) string {
	payload, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func seedFrom(matchID string) int64 {
	sum := sha256.Sum256([]byte("gensoulkyo:" + matchID))
	var seed int64
	for i := 0; i < 8; i++ {
		seed = (seed << 8) | int64(sum[i])
	}
	if seed < 0 {
		seed = -seed
	}
	return seed
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func lifecycleFingerprint(parts ...string) string {
	return "sha256:" + shortHash(strings.Join(parts, "\x1f"))
}

func prefixString(value string, length int) string {
	if length <= 0 || len(value) <= length {
		return value
	}
	return value[:length]
}

func settlementKey(matchID string, userID string) string {
	return matchID + ":" + userID
}

func battleResultSettlementKey(matchID string) string {
	return "battle-result:" + matchID
}

func activityClaimKey(kind string, claimID string, userID string) string {
	return kind + ":" + claimID + ":" + userID
}

func percentileFor(result string) float64 {
	switch result {
	case "win":
		return 0.25
	case "draw":
		return 0.40
	default:
		return 0.65
	}
}

func asInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		if math.Trunc(v) == v {
			return int(v), true
		}
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func intFromAnyValue(value any) int {
	parsed, ok := asInt(value)
	if !ok {
		return 0
	}
	return parsed
}

func floatFromAnyValue(value any, fallback float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func asBool(value any) bool {
	v, _ := value.(bool)
	return v
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(value)
	}
}

func copyIntMap(source map[string]int) map[string]int {
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyTasks(source map[string]TaskState) map[string]TaskState {
	out := make(map[string]TaskState, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyEvents(source map[string]EventState) map[string]EventState {
	out := make(map[string]EventState, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyLeaderboards(source map[string]LeaderboardRow) map[string]LeaderboardRow {
	out := make(map[string]LeaderboardRow, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyCertificationProfile(source CertificationProfile) CertificationProfile {
	return normalizedCertificationProfile(source)
}

func copyAnyMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyLobbyMessage(source LobbyMessage) LobbyMessage {
	out := source
	out.Metadata = copyAnyMap(source.Metadata)
	return out
}

func copyLobbyMessages(source []LobbyMessage) []LobbyMessage {
	out := make([]LobbyMessage, len(source))
	for index, message := range source {
		out[index] = copyLobbyMessage(message)
	}
	return out
}

func copyStringSlice(source []string) []string {
	out := make([]string, len(source))
	copy(out, source)
	return out
}

func copyDeckSnapshot(source DeckSnapshot) DeckSnapshot {
	out := source
	out.CardIDs = append([]string{}, source.CardIDs...)
	return out
}

func copyBattleAllocation(source *BattleServerAllocation) BattleServerAllocation {
	if source == nil {
		return BattleServerAllocation{}
	}
	out := *source
	out.Players = append([]BattleAllocationPlayer{}, source.Players...)
	return out
}

func copySignedBattleTicket(source *SignedBattleTicket) SignedBattleTicket {
	if source == nil {
		return SignedBattleTicket{}
	}
	out := *source
	return out
}

func copyDeckRecord(source DeckRecord) DeckRecord {
	out := source
	out.CardIDs = copyStringSlice(source.CardIDs)
	return out
}

func copyChestPity(source map[string]ChestPityState) map[string]ChestPityState {
	out := make(map[string]ChestPityState, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyChestOpenResults(source []ChestOpenResult) []ChestOpenResult {
	out := make([]ChestOpenResult, len(source))
	copy(out, source)
	return out
}

func copyChestOpenings(source []ChestOpeningRecord) []ChestOpeningRecord {
	out := make([]ChestOpeningRecord, len(source))
	for index, record := range source {
		out[index] = record
		out[index].Cost = copyIntMap(record.Cost)
		out[index].Results = copyChestOpenResults(record.Results)
	}
	return out
}

func inventoryItems(source map[string]CardInventoryEntry) []CardInventoryEntry {
	keys := make([]string, 0, len(source))
	for cardID := range source {
		keys = append(keys, cardID)
	}
	sort.Strings(keys)
	out := make([]CardInventoryEntry, 0, len(keys))
	for _, cardID := range keys {
		out = append(out, source[cardID])
	}
	return out
}

func deckRecords(source map[string]DeckRecord) []DeckRecord {
	keys := make([]string, 0, len(source))
	for deckID := range source {
		keys = append(keys, deckID)
	}
	sort.Strings(keys)
	out := make([]DeckRecord, 0, len(keys))
	for _, deckID := range keys {
		out = append(out, copyDeckRecord(source[deckID]))
	}
	return out
}

func copyReplayRecord(source *ReplayRecord) ReplayRecord {
	if source == nil {
		return ReplayRecord{}
	}
	out := *source
	out.FinalResult = copyAnyMap(source.FinalResult)
	out.ModeResult = copyAnyMap(source.ModeResult)
	out.Events = copyMatchEvents(source.Events)
	out.Settlement = copyMatchEndEvent(&source.Settlement)
	return out
}

func copyMatchEndEvent(source *MatchEndEvent) MatchEndEvent {
	if source == nil {
		return MatchEndEvent{}
	}
	out := *source
	out.FinalResult = copyAnyMap(source.FinalResult)
	out.ModeResult = copyAnyMap(source.ModeResult)
	out.RewardJSON = append([]RewardItem{}, source.RewardJSON...)
	out.TaskProgress = append([]TaskProgress{}, source.TaskProgress...)
	out.EventPoints = copyIntMap(source.EventPoints)
	out.LeaderboardUpdates = append([]LeaderboardRow{}, source.LeaderboardUpdates...)
	return out
}

func anyMapFrom(value any) map[string]any {
	if source, ok := value.(map[string]any); ok {
		return copyAnyMap(source)
	}
	return map[string]any{}
}

func anySliceFrom(value any) []any {
	if source, ok := value.([]any); ok {
		out := make([]any, len(source))
		copy(out, source)
		return out
	}
	return []any{}
}

func clamp(value float64, minValue float64, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}

func clampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func round(value float64, precision int) float64 {
	scale := math.Pow10(precision)
	return math.Round(value*scale) / scale
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func normalizedCursor(cursor int) int {
	if cursor < 0 {
		return 0
	}
	return cursor
}

func eventCursorBounds(match *matchState) (int, int) {
	if match == nil || len(match.EventLog) == 0 {
		return 0, 0
	}
	return match.EventLog[0].Seq, match.EventLog[len(match.EventLog)-1].Seq
}

func connectedPlayerCount(match *matchState) int {
	if match == nil {
		return 0
	}
	count := 0
	for _, player := range match.Players {
		if player != nil && player.Connected {
			count++
		}
	}
	return count
}
