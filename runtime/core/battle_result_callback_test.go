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

func battleResultCallbackForAllocation(t *testing.T, allocation *BattleServerAllocation, winnerPlayerID string) BattleResultCallback {
	t.Helper()
	if allocation == nil {
		t.Fatal("battle allocation is nil")
	}
	if len(allocation.Players) == 0 {
		t.Fatal("battle allocation has no players")
	}
	if winnerPlayerID == "" {
		winnerPlayerID = allocation.Players[0].PlayerID
	}
	players := make([]BattleResultPlayer, 0, len(allocation.Players))
	for index, player := range allocation.Players {
		bossCurrentHP := int64(250)
		if player.PlayerID == winnerPlayerID {
			bossCurrentHP = 0
		}
		players = append(players, BattleResultPlayer{
			PlayerID:      player.PlayerID,
			DamageDealt:   int64(1000 - index*200),
			BossCurrentHP: bossCurrentHP,
		})
	}
	return BattleResultCallback{
		MatchID:        allocation.MatchID,
		ModeID:         allocation.ModeID,
		RulesetVersion: allocation.Version.RulesetVersion,
		MatchSeed:      uint64(allocation.ServerSeed),
		WinnerPlayerID: winnerPlayerID,
		WinnerTick:     1200,
		StateHash:      "sha256:callback-state",
		Players:        players,
	}
}

func TestApplyBattleResultCallbackSettlesAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice")
	bob := mustLogin(t, service, "Bob")
	matchID := runningMatch(t, service, alice, bob)
	allocation, ok := service.BattleAllocationForMatch(matchID)
	if !ok {
		t.Fatalf("allocation for match %s not found", matchID)
	}
	ids := playerIDsByUser(t, service, matchID, alice.UserID, bob.UserID)
	alicePlayer, bobPlayer := ids[alice.UserID], ids[bob.UserID]

	req := BattleResultCallback{
		MatchID:        matchID,
		ModeID:         allocation.ModeID,
		RulesetVersion: allocation.Version.RulesetVersion,
		MatchSeed:      uint64(allocation.ServerSeed),
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

func TestApplyBattleResultCallbackRejectsAllocationContractMismatch(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Callback Contract Alice")
	bob := mustLogin(t, service, "Callback Contract Bob")
	matchID := runningMatch(t, service, alice, bob)
	allocation, ok := service.BattleAllocationForMatch(matchID)
	if !ok {
		t.Fatalf("allocation for match %s not found", matchID)
	}
	valid := battleResultCallbackForAllocation(t, allocation, allocation.Players[0].PlayerID)

	tests := []struct {
		name   string
		code   string
		mutate func(*BattleResultCallback)
	}{
		{
			name: "mode",
			code: codeInvalidMode,
			mutate: func(req *BattleResultCallback) {
				req.ModeID = "mode-tampered"
			},
		},
		{
			name: "ruleset",
			code: codeInvalidRequest,
			mutate: func(req *BattleResultCallback) {
				req.RulesetVersion = "ruleset-tampered"
			},
		},
		{
			name: "seed",
			code: codeInvalidRequest,
			mutate: func(req *BattleResultCallback) {
				req.MatchSeed++
			},
		},
		{
			name: "state_hash",
			code: codeInvalidRequest,
			mutate: func(req *BattleResultCallback) {
				req.StateHash = ""
			},
		},
		{
			name: "players",
			code: codeInvalidRequest,
			mutate: func(req *BattleResultCallback) {
				req.Players = req.Players[:1]
			},
		},
		{
			name: "winner",
			code: codeInvalidRequest,
			mutate: func(req *BattleResultCallback) {
				req.WinnerPlayerID = "player-tampered"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			req.Players = append([]BattleResultPlayer(nil), valid.Players...)
			test.mutate(&req)
			if _, err := service.ApplyBattleResultCallback(req); ErrorCode(err) != test.code {
				t.Fatalf("expected %s, got %v", test.code, err)
			}
		})
	}

	service.mu.Lock()
	match := service.matches[matchID]
	service.mu.Unlock()
	if match == nil || match.Status == "ended" || match.BattleResultHash != "" {
		t.Fatalf("rejected callbacks must not settle the match: %+v", match)
	}
}

func TestApplyBattleResultCallbackAuditsAcceptedDuplicateAndRejected(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                    func() time.Time { return now },
		BattleLifecycleAuditRepo: repo,
	})
	alice := mustLogin(t, service, "Callback Audit Alice")
	bob := mustLogin(t, service, "Callback Audit Bob")
	matchID := runningMatch(t, service, alice, bob)
	allocation, ok := service.BattleAllocationForMatch(matchID)
	if !ok {
		t.Fatalf("allocation for match %s not found", matchID)
	}
	valid := battleResultCallbackForAllocation(t, allocation, allocation.Players[0].PlayerID)

	rejected := valid
	rejected.StateHash = ""
	if _, err := service.ApplyBattleResultCallback(rejected); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected rejected callback, got %v", err)
	}
	if len(repo.results) != 1 || repo.results[0].Status != "rejected" || repo.results[0].RejectReason != codeInvalidRequest {
		t.Fatalf("rejected callback audit invalid: %+v", repo.results)
	}

	accepted, err := service.ApplyBattleResultCallback(valid)
	if err != nil {
		t.Fatalf("accepted callback: %v", err)
	}
	if !accepted.OK || accepted.Duplicate {
		t.Fatalf("unexpected accepted callback response: %+v", accepted)
	}
	duplicate, err := service.ApplyBattleResultCallback(valid)
	if err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if !duplicate.OK || !duplicate.Duplicate {
		t.Fatalf("unexpected duplicate callback response: %+v", duplicate)
	}
	if len(repo.results) != 3 {
		t.Fatalf("expected rejected, accepted and duplicate audits: %+v", repo.results)
	}
	if repo.results[1].Status != "accepted" || repo.results[1].MatchID != matchID || repo.results[1].BattleServerID != allocation.BattleServerID || repo.results[1].KeyID != allocation.BattleServerID || repo.results[1].ResultHash != valid.StateHash || len(repo.results[1].PlayerIDs) != len(allocation.Players) || repo.results[1].SettlementKey == "" || !repo.results[1].ServerAuthoritative {
		t.Fatalf("accepted callback audit invalid: %+v", repo.results[1])
	}
	if repo.results[2].Status != "duplicate" || repo.results[2].MatchID != matchID || repo.results[2].RejectReason != "" || !repo.results[2].ServerAuthoritative {
		t.Fatalf("duplicate callback audit invalid: %+v", repo.results[2])
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ResultRecords != 1 || status.ResultDuplicateRecords != 1 || status.ResultRejectedRecords != 1 || status.ReplayRecords != 2 || status.RejectedRecords != 0 || status.LastSuccessOperation != "battle_result_duplicate" {
		t.Fatalf("callback audit status invalid: %+v", status)
	}
}

func TestApplyBattleResultCallbackRejectsLoadingMatch(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Callback Loading Alice")
	bob := mustLogin(t, service, "Callback Loading Bob")
	first, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "loading-alice-deck",
		DeckSnapshot: validDeck("loading-alice-deck"),
	})
	if err != nil {
		t.Fatalf("join alice: %v", err)
	}
	second, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "loading-bob-deck",
		DeckSnapshot: validDeck("loading-bob-deck"),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	if first.MatchID != "" || second.MatchID == "" {
		t.Fatalf("expected a loading match after queue fill: first=%+v second=%+v", first, second)
	}
	allocation, ok := service.BattleAllocationForMatch(second.MatchID)
	if !ok {
		t.Fatalf("allocation for match %s not found", second.MatchID)
	}
	req := battleResultCallbackForAllocation(t, allocation, allocation.Players[0].PlayerID)
	if _, err := service.ApplyBattleResultCallback(req); ErrorCode(err) != codeMatchState {
		t.Fatalf("expected loading match rejection, got %v", err)
	}
	service.mu.Lock()
	match := service.matches[second.MatchID]
	service.mu.Unlock()
	if match == nil || match.Status != "loading" || match.BattleResultHash != "" {
		t.Fatalf("loading callback must not settle match: %+v", match)
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
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                    func() time.Time { return now },
		BattleLifecycleAuditRepo: repo,
	})
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
	revokedCount := 0
	var revoked BattleTicketAuditRecord
	for _, record := range repo.tickets {
		if record.Status == "revoked" {
			revokedCount++
			if record.TicketID == before.Ticket.TicketID {
				revoked = record
			}
		}
	}
	if revoked.TicketID == "" || !revoked.ConsumedAt.Equal(now) || revoked.ExpiresAt != before.Ticket.ExpiresAt || !revoked.ServerAuthoritative {
		t.Fatalf("allocation rebind must audit the old ticket as revoked: tickets=%+v", repo.tickets)
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || revokedCount != 2 || status.TicketRevokedRecords != revokedCount {
		t.Fatalf("allocation rebind should expose one revoked audit per old player ticket: count=%d status=%+v", revokedCount, status)
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
