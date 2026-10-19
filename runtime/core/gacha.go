package core

// gacha.go implements the dual gacha pools ("totem draws"): gacha.cards rolls
// the 20-card catalog and gacha.characters rolls the 52 player shot types.
//
// Design reference: Mahjong Soul's 寻觅 system (design doc:
// /root/gotouhou/task-queue/out/T10_gacha_design.md):
//   - weighted rarity roll + per-pool soft pity (rare/epic counters),
//   - a ten-pull rare-plus guarantee (雀魂「十连保底紫礼物」),
//   - an inheritable "faith" counter, 1 per pull, shared across both pools,
//     that exchanges any locked shot at 150 faith (雀魂「信仰井」),
//   - duplicate conversion: cards -> card_dust, shots -> faith.
//
// The client only mirrors the pool tables for rendering; every result is
// rolled server-side under Service.mu (same rule as the chest pools).

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	gachaCardPoolID      = "gacha.cards"
	gachaCharacterPoolID = "gacha.characters"

	// 雀魂 prices 辉玉 at 200 per single and 1800 per ten-pull; we mirror the
	// same numbers in gems so the economy reads the same way.
	gachaSingleCostGems = 200
	gachaTenCostGems    = 1800
	gachaTenPullCount   = 10

	// 雀魂 role well: 150 faith exchanges one character. Shots cost the same.
	gachaFaithExchangeCost = 150

	// Wallet key for the inheritable faith counter (1 per pull, shared across
	// both pools, Mahjong-Soul style).
	gachaFaithCurrency = "faith"

	// Per-user audit trail cap (oldest openings are dropped first).
	maxGachaOpeningRecords = 50
)

// gachaCharacterEntry is one rollable player shot. Rarity is bound to the
// shot form: A1 = common, A2 = uncommon, B1 = rare, B2 = epic (13 x 4 = 52).
type gachaCharacterEntry struct {
	ShotID string
	Chara  string
	Rarity string
}

// gachaCharacterTable is the authoritative character-pool roster, ordered by
// the same character order as allowedShotTypes' documentation block
// (Reimu, Marisa, Sanae, Sakuya, Reisen, Youmu, Aya, Seija, Cirno, Yukari,
// Alice, Remillia, Yuyuko), each with A1/A2/B1/B2. gacha_test.go asserts the
// set equals allowedShotTypes exactly, so the two cannot drift apart.
var gachaCharacterTable = []gachaCharacterEntry{
	{"ReimuA1", "Reimu", "common"}, {"ReimuA2", "Reimu", "uncommon"}, {"ReimuB1", "Reimu", "rare"}, {"ReimuB2", "Reimu", "epic"},
	{"MarisaA1", "Marisa", "common"}, {"MarisaA2", "Marisa", "uncommon"}, {"MarisaB1", "Marisa", "rare"}, {"MarisaB2", "Marisa", "epic"},
	{"SanaeA1", "Sanae", "common"}, {"SanaeA2", "Sanae", "uncommon"}, {"SanaeB1", "Sanae", "rare"}, {"SanaeB2", "Sanae", "epic"},
	{"SakuyaA1", "Sakuya", "common"}, {"SakuyaA2", "Sakuya", "uncommon"}, {"SakuyaB1", "Sakuya", "rare"}, {"SakuyaB2", "Sakuya", "epic"},
	{"ReisenA1", "Reisen", "common"}, {"ReisenA2", "Reisen", "uncommon"}, {"ReisenB1", "Reisen", "rare"}, {"ReisenB2", "Reisen", "epic"},
	{"YoumuA1", "Youmu", "common"}, {"YoumuA2", "Youmu", "uncommon"}, {"YoumuB1", "Youmu", "rare"}, {"YoumuB2", "Youmu", "epic"},
	{"AyaA1", "Aya", "common"}, {"AyaA2", "Aya", "uncommon"}, {"AyaB1", "Aya", "rare"}, {"AyaB2", "Aya", "epic"},
	{"SeijaA1", "Seija", "common"}, {"SeijaA2", "Seija", "uncommon"}, {"SeijaB1", "Seija", "rare"}, {"SeijaB2", "Seija", "epic"},
	{"CirnoA1", "Cirno", "common"}, {"CirnoA2", "Cirno", "uncommon"}, {"CirnoB1", "Cirno", "rare"}, {"CirnoB2", "Cirno", "epic"},
	{"YukariA1", "Yukari", "common"}, {"YukariA2", "Yukari", "uncommon"}, {"YukariB1", "Yukari", "rare"}, {"YukariB2", "Yukari", "epic"},
	{"AliceA1", "Alice", "common"}, {"AliceA2", "Alice", "uncommon"}, {"AliceB1", "Alice", "rare"}, {"AliceB2", "Alice", "epic"},
	{"RemilliaA1", "Remillia", "common"}, {"RemilliaA2", "Remillia", "uncommon"}, {"RemilliaB1", "Remillia", "rare"}, {"RemilliaB2", "Remillia", "epic"},
	{"YuyukoA1", "Yuyuko", "common"}, {"YuyukoA2", "Yuyuko", "uncommon"}, {"YuyukoB1", "Yuyuko", "rare"}, {"YuyukoB2", "Yuyuko", "epic"},
}

// defaultGachaShotIDs is the starter kit every identity receives: 博丽灵梦 A
// 型 (both A-shot variants). Everything else must be drawn or exchanged.
var defaultGachaShotIDs = []string{"ReimuA1", "ReimuA2"}

// GachaPool is one server-owned gacha pool. Weights are rarity weights; the
// client renders them but never rolls.
type GachaPool struct {
	PoolID      string         `json:"pool_id"`
	Kind        string         `json:"kind"` // "card" | "character"
	SeasonID    string         `json:"season_id"`
	Name        string         `json:"name"`
	NameKey     string         `json:"name_key"`
	CostGems    int            `json:"cost_gems"`     // single pull
	CostTenGems int            `json:"cost_ten_gems"` // ten-pull
	Weights     map[string]int `json:"weights"`
	Pity        ChestPityRules `json:"pity"`
	Featured    []string       `json:"featured,omitempty"` // reserved: limited UP lists (v1 unused)
	StartsAt    string         `json:"starts_at"`
	EndsAt      string         `json:"ends_at,omitempty"`
	Enabled     bool           `json:"enabled"`
}

// GachaPullRequest selects a pool and a pull size. Count must be 1 or 10.
type GachaPullRequest struct {
	PoolID string `json:"pool_id"`
	Count  int    `json:"count"`
}

// GachaPullResult is one draw. CardID or ShotID is set depending on the pool;
// IsNew is false for duplicates, which converted to Dust (cards) or
// FaithGained (characters).
type GachaPullResult struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // "card" | "character"
	CardID       string `json:"card_id,omitempty"`
	ShotID       string `json:"shot_id,omitempty"`
	Chara        string `json:"chara,omitempty"`
	NameKey      string `json:"name_key"`
	Rarity       string `json:"rarity"`
	IsNew        bool   `json:"is_new"`
	Dust         int    `json:"dust"`
	FaithGained  int    `json:"faith_gained"`
}

// GachaOpeningRecord is the per-user audit trail entry (same shape contract as
// ChestOpeningRecord): pool, count, cost, the server seed and every result.
type GachaOpeningRecord struct {
	OpeningID  string           `json:"opening_id"`
	UserID     string           `json:"user_id"`
	PoolID     string           `json:"pool_id"`
	Count      int              `json:"count"`
	CostGems   int              `json:"cost_gems"`
	ServerSeed string           `json:"server_seed"`
	Results    []GachaPullResult `json:"results"`
	OpenedAt   time.Time        `json:"opened_at"`
}

// CharacterOwnership records one unlocked shot. Source is "default",
// "gacha" or "faith_exchange"; Dupes counts duplicate conversions.
type CharacterOwnership struct {
	ShotID     string    `json:"shot_id"`
	Chara      string    `json:"chara"`
	Source     string    `json:"source"`
	Dupes      int       `json:"dupes"`
	ObtainedAt time.Time `json:"obtained_at"`
}

// CharacterRosterSnapshot is the server-owned projection of what the identity
// currently owns: shots, derived bombs, faith and per-pool pity counters.
type CharacterRosterSnapshot struct {
	OK                bool                        `json:"ok"`
	UserID            string                      `json:"user_id"`
	Shots             []CharacterOwnership        `json:"shots"`
	Bombs             []string                    `json:"bombs"`
	Faith             int                         `json:"faith"`
	PityCounters      map[string]ChestPityState   `json:"pity_counters"`
	LockedShotIDs     []string                    `json:"locked_shot_ids"`
	ExchangeCostFaith int                         `json:"exchange_cost_faith"`
	ServerAuthoritative bool                      `json:"server_authoritative"`
	ServerTime        time.Time                   `json:"server_time"`
}

// GachaPoolsResponse lists both pools plus the wallet and pity state.
type GachaPoolsResponse struct {
	OK                  bool                      `json:"ok"`
	Pools               []GachaPool               `json:"pools"`
	Wallet              map[string]int            `json:"wallet"`
	Faith               int                       `json:"faith"`
	PityCounters        map[string]ChestPityState `json:"pity_counters"`
	ServerAuthoritative bool                      `json:"server_authoritative"`
	ServerTime          time.Time                 `json:"server_time"`
}

// GachaPullResponse returns the draws plus the resulting wallet, inventory,
// roster, pity counters and the audit record of the opening.
type GachaPullResponse struct {
	OK                   bool                      `json:"ok"`
	PoolID               string                    `json:"pool_id"`
	Count                int                       `json:"count"`
	Results              []GachaPullResult         `json:"results"`
	Wallet               map[string]int            `json:"wallet"`
	Faith                int                       `json:"faith"`
	Inventory            InventorySnapshot         `json:"inventory"`
	Characters           CharacterRosterSnapshot   `json:"characters"`
	PityCounters         map[string]ChestPityState `json:"pity_counters"`
	Audit                GachaOpeningRecord        `json:"audit"`
	ServerAuthoritative  bool                      `json:"server_authoritative"`
	ServerTime           time.Time                 `json:"server_time"`
}

// FaithExchangeRequest unlocks one locked shot with faith.
type FaithExchangeRequest struct {
	ItemID string `json:"item_id"`
}

// FaithExchangeResponse confirms the unlock and returns the new roster.
type FaithExchangeResponse struct {
	OK                   bool                    `json:"ok"`
	ItemID               string                  `json:"item_id"`
	Faith                int                     `json:"faith"`
	FaithSpent           int                     `json:"faith_spent"`
	Characters           CharacterRosterSnapshot `json:"characters"`
	Wallet               map[string]int          `json:"wallet"`
	ServerAuthoritative  bool                    `json:"server_authoritative"`
	ServerTime           time.Time               `json:"server_time"`
}

// gachaPools is the authoritative pool table. Card weights and pity mirror
// the existing chest pool so the two economies stay on one scale; the
// character pool runs hotter because 13 entries share every rarity tier.
func gachaPools() []GachaPool {
	return []GachaPool{
		{
			PoolID:      gachaCardPoolID,
			Kind:        "card",
			SeasonID:    "local_s0",
			Name:        "Card Totem",
			NameKey:     "screen.gacha.cards",
			CostGems:    gachaSingleCostGems,
			CostTenGems: gachaTenCostGems,
			Weights:     map[string]int{"common": 70, "uncommon": 20, "rare": 8, "epic": 2},
			Pity:        ChestPityRules{RareEvery: 10, EpicEvery: 60, Inherit: false},
			StartsAt:    "2026-01-01T00:00:00Z",
			Enabled:     true,
		},
		{
			PoolID:      gachaCharacterPoolID,
			Kind:        "character",
			SeasonID:    "local_s0",
			Name:        "Character Totem",
			NameKey:     "screen.gacha.characters",
			CostGems:    gachaSingleCostGems,
			CostTenGems: gachaTenCostGems,
			Weights:     map[string]int{"common": 50, "uncommon": 30, "rare": 14, "epic": 6},
			Pity:        ChestPityRules{RareEvery: 10, EpicEvery: 30, Inherit: false},
			StartsAt:    "2026-01-01T00:00:00Z",
			Enabled:     true,
		},
	}
}

func gachaPoolByID(poolID string) (GachaPool, bool) {
	for _, pool := range gachaPools() {
		if pool.PoolID == poolID {
			return pool, true
		}
	}
	return GachaPool{}, false
}

func gachaCharacterEntries() []gachaCharacterEntry {
	out := make([]gachaCharacterEntry, len(gachaCharacterTable))
	copy(out, gachaCharacterTable)
	return out
}

func gachaCharacterEntryByShot(shotID string) (gachaCharacterEntry, bool) {
	for _, entry := range gachaCharacterTable {
		if entry.ShotID == shotID {
			return entry, true
		}
	}
	return gachaCharacterEntry{}, false
}

// gachaOpeningSeed derives the per-opening seed. Same string shape as the
// chest seed family so auditors read one convention.
func gachaOpeningSeed(userID, poolID, openingID string, openingCount, count int) string {
	return shortHash(fmt.Sprintf("gacha:%s:%s:%s:%d:%d", userID, poolID, openingID, openingCount, count))
}

// rollGachaRarity mirrors rollChestRarity over the pool weights.
func rollGachaRarity(weights map[string]int, seed string, index int) string {
	total := 0
	for _, value := range weights {
		if value > 0 {
			total += value
		}
	}
	if total <= 0 {
		return "common"
	}
	roll := int(deterministicUnit(int64(index+1), index+1, "gacha:"+seed, 0) * float64(total))
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

// applyGachaPity mirrors applyChestPity: soft pity per pool id, counters reset
// once the tier is hit.
func applyGachaPity(counters map[string]ChestPityState, pool GachaPool, rolledRarity string) string {
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

// pickGachaCardForRarity reuses the chest picker: uniform over the rarity
// class inside the shared card catalog.
func pickGachaCardForRarity(rarity, seed string, index int) string {
	return pickChestCardForRarity(rarity, seed, index)
}

// pickGachaShotForRarity picks uniformly among the 13 shots of the rolled
// rarity class. ok is false only for a malformed catalog (tests cover it).
func pickGachaShotForRarity(rarity, seed string, index int) (gachaCharacterEntry, bool) {
	candidates := []gachaCharacterEntry{}
	for _, entry := range gachaCharacterTable {
		if entry.Rarity == rarity {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) == 0 {
		return gachaCharacterEntry{}, false
	}
	offset := int(deterministicUnit(int64(index+17), index+3, "gacha_shot:"+seed+":"+rarity, 0) * float64(len(candidates)))
	if offset >= len(candidates) {
		offset = len(candidates) - 1
	}
	return candidates[offset], true
}

// gachaGuaranteedRarity forces a rare-or-better roll on the tenth slot of a
// ten-pull (雀魂「十连保底一个紫礼物」). Deterministic: attempts advance the
// stream id until the weight table yields rare+.
func gachaGuaranteedRarity(weights map[string]int, seed string, index int) string {
	for attempt := 0; attempt < 200; attempt++ {
		rarity := rollGachaRarity(weights, fmt.Sprintf("%s#guarantee%d", seed, attempt), index)
		if rarityRank(rarity) >= rarityRank("rare") {
			return rarity
		}
	}
	return "rare"
}

// characterDupeFaith is the faith a duplicate shot converts to, scaled by
// rarity (雀魂 duplicate -> 星钥, our shots convert into the well).
func characterDupeFaith(rarity string) int {
	switch rarity {
	case "epic":
		return 80
	case "rare":
		return 40
	case "uncommon":
		return 20
	default:
		return 10
	}
}

// initGachaStateLocked wires the gacha fields of a freshly built userState.
// Called by every construction site (anonymous, external, account bridge).
func initGachaStateLocked(user *userState, now time.Time) {
	if user == nil {
		return
	}
	if user.Characters == nil {
		user.Characters = map[string]CharacterOwnership{}
	}
	if user.GachaPity == nil {
		user.GachaPity = map[string]ChestPityState{}
	}
	// GachaOpenings/GachaLastResults stay nil-safe: append grows nil slices
	// and resetting them here would wipe the audit trail on every pull.
	ensureDefaultCharactersLocked(user, now)
}

// ensureDefaultCharactersLocked grants the starter kit (博丽灵梦 A) once.
// It is also the lazy migration for identities that predate the gacha state
// (sessions restored without the new maps).
func ensureDefaultCharactersLocked(user *userState, now time.Time) {
	if user == nil {
		return
	}
	if user.Characters == nil {
		user.Characters = map[string]CharacterOwnership{}
	}
	for _, shotID := range defaultGachaShotIDs {
		if _, owned := user.Characters[shotID]; owned {
			continue
		}
		entry, ok := gachaCharacterEntryByShot(shotID)
		if !ok {
			continue
		}
		user.Characters[shotID] = CharacterOwnership{
			ShotID:     shotID,
			Chara:      entry.Chara,
			Source:     "default",
			ObtainedAt: now,
		}
	}
}

func gachaFaithValue(wallet map[string]int) int {
	if wallet == nil {
		return 0
	}
	return wallet[gachaFaithCurrency]
}

func ownsShotLocked(user *userState, shotID string) bool {
	if user == nil || user.Characters == nil {
		return false
	}
	_, owned := user.Characters[shotID]
	return owned
}

// ownsBombLocked reports whether the character behind a bomb is unlocked:
// owning any shot of that character unlocks its bomb.
func ownsBombLocked(user *userState, bombID string) bool {
	if user == nil || user.Characters == nil {
		return false
	}
	bombID = strings.TrimSpace(bombID)
	if bombID == "" {
		return true
	}
	for shotID := range user.Characters {
		if entry, ok := gachaCharacterEntryByShot(shotID); ok && entry.Chara == bombID {
			return true
		}
	}
	return false
}

// validateLoadoutOwnershipLocked enforces the gacha roster on a validated
// loadout: a submitted shot must be owned and a submitted bomb needs at least
// one owned shot of that character. Empty values stay legal (default 自机).
func validateLoadoutOwnershipLocked(user *userState, loadout PlayerLoadout, now time.Time) error {
	ensureDefaultCharactersLocked(user, now)
	if shot := strings.TrimSpace(loadout.ShotType); shot != "" && !ownsShotLocked(user, shot) {
		return newError(codeInvalidMode, "shot %q is locked (draw it in the character totem first)", shot)
	}
	if bomb := strings.TrimSpace(loadout.BombType); bomb != "" && !ownsBombLocked(user, bomb) {
		return newError(codeInvalidMode, "bomb %q is locked (unlock the character first)", bomb)
	}
	return nil
}

// characterRosterSnapshotLocked projects the roster: owned shots (sorted),
// derived bombs, faith, pity counters and the still-locked shot ids.
func (s *Service) characterRosterSnapshotLocked(user *userState) CharacterRosterSnapshot {
	ensureDefaultCharactersLocked(user, s.clock())
	shots := make([]CharacterOwnership, 0, len(user.Characters))
	shotIDs := make([]string, 0, len(user.Characters))
	for shotID := range user.Characters {
		shotIDs = append(shotIDs, shotID)
	}
	sort.Strings(shotIDs)
	for _, shotID := range shotIDs {
		shots = append(shots, user.Characters[shotID])
	}
	bombSet := map[string]struct{}{}
	for _, shot := range shots {
		bombSet[shot.Chara] = struct{}{}
	}
	bombs := make([]string, 0, len(bombSet))
	for bomb := range bombSet {
		bombs = append(bombs, bomb)
	}
	sort.Strings(bombs)
	locked := make([]string, 0, len(gachaCharacterTable)-len(shots))
	for _, entry := range gachaCharacterTable {
		if _, owned := user.Characters[entry.ShotID]; !owned {
			locked = append(locked, entry.ShotID)
		}
	}
	return CharacterRosterSnapshot{
		OK:                 true,
		UserID:             user.UserID,
		Shots:              shots,
		Bombs:              bombs,
		Faith:              gachaFaithValue(user.Wallet),
		PityCounters:       copyGachaPity(user.GachaPity),
		LockedShotIDs:      locked,
		ExchangeCostFaith:  gachaFaithExchangeCost,
		ServerAuthoritative: true,
		ServerTime:         s.clock(),
	}
}

func copyGachaPity(source map[string]ChestPityState) map[string]ChestPityState {
	out := make(map[string]ChestPityState, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyGachaPullResults(source []GachaPullResult) []GachaPullResult {
	return append([]GachaPullResult(nil), source...)
}

func copyGachaOpenings(source []GachaOpeningRecord) []GachaOpeningRecord {
	out := make([]GachaOpeningRecord, 0, len(source))
	for _, record := range source {
		record.Results = copyGachaPullResults(record.Results)
		out = append(out, record)
	}
	return out
}

func copyGachaPools(source []GachaPool) []GachaPool {
	out := append([]GachaPool(nil), source...)
	for i := range out {
		out[i].Weights = copyIntMap(out[i].Weights)
	}
	return out
}

// GachaPools lists both pools with the wallet and the per-pool pity counters.
// Lookup-only: the client cannot influence any roll through this call.
func (s *Service) GachaPools(sessionToken string) (*GachaPoolsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	initGachaStateLocked(user, s.clock())
	return &GachaPoolsResponse{
		OK:                   true,
		Pools:                copyGachaPools(gachaPools()),
		Wallet:               copyIntMap(user.Wallet),
		Faith:                gachaFaithValue(user.Wallet),
		PityCounters:         copyGachaPity(user.GachaPity),
		ServerAuthoritative:  true,
		ServerTime:           s.clock(),
	}, nil
}

// GachaRoster projects the owned shots / bombs, faith and locked ids.
func (s *Service) GachaRoster(sessionToken string) (*CharacterRosterSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	initGachaStateLocked(user, s.clock())
	roster := s.characterRosterSnapshotLocked(user)
	return &roster, nil
}

// GachaPull spends gems and rolls the requested pool 1 or 10 times. Every
// result is decided under the service lock; the client only renders what the
// server returns.
func (s *Service) GachaPull(sessionToken string, req GachaPullRequest) (*GachaPullResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	initGachaStateLocked(user, s.clock())
	poolID := strings.TrimSpace(req.PoolID)
	if poolID == "" {
		poolID = gachaCardPoolID
	}
	pool, ok := gachaPoolByID(poolID)
	if !ok || !pool.Enabled {
		return nil, newError(codeNotFound, "gacha pool not found")
	}
	count := req.Count
	if count != 1 && count != gachaTenPullCount {
		return nil, newError(codeInvalidRequest, "count must be 1 or %d", gachaTenPullCount)
	}
	costGems := pool.CostGems
	if count == gachaTenPullCount {
		costGems = pool.CostTenGems
	}
	if user.Wallet == nil {
		user.Wallet = defaultWallet()
	}
	if user.Wallet["gems"] < costGems {
		return nil, newError(codeInsufficientCurrency, "not enough gems")
	}
	user.Wallet["gems"] -= costGems

	now := s.clock()
	openingID := s.nextIDLocked("gacha_opening")
	seed := gachaOpeningSeed(user.UserID, pool.PoolID, openingID, len(user.GachaOpenings), count)
	if user.GachaPity == nil {
		user.GachaPity = map[string]ChestPityState{}
	}
	results := make([]GachaPullResult, 0, count)
	for index := 0; index < count; index++ {
		rolled := rollGachaRarity(pool.Weights, seed, index)
		// 雀魂 ten-pull guarantee: the last slot of a ten-pull is rare+.
		if count == gachaTenPullCount && index == count-1 && rarityRank(rolled) < rarityRank("rare") {
			rolled = gachaGuaranteedRarity(pool.Weights, seed, index)
		}
		rarity := applyGachaPity(user.GachaPity, pool, rolled)
		result := GachaPullResult{
			ID:      fmt.Sprintf("%s_%02d", openingID, index+1),
			Kind:    pool.Kind,
			Rarity:  rarity,
			NameKey: gachaResultNameKey(pool.Kind, "", rarity),
		}
		switch pool.Kind {
		case "card":
			cardID := pickGachaCardForRarity(rarity, seed, index)
			accepted, overflow, dust := grantCardToUserLocked(user, cardID, 1, now)
			result.CardID = cardID
			result.NameKey = gachaResultNameKey("card", cardID, rarity)
			result.IsNew = accepted > 0
			result.Dust = dust
			result.FaithGained = 0
			_ = overflow
		case "character":
			entry, found := pickGachaShotForRarity(rarity, seed, index)
			if !found {
				// Unreachable with the shipped catalog; fail closed rather
				// than invent an item.
				return nil, newError(codeInvalidRequest, "character pool has no %s entries", rarity)
			}
			if _, owned := user.Characters[entry.ShotID]; owned {
				ownership := user.Characters[entry.ShotID]
				ownership.Dupes++
				user.Characters[entry.ShotID] = ownership
				result.FaithGained = characterDupeFaith(rarity)
			} else {
				user.Characters[entry.ShotID] = CharacterOwnership{
					ShotID:     entry.ShotID,
					Chara:      entry.Chara,
					Source:     "gacha",
					ObtainedAt: now,
				}
				result.IsNew = true
			}
			result.ShotID = entry.ShotID
			result.Chara = entry.Chara
			result.NameKey = gachaResultNameKey("character", entry.ShotID, rarity)
		}
		results = append(results, result)
	}
	// 雀魂 faith well: one faith per pull, shared across pools.
	user.Wallet[gachaFaithCurrency] += count

	audit := GachaOpeningRecord{
		OpeningID:  openingID,
		UserID:     user.UserID,
		PoolID:     pool.PoolID,
		Count:      count,
		CostGems:   costGems,
		ServerSeed: seed,
		Results:    copyGachaPullResults(results),
		OpenedAt:   now,
	}
	user.GachaOpenings = append(user.GachaOpenings, audit)
	if len(user.GachaOpenings) > maxGachaOpeningRecords {
		user.GachaOpenings = append([]GachaOpeningRecord(nil), user.GachaOpenings[len(user.GachaOpenings)-maxGachaOpeningRecords:]...)
	}
	user.GachaLastResults = copyGachaPullResults(results)

	return &GachaPullResponse{
		OK:                   true,
		PoolID:               pool.PoolID,
		Count:                count,
		Results:              copyGachaPullResults(results),
		Wallet:               copyIntMap(user.Wallet),
		Faith:                gachaFaithValue(user.Wallet),
		Inventory:            s.inventorySnapshotLocked(user),
		Characters:           s.characterRosterSnapshotLocked(user),
		PityCounters:         copyGachaPity(user.GachaPity),
		Audit:                audit,
		ServerAuthoritative:  true,
		ServerTime:           s.clock(),
	}, nil
}

// ExchangeFaith unlocks one locked shot with faith (雀魂 150-faith well).
// Cards are not exchangeable: duplicate cards already convert to dust.
func (s *Service) ExchangeFaith(sessionToken string, req FaithExchangeRequest) (*FaithExchangeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	initGachaStateLocked(user, s.clock())
	itemID := strings.TrimSpace(req.ItemID)
	if itemID == "" {
		return nil, newError(codeInvalidRequest, "item_id is required")
	}
	entry, ok := gachaCharacterEntryByShot(itemID)
	if !ok {
		return nil, newError(codeNotFound, "unknown character item %q", itemID)
	}
	if _, owned := user.Characters[itemID]; owned {
		return nil, newError(codeAlreadyClaimed, "character %q is already owned", itemID)
	}
	faith := gachaFaithValue(user.Wallet)
	if faith < gachaFaithExchangeCost {
		return nil, newError(codeInsufficientFunds, "not enough faith (%d/%d)", faith, gachaFaithExchangeCost)
	}
	user.Wallet[gachaFaithCurrency] = faith - gachaFaithExchangeCost
	user.Characters[itemID] = CharacterOwnership{
		ShotID:     itemID,
		Chara:      entry.Chara,
		Source:     "faith_exchange",
		ObtainedAt: s.clock(),
	}
	return &FaithExchangeResponse{
		OK:                   true,
		ItemID:               itemID,
		Faith:                gachaFaithValue(user.Wallet),
		FaithSpent:           gachaFaithExchangeCost,
		Characters:           s.characterRosterSnapshotLocked(user),
		Wallet:               copyIntMap(user.Wallet),
		ServerAuthoritative:  true,
		ServerTime:           s.clock(),
	}, nil
}

// gachaResultNameKey keys the localized result label. Cards reuse the
// catalog name key; shots fall back to a stable gacha key.
func gachaResultNameKey(kind, itemID, rarity string) string {
	if kind == "card" && itemID != "" {
		if key := cardNameKey(itemID); key != "" {
			return key
		}
	}
	return "screen.gacha.result." + rarity
}

