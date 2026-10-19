package core

// gacha_test.go: acceptance tests for the dual gacha pools (T10).
// Covers the starter roster (博丽灵梦 A), weighted rolls, pity, the ten-pull
// guarantee, currency costs, duplicate conversion, the faith well, the op
// contract surface and the loadout ownership gate.

import (
	"strings"
	"testing"
)

// grantShotForTest unlocks one shot directly (test shortcut around the random
// roll; the roll itself is covered by the distribution tests below).
func grantShotForTest(t *testing.T, service *Service, sessionToken, shotID string) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	user, err := service.userBySessionLocked(sessionToken)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	ensureDefaultCharactersLocked(user, service.clock())
	entry, ok := gachaCharacterEntryByShot(shotID)
	if !ok {
		t.Fatalf("unknown shot %q", shotID)
	}
	if _, owned := user.Characters[shotID]; !owned {
		user.Characters[shotID] = CharacterOwnership{
			ShotID:     shotID,
			Chara:      entry.Chara,
			Source:     "test",
			ObtainedAt: service.clock(),
		}
	}
}

// setWalletForTest mutates a user's wallet through the service lock.
func setWalletForTest(t *testing.T, service *Service, sessionToken string, mutate func(map[string]int)) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	user, err := service.userBySessionLocked(sessionToken)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if user.Wallet == nil {
		user.Wallet = defaultWallet()
	}
	mutate(user.Wallet)
}

func TestGachaCharacterTableMatchesAllowedShotTypes(t *testing.T) {
	if len(gachaCharacterTable) != 52 {
		t.Fatalf("character table size = %d, want 52", len(gachaCharacterTable))
	}
	seen := map[string]bool{}
	rarityCount := map[string]int{}
	for _, entry := range gachaCharacterTable {
		if _, ok := allowedShotTypes[entry.ShotID]; !ok {
			t.Fatalf("pool entry %q is not in allowedShotTypes", entry.ShotID)
		}
		if seen[entry.ShotID] {
			t.Fatalf("duplicate pool entry %q", entry.ShotID)
		}
		seen[entry.ShotID] = true
		rarityCount[entry.Rarity]++
	}
	for shot := range allowedShotTypes {
		if !seen[shot] {
			t.Fatalf("allowedShotTypes entry %q missing from the gacha pool", shot)
		}
	}
	for _, want := range []struct {
		rarity string
		count  int
	}{{"common", 13}, {"uncommon", 13}, {"rare", 13}, {"epic", 13}} {
		if rarityCount[want.rarity] != want.count {
			t.Fatalf("rarity %s count = %d, want %d", want.rarity, rarityCount[want.rarity], want.count)
		}
	}
	// The starter kit is 博丽灵梦 A 型: both A-shot variants of Reimu.
	for _, shot := range defaultGachaShotIDs {
		entry, ok := gachaCharacterEntryByShot(shot)
		if !ok || entry.Chara != "Reimu" || !strings.HasPrefix(shot, "ReimuA") {
			t.Fatalf("starter %q = %+v ok=%v, want a Reimu A-shot", shot, entry, ok)
		}
	}
}

func TestDefaultUserReceivesReimuAStarterKit(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Starter")
	roster, err := service.GachaRoster(session.SessionToken)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if len(roster.Shots) != 2 || roster.Shots[0].ShotID != "ReimuA1" || roster.Shots[1].ShotID != "ReimuA2" {
		t.Fatalf("default shots = %+v, want ReimuA1+ReimuA2", roster.Shots)
	}
	for _, shot := range roster.Shots {
		if shot.Source != "default" || shot.Chara != "Reimu" {
			t.Fatalf("default shot %+v must be default/Reimu", shot)
		}
	}
	if len(roster.Bombs) != 1 || roster.Bombs[0] != "Reimu" {
		t.Fatalf("default bombs = %+v, want [Reimu]", roster.Bombs)
	}
	if len(roster.LockedShotIDs) != 50 {
		t.Fatalf("locked shots = %d, want 50", len(roster.LockedShotIDs))
	}
	for _, locked := range roster.LockedShotIDs {
		if locked == "ReimuA1" || locked == "ReimuA2" {
			t.Fatalf("starter shot %q must not be locked", locked)
		}
	}
	if roster.Faith != 0 {
		t.Fatalf("starter faith = %d, want 0", roster.Faith)
	}
	if roster.ExchangeCostFaith != gachaFaithExchangeCost {
		t.Fatalf("exchange cost = %d, want %d", roster.ExchangeCostFaith, gachaFaithExchangeCost)
	}
	// Wallet exposes the faith key so the shop strip can render it.
	if _, ok := defaultWallet()["faith"]; !ok {
		t.Fatal("default wallet must carry the faith key")
	}
}

func TestGachaPoolsContractAndCatalog(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Pools")
	pools, err := service.GachaPools(session.SessionToken)
	if err != nil {
		t.Fatalf("pools: %v", err)
	}
	if len(pools.Pools) != 2 {
		t.Fatalf("pool count = %d, want 2", len(pools.Pools))
	}
	byID := map[string]GachaPool{}
	for _, pool := range pools.Pools {
		byID[pool.PoolID] = pool
		if pool.CostGems != gachaSingleCostGems || pool.CostTenGems != gachaTenCostGems {
			t.Fatalf("pool %s cost = %d/%d, want %d/%d", pool.PoolID, pool.CostGems, pool.CostTenGems, gachaSingleCostGems, gachaTenCostGems)
		}
		if !pool.Enabled {
			t.Fatalf("pool %s must be enabled", pool.PoolID)
		}
	}
	card, ok := byID[gachaCardPoolID]
	if !ok || card.Kind != "card" {
		t.Fatalf("card pool missing: %+v", byID)
	}
	if card.Weights["common"] != 70 || card.Weights["epic"] != 2 {
		t.Fatalf("card weights = %+v, want chest-parity 70/20/8/2", card.Weights)
	}
	if card.Pity.RareEvery != 10 || card.Pity.EpicEvery != 60 {
		t.Fatalf("card pity = %+v, want 10/60", card.Pity)
	}
	character, ok := byID[gachaCharacterPoolID]
	if !ok || character.Kind != "character" {
		t.Fatalf("character pool missing: %+v", byID)
	}
	if character.Weights["common"] != 50 || character.Weights["epic"] != 6 {
		t.Fatalf("character weights = %+v, want 50/30/14/6", character.Weights)
	}
	if character.Pity.RareEvery != 10 || character.Pity.EpicEvery != 30 {
		t.Fatalf("character pity = %+v, want 10/30", character.Pity)
	}

	// The four gacha ops are client contract operations with the business
	// envelope required (the exact rule every post-login op follows).
	contracts := map[string]ClientOperationContract{}
	for _, contract := range ContractClientOperationContracts() {
		contracts[contract.Operation] = contract
	}
	for _, op := range []string{"gacha.pools", "gacha.pull", "gacha.roster", "gacha.faith.exchange"} {
		contract, ok := contracts[op]
		if !ok {
			t.Fatalf("op %q missing from the client contract", op)
		}
		if !contract.BusinessEnvelopeRequired || !contract.ServerAuthoritativeProjection {
			t.Fatalf("op %q contract = %+v, want envelope+server-authoritative", op, contract)
		}
		if len(contract.ForbiddenClientRequestFields) == 0 {
			t.Fatalf("op %q must publish forbidden client fields", op)
		}
	}
	clientOps := ContractClientOperations()
	for _, op := range []string{"gacha.pools", "gacha.pull", "gacha.roster", "gacha.faith.exchange"} {
		if !stringSliceContains(clientOps, op) {
			t.Fatalf("op %q missing from contractClientOperations", op)
		}
	}
}

func TestRollGachaRarityFollowsWeights(t *testing.T) {
	// Deterministic roll (no pity) must track the weight table closely.
	weights := map[string]int{"common": 70, "uncommon": 20, "rare": 8, "epic": 2}
	counts := map[string]int{}
	iterations := 20000
	for i := 0; i < iterations; i++ {
		counts[rollGachaRarity(weights, "dist-seed", i)]++
	}
	total := 0
	for _, value := range weights {
		total += value
	}
	for rarity, weight := range weights {
		got := float64(counts[rarity]) / float64(iterations)
		want := float64(weight) / float64(total)
		if got < want-0.02 || got > want+0.02 {
			t.Fatalf("rarity %s frequency = %.4f, want %.4f (+-0.02)", rarity, got, want)
		}
	}
}

func TestApplyGachaPityUpgradesWithinWindow(t *testing.T) {
	pool := GachaPool{PoolID: "pity_pool", Pity: ChestPityRules{RareEvery: 10, EpicEvery: 30}}
	counters := map[string]ChestPityState{}
	// Ten commons in a row: the tenth must be upgraded to rare.
	for i := 0; i < 9; i++ {
		if got := applyGachaPity(counters, pool, "common"); got != "common" {
			t.Fatalf("pull %d = %q, want common before the window closes", i+1, got)
		}
	}
	if got := applyGachaPity(counters, pool, "common"); got != "rare" {
		t.Fatalf("tenth pull = %q, want rare (pity window 10)", got)
	}
	if counters[pool.PoolID].RareCounter != 0 {
		t.Fatalf("rare counter must reset after the upgrade, got %+v", counters[pool.PoolID])
	}
	// Epic pity at 30 with non-stop rares. The epic window counts every pull
	// (the rare upgrade above also advanced it), so count until the upgrade.
	counters = map[string]ChestPityState{}
	for i := 1; i <= 30; i++ {
		if got := applyGachaPity(counters, pool, "rare"); got == "epic" {
			if i > 30 {
				t.Fatalf("epic arrived at pull %d, want <= 30", i)
			}
			if counters[pool.PoolID].EpicCounter != 0 {
				t.Fatalf("epic counter must reset after the upgrade, got %+v", counters[pool.PoolID])
			}
			return
		}
	}
	t.Fatal("epic pity never fired within the 30-pull window")
}

func TestGachaGuaranteedRarityIsRarePlus(t *testing.T) {
	weights := map[string]int{"common": 70, "uncommon": 20, "rare": 8, "epic": 2}
	for i := 0; i < 200; i++ {
		rarity := gachaGuaranteedRarity(weights, "guarantee-seed", i)
		if rarityRank(rarity) < rarityRank("rare") {
			t.Fatalf("guaranteed rarity %q below rare", rarity)
		}
	}
}

func TestGachaPullCardPoolSpendsGemsAndGainsFaith(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Card Pull")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 5000
	})
	response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: 1})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(response.Results))
	}
	result := response.Results[0]
	if result.Kind != "card" || result.CardID == "" {
		t.Fatalf("result = %+v, want a card draw", result)
	}
	if _, ok := serverCardCatalog[result.CardID]; !ok {
		t.Fatalf("drawn card %q is not in the catalog", result.CardID)
	}
	if result.Rarity != serverCardRarities[result.CardID] {
		t.Fatalf("result rarity %q != catalog rarity %q", result.Rarity, serverCardRarities[result.CardID])
	}
	if response.Wallet["gems"] != 4800 {
		t.Fatalf("gems after single = %d, want 4800", response.Wallet["gems"])
	}
	if response.Faith != 1 || response.Wallet["faith"] != 1 {
		t.Fatalf("faith after single = %d/%d, want 1", response.Faith, response.Wallet["faith"])
	}
	if response.Count != 1 || response.PoolID != gachaCardPoolID {
		t.Fatalf("response header = %+v", response)
	}
	if response.Audit.Count != 1 || response.Audit.CostGems != gachaSingleCostGems || len(response.Audit.Results) != 1 {
		t.Fatalf("audit record = %+v", response.Audit)
	}
	if !response.ServerAuthoritative {
		t.Fatal("pull response must be server authoritative")
	}
}

func TestGachaPullTenIsDiscountedAndGuaranteesRarePlus(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Ten Pull")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 5000
	})
	response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: gachaTenPullCount})
	if err != nil {
		t.Fatalf("ten pull: %v", err)
	}
	if len(response.Results) != gachaTenPullCount {
		t.Fatalf("results = %d, want %d", len(response.Results), gachaTenPullCount)
	}
	if response.Wallet["gems"] != 5000-gachaTenCostGems {
		t.Fatalf("gems after ten = %d, want %d", response.Wallet["gems"], 5000-gachaTenCostGems)
	}
	if response.Faith != gachaTenPullCount {
		t.Fatalf("faith after ten = %d, want %d", response.Faith, gachaTenPullCount)
	}
	best := 0
	for _, result := range response.Results {
		if rank := rarityRank(result.Rarity); rank > best {
			best = rank
		}
	}
	if best < rarityRank("rare") {
		t.Fatal("ten-pull must contain at least one rare+ result")
	}
}

func TestGachaPullRejectsUnknownPoolAndBadCount(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Validation")
	if _, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: "gacha.nope", Count: 1}); err == nil || ErrorCode(err) != codeNotFound {
		t.Fatalf("unknown pool err = %v, want not_found", err)
	}
	for _, count := range []int{0, 2, 5, 9, 11, -1} {
		if _, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: count}); err == nil || ErrorCode(err) != codeInvalidRequest {
			t.Fatalf("count %d err = %v, want invalid_request", count, err)
		}
	}
}

func TestGachaPullRejectsInsufficientGems(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Broke")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 199
	})
	if _, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: 1}); err == nil || ErrorCode(err) != codeInsufficientCurrency {
		t.Fatalf("single with 199 gems err = %v, want insufficient_currency", err)
	}
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 1799
	})
	if _, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCharacterPoolID, Count: gachaTenPullCount}); err == nil || ErrorCode(err) != codeInsufficientCurrency {
		t.Fatalf("ten with 1799 gems err = %v, want insufficient_currency", err)
	}
}

func TestGachaCharacterPullsUnlockShotsAndBombs(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Characters")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 100000
	})
	seenDupe := false
	dupeFaith := 0
	for i := 0; i < 120; i++ {
		response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCharacterPoolID, Count: 1})
		if err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
		result := response.Results[0]
		if result.Kind != "character" || result.ShotID == "" {
			t.Fatalf("pull %d result = %+v, want a character draw", i, result)
		}
		if entry, ok := gachaCharacterEntryByShot(result.ShotID); !ok || entry.Rarity != result.Rarity || entry.Chara != result.Chara {
			t.Fatalf("pull %d: shot/rarity/chara drift %+v", i, result)
		}
		if result.IsNew {
			if response.Characters.Shots == nil {
				t.Fatalf("pull %d: new shot without a roster projection", i)
			}
		} else {
			seenDupe = true
			if result.FaithGained <= 0 {
				t.Fatalf("pull %d: dupe %q granted no faith", i, result.ShotID)
			}
			if result.FaithGained != characterDupeFaith(result.Rarity) {
				t.Fatalf("pull %d: dupe faith = %d, want %d", i, result.FaithGained, characterDupeFaith(result.Rarity))
			}
			dupeFaith += result.FaithGained
		}
	}
	if !seenDupe {
		t.Fatal("120 character pulls must produce at least one duplicate conversion")
	}
	if dupeFaith <= 0 {
		t.Fatal("duplicate conversions must accumulate faith")
	}
	roster, err := service.GachaRoster(session.SessionToken)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	unique := map[string]bool{}
	for _, shot := range roster.Shots {
		if unique[shot.ShotID] {
			t.Fatalf("roster lists %q twice", shot.ShotID)
		}
		unique[shot.ShotID] = true
		if shot.Source == "gacha" && shot.Dupes < 0 {
			t.Fatalf("negative dupes for %q", shot.ShotID)
		}
	}
	if len(roster.Shots) < 10 {
		t.Fatalf("unique shots = %d, want >= 10 after 120 pulls", len(roster.Shots))
	}
	if len(roster.Bombs) != len(uniqueCharacterCharas(roster.Shots)) {
		t.Fatal("bombs must mirror the characters behind the owned shots")
	}
	// Faith is shared across pools: pull the card pool once more and the
	// counter must keep growing.
	before := roster.Faith
	response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: 1})
	if err != nil {
		t.Fatalf("card pull: %v", err)
	}
	if response.Faith != before+1 {
		t.Fatalf("faith across pools = %d, want %d", response.Faith, before+1)
	}
}

func uniqueCharacterCharas(shots []CharacterOwnership) map[string]bool {
	out := map[string]bool{}
	for _, shot := range shots {
		out[shot.Chara] = true
	}
	return out
}

func TestGachaCharacterPityForcesEpicWithinThirty(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Pity")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 100000
	})
	sawEpic := false
	for i := 0; i < 30; i++ {
		response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCharacterPoolID, Count: 1})
		if err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
		for _, result := range response.Results {
			if result.Rarity == "epic" {
				sawEpic = true
			}
		}
	}
	if !sawEpic {
		t.Fatal("character pool soft pity must force an epic within 30 pulls")
	}
}

func TestGachaCardPityForcesRareWithinTen(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Card Pity")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 100000
	})
	sawRare := false
	for i := 0; i < 10; i++ {
		response, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: 1})
		if err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
		for _, result := range response.Results {
			if rarityRank(result.Rarity) >= rarityRank("rare") {
				sawRare = true
			}
		}
	}
	if !sawRare {
		t.Fatal("card pool soft pity must force a rare+ within 10 pulls")
	}
}

func TestExchangeFaithUnlocksLockedShot(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Exchange")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["faith"] = gachaFaithExchangeCost + 5
	})
	response, err := service.ExchangeFaith(session.SessionToken, FaithExchangeRequest{ItemID: "MarisaB2"})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if response.ItemID != "MarisaB2" || response.FaithSpent != gachaFaithExchangeCost || response.Faith != 5 {
		t.Fatalf("exchange response = %+v", response)
	}
	owned := false
	for _, shot := range response.Characters.Shots {
		if shot.ShotID == "MarisaB2" && shot.Source == "faith_exchange" {
			owned = true
		}
	}
	if !owned {
		t.Fatal("exchanged shot must be owned with source faith_exchange")
	}
	if _, err := service.ExchangeFaith(session.SessionToken, FaithExchangeRequest{ItemID: "MarisaB2"}); err == nil || ErrorCode(err) != codeAlreadyClaimed {
		t.Fatalf("re-exchange err = %v, want already_claimed", err)
	}
	if _, err := service.ExchangeFaith(session.SessionToken, FaithExchangeRequest{ItemID: "NotAShot"}); err == nil || ErrorCode(err) != codeNotFound {
		t.Fatalf("unknown item err = %v, want not_found", err)
	}
	if _, err := service.ExchangeFaith(session.SessionToken, FaithExchangeRequest{ItemID: ""}); err == nil || ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("empty item err = %v, want invalid_request", err)
	}
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["faith"] = gachaFaithExchangeCost - 1
	})
	if _, err := service.ExchangeFaith(session.SessionToken, FaithExchangeRequest{ItemID: "YoumuB2"}); err == nil || ErrorCode(err) != codeInsufficientFunds {
		t.Fatalf("poor exchange err = %v, want insufficient_funds", err)
	}
}

func TestLoadoutOwnershipGateRejectsLockedShots(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Gate")
	base := func(shot, bomb string) map[string]any {
		return map[string]any{"stage_id": "lunar_maze", "shot_type": shot, "bomb_type": bomb}
	}
	// Locked shot: rejected with mode_invalid.
	if _, err := service.JoinQueue(session.SessionToken, JoinQueueRequest{
		ModeID: "mvp_boss_race", ActiveDeckID: "gate_deck", DeckSnapshot: validDeck("gate_deck"),
		ModeParams: base("MarisaA1", "Marisa"),
	}); err == nil || ErrorCode(err) != codeInvalidMode {
		t.Fatalf("locked shot join err = %v, want mode_invalid", err)
	}
	// Starter kit (empty loadout = default 自机) stays legal.
	if _, err := service.JoinQueue(session.SessionToken, JoinQueueRequest{
		ModeID: "mvp_boss_race", ActiveDeckID: "gate_deck", DeckSnapshot: validDeck("gate_deck"),
		ModeParams: map[string]any{"stage_id": "lunar_maze"},
	}); err != nil {
		t.Fatalf("default loadout join: %v", err)
	}
	// Unlock via the character totem, then the same loadout is accepted.
	grantShotForTest(t, service, session.SessionToken, "MarisaA1")
	ticket, err := service.JoinQueue(session.SessionToken, JoinQueueRequest{
		ModeID: "mvp_boss_race", ActiveDeckID: "gate_deck", DeckSnapshot: validDeck("gate_deck"),
		ModeParams: base("MarisaA1", "Marisa"),
	})
	if err != nil {
		t.Fatalf("owned shot join: %v", err)
	}
	if ticket == nil {
		t.Fatal("expected a queue ticket")
	}
	// A bomb whose character is not unlocked is still rejected.
	grantShotForTest(t, service, session.SessionToken, "YoumuA1")
	if _, err := service.JoinQueue(session.SessionToken, JoinQueueRequest{
		ModeID: "mvp_boss_race", ActiveDeckID: "gate_deck", DeckSnapshot: validDeck("gate_deck"),
		ModeParams: base("YoumuA1", "Reisen"),
	}); err == nil || ErrorCode(err) != codeInvalidMode {
		t.Fatalf("locked bomb join err = %v, want mode_invalid", err)
	}
}

func TestBootstrapSnapshotCarriesGachaRoster(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Bootstrap")
	snapshot, err := service.Bootstrap(session.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(snapshot.Characters.Shots) != 2 || snapshot.Characters.Shots[0].ShotID != "ReimuA1" {
		t.Fatalf("bootstrap roster = %+v, want the Reimu A starter kit", snapshot.Characters.Shots)
	}
	if snapshot.Wallet["faith"] != 0 {
		t.Fatalf("bootstrap wallet faith = %d, want 0", snapshot.Wallet["faith"])
	}
	projections := clientOperationProjectionFields("bootstrap")
	if !stringSliceContains(projections, "characters") {
		t.Fatalf("bootstrap projection = %+v, want characters", projections)
	}
}

func TestGachaOpeningAuditTrailIsCapped(t *testing.T) {
	service := NewService(Config{})
	session := mustLogin(t, service, "Gacha Audit")
	setWalletForTest(t, service, session.SessionToken, func(wallet map[string]int) {
		wallet["gems"] = 100000
	})
	for i := 0; i < maxGachaOpeningRecords+5; i++ {
		if _, err := service.GachaPull(session.SessionToken, GachaPullRequest{PoolID: gachaCardPoolID, Count: 1}); err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
	}
	service.mu.Lock()
	user, err := service.userBySessionLocked(session.SessionToken)
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if len(user.GachaOpenings) != maxGachaOpeningRecords {
		t.Fatalf("openings = %d, want capped at %d", len(user.GachaOpenings), maxGachaOpeningRecords)
	}
	if len(user.GachaLastResults) != 1 {
		t.Fatalf("last results = %d, want 1", len(user.GachaLastResults))
	}
}

