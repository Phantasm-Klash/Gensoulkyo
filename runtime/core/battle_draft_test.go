package core

import (
	"sort"
	"strings"
	"testing"
)

// newDraftTestMatch builds a minimal, deterministic battle-royale matchState
// with the given per-player decks. It does not go through the queue so tests
// can control deck composition and hand size directly.
func newDraftTestMatch(mode string, seed int64, decks map[string][]string) *matchState {
	ids := []string{}
	for id := range decks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	match := &matchState{
		MatchID:     "match_draft",
		ModeID:      mode,
		ServerSeed:  seed,
		Status:      "running",
		PlayerIDs:   ids,
		Players:     map[string]*playerState{},
		ModeState:   defaultModeState(mode),
		ModeActions: map[string]ModeActionResponse{},
		ActiveCards: map[string]*activeCardState{},
		Bullets:     map[string]*bulletState{},
	}
	for _, id := range ids {
		deck := DeckSnapshot{DeckID: id + "_deck", CardIDs: decks[id]}
		hand, cursor := initialHand(deck)
		match.Players[id] = &playerState{
			UserID:       id,
			Connected:    true,
			Hand:         hand,
			DrawCursor:   cursor,
			Cooldowns:    map[string]int{},
			DeckSnapshot: deck,
		}
	}
	return match
}

func countEvents(match *matchState, eventType string) int {
	count := 0
	for _, event := range match.EventLog {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func draftTestDecks(cards ...string) map[string][]string {
	return map[string][]string{"alice": cards, "bob": cards}
}

func TestDraftMixedPoolDedupSortHash(t *testing.T) {
	match := newDraftTestMatch("battle_royale", 7, map[string][]string{
		"alice": {"tempo_break", "focus_lens", "focus_lens"},
		"bob":   {"focus_lens", "density_surge"},
	})
	pool := buildMixedPool(match)
	want := "density_surge,focus_lens,tempo_break"
	if got := strings.Join(pool, ","); got != want {
		t.Fatalf("mixed pool = %q, want %q", got, want)
	}
	if got := mixedPoolHash(pool); got != shortHash(want) {
		t.Fatalf("pool hash = %q, want %q", got, shortHash(want))
	}
}

func TestDraftDrawsThreeDistinctCandidates(t *testing.T) {
	match := newDraftTestMatch("battle_royale", 12345, map[string][]string{
		"alice": {"a", "b", "c", "d", "e"},
		"bob":   {"c", "d", "e", "f", "g"},
	})
	candidates := drawMixedPoolCandidates(match, 1, 42)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %v", candidates)
	}
	pool := buildMixedPool(match)
	seen := map[string]bool{}
	for _, cardID := range candidates {
		if seen[cardID] {
			t.Fatalf("candidates repeat: %v", candidates)
		}
		seen[cardID] = true
		if !stringSliceContains(pool, cardID) {
			t.Fatalf("candidate %q is outside the mixed pool %v", cardID, pool)
		}
	}
	again := drawMixedPoolCandidates(match, 1, 42)
	if strings.Join(again, ",") != strings.Join(candidates, ",") {
		t.Fatalf("draw is not deterministic: %v vs %v", candidates, again)
	}
}

func TestDraftDrawCapsAtPoolSize(t *testing.T) {
	match := newDraftTestMatch("battle_royale", 1, map[string][]string{
		"alice": {"a"},
		"bob":   {"b"},
	})
	if got := drawMixedPoolCandidates(match, 1, 0); len(got) != 2 {
		t.Fatalf("pool smaller than 3 must draw min(3, len(pool)); got %v", got)
	}
}

func TestDraftDuplicateBossDefeatIsNoOp(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 99, draftTestDecks("a", "b", "c", "d"))
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss_1", 100); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "bob", "boss_1", 100); err != nil {
		t.Fatalf("second report: %v", err)
	}
	opensBefore := countEvents(match, "draft_open")
	roundBefore := match.Draft.RoundIndex
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss_1", 100); err != nil {
		t.Fatalf("duplicate report: %v", err)
	}
	if countEvents(match, "draft_open") != opensBefore {
		t.Fatalf("a duplicate report must not open a new round")
	}
	if match.Draft.RoundIndex != roundBefore {
		t.Fatalf("duplicate report changed the round index")
	}
	if countEvents(match, "draft_skipped") == 0 {
		t.Fatalf("duplicate report must emit draft_skipped")
	}
}

func TestDraftSameTickTiebreakUsesSortedOrder(t *testing.T) {
	service := NewService(Config{})
	// "zeta" arrives first but sortedPlayerIDs puts "alice" first.
	match := newDraftTestMatch("battle_royale", 5, map[string][]string{
		"alice": {"a", "b", "c", "d"},
		"zeta":  {"a", "b", "c", "d"},
	})
	if _, err := service.reportBossDefeatLocked(match, "zeta", "boss", 50); err != nil {
		t.Fatalf("zeta report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 50); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	if match.Draft.FirstUser != "alice" || match.Draft.SecondUser != "zeta" {
		t.Fatalf("same-tick tiebreak must follow sortedPlayerIDs order: first=%q second=%q", match.Draft.FirstUser, match.Draft.SecondUser)
	}
	if !match.Draft.SameTickTiebreak {
		t.Fatalf("same-tick tiebreak flag must be set")
	}
	if lastMatchEvent(match).Status != "same_tick_tiebreak" {
		t.Fatalf("tiebreak must be auditable in the event, got %+v", lastMatchEvent(match))
	}
}

func TestDraftPickRulesAndAtomicGrant(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 3, draftTestDecks("a", "b", "c", "d", "e"))
	// leave a free slot in each hand so the grant is observable
	match.Players["alice"].Hand = []string{"a"}
	match.Players["bob"].Hand = []string{"a"}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 10); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "bob", "boss", 20); err != nil {
		t.Fatalf("bob report: %v", err)
	}
	candidates := match.Draft.Candidates
	aliceHand := strings.Join(match.Players["alice"].Hand, ",")
	bobHand := strings.Join(match.Players["bob"].Hand, ",")

	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["alice"], "act1", map[string]any{"card_id": "not_a_candidate"}); err == nil {
		t.Fatalf("first picker must not pick a card outside the candidates")
	}
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["bob"], "act2", map[string]any{"card_id": candidates[1]}); err == nil {
		t.Fatalf("the second picker must not pick before the first picker")
	}
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["alice"], "act3", map[string]any{"card_id": candidates[0]}); err != nil {
		t.Fatalf("first pick: %v", err)
	}
	if strings.Join(match.Players["alice"].Hand, ",") != aliceHand || strings.Join(match.Players["bob"].Hand, ",") != bobHand {
		t.Fatalf("no hand may change until both picks exist")
	}
	if match.Draft.Granted {
		t.Fatalf("round must not be granted after a single pick")
	}
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["bob"], "act4", map[string]any{"card_id": candidates[0]}); err == nil {
		t.Fatalf("the second picker must not take the first picker's card")
	}
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["bob"], "act5", map[string]any{"card_id": candidates[1]}); err != nil {
		t.Fatalf("second pick: %v", err)
	}
	maybeGrantDraftLocked(match)
	if !match.Draft.Granted {
		t.Fatalf("round must be granted once both picks exist")
	}
	if !stringSliceContains(match.Players["alice"].Hand, candidates[0]) {
		t.Fatalf("alice must hold her pick: %v", match.Players["alice"].Hand)
	}
	if !stringSliceContains(match.Players["bob"].Hand, candidates[1]) {
		t.Fatalf("bob must hold his pick: %v", match.Players["bob"].Hand)
	}
	// both grants happen in one critical section: same grant id and tick
	grants := []MatchEvent{}
	for _, event := range match.EventLog {
		if event.Type == "draft_grant" {
			grants = append(grants, event)
		}
	}
	if len(grants) != 2 || grants[0].ActionID != grants[1].ActionID || grants[0].Tick != grants[1].Tick {
		t.Fatalf("both grants must share one grant id and tick: %+v", grants)
	}
}

func TestDraftPickThroughModeActionAndCandidateIndex(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 17, draftTestDecks("a", "b", "c", "d", "e"))
	match.Players["alice"].Hand = []string{"a"}
	match.Players["bob"].Hand = []string{"a"}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 10); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "bob", "boss", 20); err != nil {
		t.Fatalf("bob report: %v", err)
	}
	candidates := match.Draft.Candidates
	// the C++ intent validates candidate_index 0..2; the lobby accepts it too
	resp := service.applyModeActionLocked(match, match.Players["alice"], ModeActionRequest{
		ModeID: "battle_royale", ActionType: "select_round_card", Payload: map[string]any{"candidate_index": 0},
	})
	if !resp.Accepted {
		t.Fatalf("candidate_index pick must be accepted: %+v", resp)
	}
	if match.Draft.Picks["alice"] != candidates[0] {
		t.Fatalf("alice pick = %q, want %q", match.Draft.Picks["alice"], candidates[0])
	}
	// a non-designated pick is rejected with a reason
	rejected := service.applyModeActionLocked(match, match.Players["alice"], ModeActionRequest{
		ModeID: "battle_royale", ActionType: "select_round_card", Payload: map[string]any{"candidate_index": 1},
	})
	if rejected.Accepted || rejected.Reason != codeModeAction {
		t.Fatalf("a repeat pick by the same player must be rejected: %+v", rejected)
	}
}

func TestDraftTimeoutAutoPicksAndGrants(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 11, draftTestDecks("a", "b", "c", "d", "e"))
	match.Players["alice"].Hand = []string{"a"}
	match.Players["bob"].Hand = []string{"a"}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 10); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "bob", "boss", 30); err != nil {
		t.Fatalf("bob report: %v", err)
	}
	candidates := match.Draft.Candidates
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["alice"], "act1", map[string]any{"card_id": candidates[0]}); err != nil {
		t.Fatalf("alice pick: %v", err)
	}
	if match.Draft.Granted {
		t.Fatalf("round must stay open until the second pick or the deadline")
	}
	match.Tick = match.Draft.ChoiceDeadlineTick
	resolveDraftTimeoutLocked(match)
	if !match.Draft.Granted {
		t.Fatalf("timeout must close the round")
	}
	if countEvents(match, "draft_timeout") != 1 {
		t.Fatalf("timeout must emit draft_timeout, log=%+v", match.EventLog)
	}
	bobCard := match.Draft.Picks["bob"]
	if bobCard == "" || bobCard == candidates[0] {
		t.Fatalf("bob must auto-pick a remaining card, got %q", bobCard)
	}
	if !stringSliceContains(match.Players["bob"].Hand, bobCard) {
		t.Fatalf("bob must be granted his auto pick: %v", match.Players["bob"].Hand)
	}
}

func TestDraftTimeoutWithoutSecondDefeatGrantsFirstOnly(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 23, draftTestDecks("a", "b", "c", "d", "e"))
	match.Players["alice"].Hand = []string{"a"}
	match.Players["bob"].Hand = []string{"a"}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 10); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	match.Tick = match.Draft.ChoiceDeadlineTick
	resolveDraftTimeoutLocked(match)
	if !match.Draft.Granted {
		t.Fatalf("timeout must close the round even without a second defeat")
	}
	if !stringSliceContains(match.Players["alice"].Hand, match.Draft.Picks["alice"]) {
		t.Fatalf("the first picker must be granted on timeout: %v", match.Players["alice"].Hand)
	}
	if len(match.Players["bob"].Hand) != 1 || countEvents(match, "draft_grant") != 1 {
		t.Fatalf("a player who never defeated the boss must get nothing: hand=%v grants=%d", match.Players["bob"].Hand, countEvents(match, "draft_grant"))
	}
}

func TestDraftGrantDefersWhenHandFull(t *testing.T) {
	service := NewService(Config{})
	match := newDraftTestMatch("battle_royale", 21, draftTestDecks("a", "b", "c", "d", "e"))
	for _, userID := range []string{"alice", "bob"} {
		match.Players[userID].Hand = []string{"a", "b", "c", "d"}
	}
	if _, err := service.reportBossDefeatLocked(match, "alice", "boss", 10); err != nil {
		t.Fatalf("alice report: %v", err)
	}
	if _, err := service.reportBossDefeatLocked(match, "bob", "boss", 20); err != nil {
		t.Fatalf("bob report: %v", err)
	}
	candidates := match.Draft.Candidates
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["alice"], "act1", map[string]any{"card_id": candidates[0]}); err != nil {
		t.Fatalf("alice pick: %v", err)
	}
	if _, err := applyMixedPoolDraftPickLocked(match, match.Players["bob"], "act2", map[string]any{"card_id": candidates[1]}); err != nil {
		t.Fatalf("bob pick: %v", err)
	}
	maybeGrantDraftLocked(match)
	for _, userID := range []string{"alice", "bob"} {
		if len(match.Players[userID].Hand) != cardHandLimit {
			t.Fatalf("%s hand must stay at the limit, got %v", userID, match.Players[userID].Hand)
		}
		if len(match.Players[userID].PendingGrants) != 1 {
			t.Fatalf("%s grant must be deferred, got %v", userID, match.Players[userID].PendingGrants)
		}
	}
	if countEvents(match, "card_grant_deferred") != 2 {
		t.Fatalf("expected two deferred grants, log=%+v", match.EventLog)
	}
	// playing a card frees a slot and promotes the deferred grant
	player := match.Players["alice"]
	player.Hand = player.Hand[1:]
	promotePendingGrants(player)
	if len(player.PendingGrants) != 0 || len(player.Hand) != cardHandLimit {
		t.Fatalf("deferred grant must be promoted after a slot frees: hand=%v pending=%v", player.Hand, player.PendingGrants)
	}
}
