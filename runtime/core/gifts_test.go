package core

import (
	"fmt"
	"testing"
)

// TestGiftCatalogContractReplaysClientTable pins the server gift table to the
// client catalog (SpellKard laya src/core/game/gift_catalog.ts): every id the
// client can send must resolve here with the same name, the table must hold no
// extra entries, and an id outside the table must not resolve. A drift on
// either side is a protocol break, so the whole table is asserted rather than
// sampled.
func TestGiftCatalogContractReplaysClientTable(t *testing.T) {
	want := []struct {
		giftID string
		name   string
		cost   int
	}{
		{"heart", "心愿", 1},
		{"star", "星砂", 1},
		{"ribbon", "丝带", 1},
		{"lantern", "灯笼", 2},
		{"wand", "御币", 2},
		{"cup", "茶", 3},
	}
	if len(giftCatalog) != len(want) {
		t.Fatalf("gift catalog holds %d entries, want %d", len(giftCatalog), len(want))
	}
	total := 0
	for _, expected := range want {
		definition, ok := giftDefinitionFor(expected.giftID)
		if !ok {
			t.Fatalf("gift %q missing from the server catalog", expected.giftID)
		}
		if definition.Name != expected.name || definition.Cost != expected.cost {
			t.Fatalf("gift %q = {name %q, cost %d}, want {name %q, cost %d}", expected.giftID, definition.Name, definition.Cost, expected.name, expected.cost)
		}
		total += definition.Cost
	}
	// One full pass of the catalog must out-cost the per-match budget, so the
	// charge pool stays a real choice rather than a formality.
	if total <= giftChargeBudget {
		t.Fatalf("catalog total = %d charges, want more than the %d-charge budget", total, giftChargeBudget)
	}
	// Lookup is exact: the caller trims the payload, so padded or cased ids
	// must not resolve by accident.
	for _, unknown := range []string{"", "diamond", "Heart", "heart "} {
		if _, ok := giftDefinitionFor(unknown); ok {
			t.Fatalf("gift id %q must not resolve", unknown)
		}
	}
}

// giftMatchFixture logs in two players and runs them into a started
// two-player match — the only seat layout a gift can target.
func giftMatchFixture(t *testing.T, modeID string) (*Service, []*AuthSession, string) {
	t.Helper()
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Gift 1"),
		mustLogin(t, service, "Gift 2"),
	}
	matchID := matchPlayers(t, service, players, modeID)
	if status := service.matches[matchID].Status; status != "running" {
		t.Fatalf("fixture match status = %s, want running", status)
	}
	return service, players, matchID
}

// submitGift sends one gift mode action and fails the test on an RPC-level
// error: every gift refusal is a domain rejection the client can render.
func submitGift(t *testing.T, service *Service, session *AuthSession, matchID string, giftID string) *ModeActionResponse {
	t.Helper()
	response, err := service.SubmitModeAction(session.SessionToken, matchID, map[string]any{
		"mode_id":     service.matches[matchID].ModeID,
		"action_type": "gift",
		"payload":     map[string]any{"gift_id": giftID},
	})
	if err != nil {
		t.Fatalf("gift %q should be a domain rejection, got rpc error: %v", giftID, err)
	}
	return response
}

// giftChargesUsed reads a player's spent gift charges for the match.
func giftChargesUsed(service *Service, matchID string, userID string) int {
	return service.matches[matchID].GiftChargesUsed[userID]
}

// countGiftSentEvents counts the gift_sent rows on a match's event stream.
func countGiftSentEvents(t *testing.T, service *Service, session *AuthSession, matchID string) int {
	t.Helper()
	events, err := service.MatchEvents(session.SessionToken, matchID, 0, 64)
	if err != nil {
		t.Fatalf("match events: %v", err)
	}
	count := 0
	for _, event := range events.Events {
		if event.Type == "gift_sent" {
			count++
		}
	}
	return count
}

// TestGiftModeActionSpendsChargesAndAnnouncesGiftSent walks the happy path: an
// in-budget gift is accepted, the sender's charge counter rises by exactly the
// catalog cost, the receiver is never charged, the card energy of both players
// is untouched, and one gift_sent event lands on the match event stream with
// the client's field contract (card_id = gift id, user_id = sender,
// target_user_id = receiver). The deterministic simulation (tick, boss HP,
// bullets) must not move.
func TestGiftModeActionSpendsChargesAndAnnouncesGiftSent(t *testing.T) {
	service, players, matchID := giftMatchFixture(t, "pvp_duel")
	match := service.matches[matchID]
	sender := match.Players[players[0].UserID]
	receiver := match.Players[players[1].UserID]
	beforeTick, beforeBossHP, beforeBullets := match.Tick, match.BossHP, len(match.Bullets)

	response := submitGift(t, service, players[0], matchID, "ribbon")
	if !response.Accepted || response.Status != "accepted" || response.Reason != "none" {
		t.Fatalf("gift response invalid: %+v", response)
	}
	event := response.Event
	if event.Type != "gift_sent" || event.CardID != "ribbon" || event.UserID != players[0].UserID || event.TargetUserID != players[1].UserID {
		t.Fatalf("gift_sent event fields wrong: %+v", event)
	}
	if event.ActionType != "gift" || event.ActionID == "" || event.Value != 1 {
		t.Fatalf("gift_sent event metadata wrong: %+v", event)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != 1 {
		t.Fatalf("sender gift charges used = %d, want 1 (ribbon 1)", used)
	}
	if _, charged := match.GiftChargesUsed[players[1].UserID]; charged {
		t.Fatalf("receiver was charged gift charges: %+v", match.GiftChargesUsed)
	}
	if sender.Energy != cardStartingEnergy || receiver.Energy != cardStartingEnergy {
		t.Fatalf("gift touched card energy: sender %v receiver %v, want untouched %v", sender.Energy, receiver.Energy, cardStartingEnergy)
	}
	if match.Tick != beforeTick || match.BossHP != beforeBossHP || len(match.Bullets) != beforeBullets {
		t.Fatalf("gift moved the deterministic simulation: tick %d->%d boss %d->%d bullets %d->%d", beforeTick, match.Tick, beforeBossHP, match.BossHP, beforeBullets, len(match.Bullets))
	}
	if count := countGiftSentEvents(t, service, players[0], matchID); count != 1 {
		t.Fatalf("event stream holds %d gift_sent events, want 1", count)
	}

	// Gifts are repeatable social actions (no one-shot dedupe): a second gift
	// of another id is accepted and priced again.
	repeat := submitGift(t, service, players[0], matchID, "heart")
	if !repeat.Accepted {
		t.Fatalf("a repeat gift should be accepted: %+v", repeat)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != 2 {
		t.Fatalf("sender gift charges used = %d, want 2 (ribbon 1 + heart 1)", used)
	}
	if count := countGiftSentEvents(t, service, players[0], matchID); count != 2 {
		t.Fatalf("event stream holds %d gift_sent events, want 2", count)
	}

	// Charges accumulate across expensive gifts: cup costs 3, draining the
	// 5-charge budget exactly.
	cup := submitGift(t, service, players[0], matchID, "cup")
	if !cup.Accepted || cup.Event.Value != 3 {
		t.Fatalf("cup gift invalid: %+v", cup)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != giftChargeBudget {
		t.Fatalf("sender gift charges used = %d, want the %d-charge budget fully drained", used, giftChargeBudget)
	}
}

// TestGiftModeActionIgnoresCardEnergy proves the two pools are decoupled: a
// sender with no card energy at all can still send a gift, and sending one
// leaves the card energy untouched.
func TestGiftModeActionIgnoresCardEnergy(t *testing.T) {
	service, players, matchID := giftMatchFixture(t, "pvp_duel")
	sender := service.matches[matchID].Players[players[0].UserID]
	service.mu.Lock()
	sender.Energy = 0 // no card energy whatsoever
	service.mu.Unlock()

	response := submitGift(t, service, players[0], matchID, "heart")
	if !response.Accepted {
		t.Fatalf("gift must not depend on card energy: %+v", response)
	}
	if sender.Energy != 0 {
		t.Fatalf("gift touched card energy: %v", sender.Energy)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != 1 {
		t.Fatalf("gift charges used = %d, want 1", used)
	}
}

// TestGiftModeActionRejectsExhaustedGiftCharges: the per-match charge budget
// caps the social interaction. cup(3)+wand(2) exactly drains the 5-charge
// budget, the next gift — even the cheapest — is refused, and a refused gift
// neither spends a charge nor writes a gift_sent event.
func TestGiftModeActionRejectsExhaustedGiftCharges(t *testing.T) {
	service, players, matchID := giftMatchFixture(t, "pvp_duel")

	cup := submitGift(t, service, players[0], matchID, "cup")
	if !cup.Accepted {
		t.Fatalf("cup gift rejected: %+v", cup)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != 3 {
		t.Fatalf("gift charges used = %d, want 3 (cup 3)", used)
	}
	wand := submitGift(t, service, players[0], matchID, "wand")
	if !wand.Accepted {
		t.Fatalf("wand gift rejected: %+v", wand)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != giftChargeBudget {
		t.Fatalf("gift charges used = %d, want the %d-charge budget fully drained", used, giftChargeBudget)
	}

	beforeEvents := countGiftSentEvents(t, service, players[0], matchID)
	rejected := submitGift(t, service, players[0], matchID, "heart")
	if rejected.Accepted || rejected.Reason != codeInvalidRequest || rejected.Status != "rejected" {
		t.Fatalf("expected exhausted-charge rejection, got %+v", rejected)
	}
	if used := giftChargesUsed(service, matchID, players[0].UserID); used != giftChargeBudget {
		t.Fatalf("refused gift spent a charge: used = %d", used)
	}
	if count := countGiftSentEvents(t, service, players[0], matchID); count != beforeEvents {
		t.Fatalf("refused gift wrote a gift_sent event: %d -> %d", beforeEvents, count)
	}
}

// TestGiftModeActionRejectsUnknownGiftWithoutSpendingCharges: an id outside the
// catalog is an invalid_request domain rejection, spends no charge, and writes
// no gift_sent event. A missing gift_id is a mode-action rejection, not an
// accept.
func TestGiftModeActionRejectsUnknownGiftWithoutSpendingCharges(t *testing.T) {
	service, players, matchID := giftMatchFixture(t, "pvp_duel")
	sender := service.matches[matchID].Players[players[0].UserID]

	response := submitGift(t, service, players[0], matchID, "diamond")
	if response.Accepted || response.Reason != codeInvalidRequest || response.Status != "rejected" {
		t.Fatalf("expected unknown-gift rejection, got %+v", response)
	}
	if _, charged := service.matches[matchID].GiftChargesUsed[players[0].UserID]; charged {
		t.Fatalf("rejected gift created a charge entry: %+v", service.matches[matchID].GiftChargesUsed)
	}
	if sender.Energy != cardStartingEnergy {
		t.Fatalf("rejected gift touched card energy: %v", sender.Energy)
	}
	if count := countGiftSentEvents(t, service, players[0], matchID); count != 0 {
		t.Fatalf("rejected gift wrote %d gift_sent events, want 0", count)
	}

	missing, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "pvp_duel",
		"action_type": "gift",
		"payload":     map[string]any{},
	})
	if err != nil {
		t.Fatalf("missing gift_id should be a domain rejection: %v", err)
	}
	if missing.Accepted || missing.Reason != codeModeAction {
		t.Fatalf("expected missing gift_id rejection, got %+v", missing)
	}
	if _, charged := service.matches[matchID].GiftChargesUsed[players[0].UserID]; charged {
		t.Fatalf("missing-gift rejection created a charge entry: %+v", service.matches[matchID].GiftChargesUsed)
	}
}

// TestGiftModeActionRejectsNonRunningMatch: before every seat readies (match
// still loading) the gift branch itself refuses with match_state_invalid; once
// the match is ended the RPC entry refuses before the branch runs.
func TestGiftModeActionRejectsNonRunningMatch(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Gift Loading 1"),
		mustLogin(t, service, "Gift Loading 2"),
	}
	matchID := ""
	for index, player := range players {
		deckID := fmt.Sprintf("%s_deck_%d", player.UserID, index)
		queue, err := service.JoinQueue(player.SessionToken, JoinQueueRequest{
			ModeID:       "pvp_duel",
			ActiveDeckID: deckID,
			DeckSnapshot: validDeck(deckID),
		})
		if err != nil {
			t.Fatalf("join player %d: %v", index, err)
		}
		if queue.MatchID != "" {
			matchID = queue.MatchID
		}
	}
	if matchID == "" {
		t.Fatalf("expected a match id before readying")
	}
	if status := service.matches[matchID].Status; status != "loading" {
		t.Fatalf("fixture match status = %s, want loading", status)
	}
	loading, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "pvp_duel",
		"action_type": "gift",
		"payload":     map[string]any{"gift_id": "heart"},
	})
	if err != nil {
		t.Fatalf("loading-match gift should be a domain rejection: %v", err)
	}
	if loading.Accepted || loading.Reason != codeMatchState {
		t.Fatalf("expected loading-match rejection, got %+v", loading)
	}

	service.mu.Lock()
	service.matches[matchID].Status = "ended"
	service.mu.Unlock()
	if _, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "pvp_duel",
		"action_type": "gift",
		"payload":     map[string]any{"gift_id": "heart"},
	}); ErrorCode(err) != codeMatchState {
		t.Fatalf("expected ended-match rejection, got %v", err)
	}
}

// TestGiftModeActionRejectsPartyModeWithoutUniqueOpponent: the gift payload
// carries no target, so a mode seating more than two players has no unique
// receiver and the action is refused rather than guessing one.
func TestGiftModeActionRejectsPartyModeWithoutUniqueOpponent(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Gift Party 1"),
		mustLogin(t, service, "Gift Party 2"),
		mustLogin(t, service, "Gift Party 3"),
		mustLogin(t, service, "Gift Party 4"),
	}
	matchID := matchPlayers(t, service, players, "world_boss")
	response := submitGift(t, service, players[0], matchID, "heart")
	if response.Accepted || response.Reason != codeModeAction {
		t.Fatalf("expected party-mode gift rejection, got %+v", response)
	}
	if _, charged := service.matches[matchID].GiftChargesUsed[players[0].UserID]; charged {
		t.Fatalf("rejected party gift created a charge entry: %+v", service.matches[matchID].GiftChargesUsed)
	}
	if count := countGiftSentEvents(t, service, players[0], matchID); count != 0 {
		t.Fatalf("rejected party gift wrote %d gift_sent events, want 0", count)
	}
}
