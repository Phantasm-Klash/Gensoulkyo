package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// T9: mixed-pool synchronized card draft.
//
// Both players' deck card ids are merged into ONE shared pool ("双方混池").
// When a player defeats a boss the lobby draws three cards from that pool. The
// player who defeated the boss first ("first picker") takes one of the three;
// the second player, after defeating the same boss, takes one of the remaining
// two. Only after BOTH picks exist are the cards granted, inside one critical
// section, so the first picker gains no early advantage.
//
// The authoritative state lives on matchState.Draft (NOT on ModeState):
// updateModeStateLocked recomputes the battle_royale ModeState keys from
// match.Tick on every tick advance and would clobber it.

const draftPickCount = 3

// draftRound is the authoritative state of one battle-royale draft round.
type draftRound struct {
	RoundIndex         int
	BossID             string
	Candidates         []string
	PoolHash           string
	FirstUser          string
	SecondUser         string
	FirstDefeatTick    int
	SameTickTiebreak   bool
	Picks              map[string]string // userID -> cardID
	Granted            bool
	ChoiceDeadlineTick int
}

// BossDefeatReportRequest is the lobby-side boss-defeat report. It is the
// server-authoritative trigger for a mixed-pool draft round; the battle server
// is expected to call it (see SubmitBattleEvents for the ingress hook).
type BossDefeatReportRequest struct {
	MatchID        string `json:"match_id"`
	BattleServerID string `json:"battle_server_id,omitempty"`
	UserID         string `json:"user_id"`
	BossID         string `json:"boss_id"`
	Tick           int    `json:"tick"`
}

// BossDefeatReportResponse reports which draft round the report opened or fed.
type BossDefeatReportResponse struct {
	OK                  bool         `json:"ok"`
	Version             VersionStamp `json:"version"`
	MatchID             string       `json:"match_id"`
	RoundIndex          int          `json:"round_index"`
	Status              string       `json:"status"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

// buildMixedPool merges every player's deck card ids into one deduped, sorted
// pool. Dedup is the rule's "duplicate draw" requirement: a card present in
// both decks is counted once.
func buildMixedPool(match *matchState) []string {
	seen := map[string]struct{}{}
	pool := []string{}
	for _, userID := range sortedPlayerIDs(match) {
		player := match.Players[userID]
		if player == nil {
			continue
		}
		for _, raw := range player.DeckSnapshot.CardIDs {
			cardID := strings.TrimSpace(raw)
			if cardID == "" {
				continue
			}
			if _, ok := seen[cardID]; ok {
				continue
			}
			seen[cardID] = struct{}{}
			pool = append(pool, cardID)
		}
	}
	sort.Strings(pool)
	return pool
}

// mixedPoolHash is the publishable, verifiable hash of a mixed pool.
func mixedPoolHash(pool []string) string {
	return shortHash(strings.Join(pool, ","))
}

// drawMixedPoolCandidates draws up to draftPickCount distinct cards from the
// mixed pool. It is deterministic and auditable: every position comes from
// deterministicUnit(ServerSeed, tick, "draft_draw", roundIndex*3+i) with linear
// probing, so the three candidates never repeat.
func drawMixedPoolCandidates(match *matchState, roundIndex int, tick int) []string {
	pool := buildMixedPool(match)
	if len(pool) == 0 {
		return []string{}
	}
	want := draftPickCount
	if len(pool) < want {
		want = len(pool)
	}
	candidates := make([]string, 0, want)
	taken := map[int]struct{}{}
	for i := 0; i < want; i++ {
		unit := deterministicUnit(match.ServerSeed, tick, "draft_draw", roundIndex*draftPickCount+i)
		pos := int(unit * float64(len(pool)))
		if pos >= len(pool) {
			pos = len(pool) - 1
		}
		for {
			if _, ok := taken[pos]; !ok {
				break
			}
			pos = (pos + 1) % len(pool)
		}
		taken[pos] = struct{}{}
		candidates = append(candidates, pool[pos])
	}
	return candidates
}

// ReportBossDefeat is the clearly named lobby entry point that drives the
// mixed-pool draft from a boss defeat. It is server-authoritative and follows
// the allocation-ownership rules of SubmitBattleEvents.
func (s *Service) ReportBossDefeat(req BossDefeatReportRequest) (*BossDefeatReportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	match, ok := s.matches[req.MatchID]
	if !ok {
		return nil, newError(codeNotFound, "match not found")
	}
	allocation := s.battleAllocations[req.MatchID]
	if allocation == nil {
		allocation = match.BattleAllocation
	}
	if allocation != nil && strings.TrimSpace(req.BattleServerID) != "" && req.BattleServerID != allocation.BattleServerID {
		return nil, newError(codeBattleServer, "boss defeat server id %q does not match battle server %q", req.BattleServerID, allocation.BattleServerID)
	}
	if match.Status != "running" {
		return nil, newError(codeMatchState, "boss defeat requires a running match, got %s", match.Status)
	}
	if _, ok := match.Players[req.UserID]; !ok {
		return nil, newError(codeInvalidRequest, "boss defeat user %q is not a player of match %q", req.UserID, match.MatchID)
	}
	if req.Tick < 0 {
		return nil, newError(codeInvalidRequest, "boss defeat tick must not be negative")
	}
	bossID := strings.TrimSpace(req.BossID)
	if bossID == "" {
		return nil, newError(codeInvalidRequest, "boss id is required")
	}
	round, err := s.reportBossDefeatLocked(match, req.UserID, bossID, req.Tick)
	if err != nil {
		return nil, err
	}
	return &BossDefeatReportResponse{
		OK:                  true,
		Version:             currentVersionStamp(),
		MatchID:             match.MatchID,
		RoundIndex:          round.RoundIndex,
		Status:              "recorded",
		ServerAuthoritative: true,
		ServerTime:          s.clock(),
	}, nil
}

// reportBossDefeatLocked routes a boss-defeat report into the draft engine. It
// is idempotent per round: a duplicate report is a no-op that emits
// draft_skipped, and a same-tick double defeat is resolved deterministically.
func (s *Service) reportBossDefeatLocked(match *matchState, userID string, bossID string, tick int) (*draftRound, error) {
	if match.ModeID != "battle_royale" {
		return nil, newError(codeModeAction, "mixed-pool draft requires battle_royale")
	}
	round := match.Draft
	switch {
	case round == nil:
		return s.openDraftRoundLocked(match, userID, bossID, tick)
	case round.BossID == bossID:
		return s.recordDraftDefeatLocked(match, round, userID, tick)
	case !round.Granted:
		// A different boss while a round is still open: never run two
		// concurrent draft rounds; record an auditable skip.
		appendMatchEventLocked(match, MatchEvent{
			Type: "draft_skipped", Tick: tick, UserID: userID, ActionID: bossID,
			RoundIndex: round.RoundIndex, Reason: "draft_in_progress", Status: "skipped",
		})
		return round, nil
	default:
		return s.openDraftRoundLocked(match, userID, bossID, tick)
	}
}

func (s *Service) openDraftRoundLocked(match *matchState, firstUser string, bossID string, tick int) (*draftRound, error) {
	match.DraftRoundSeq++
	roundIndex := match.DraftRoundSeq
	pool := buildMixedPool(match)
	candidates := drawMixedPoolCandidates(match, roundIndex, tick)
	round := &draftRound{
		RoundIndex:         roundIndex,
		BossID:             bossID,
		Candidates:         candidates,
		PoolHash:           mixedPoolHash(pool),
		FirstUser:          firstUser,
		FirstDefeatTick:    tick,
		Picks:              map[string]string{},
		ChoiceDeadlineTick: tick + TickRate*30,
	}
	match.Draft = round
	appendDraftOpenLocked(match, round, tick, "open")
	publishDraftStateLocked(match, round)
	return round, nil
}

// appendDraftOpenLocked emits the round-opening event. It is emitted once with
// status "open", and again (with status "reordered" / "same_tick_tiebreak")
// whenever the first/second picker assignment is corrected, so the seat
// decision is auditable.
func appendDraftOpenLocked(match *matchState, round *draftRound, tick int, status string) {
	appendMatchEventLocked(match, MatchEvent{
		Type: "draft_open", Tick: tick, ActionID: round.BossID, ActionType: "draft_open",
		UserID: round.FirstUser, FromUserID: round.SecondUser, CardID: strings.Join(round.Candidates, ","),
		RoundIndex: round.RoundIndex, ExpiresTick: round.ChoiceDeadlineTick,
		Status: status, Reason: round.PoolHash, Value: len(round.Candidates),
	})
}

func (s *Service) recordDraftDefeatLocked(match *matchState, round *draftRound, userID string, tick int) (*draftRound, error) {
	if round.Granted || userID == round.FirstUser || (round.SecondUser != "" && userID == round.SecondUser) {
		appendMatchEventLocked(match, MatchEvent{
			Type: "draft_skipped", Tick: tick, UserID: userID, ActionID: round.BossID,
			RoundIndex: round.RoundIndex, Reason: "already_drawn", Status: "skipped",
		})
		return round, nil
	}
	if round.SecondUser != "" {
		appendMatchEventLocked(match, MatchEvent{
			Type: "draft_skipped", Tick: tick, UserID: userID, ActionID: round.BossID,
			RoundIndex: round.RoundIndex, Reason: "round_full", Status: "skipped",
		})
		return round, nil
	}
	switch {
	case tick < round.FirstDefeatTick:
		// Out-of-order arrival: this report is actually the earlier defeat.
		round.SecondUser = round.FirstUser
		round.FirstUser = userID
		round.FirstDefeatTick = tick
		round.ChoiceDeadlineTick = tick + TickRate*30
		appendDraftOpenLocked(match, round, tick, "reordered")
	case tick == round.FirstDefeatTick:
		// Same tick: the first picker is decided by sortedPlayerIDs order,
		// never by arrival order.
		round.SameTickTiebreak = true
		order := sortedPlayerIDs(match)
		if indexOfString(order, userID) < indexOfString(order, round.FirstUser) {
			round.SecondUser = round.FirstUser
			round.FirstUser = userID
		} else {
			round.SecondUser = userID
		}
		appendDraftOpenLocked(match, round, tick, "same_tick_tiebreak")
	default:
		round.SecondUser = userID
	}
	publishDraftStateLocked(match, round)
	return round, nil
}

// applyMixedPoolDraftPickLocked records one seat's pick. It enforces the
// two-phase rule: only the designated first picker may pick while no pick
// exists, and the second picker may only take a card the first picker left.
// It never grants; the grant happens once both picks exist (see
// maybeGrantDraftLocked).
func applyMixedPoolDraftPickLocked(match *matchState, player *playerState, actionID string, payload map[string]any) (MatchEvent, error) {
	round := match.Draft
	if round == nil || round.Granted {
		return MatchEvent{}, newError(codeModeAction, "no open draft round")
	}
	cardID, err := draftPickCardID(round, payload)
	if err != nil {
		return MatchEvent{}, err
	}
	role := ""
	switch {
	case player.UserID == round.FirstUser && len(round.Picks) == 0:
		role = "first"
	case round.SecondUser != "" && player.UserID == round.SecondUser && len(round.Picks) == 1:
		if firstCard, ok := round.Picks[round.FirstUser]; ok && cardID == firstCard {
			return MatchEvent{}, newError(codeModeAction, "card already taken by the first picker")
		}
		role = "second"
	default:
		return MatchEvent{}, newError(codeModeAction, "not the designated picker for this round")
	}
	if !stringSliceContains(round.Candidates, cardID) {
		return MatchEvent{}, newError(codeModeAction, "card is not a server candidate")
	}
	round.Picks[player.UserID] = cardID
	publishDraftStateLocked(match, round)
	return MatchEvent{
		Type: "draft_pick", Tick: match.Tick, UserID: player.UserID, ActionID: actionID,
		ActionType: "select_round_card", CardID: cardID, RoundIndex: round.RoundIndex, Status: role,
	}, nil
}

func draftPickCardID(round *draftRound, payload map[string]any) (string, error) {
	// asString(nil) renders "<nil>"; normalise it away so a candidate_index
	// payload is not mistaken for a card id.
	cardID := strings.TrimSpace(asString(payload["card_id"]))
	if cardID == "<nil>" {
		cardID = ""
	}
	if cardID != "" {
		return cardID, nil
	}
	// The C++ battle intent validates select_round_card by candidate_index
	// (0..2); accept it too and resolve against this round's candidates.
	if index, ok := asInt(payload["candidate_index"]); ok {
		if index < 0 || index >= len(round.Candidates) {
			return "", newError(codeModeAction, "candidate_index is out of range")
		}
		return round.Candidates[index], nil
	}
	return "", newError(codeModeAction, "card_id or candidate_index is required")
}

// maybeGrantDraftLocked grants both cards in one step once every designated
// picker has picked. A round where the second player never reported stays open
// until the deadline (see resolveDraftTimeoutLocked).
func maybeGrantDraftLocked(match *matchState) {
	round := match.Draft
	if round == nil || round.Granted || !draftAllPicked(round) {
		return
	}
	grantDraftLocked(match, round, match.Tick)
}

func draftAllPicked(round *draftRound) bool {
	if round.FirstUser == "" {
		return false
	}
	if _, ok := round.Picks[round.FirstUser]; !ok {
		return false
	}
	if round.SecondUser == "" {
		return false
	}
	if _, ok := round.Picks[round.SecondUser]; !ok {
		return false
	}
	return true
}

// grantDraftLocked writes both picks into their owners' hands inside a single
// critical section and marks the round granted. This is the only point at
// which a draft card reaches a hand.
func grantDraftLocked(match *matchState, round *draftRound, tick int) {
	grantID := fmt.Sprintf("%s_g%06d", match.MatchID, round.RoundIndex)
	for _, userID := range []string{round.FirstUser, round.SecondUser} {
		if userID == "" {
			continue
		}
		cardID, ok := round.Picks[userID]
		if !ok {
			continue
		}
		player := match.Players[userID]
		if player == nil {
			continue
		}
		if !grantCardToPlayer(player, cardID) {
			appendMatchEventLocked(match, MatchEvent{
				Type: "card_grant_deferred", Tick: tick, UserID: userID, CardID: cardID,
				ActionID: grantID, RoundIndex: round.RoundIndex, Reason: "hand_full", Status: "deferred",
			})
		}
		appendMatchEventLocked(match, MatchEvent{
			Type: "draft_grant", Tick: tick, UserID: userID, CardID: cardID,
			ActionID: grantID, RoundIndex: round.RoundIndex, Status: "granted",
		})
	}
	round.Granted = true
	publishDraftStateLocked(match, round)
}

// resolveDraftTimeoutLocked closes a round that reached its deadline so it can
// never hang. Every designated picker without a pick auto-picks deterministically.
func resolveDraftTimeoutLocked(match *matchState) {
	round := match.Draft
	if round == nil || round.Granted || match.Tick < round.ChoiceDeadlineTick {
		return
	}
	for _, userID := range []string{round.FirstUser, round.SecondUser} {
		if userID == "" {
			continue
		}
		if _, ok := round.Picks[userID]; ok {
			continue
		}
		cardID := autoDraftPick(match, round)
		if cardID == "" {
			continue
		}
		round.Picks[userID] = cardID
		appendMatchEventLocked(match, MatchEvent{
			Type: "draft_timeout", Tick: match.Tick, UserID: userID, CardID: cardID,
			RoundIndex: round.RoundIndex, Reason: "choice_deadline_expired", Status: "auto",
		})
	}
	grantDraftLocked(match, round, match.Tick)
}

// autoDraftPick picks deterministically among the cards not yet taken, ordered
// by (unit asc, cardID asc).
func autoDraftPick(match *matchState, round *draftRound) string {
	type scored struct {
		cardID string
		unit   float64
	}
	remaining := []scored{}
	for _, cardID := range round.Candidates {
		if draftCardTaken(round, cardID) {
			continue
		}
		remaining = append(remaining, scored{
			cardID: cardID,
			unit:   deterministicUnit(match.ServerSeed, round.ChoiceDeadlineTick, "draft_auto", len(remaining)),
		})
	}
	if len(remaining) == 0 {
		return ""
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].unit != remaining[j].unit {
			return remaining[i].unit < remaining[j].unit
		}
		return remaining[i].cardID < remaining[j].cardID
	})
	return remaining[0].cardID
}

func draftCardTaken(round *draftRound, cardID string) bool {
	for _, picked := range round.Picks {
		if picked == cardID {
			return true
		}
	}
	return false
}

// grantCardToPlayer appends to the hand, or defers to PendingGrants when the
// hand is full (documented default). It reports whether the card landed in the
// hand immediately.
func grantCardToPlayer(player *playerState, cardID string) bool {
	if player == nil || cardID == "" {
		return false
	}
	if len(player.Hand) < cardHandLimit {
		player.Hand = append(player.Hand, cardID)
		return true
	}
	player.PendingGrants = append(player.PendingGrants, cardID)
	return false
}

// promotePendingGrants moves deferred cards into the hand when a played card
// frees a slot.
func promotePendingGrants(player *playerState) {
	for player != nil && len(player.PendingGrants) > 0 && len(player.Hand) < cardHandLimit {
		cardID := player.PendingGrants[0]
		player.PendingGrants = player.PendingGrants[1:]
		player.Hand = append(player.Hand, cardID)
	}
}

func publishDraftStateLocked(match *matchState, round *draftRound) {
	if match.ModeState == nil {
		match.ModeState = map[string]any{}
	}
	picks := map[string]any{}
	for userID, cardID := range round.Picks {
		picks[userID] = cardID
	}
	match.ModeState["draft"] = map[string]any{
		"round_index":          round.RoundIndex,
		"boss_id":              round.BossID,
		"candidates":           append([]string{}, round.Candidates...),
		"first_user":           round.FirstUser,
		"second_user":          round.SecondUser,
		"picks":                picks,
		"granted":              round.Granted,
		"choice_deadline_tick": round.ChoiceDeadlineTick,
		"public_pool_hash":     round.PoolHash,
		"zero_round_order":     sortedPlayerIDs(match),
		"same_tick_tiebreak":   round.SameTickTiebreak,
	}
}

func indexOfString(values []string, needle string) int {
	for index, value := range values {
		if value == needle {
			return index
		}
	}
	return -1
}
