package core

import (
	"testing"
	"time"
)

func runningMatch(t *testing.T, service *Service, alice *AuthSession, bob *AuthSession) string {
	t.Helper()
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")
	if _, err := service.ReadyMatch(alice.SessionToken, matchID); err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if _, err := service.ReadyMatch(bob.SessionToken, matchID); err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	return matchID
}

func playerIDsByUser(t *testing.T, service *Service, matchID string, users ...string) map[string]string {
	t.Helper()
	allocation, ok := service.BattleAllocationForMatch(matchID)
	if !ok {
		t.Fatalf("allocation for match %s not found", matchID)
	}
	wanted := map[string]struct{}{}
	for _, user := range users {
		wanted[user] = struct{}{}
	}
	out := map[string]string{}
	for _, player := range allocation.Players {
		if _, want := wanted[player.UserID]; want {
			out[player.UserID] = player.PlayerID
		}
	}
	return out
}

func TestApplyBattleResultCallbackSettlesAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice")
	bob := mustLogin(t, service, "Bob")
	matchID := runningMatch(t, service, alice, bob)
	ids := playerIDsByUser(t, service, matchID, alice.UserID, bob.UserID)
	alicePlayer, bobPlayer := ids[alice.UserID], ids[bob.UserID]

	req := BattleResultCallback{
		MatchID:        matchID,
		ModeID:         "mvp_boss_race",
		RulesetVersion: RulesetVersion,
		WinnerPlayerID: alicePlayer,
		WinnerTick:     1200,
		StateHash:      "sha256:state-1",
		Players: []BattleResultPlayer{
			{PlayerID: alicePlayer, DamageDealt: 1000, BossCurrentHP: 0},
			{PlayerID: bobPlayer, DamageDealt: 800, BossCurrentHP: 250},
		},
	}
	resp, err := service.ApplyBattleResultCallback(req)
	if err != nil {
		t.Fatalf("apply callback: %v", err)
	}
	if !resp.OK || resp.Duplicate {
		t.Fatalf("unexpected first response: %+v", resp)
	}
	if resp.WinnerPlayerID != alicePlayer || resp.WinnerUserID != alice.UserID {
		t.Fatalf("winner mismatch: %+v", resp)
	}
	if resp.Points[alicePlayer] != bossRaceWinPoints || resp.Points[bobPlayer] != bossRaceLossPoints {
		t.Fatalf("points mismatch: %+v", resp.Points)
	}
	if resp.ReplayID == "" {
		t.Fatalf("expected a replay id")
	}

	service.mu.Lock()
	match := service.matches[matchID]
	aliceSettlement := service.settlements[settlementKey(matchID, alice.UserID)]
	bobSettlement := service.settlements[settlementKey(matchID, bob.UserID)]
	service.mu.Unlock()

	if match == nil || match.Status != "ended" {
		t.Fatalf("match should be ended: %+v", match)
	}
	if aliceSettlement == nil || aliceSettlement.Result != "win" {
		t.Fatalf("alice settlement should be a win: %+v", aliceSettlement)
	}
	if bobSettlement == nil || bobSettlement.Result != "loss" {
		t.Fatalf("bob settlement should be a loss: %+v", bobSettlement)
	}

	duplicate, err := service.ApplyBattleResultCallback(req)
	if err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if !duplicate.Duplicate || duplicate.ReplayID != resp.ReplayID {
		t.Fatalf("expected idempotent duplicate: %+v", duplicate)
	}

	conflicting := req
	conflicting.StateHash = "sha256:other"
	if _, err := service.ApplyBattleResultCallback(conflicting); err == nil {
		t.Fatalf("expected conflict error for a different state hash")
	}
}

func TestApplyBattleResultCallbackUnknownMatch(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.ApplyBattleResultCallback(BattleResultCallback{MatchID: "match_missing"}); err == nil {
		t.Fatalf("expected not_found for unknown match")
	}
}

func TestBindBattleServerAllocationReissuesTickets(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice")
	bob := mustLogin(t, service, "Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	before, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket before bind: %v", err)
	}
	binding, err := service.BindBattleServerAllocation(matchID, "battle-test-1", "10.0.0.5:5555")
	if err != nil {
		t.Fatalf("bind allocation: %v", err)
	}
	if binding.Endpoint != "10.0.0.5:5555" || binding.BattleServerID != "battle-test-1" {
		t.Fatalf("binding mismatch: %+v", binding)
	}
	after, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket after bind: %v", err)
	}
	if after.Ticket.Endpoint != "10.0.0.5:5555" {
		t.Fatalf("ticket endpoint not updated: %+v", after.Ticket)
	}
	if after.Ticket.TicketID == before.Ticket.TicketID {
		t.Fatalf("expected a freshly issued ticket after bind")
	}
	if _, err := service.BindBattleServerAllocation("match_missing", "x", "1.2.3.4:1"); err == nil {
		t.Fatalf("expected not_found for unknown match")
	}
}

func TestMatchStartReadOnly(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice")
	bob := mustLogin(t, service, "Bob")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "alice_deck",
		DeckSnapshot: validDeck("alice_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_deck",
		DeckSnapshot: validDeck("bob_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	matchID := joined.MatchID
	if matchID == "" {
		t.Fatalf("expected a match after the room filled up")
	}

	if _, _, err := service.MatchStart(alice.SessionToken, matchID); err == nil {
		t.Fatalf("match start should fail while the match is loading")
	}
	if _, err := service.ReadyMatch(alice.SessionToken, matchID); err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if _, err := service.ReadyMatch(bob.SessionToken, matchID); err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	event, ticket, err := service.MatchStart(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("match start: %v", err)
	}
	if event.MatchID != matchID || event.BattleAllocation == nil {
		t.Fatalf("unexpected match start event: %+v", event)
	}
	if ticket == nil || ticket.Ticket.MatchID != matchID {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}
}
