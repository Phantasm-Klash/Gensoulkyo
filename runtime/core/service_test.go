package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	phkv1 "github.com/phantasm-klash/phk-protocol/gen/go/phk/v1"
)

func TestAuthoritativeMatchLifecycleSettlementAndClaims(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice")
	bob := mustLogin(t, service, "Bob")
	service.mu.Lock()
	aliceState := service.users[alice.UserID]
	aliceState.Certification.RankScore = 1255
	aliceState.Certification.Percentile = 0.34
	aliceState.Leaderboards["rank_score"] = LeaderboardRow{LeaderboardID: "rank_score", LabelKey: "leaderboard.rank_score", Score: 1255, Rank: 34, Percentile: 0.34, SeasonID: defaultSeasonID}
	service.mu.Unlock()

	firstQueue, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "alice_deck",
		DeckSnapshot: validDeck("alice_deck"),
	})
	if err != nil {
		t.Fatalf("join alice: %v", err)
	}
	if firstQueue.QueueStatus != "queued" || firstQueue.MatchID != "" {
		t.Fatalf("expected alice queued without match, got %+v", firstQueue)
	}

	secondQueue, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_deck",
		DeckSnapshot: validDeck("bob_deck"),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	if secondQueue.QueueStatus != "found" || secondQueue.MatchID == "" {
		t.Fatalf("expected match found, got %+v", secondQueue)
	}
	matchID := secondQueue.MatchID

	readyAlice, err := service.ReadyMatch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if readyAlice.ReadyStatus != "loading" || readyAlice.MatchStart != nil {
		t.Fatalf("alice should not start match alone: %+v", readyAlice)
	}
	readyBob, err := service.ReadyMatch(bob.SessionToken, matchID)
	if err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	if readyBob.ReadyStatus != "running" || readyBob.MatchStart == nil {
		t.Fatalf("bob ready should start match: %+v", readyBob)
	}
	if readyBob.MatchStart.ServerSeed == 0 || readyBob.MatchStart.InputDelayTicks != DefaultInputDelayTick {
		t.Fatalf("match start missing authority data: %+v", readyBob.MatchStart)
	}
	if readyBob.MatchStart.StageID != "starlit_lanes" || len(readyBob.MatchStart.Players) != 2 || readyBob.MatchStart.Players[0].Loadout.StageID == "" {
		t.Fatalf("match start missing server loadout: %+v", readyBob.MatchStart)
	}
	duplicateReadyAlice, err := service.ReadyMatch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("duplicate ready alice: %v", err)
	}
	if duplicateReadyAlice.ReadyStatus != "running" || duplicateReadyAlice.ReadyCount != 2 || duplicateReadyAlice.MatchStart == nil || duplicateReadyAlice.BattleTicket == nil {
		t.Fatalf("duplicate ready should be idempotent with running state: %+v", duplicateReadyAlice)
	}

	for i := 1; i <= 6; i++ {
		resp, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
			"tick":      i,
			"seq":       i,
			"dir":       6,
			"slow":      i%2 == 0,
			"shoot":     true,
			"bomb":      i == 2,
			"card_slot": -1,
		})
		if err != nil {
			t.Fatalf("input alice %d: %v", i, err)
		}
		if !resp.Accepted || resp.Snapshot.MatchID != matchID || resp.Snapshot.StateHash == "" {
			t.Fatalf("input response invalid: %+v", resp)
		}
		if i == 1 {
			assertAuthoritativeBulletSnapshot(t, resp.Snapshot)
		}
	}
	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":        7,
		"seq":         7,
		"dir":         0,
		"reward_json": []any{},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected forbidden reward field, got %v", err)
	}

	settlement, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{})
	if err != nil {
		t.Fatalf("settle alice: %v", err)
	}
	if !settlement.OK || settlement.Type != "match_end" || !settlement.ServerAuthoritative || settlement.ClientAuthoredReward {
		t.Fatalf("settlement must be server authoritative: %+v", settlement)
	}
	if settlement.ReplayID == "" || len(settlement.RewardJSON) == 0 || settlement.SettlementKey == "" {
		t.Fatalf("settlement missing replay/reward/idempotency data: %+v", settlement)
	}
	if settlement.StageID != "starlit_lanes" || settlement.Loadout.CharacterID == "" || !settlement.Loadout.ServerAuthoritative {
		t.Fatalf("settlement missing authoritative loadout: %+v", settlement)
	}
	if settlement.Loadout.RatingCode != defaultRatingCode {
		t.Fatalf("certification match should carry server rating loadout: %+v", settlement.Loadout)
	}
	if intFromAny(settlement.ModeResult["damage_dealt"]) <= 0 {
		t.Fatalf("settlement should include server-owned damage: %+v", settlement.ModeResult)
	}
	if intFromAny(settlement.ModeResult["rank_score_delta"]) <= 0 || intFromAny(settlement.ModeResult["rank_score_after"]) <= 1255 {
		t.Fatalf("certification rank should be server calculated: %+v", settlement.ModeResult)
	}
	if !boolFromAny(settlement.ModeResult["qualified_top_30"]) || !boolFromAny(settlement.ModeResult["next_certification_unlocked"]) {
		t.Fatalf("certification top 30 qualification should be server calculated: %+v", settlement.ModeResult)
	}
	if intFromAny(settlement.FinalResult["boss_hp_after"]) >= intFromAny(settlement.ModeResult["boss_max_hp"]) {
		t.Fatalf("boss hp should be reduced by server simulation: final=%+v mode=%+v", settlement.FinalResult, settlement.ModeResult)
	}
	replay, err := service.Replay(alice.SessionToken, settlement.ReplayID)
	if err != nil {
		t.Fatalf("replay alice: %v", err)
	}
	if !replay.OK || !replay.ServerAuthoritative || replay.ReplayID != settlement.ReplayID || replay.MatchID != matchID || replay.UserID != alice.UserID {
		t.Fatalf("replay authority envelope invalid: %+v", replay)
	}
	if replay.StageID != settlement.StageID || replay.Loadout.StageID != settlement.Loadout.StageID {
		t.Fatalf("replay missing loadout audit data: %+v", replay)
	}
	if replay.StateHash == "" || replay.InputCount == 0 || replay.EventCount == 0 || len(replay.Events) == 0 || replay.Settlement.ReplayID != settlement.ReplayID {
		t.Fatalf("replay missing audit data: %+v", replay)
	}
	goldenReplaySummary := map[string]any{
		"replay_id":         phkv1.GoldenReplaySummaryReplayID,
		"match_id":          phkv1.GoldenReplaySummaryMatchID,
		"owner_user_id":     phkv1.GoldenReplaySummaryOwnerUserID,
		"input_count":       phkv1.GoldenReplaySummaryInputCount,
		"event_count":       phkv1.GoldenReplaySummaryEventCount,
		"input_stream_hash": phkv1.GoldenReplaySummaryInputStreamHash,
		"event_stream_hash": phkv1.GoldenReplaySummaryEventStreamHash,
		"final_state_hash":  phkv1.GoldenReplaySummaryFinalStateHash,
		"final_tick":        phkv1.GoldenReplaySummaryFinalTick,
	}
	if goldenReplaySummary["replay_id"] == "" || goldenReplaySummary["match_id"] == "" || goldenReplaySummary["owner_user_id"] == "" {
		t.Fatalf("golden replay summary identity constants missing: %+v", goldenReplaySummary)
	}
	if phkv1.GoldenReplaySummaryInputCount <= 0 || phkv1.GoldenReplaySummaryEventCount <= 0 || phkv1.GoldenReplaySummaryFinalTick <= 0 {
		t.Fatalf("golden replay summary count constants invalid: %+v", goldenReplaySummary)
	}
	for _, hashValue := range []string{phkv1.GoldenReplaySummaryInputStreamHash, phkv1.GoldenReplaySummaryEventStreamHash, phkv1.GoldenReplaySummaryFinalStateHash} {
		if !strings.HasPrefix(hashValue, "sha256:") {
			t.Fatalf("golden replay summary hash constant must be sha256-prefixed: %s", hashValue)
		}
	}
	if _, err := service.Replay(bob.SessionToken, settlement.ReplayID); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("expected cross-user replay rejection, got %v", err)
	}
	bootstrapAfterSettle, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after settle: %v", err)
	}
	pointsAfterSettle := bootstrapAfterSettle.Wallet["points"]
	if pointsAfterSettle <= 0 {
		t.Fatalf("expected wallet points from server reward, got %+v", bootstrapAfterSettle.Wallet)
	}
	if task := bootstrapAfterSettle.Tasks["daily_complete_match"]; task.Progress < task.Target {
		t.Fatalf("daily task should be complete: %+v", task)
	}
	if board := bootstrapAfterSettle.Leaderboards["single_score"]; board.Rank <= 0 || board.Percentile <= 0 {
		t.Fatalf("leaderboard should be projected from server result: %+v", board)
	}
	if cert := bootstrapAfterSettle.Certification; !cert.OK || cert.RankScore <= 1255 || !cert.Top30Qualified || !cert.NextCertificationUnlocked || cert.LastRankScoreDelta <= 0 || !cert.ServerAuthoritative {
		t.Fatalf("bootstrap should project certification profile: %+v", cert)
	}

	duplicateSettlement, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{})
	if err != nil {
		t.Fatalf("duplicate settle: %v", err)
	}
	if !duplicateSettlement.Duplicate {
		t.Fatalf("expected duplicate settlement flag: %+v", duplicateSettlement)
	}
	bootstrapAfterDuplicate, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap duplicate: %v", err)
	}
	if got := bootstrapAfterDuplicate.Wallet["points"]; got != pointsAfterSettle {
		t.Fatalf("duplicate settlement changed wallet: got %d want %d", got, pointsAfterSettle)
	}

	leaderboardClaim, err := service.ClaimActivity(alice.SessionToken, map[string]any{"claim_kind": "leaderboard", "claim_id": "rank_score"})
	if err != nil {
		t.Fatalf("claim leaderboard: %v", err)
	}
	if !leaderboardClaim.OK || leaderboardClaim.ClaimID != "rank_score" || len(leaderboardClaim.RewardJSON) < 2 {
		t.Fatalf("leaderboard claim should use server top 30 projection: %+v", leaderboardClaim)
	}

	claim, err := service.ClaimActivity(alice.SessionToken, map[string]any{"claim_kind": "task", "claim_id": "daily_complete_match"})
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if !claim.OK || !claim.ServerAuthoritative || claim.SettlementKey == "" || len(claim.RewardJSON) == 0 {
		t.Fatalf("claim result invalid: %+v", claim)
	}
	bootstrapAfterClaim, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after claim: %v", err)
	}
	if !bootstrapAfterClaim.Tasks["daily_complete_match"].Claimed {
		t.Fatalf("task claim projection missing: %+v", bootstrapAfterClaim.Tasks["daily_complete_match"])
	}
	pointsAfterClaim := bootstrapAfterClaim.Wallet["points"]
	if pointsAfterClaim <= pointsAfterSettle {
		t.Fatalf("claim should grant points once: settle=%d claim=%d", pointsAfterSettle, pointsAfterClaim)
	}
	duplicateClaim, err := service.ClaimActivity(alice.SessionToken, map[string]any{"claim_kind": "task", "claim_id": "daily_complete_match"})
	if err != nil {
		t.Fatalf("duplicate claim: %v", err)
	}
	if !duplicateClaim.Duplicate {
		t.Fatalf("expected duplicate claim: %+v", duplicateClaim)
	}
	bootstrapAfterDuplicateClaim, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after duplicate claim: %v", err)
	}
	if got := bootstrapAfterDuplicateClaim.Wallet["points"]; got != pointsAfterClaim {
		t.Fatalf("duplicate claim changed wallet: got %d want %d", got, pointsAfterClaim)
	}
}

func TestExternalSessionCreatesAndRefreshesAuthoritativeUser(t *testing.T) {
	service := NewService(Config{})
	first, err := service.LoginExternal(ExternalSessionRequest{
		UserID:       "nakama-user-1",
		SessionToken: "nakama-session-a",
		DisplayName:  "Nakama Player",
		Provider:     "nakama",
	})
	if err != nil {
		t.Fatalf("login external first: %v", err)
	}
	if first.UserID != "nakama-user-1" || first.SessionToken != "nakama-session-a" || first.DisplayName != "Nakama Player" {
		t.Fatalf("external session invalid: %+v", first)
	}
	if bootstrap, err := service.Bootstrap(first.SessionToken); err != nil || bootstrap.UserID != first.UserID || len(bootstrap.Inventory.Items) == 0 {
		t.Fatalf("external bootstrap should create authoritative defaults: bootstrap=%+v err=%v", bootstrap, err)
	}

	refreshed, err := service.LoginExternal(ExternalSessionRequest{
		UserID:       "nakama-user-1",
		SessionToken: "nakama-session-b",
		DisplayName:  "Renamed Player",
		Provider:     "nakama",
	})
	if err != nil {
		t.Fatalf("login external refresh: %v", err)
	}
	if refreshed.CreatedAt != first.CreatedAt || refreshed.SessionToken != "nakama-session-b" || refreshed.DisplayName != "Renamed Player" {
		t.Fatalf("external refresh should preserve user and replace session: first=%+v refreshed=%+v", first, refreshed)
	}
	if _, err := service.Bootstrap(first.SessionToken); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("old external session should be invalid, got %v", err)
	}
	if bootstrap, err := service.Bootstrap(refreshed.SessionToken); err != nil || bootstrap.UserID != first.UserID {
		t.Fatalf("refreshed session should bootstrap: bootstrap=%+v err=%v", bootstrap, err)
	}
}

func TestValidationRejectsInvalidDeckAndClientResults(t *testing.T) {
	service := NewService(Config{})
	user := mustLogin(t, service, "Deck Tester")
	invalidDeck := validDeck("bad_deck")
	invalidDeck.CardIDs = invalidDeck.CardIDs[:19]
	if _, err := service.JoinQueue(user.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bad_deck",
		DeckSnapshot: invalidDeck,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("expected invalid deck, got %v", err)
	}

	if _, err := service.ClaimActivity(user.SessionToken, map[string]any{
		"claim_kind":             "task",
		"claim_id":               "daily_complete_match",
		"client_authored_reward": true,
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected client-authored reward rejection, got %v", err)
	}

	if _, err := service.ClaimActivity(user.SessionToken, map[string]any{
		"claim_kind": "task",
		"claim_id":   "daily_complete_match",
	}); ErrorCode(err) != codeClaimIneligible {
		t.Fatalf("expected incomplete task rejection, got %v", err)
	}
}

func TestBattleAllocationAndSignedTicketUseRegisteredServer(t *testing.T) {
	now := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	status, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "aaa-battle-dev",
		Endpoint:       "127.0.0.1:7909",
		Region:         "local",
		BuildID:        "battle-test",
		Capacity:       32,
		Status:         "online",
		SupportedModes: []string{"certification"},
	})
	if err != nil {
		t.Fatalf("register battle server: %v", err)
	}
	if !status.OK || status.BattleServerID != "aaa-battle-dev" || status.Endpoint != "127.0.0.1:7909" || !status.ServerAuthoritative {
		t.Fatalf("battle server status invalid: %+v", status)
	}
	servers := service.BattleServers()
	if !servers.OK || len(servers.Servers) < 2 {
		t.Fatalf("battle server list should include default and registered servers: %+v", servers)
	}

	alice := mustLogin(t, service, "Ticket Alice")
	bob := mustLogin(t, service, "Ticket Bob")
	if _, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "ticket_alice_deck",
		DeckSnapshot: validDeck("ticket_alice_deck"),
	}); err != nil {
		t.Fatalf("join alice: %v", err)
	}
	queueBob, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "ticket_bob_deck",
		DeckSnapshot: validDeck("ticket_bob_deck"),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	if queueBob.MatchID == "" || queueBob.BattleAllocation == nil || queueBob.BattleTicket == nil {
		t.Fatalf("matched queue should include allocation and ticket: %+v", queueBob)
	}
	if queueBob.BattleAllocation.BattleServerID != "aaa-battle-dev" || queueBob.BattleAllocation.Endpoint != "127.0.0.1:7909" {
		t.Fatalf("allocation did not use registered server: %+v", queueBob.BattleAllocation)
	}
	if queueBob.BattleAllocation.Version.ProtocolVersion != ProtocolVersion || queueBob.BattleAllocation.ModeConfigHash == "" || len(queueBob.BattleAllocation.Players) != 2 {
		t.Fatalf("allocation missing protocol fields: %+v", queueBob.BattleAllocation)
	}
	if queueBob.BattleTicket.Ticket.UserID != bob.UserID || queueBob.BattleTicket.Ticket.MatchID != queueBob.MatchID || queueBob.BattleTicket.Ticket.BattleServerID != "aaa-battle-dev" {
		t.Fatalf("queue battle ticket not bound to bob/match/server: %+v", queueBob.BattleTicket)
	}
	if queueBob.BattleTicket.Ticket.BusinessSessionID == bob.SessionToken || !strings.HasPrefix(queueBob.BattleTicket.Ticket.BusinessSessionID, "session-ref:") || queueBob.BattleTicket.Ticket.ExpiresAtMS <= queueBob.BattleTicket.Ticket.IssuedAtMS {
		t.Fatalf("battle ticket missing session or expiry: %+v", queueBob.BattleTicket.Ticket)
	}
	if !strings.HasPrefix(queueBob.BattleTicket.Ticket.DeckSnapshotHash, "sha256:") || queueBob.BattleTicket.Ticket.TicketNonceHex == "" {
		t.Fatalf("battle ticket missing hash/nonce: %+v", queueBob.BattleTicket.Ticket)
	}
	if !verifySignedBattleTicket(t, queueBob.BattleTicket) {
		t.Fatalf("battle ticket signature did not verify: %+v", queueBob.BattleTicket)
	}

	readyAlice, err := service.ReadyMatch(alice.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if readyAlice.BattleTicket != nil {
		t.Fatalf("single ready player should not receive running battle ticket: %+v", readyAlice)
	}
	readyBob, err := service.ReadyMatch(bob.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	if readyBob.MatchStart == nil || readyBob.MatchStart.BattleAllocation == nil || readyBob.BattleTicket == nil {
		t.Fatalf("running ready should include match start allocation and ticket: %+v", readyBob)
	}
	if len(readyBob.MatchStart.Players) != 2 || readyBob.MatchStart.Players[0].PlayerID == "" {
		t.Fatalf("match start players should expose battle player ids: %+v", readyBob.MatchStart.Players)
	}

	allocation, err := service.BattleAllocation(alice.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("explicit allocation: %v", err)
	}
	if allocation.MatchID != queueBob.MatchID || allocation.Endpoint != "127.0.0.1:7909" {
		t.Fatalf("explicit allocation mismatch: %+v", allocation)
	}
	ticketAlice, err := service.BattleTicket(alice.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("explicit battle ticket: %v", err)
	}
	if ticketAlice.Ticket.UserID != alice.UserID || !verifySignedBattleTicket(t, ticketAlice) {
		t.Fatalf("explicit alice ticket invalid: %+v", ticketAlice)
	}
}

func TestBattleAllocationFallbackAccountsServerLoad(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Fallback Alice")
	bob := mustLogin(t, service, "Fallback Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if allocation.BattleServerID != DefaultBattleServerID {
		t.Fatalf("expected default fallback allocation, got %+v", allocation)
	}

	servers := service.BattleServers()
	var fallback BattleServerStatus
	for _, server := range servers.Servers {
		if server.BattleServerID == DefaultBattleServerID {
			fallback = server
			break
		}
	}
	if fallback.BattleServerID == "" || fallback.ActiveMatches != 1 || fallback.Load <= 0 || !fallback.ServerAuthoritative {
		t.Fatalf("fallback allocation should update server load accounting: %+v", fallback)
	}

	again, err := service.BattleAllocation(bob.SessionToken, matchID)
	if err != nil {
		t.Fatalf("second allocation read: %v", err)
	}
	if again.BattleServerID != DefaultBattleServerID {
		t.Fatalf("second read changed allocation: %+v", again)
	}
	servers = service.BattleServers()
	for _, server := range servers.Servers {
		if server.BattleServerID == DefaultBattleServerID && server.ActiveMatches != 1 {
			t.Fatalf("re-reading existing allocation must not double count fallback load: %+v", server)
		}
	}
}

func TestBattleServerOfflineLifecycleSkipsFutureAllocations(t *testing.T) {
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{BattleLifecycleAuditRepo: repo})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-offline-a",
		Endpoint:       "127.0.0.1:7911",
		Region:         "local",
		BuildID:        "offline-a",
		Capacity:       8,
		Status:         "offline",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register offline candidate: %v", err)
	}
	if repo.allocations[0].AllocationJSON == "" || !strings.Contains(repo.allocations[0].AllocationJSON, `"status":"online"`) {
		t.Fatalf("register audit must canonicalize callback status to online: %+v", repo.allocations[0])
	}
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-online-b",
		Endpoint:       "127.0.0.1:7912",
		Region:         "local",
		BuildID:        "online-b",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register online candidate: %v", err)
	}
	heartbeat, err := service.BattleServerHeartbeat(BattleServerHeartbeatRequest{
		BattleServerID: "pvp-offline-a",
		ActiveMatches:  0,
		Load:           0.5,
		Status:         "draining",
	})
	if err != nil {
		t.Fatalf("heartbeat offline candidate: %v", err)
	}
	if heartbeat.Status != "online" {
		t.Fatalf("heartbeat must canonicalize payload status to online: %+v", heartbeat)
	}
	offline, err := service.BattleServerOffline(BattleServerOfflineRequest{BattleServerID: "pvp-offline-a", Status: "online"})
	if err != nil {
		t.Fatalf("offline battle server: %v", err)
	}
	if offline.Status != "offline" || offline.Load != 0 || !offline.ServerAuthoritative {
		t.Fatalf("offline status must be canonical even when payload asks for another state: %+v", offline)
	}

	alice := mustLogin(t, service, "Offline Alice")
	bob := mustLogin(t, service, "Offline Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation after offline: %v", err)
	}
	if allocation.BattleServerID != "pvp-online-b" {
		t.Fatalf("offline server must be skipped for future allocations: %+v", allocation)
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ServerLifecycleRecords != 4 || status.AllocationRecords != 1 || status.TicketRecords == 0 || status.LastSuccessOperation == "" {
		t.Fatalf("offline lifecycle audit status invalid: %+v", status)
	}
	if len(repo.allocations) != 5 || repo.allocations[2].Status != "server_heartbeat" || !strings.Contains(repo.allocations[2].AllocationJSON, `"status":"online"`) || repo.allocations[3].Status != "server_offline" || repo.allocations[3].BattleServerID != "pvp-offline-a" {
		t.Fatalf("expected register/register/heartbeat/offline/allocation audit records with canonical statuses, got %+v", repo.allocations)
	}
	if _, err := service.BattleServerOffline(BattleServerOfflineRequest{BattleServerID: "missing-battle"}); ErrorCode(err) != codeNotFound {
		t.Fatalf("missing offline should be not_found, got %v", err)
	}
}

func TestBattleServerStaleHeartbeatSkipsFutureAllocations(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-stale-a",
		Endpoint:       "127.0.0.1:7921",
		Region:         "local",
		BuildID:        "stale-a",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register stale candidate: %v", err)
	}
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-fresh-b",
		Endpoint:       "127.0.0.1:7922",
		Region:         "local",
		BuildID:        "fresh-b",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register fresh candidate: %v", err)
	}
	now = now.Add(time.Duration(BattleServerHeartbeatTTLSeconds+1) * time.Second)
	if _, err := service.BattleServerHeartbeat(BattleServerHeartbeatRequest{
		BattleServerID: "pvp-fresh-b",
		Endpoint:       "127.0.0.1:7922",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("refresh fresh candidate: %v", err)
	}

	alice := mustLogin(t, service, "Stale Alice")
	bob := mustLogin(t, service, "Stale Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation after stale heartbeat: %v", err)
	}
	if allocation.BattleServerID != "pvp-fresh-b" {
		t.Fatalf("stale registered server must be skipped for new allocations: %+v", allocation)
	}
	servers := service.BattleServers()
	for _, server := range servers.Servers {
		if server.BattleServerID == "pvp-stale-a" && server.Status != "online" {
			t.Fatalf("stale heartbeat should not mutate discovery status without an offline callback: %+v", server)
		}
	}
}

func TestBattleAllocationReadKeepsExistingServerAfterHeartbeatStales(t *testing.T) {
	now := time.Date(2026, 6, 29, 13, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-existing-a",
		Endpoint:       "127.0.0.1:7923",
		Region:         "local",
		BuildID:        "existing-a",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register existing candidate: %v", err)
	}

	alice := mustLogin(t, service, "Existing Alice")
	bob := mustLogin(t, service, "Existing Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	first, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("initial allocation: %v", err)
	}
	if first.BattleServerID != "pvp-existing-a" {
		t.Fatalf("expected registered server before heartbeat stales: %+v", first)
	}
	now = now.Add(time.Duration(BattleServerHeartbeatTTLSeconds+1) * time.Second)
	second, err := service.BattleAllocation(bob.SessionToken, matchID)
	if err != nil {
		t.Fatalf("existing allocation read after stale heartbeat: %v", err)
	}
	if second.BattleServerID != first.BattleServerID || second.Endpoint != first.Endpoint {
		t.Fatalf("existing allocation reads must remain stable after heartbeat stales: first=%+v second=%+v", first, second)
	}
}

func TestPvPDuelModeContractAllocationAndSettlement(t *testing.T) {
	now := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})

	alice := mustLogin(t, service, "PvP Alice")
	bob := mustLogin(t, service, "PvP Bob")
	bootstrap, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	pvpMode := modeConfigByID(bootstrap.Modes, "pvp_duel")
	if pvpMode.ModeID != "pvp_duel" || pvpMode.MinPlayers != 2 || pvpMode.MaxPlayers != 2 || pvpMode.ModeRulesetVersion != "pvp-duel-s0" || pvpMode.RewardTableID != "pvp_duel_s0_rewards" {
		t.Fatalf("bootstrap missing pvp_duel contract: %+v", pvpMode)
	}

	status, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "pvp-battle-dev",
		Endpoint:       "127.0.0.1:7910",
		Region:         "local",
		BuildID:        "pvp-contract-test",
		Capacity:       16,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	})
	if err != nil {
		t.Fatalf("register pvp battle server: %v", err)
	}
	if !status.OK || status.BattleServerID != "pvp-battle-dev" || len(status.SupportedModes) != 1 || status.SupportedModes[0] != "pvp_duel" {
		t.Fatalf("pvp battle server status invalid: %+v", status)
	}

	queueAlice, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "pvp_alice_deck",
		DeckSnapshot: validDeck("pvp_alice_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("join pvp alice: %v", err)
	}
	if queueAlice.QueueStatus != "queued" || queueAlice.MatchID != "" {
		t.Fatalf("alice should wait for pvp opponent: %+v", queueAlice)
	}
	queueBob, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "pvp_bob_deck",
		DeckSnapshot: validDeck("pvp_bob_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "wide"},
	})
	if err != nil {
		t.Fatalf("join pvp bob: %v", err)
	}
	if queueBob.QueueStatus != "found" || queueBob.MatchID == "" || queueBob.BattleAllocation == nil || queueBob.BattleTicket == nil {
		t.Fatalf("pvp match should be found with allocation and ticket: %+v", queueBob)
	}
	if queueBob.BattleAllocation.ModeID != "pvp_duel" || queueBob.BattleAllocation.BattleServerID != "pvp-battle-dev" || queueBob.BattleAllocation.Endpoint != "127.0.0.1:7910" {
		t.Fatalf("pvp allocation should use pvp battle server: %+v", queueBob.BattleAllocation)
	}
	if queueBob.BattleTicket.Ticket.ModeID != "pvp_duel" || queueBob.BattleTicket.Ticket.UserID != bob.UserID || queueBob.BattleTicket.Ticket.BattleServerID != "pvp-battle-dev" {
		t.Fatalf("pvp ticket not bound to bob/mode/server: %+v", queueBob.BattleTicket)
	}
	if !verifySignedBattleTicket(t, queueBob.BattleTicket) {
		t.Fatalf("pvp battle ticket signature did not verify: %+v", queueBob.BattleTicket)
	}

	readyAlice, err := service.ReadyMatch(alice.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("ready pvp alice: %v", err)
	}
	if readyAlice.ReadyStatus != "loading" || readyAlice.MatchStart != nil {
		t.Fatalf("single pvp ready should stay loading: %+v", readyAlice)
	}
	readyBob, err := service.ReadyMatch(bob.SessionToken, queueBob.MatchID)
	if err != nil {
		t.Fatalf("ready pvp bob: %v", err)
	}
	if readyBob.ReadyStatus != "running" || readyBob.MatchStart == nil || readyBob.BattleTicket == nil {
		t.Fatalf("all pvp players ready should start match with ticket: %+v", readyBob)
	}
	if readyBob.MatchStart.ModeID != "pvp_duel" || readyBob.MatchStart.ModeRulesetVersion != "pvp-duel-s0" || readyBob.MatchStart.BattleAllocation.BattleServerID != "pvp-battle-dev" {
		t.Fatalf("pvp start event missing contract data: %+v", readyBob.MatchStart)
	}

	input, err := service.SubmitInput(alice.SessionToken, queueBob.MatchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       2,
		"slow":      true,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	})
	if err != nil {
		t.Fatalf("pvp input: %v", err)
	}
	if !input.Accepted || input.Snapshot.ModeState["duel_status"] != "running" || intFromAny(input.Snapshot.ModeState["duel_score_limit"]) != 1 {
		t.Fatalf("pvp snapshot should expose duel mode state: %+v", input.Snapshot.ModeState)
	}

	before, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap before pvp settle: %v", err)
	}
	settlement, err := service.SettleMatch(alice.SessionToken, queueBob.MatchID, map[string]any{})
	if err != nil {
		t.Fatalf("pvp settle: %v", err)
	}
	if settlement.Mode != "pvp_duel" || settlement.ModeRulesetVersion != "pvp-duel-s0" || !settlement.ServerAuthoritative {
		t.Fatalf("pvp settlement missing mode contract: %+v", settlement)
	}
	if intFromAny(settlement.ModeResult["rank_score_delta"]) != 0 || boolFromAny(settlement.ModeResult["next_certification_unlocked"]) {
		t.Fatalf("pvp settlement should not mutate certification mode result: %+v", settlement.ModeResult)
	}
	after, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after pvp settle: %v", err)
	}
	if after.Certification.RankScore != before.Certification.RankScore || after.Certification.NextCertificationUnlocked != before.Certification.NextCertificationUnlocked || after.Certification.LastRankScoreDelta != before.Certification.LastRankScoreDelta {
		t.Fatalf("pvp settlement should not mutate certification profile: before=%+v after=%+v", before.Certification, after.Certification)
	}
}

func TestBattleResultSubmitVerifiesAllocationAndSettlesMatch(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	status, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "result-battle-dev",
		Endpoint:       "127.0.0.1:7912",
		Region:         "local",
		BuildID:        "result-test",
		Capacity:       8,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	})
	if err != nil || !status.OK {
		t.Fatalf("register result battle server: status=%+v err=%v", status, err)
	}
	alice := mustLogin(t, service, "Result Alice")
	bob := mustLogin(t, service, "Result Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if allocation.BattleServerID != "result-battle-dev" {
		t.Fatalf("expected dedicated result battle server: %+v", allocation)
	}

	bad := signedBattleResultForAllocation(allocation)
	bad.Result.PlayerIDs = []string{"p-missing"}
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: bad}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected player mismatch rejection, got %v", err)
	}

	badRewardProjection := signedBattleResultForAllocation(allocation)
	badRewardProjection.Result.RewardProjectionJSON = `{"source":"battle_server","reward":{"gold":999999}}`
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: badRewardProjection}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected reward projection authority-field rejection, got %v", err)
	}

	badModeProjection := signedBattleResultForAllocation(allocation)
	badModeProjection.Result.ModeResultJSON = `{"verified":true,"players":[{"boss_hp":0}]}`
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: badModeProjection}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected mode projection authority-field rejection, got %v", err)
	}

	missingBusinessVersion := signedBattleResultForAllocation(allocation)
	missingBusinessVersion.Result.Version.BusinessAPIVersion = ""
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: missingBusinessVersion}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected missing result business api version rejection, got %v", err)
	}

	staleBusinessVersion := signedBattleResultForAllocation(allocation)
	staleBusinessVersion.Result.Version.BusinessAPIVersion = "0.0.0-stale"
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: staleBusinessVersion}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected stale result business api version rejection, got %v", err)
	}

	staleRulesetVersion := signedBattleResultForAllocation(allocation)
	staleRulesetVersion.Result.Version.RulesetVersion = "ruleset-stale"
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: staleRulesetVersion}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected stale result ruleset version rejection, got %v", err)
	}

	signed := signedBattleResultForAllocation(allocation)
	resp, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: signed})
	if err != nil {
		t.Fatalf("submit battle result: %v", err)
	}
	if !resp.OK || !resp.Accepted || resp.Duplicate || resp.MatchID != matchID || resp.SettlementKey == "" {
		t.Fatalf("battle result response invalid: %+v", resp)
	}
	settlement, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{})
	if err != nil {
		t.Fatalf("read settlement after battle result: %v", err)
	}
	if !settlement.Duplicate || settlement.Mode != "pvp_duel" || settlement.ModeResult["battle_result_hash"] != signed.Result.ResultHash || settlement.ModeResult["battle_result_key_id"] != "result-battle-dev" {
		t.Fatalf("settlement missing battle result audit fields: %+v", settlement)
	}
	replay, err := service.Replay(alice.SessionToken, settlement.ReplayID)
	if err != nil {
		t.Fatalf("replay after battle result: %v", err)
	}
	if replay.ModeResult["battle_result_replay_id"] != signed.Result.ReplayID {
		t.Fatalf("replay missing battle result audit fields: %+v", replay.ModeResult)
	}

	duplicate, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: signed})
	if err != nil {
		t.Fatalf("duplicate battle result should be accepted idempotently: %v", err)
	}
	if !duplicate.Duplicate || !duplicate.Accepted {
		t.Fatalf("duplicate battle result response invalid: %+v", duplicate)
	}
}

func TestBattleLifecycleAuditRepositoryReceivesAllocationTicketResultAndReplayRecords(t *testing.T) {
	now := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                    func() time.Time { return now },
		BattleLifecycleAuditRepo: repo,
	})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "audit-battle-dev",
		Endpoint:       "127.0.0.1:7920",
		Region:         "local",
		BuildID:        "audit-test",
		Capacity:       4,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register battle server: %v", err)
	}
	if _, err := service.BattleServerHeartbeat(BattleServerHeartbeatRequest{
		BattleServerID: "audit-battle-dev",
		ActiveMatches:  0,
		Load:           0,
		Status:         "online",
	}); err != nil {
		t.Fatalf("battle server heartbeat: %v", err)
	}
	alice := mustLogin(t, service, "Audit Alice")
	bob := mustLogin(t, service, "Audit Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if allocation.BattleServerID != "audit-battle-dev" {
		t.Fatalf("expected audit battle server: %+v", allocation)
	}
	if len(repo.allocations) != 3 {
		t.Fatalf("expected one allocation audit, got %+v", repo.allocations)
	}
	if repo.allocations[0].Status != "server_registered" || repo.allocations[0].MatchID != "battle-server:audit-battle-dev" || repo.allocations[0].ModeID != "battle_server_lifecycle" || repo.allocations[0].PlayerCount != 0 || repo.allocations[0].AllocationJSON == "" {
		t.Fatalf("registration audit invalid: %+v", repo.allocations[0])
	}
	if repo.allocations[1].Status != "server_heartbeat" || repo.allocations[1].MatchID != "battle-server:audit-battle-dev" || repo.allocations[1].PlayerCount != 0 || repo.allocations[1].AllocationJSON == "" {
		t.Fatalf("heartbeat audit invalid: %+v", repo.allocations[1])
	}
	allocationAudit := repo.allocations[2]
	if allocationAudit.MatchID != matchID || allocationAudit.ModeID != "pvp_duel" || allocationAudit.BattleServerID != "audit-battle-dev" || allocationAudit.PlayerCount != 2 || allocationAudit.AllocationJSON == "" {
		t.Fatalf("allocation audit invalid: %+v", allocationAudit)
	}

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	if ticket.Ticket.UserID != alice.UserID {
		t.Fatalf("ticket user mismatch: %+v", ticket)
	}
	if len(repo.tickets) == 0 {
		t.Fatalf("expected ticket audit")
	}
	ticketAudit := repo.tickets[len(repo.tickets)-1]
	if ticketAudit.TicketID != ticket.Ticket.TicketID || ticketAudit.UserID != alice.UserID || ticketAudit.MatchID != matchID || ticketAudit.SignaturePrefix == "" || ticketAudit.Status != "issued" {
		t.Fatalf("ticket audit invalid: %+v", ticketAudit)
	}

	signed := signedBattleResultForAllocation(allocation)
	result, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: signed})
	if err != nil {
		t.Fatalf("submit result: %v", err)
	}
	if !result.Accepted || result.Duplicate {
		t.Fatalf("result response invalid: %+v", result)
	}
	if len(repo.results) != 1 {
		t.Fatalf("expected one result audit, got %+v", repo.results)
	}
	resultAudit := repo.results[0]
	if resultAudit.MatchID != matchID || resultAudit.ResultHash != signed.Result.ResultHash || resultAudit.ReplayID != signed.Result.ReplayID || len(resultAudit.PlayerIDs) != 2 || resultAudit.SettlementKey == "" || resultAudit.Status != "accepted" {
		t.Fatalf("result audit invalid: %+v", resultAudit)
	}
	if len(repo.replays) != 2 {
		t.Fatalf("expected per-player replay audits, got %+v", repo.replays)
	}
	for _, replayAudit := range repo.replays {
		if replayAudit.MatchID != matchID || replayAudit.StateHash == "" || replayAudit.SettlementKey == "" || !replayAudit.ServerAuthoritative {
			t.Fatalf("replay audit invalid: %+v", replayAudit)
		}
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ServerLifecycleRecords != 2 || status.AllocationRecords != 1 || status.TicketRecords == 0 || status.ResultRecords != 1 || status.ReplayRecords != 2 || status.RejectedRecords != 0 {
		t.Fatalf("audit status did not account successful lifecycle writes: %+v", status)
	}
	if status.LastSuccessOperation != "battle_result" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") || status.LastSuccessAt.IsZero() {
		t.Fatalf("audit status should expose a non-secret last-success fingerprint: %+v", status)
	}

	duplicate, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: signed})
	if err != nil {
		t.Fatalf("duplicate result callback: %v", err)
	}
	if !duplicate.Accepted || !duplicate.Duplicate {
		t.Fatalf("duplicate result response invalid: %+v", duplicate)
	}
	if len(repo.results) != 2 || repo.results[1].Status != "duplicate" || repo.results[1].MatchID != matchID || len(repo.replays) != 2 {
		t.Fatalf("duplicate callback should record one duplicate result audit without extra replay writes: results=%+v replays=%+v", repo.results, repo.replays)
	}
	status = service.BattleLifecycleAuditStatus()
	if status.ResultRecords != 1 || status.ResultDuplicateRecords != 1 || status.ReplayRecords != 2 || status.LastSuccessOperation != "battle_result_duplicate" {
		t.Fatalf("duplicate result callback audit status invalid: %+v", status)
	}
}

func TestBattleLifecycleAuditRecordsRejectedResultCallbacks(t *testing.T) {
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{BattleLifecycleAuditRepo: repo})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "reject-battle-dev",
		Endpoint:       "127.0.0.1:7921",
		Capacity:       4,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register battle server: %v", err)
	}
	alice := mustLogin(t, service, "Rejected Result Alice")
	bob := mustLogin(t, service, "Rejected Result Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	signed := signedBattleResultForAllocation(allocation)
	signed.KeyID = "wrong-battle-server"
	if _, err := service.SubmitBattleResult(BattleResultSubmitRequest{SignedResult: signed}); ErrorCode(err) != codeBattleServer {
		t.Fatalf("expected battle server rejection, got %v", err)
	}
	if len(repo.results) != 1 {
		t.Fatalf("expected rejected result audit, got %+v", repo.results)
	}
	rejected := repo.results[0]
	if rejected.Status != "rejected" || rejected.RejectReason != codeBattleServer || rejected.MatchID != matchID || rejected.BattleServerID != allocation.BattleServerID || rejected.KeyID != "wrong-battle-server" || rejected.SettlementKey == "" {
		t.Fatalf("rejected result audit invalid: %+v", rejected)
	}
	if len(repo.replays) != 0 {
		t.Fatalf("rejected battle result must not write replay audits: %+v", repo.replays)
	}
	if match := service.matches[matchID]; match.Status == "ended" || match.BattleResultHash != "" {
		t.Fatalf("rejected battle result must not settle or stamp match: %+v", match)
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ResultRejectedRecords != 1 || status.ResultRecords != 0 || status.ResultDuplicateRecords != 0 || status.ReplayRecords != 0 || status.RejectedRecords != 0 || status.LastSuccessOperation != "battle_result_rejected" {
		t.Fatalf("rejected result audit status invalid: %+v", status)
	}
}

func TestBattleLifecycleAuditStatusTracksRepositoryWriteFailures(t *testing.T) {
	repo := &captureBattleLifecycleAuditRepo{err: errors.New("audit db offline")}
	service := NewService(Config{BattleLifecycleAuditRepo: repo})
	alice := mustLogin(t, service, "Audit Error Alice")
	bob := mustLogin(t, service, "Audit Error Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	if _, err := service.BattleTicket(alice.SessionToken, matchID); err != nil {
		t.Fatalf("fallback ticket issuance should remain available while audit failure is surfaced: %v", err)
	}
	status := service.BattleLifecycleAuditStatus()
	if status.OK || !status.Configured || status.RejectedRecords == 0 || status.LastErrorOperation == "" || !strings.Contains(status.LastError, "audit db offline") {
		t.Fatalf("audit failure should be visible in status snapshot: %+v", status)
	}
	if !status.ServerAuthoritative {
		t.Fatalf("audit status must stay server authoritative: %+v", status)
	}
}

func TestBattleTicketExpiryLifecycleAudit(t *testing.T) {
	now := time.Date(2026, 6, 29, 14, 0, 0, 0, time.UTC)
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                    func() time.Time { return now },
		BattleLifecycleAuditRepo: repo,
	})
	alice := mustLogin(t, service, "Ticket Expiry Alice")
	bob := mustLogin(t, service, "Ticket Expiry Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	first, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("first ticket: %v", err)
	}
	now = first.Ticket.ExpiresAt.Add(time.Second)
	second, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("replacement ticket: %v", err)
	}
	if second.Ticket.TicketID == first.Ticket.TicketID || !second.Ticket.IssuedAt.Equal(now) {
		t.Fatalf("expired ticket should be replaced with a fresh signed ticket: first=%+v second=%+v", first.Ticket, second.Ticket)
	}

	var expired BattleTicketAuditRecord
	for _, record := range repo.tickets {
		if record.TicketID == first.Ticket.TicketID && record.Status == "expired" {
			expired = record
			break
		}
	}
	if expired.TicketID == "" || !expired.ConsumedAt.Equal(now) || expired.ExpiresAt != first.Ticket.ExpiresAt || !expired.ServerAuthoritative {
		t.Fatalf("expired ticket audit missing authoritative transition: tickets=%+v", repo.tickets)
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.TicketExpiredRecords != 1 || status.LastSuccessOperation != "battle_ticket" {
		t.Fatalf("ticket expiry status should count expired transition and fresh ticket issue: %+v", status)
	}
}

func TestBattleTicketConsumeLifecycleAudit(t *testing.T) {
	now := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	repo := &captureBattleLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                    func() time.Time { return now },
		BattleLifecycleAuditRepo: repo,
	})
	alice := mustLogin(t, service, "Ticket Consume Alice")
	bob := mustLogin(t, service, "Ticket Consume Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	consume, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		Version:        ticket.Ticket.Version,
		TicketID:       ticket.Ticket.TicketID,
		MatchID:        matchID,
		UserID:         alice.UserID,
		PlayerID:       ticket.Ticket.PlayerID,
		BattleServerID: ticket.Ticket.BattleServerID,
		TicketNonceHex: ticket.Ticket.TicketNonceHex,
	})
	if err != nil {
		t.Fatalf("consume ticket: %v", err)
	}
	if !consume.Consumed || consume.Duplicate || consume.TicketID != ticket.Ticket.TicketID || !consume.ServerAuthoritative {
		t.Fatalf("consume response invalid: %+v", consume)
	}
	if consume.IssuedAt != ticket.Ticket.IssuedAt || consume.ExpiresAt != ticket.Ticket.ExpiresAt || consume.ConsumedAt != now || consume.IssuedAtMS != ticket.Ticket.IssuedAtMS || consume.ExpiresAtMS != ticket.Ticket.ExpiresAtMS || consume.ConsumedAtMS != now.UnixMilli() {
		t.Fatalf("consume response should expose ticket lifecycle times: ticket=%+v consume=%+v", ticket.Ticket, consume)
	}
	if len(repo.tickets) < 2 || repo.tickets[len(repo.tickets)-1].Status != "consumed" || repo.tickets[len(repo.tickets)-1].ConsumedAt != now {
		t.Fatalf("consumed ticket audit missing: %+v", repo.tickets)
	}
	afterConsumeAuditCount := len(repo.tickets)
	duplicate, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		Version:        ticket.Ticket.Version,
		TicketID:       ticket.Ticket.TicketID,
		MatchID:        matchID,
		BattleServerID: ticket.Ticket.BattleServerID,
		TicketNonceHex: ticket.Ticket.TicketNonceHex,
	})
	if err != nil {
		t.Fatalf("duplicate consume ticket: %v", err)
	}
	if !duplicate.Consumed || !duplicate.Duplicate || duplicate.ServerTime != now {
		t.Fatalf("duplicate consume response invalid: %+v", duplicate)
	}
	if duplicate.ConsumedAt != consume.ConsumedAt || duplicate.ConsumedAtMS != consume.ConsumedAtMS || duplicate.ExpiresAt != ticket.Ticket.ExpiresAt {
		t.Fatalf("duplicate consume should return original lifecycle times: first=%+v duplicate=%+v", consume, duplicate)
	}
	if len(repo.tickets) != afterConsumeAuditCount {
		t.Fatalf("duplicate consume should not write another ticket audit: %+v", repo.tickets)
	}
	replacement, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("replacement battle ticket: %v", err)
	}
	if replacement.Ticket.TicketID == ticket.Ticket.TicketID {
		t.Fatalf("consumed ticket should not be reissued: old=%+v replacement=%+v", ticket.Ticket, replacement.Ticket)
	}
	if _, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		Version:        replacement.Ticket.Version,
		TicketID:       replacement.Ticket.TicketID,
		MatchID:        matchID,
		BattleServerID: replacement.Ticket.BattleServerID,
		TicketNonceHex: "wrong-nonce",
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected nonce mismatch rejection, got %v", err)
	}
	if _, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		TicketID:       replacement.Ticket.TicketID,
		MatchID:        matchID,
		BattleServerID: replacement.Ticket.BattleServerID,
		TicketNonceHex: replacement.Ticket.TicketNonceHex,
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected missing consume version rejection, got %v", err)
	}
	staleBusinessVersion := replacement.Ticket.Version
	staleBusinessVersion.BusinessAPIVersion = "0.0.0-stale"
	if _, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		Version:        staleBusinessVersion,
		TicketID:       replacement.Ticket.TicketID,
		MatchID:        matchID,
		BattleServerID: replacement.Ticket.BattleServerID,
		TicketNonceHex: replacement.Ticket.TicketNonceHex,
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected stale consume business api rejection, got %v", err)
	}
	staleVersion := replacement.Ticket.Version
	staleVersion.RulesetVersion = "ruleset-stale"
	if _, err := service.ConsumeBattleTicket(BattleTicketConsumeRequest{
		Version:        staleVersion,
		TicketID:       replacement.Ticket.TicketID,
		MatchID:        matchID,
		BattleServerID: replacement.Ticket.BattleServerID,
		TicketNonceHex: replacement.Ticket.TicketNonceHex,
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected stale consume ruleset rejection, got %v", err)
	}
	status := service.BattleLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.TicketConsumedRecords != 1 || status.TicketRecords < 2 || status.LastSuccessOperation != "battle_ticket" {
		t.Fatalf("ticket consume status invalid: %+v", status)
	}
}

type captureBattleLifecycleAuditRepo struct {
	allocations []BattleAllocationAuditRecord
	tickets     []BattleTicketAuditRecord
	results     []BattleResultAuditRecord
	replays     []ReplayAuditRecord
	err         error
}

func (repo *captureBattleLifecycleAuditRepo) RecordMatchAllocationAudit(record BattleAllocationAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.allocations = append(repo.allocations, record)
	return nil
}

func (repo *captureBattleLifecycleAuditRepo) RecordBattleTicketAudit(record BattleTicketAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.tickets = append(repo.tickets, record)
	return nil
}

func (repo *captureBattleLifecycleAuditRepo) RecordBattleResultAudit(record BattleResultAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.results = append(repo.results, record)
	return nil
}

func (repo *captureBattleLifecycleAuditRepo) RecordReplayAudit(record ReplayAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.replays = append(repo.replays, record)
	return nil
}

func TestLobbyLifecycleAuditRepositoryReceivesRoomRulesAndMessageRecords(t *testing.T) {
	now := time.Date(2026, 6, 29, 6, 15, 0, 0, time.UTC)
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{
		Clock:                   func() time.Time { return now },
		LobbyLifecycleAuditRepo: repo,
	})
	host := mustLogin(t, service, "Lobby Audit Host")
	guest := mustLogin(t, service, "Lobby Audit Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "audit_host_deck",
		DeckSnapshot: validDeck("audit_host_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if len(repo.rooms) != 1 || repo.rooms[0].Action != "created" || repo.rooms[0].RoomCode != created.RoomCode || repo.rooms[0].DeckSnapshotHash == "" {
		t.Fatalf("create room audit invalid: %+v", repo.rooms)
	}
	retryCreate, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "audit_host_retry_deck",
		DeckSnapshot: validDeck("audit_host_retry_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("retry create room: %v", err)
	}
	if retryCreate.TicketID != created.TicketID || len(repo.rooms) != 2 || repo.rooms[1].Action != "create_retry" || repo.rooms[1].TicketID != created.TicketID || repo.rooms[1].CurrentPlayers != 1 {
		t.Fatalf("create retry audit invalid: response=%+v audits=%+v", retryCreate, repo.rooms)
	}

	listed, err := service.ListRooms(guest.SessionToken)
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if len(listed.Rooms) != 1 || len(repo.rooms) != 3 || repo.rooms[2].Action != "listed" || repo.rooms[2].UserID != guest.UserID {
		t.Fatalf("list audit invalid: list=%+v audits=%+v", listed, repo.rooms)
	}

	if _, err := service.Room(guest.SessionToken, created.RoomCode); err != nil {
		t.Fatalf("room snapshot: %v", err)
	}
	if len(repo.rooms) != 4 || repo.rooms[3].Action != "snapshot_read" || repo.rooms[3].UserID != guest.UserID {
		t.Fatalf("snapshot read audit invalid: %+v", repo.rooms)
	}

	if _, err := service.RoomRules(guest.SessionToken, created.RoomCode); err != nil {
		t.Fatalf("room rules: %v", err)
	}
	if len(repo.rooms) != 5 || repo.rooms[4].Action != "rules_read" || repo.rooms[4].UserID != guest.UserID || repo.rooms[4].ModeConfigHash == "" {
		t.Fatalf("rules audit invalid: %+v", repo.rooms)
	}

	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "audit_guest_deck",
		DeckSnapshot: validDeck("audit_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if len(repo.rooms) != 6 || repo.rooms[5].Action != "joined" || repo.rooms[5].TicketID != joined.TicketID || repo.rooms[5].CurrentPlayers != 2 {
		t.Fatalf("join audit invalid: %+v", repo.rooms)
	}
	retryJoin, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "audit_guest_retry_deck",
		DeckSnapshot: validDeck("audit_guest_retry_deck"),
	})
	if err != nil {
		t.Fatalf("retry join room: %v", err)
	}
	if retryJoin.TicketID != joined.TicketID || len(repo.rooms) != 7 || repo.rooms[6].Action != "join_retry" || repo.rooms[6].TicketID != joined.TicketID || repo.rooms[6].CurrentPlayers != 2 {
		t.Fatalf("join retry audit invalid: response=%+v audits=%+v", retryJoin, repo.rooms)
	}

	polled, err := service.QueueTicket(guest.SessionToken, joined.TicketID)
	if err != nil {
		t.Fatalf("poll room ticket: %v", err)
	}
	if polled.TicketID != joined.TicketID || len(repo.rooms) != 8 || repo.rooms[7].Action != "ticket_read" || repo.rooms[7].TicketID != joined.TicketID || repo.rooms[7].CurrentPlayers != 2 {
		t.Fatalf("ticket read audit invalid: response=%+v audits=%+v", polled, repo.rooms)
	}

	roomEvent, err := service.BusinessEvent(host.SessionToken, BusinessEventRequest{
		Kind:     "room",
		RoomCode: created.RoomCode,
	})
	if err != nil {
		t.Fatalf("room business event: %v", err)
	}
	if roomEvent.Room == nil || len(repo.rooms) != 9 || repo.rooms[8].Action != "snapshot_read" || repo.rooms[8].UserID != host.UserID || repo.rooms[8].CurrentPlayers != 2 {
		t.Fatalf("room business event audit invalid: event=%+v audits=%+v", roomEvent, repo.rooms)
	}
	queueEvent, err := service.BusinessEvent(guest.SessionToken, BusinessEventRequest{
		Kind:     "matchmaking",
		TicketID: joined.TicketID,
	})
	if err != nil {
		t.Fatalf("matchmaking business event: %v", err)
	}
	if queueEvent.Queue == nil || len(repo.rooms) != 10 || repo.rooms[9].Action != "ticket_read" || repo.rooms[9].TicketID != joined.TicketID || repo.rooms[9].CurrentPlayers != 2 {
		t.Fatalf("matchmaking business event audit invalid: event=%+v audits=%+v", queueEvent, repo.rooms)
	}

	chat, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "audit-chat-1",
		Kind:      "chat",
		Text:      "audit hello",
		Metadata:  map[string]any{"client_locale": "en-US"},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	duplicate, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: chat.MessageID,
		Kind:      "chat",
		Text:      "ignored duplicate body",
	})
	if err != nil {
		t.Fatalf("duplicate chat: %v", err)
	}
	if !duplicate.Duplicate || len(repo.messages) != 2 || repo.messages[0].MessageID != chat.MessageID || repo.messages[0].MetadataHash == "" || !repo.messages[1].Duplicate {
		t.Fatalf("message audits invalid: messages=%+v duplicate=%+v", repo.messages, duplicate)
	}
	hostCollision, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: chat.MessageID,
		Kind:      "announcement",
		Text:      "host scoped message id",
	})
	if err != nil {
		t.Fatalf("host message id collision should be scoped by user: %v", err)
	}
	if hostCollision.Duplicate || hostCollision.UserID != host.UserID || hostCollision.Text != "host scoped message id" || len(repo.messages) != 3 {
		t.Fatalf("cross-user message id collision should create a new authoritative message: message=%+v audits=%+v", hostCollision, repo.messages)
	}

	left, err := service.LeaveRoom(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("leave room: %v", err)
	}
	lastRoomAudit := repo.rooms[len(repo.rooms)-1]
	if left.RoomStatus != "cancelled" || lastRoomAudit.Action != "left" || lastRoomAudit.UserID != guest.UserID || lastRoomAudit.CurrentPlayers != 1 {
		t.Fatalf("leave audit invalid: response=%+v audit=%+v", left, lastRoomAudit)
	}
	status := service.LobbyLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.RoomRecords != 3 || status.RoomReadRecords != 7 || status.RulesReadRecords != 1 || status.MessageRecords != 3 || status.RejectedRecords != 0 {
		t.Fatalf("lobby audit status invalid: %+v", status)
	}
	if status.LastSuccessOperation != "left" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") || status.LastSuccessAt.IsZero() {
		t.Fatalf("lobby audit status should expose a non-secret last-success fingerprint: %+v", status)
	}
}

func TestLobbyLifecycleAuditStatusTracksRepositoryWriteFailures(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{err: errors.New("lobby audit db offline")}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	host := mustLogin(t, service, "Lobby Audit Error Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "audit_error_deck",
		DeckSnapshot: validDeck("audit_error_deck"),
	})
	if err != nil {
		t.Fatalf("fallback room creation should remain available while audit failure is surfaced: %v", err)
	}
	if created.RoomCode == "" {
		t.Fatalf("room creation should still return a room: %+v", created)
	}
	status := service.LobbyLifecycleAuditStatus()
	if status.OK || !status.Configured || status.RejectedRecords == 0 || status.LastErrorOperation != "created" || !strings.Contains(status.LastError, "lobby audit db offline") {
		t.Fatalf("lobby audit failure should be visible in status snapshot: %+v", status)
	}
	if !status.ServerAuthoritative {
		t.Fatalf("lobby audit status must stay server authoritative: %+v", status)
	}
}

func TestLobbyLifecycleAuditRecordsRoomReadyTransitions(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	host := mustLogin(t, service, "Ready Audit Host")
	guest := mustLogin(t, service, "Ready Audit Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "ready_host_deck",
		DeckSnapshot: validDeck("ready_host_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "ready_guest_deck",
		DeckSnapshot: validDeck("ready_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.MatchID == "" {
		t.Fatalf("pvp room join should allocate a match: %+v", joined)
	}
	matchedAudits := filterLobbyRoomAudits(repo.rooms, "matched")
	if len(matchedAudits) != 2 {
		t.Fatalf("room match should audit every matched participant, got %+v", matchedAudits)
	}
	if matchedAudits[0].UserID != host.UserID || matchedAudits[0].TicketID != created.TicketID || matchedAudits[0].MatchID != joined.MatchID || matchedAudits[0].CurrentPlayers != 2 {
		t.Fatalf("host matched audit invalid: %+v", matchedAudits[0])
	}
	if matchedAudits[1].UserID != guest.UserID || matchedAudits[1].TicketID != joined.TicketID || matchedAudits[1].MatchID != joined.MatchID || matchedAudits[1].CurrentPlayers != 2 {
		t.Fatalf("guest matched audit invalid: %+v", matchedAudits[1])
	}
	beforeReadyAudits := len(repo.rooms)

	hostReady, err := service.ReadyMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("host ready: %v", err)
	}
	if hostReady.ReadyStatus != "loading" {
		t.Fatalf("first ready should keep match loading: %+v", hostReady)
	}
	guestReady, err := service.ReadyMatch(guest.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("guest ready: %v", err)
	}
	if guestReady.ReadyStatus != "running" || guestReady.MatchStart == nil || guestReady.BattleTicket == nil {
		t.Fatalf("second ready should start room match: %+v", guestReady)
	}
	duplicateHostReady, err := service.ReadyMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("duplicate host ready: %v", err)
	}
	if duplicateHostReady.ReadyCount != 2 || duplicateHostReady.ReadyStatus != "running" {
		t.Fatalf("duplicate ready should remain idempotent: %+v", duplicateHostReady)
	}

	readyAudits := repo.rooms[beforeReadyAudits:]
	if len(readyAudits) != 2 {
		t.Fatalf("expected exactly two first-ready audit records, got %+v", readyAudits)
	}
	if readyAudits[0].Action != "ready" || readyAudits[0].UserID != host.UserID || readyAudits[0].MatchID != joined.MatchID || readyAudits[0].CurrentPlayers != 1 || readyAudits[0].RequiredPlayers != 2 {
		t.Fatalf("host ready audit invalid: %+v", readyAudits[0])
	}
	if readyAudits[1].Action != "ready" || readyAudits[1].UserID != guest.UserID || readyAudits[1].MatchID != joined.MatchID || readyAudits[1].CurrentPlayers != 2 || readyAudits[1].RequiredPlayers != 2 {
		t.Fatalf("guest ready audit invalid: %+v", readyAudits[1])
	}
	status := service.LobbyLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ReadyRecords != 2 || status.LastSuccessOperation != "ready" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") {
		t.Fatalf("ready audit status invalid: %+v", status)
	}
}

func TestLobbyLifecycleAuditRecordsRoomReconnectTransitions(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	host := mustLogin(t, service, "Reconnect Audit Host")
	guest := mustLogin(t, service, "Reconnect Audit Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "reconnect_host_deck",
		DeckSnapshot: validDeck("reconnect_host_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "reconnect_guest_deck",
		DeckSnapshot: validDeck("reconnect_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if _, err := service.ReadyMatch(host.SessionToken, joined.MatchID); err != nil {
		t.Fatalf("host ready: %v", err)
	}
	if _, err := service.ReadyMatch(guest.SessionToken, joined.MatchID); err != nil {
		t.Fatalf("guest ready: %v", err)
	}
	beforeConnectionAudits := len(repo.rooms)

	disconnected, err := service.DisconnectMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if disconnected.ReconnectStatus != "disconnected" || disconnected.Connected {
		t.Fatalf("disconnect response invalid: %+v", disconnected)
	}
	duplicateDisconnect, err := service.DisconnectMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("duplicate disconnect: %v", err)
	}
	if duplicateDisconnect.ReconnectStatus != "disconnected" || duplicateDisconnect.Connected {
		t.Fatalf("duplicate disconnect response invalid: %+v", duplicateDisconnect)
	}
	reconnected, err := service.ReconnectMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if reconnected.ReconnectStatus != "restored" || !reconnected.Connected || reconnected.BattleTicket == nil {
		t.Fatalf("reconnect response invalid: %+v", reconnected)
	}
	duplicateReconnect, err := service.ReconnectMatch(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("duplicate reconnect: %v", err)
	}
	if duplicateReconnect.ReconnectStatus != "restored" || !duplicateReconnect.Connected {
		t.Fatalf("duplicate reconnect response invalid: %+v", duplicateReconnect)
	}

	connectionAudits := repo.rooms[beforeConnectionAudits:]
	if len(connectionAudits) != 2 {
		t.Fatalf("expected disconnect and reconnect audit records only, got %+v", connectionAudits)
	}
	if connectionAudits[0].Action != "disconnected" || connectionAudits[0].UserID != host.UserID || connectionAudits[0].MatchID != joined.MatchID || connectionAudits[0].CurrentPlayers != 1 || connectionAudits[0].RequiredPlayers != 2 {
		t.Fatalf("disconnect audit invalid: %+v", connectionAudits[0])
	}
	if connectionAudits[1].Action != "reconnected" || connectionAudits[1].UserID != host.UserID || connectionAudits[1].MatchID != joined.MatchID || connectionAudits[1].CurrentPlayers != 2 || connectionAudits[1].RequiredPlayers != 2 {
		t.Fatalf("reconnect audit invalid: %+v", connectionAudits[1])
	}
	status := service.LobbyLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ConnectionRecords != 2 || status.LastSuccessOperation != "reconnected" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") {
		t.Fatalf("connection audit status invalid: %+v", status)
	}
}

func TestLobbyLifecycleAuditRecordsRoomHeartbeatTransitions(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	host := mustLogin(t, service, "Audit Heartbeat Host")
	guest := mustLogin(t, service, "Audit Heartbeat Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "audit_heartbeat_host_deck",
		DeckSnapshot: validDeck("audit_heartbeat_host_deck"),
		ModeParams:   map[string]any{"stage_id": "starlit_lanes", "character_id": "balanced"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := service.Heartbeat(host.SessionToken, PresenceHeartbeatRequest{
		TicketID:        created.TicketID,
		LastEventCursor: 0,
		ClientTick:      7,
	}); err != nil {
		t.Fatalf("room heartbeat: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "audit_heartbeat_guest_deck",
		DeckSnapshot: validDeck("audit_heartbeat_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.MatchID == "" {
		t.Fatalf("join should create match: %+v", joined)
	}
	if _, err := service.Heartbeat(host.SessionToken, PresenceHeartbeatRequest{
		TicketID:        created.TicketID,
		MatchID:         joined.MatchID,
		LastEventCursor: 1,
		ClientTick:      11,
	}); err != nil {
		t.Fatalf("match heartbeat: %v", err)
	}

	heartbeatAudits := filterLobbyRoomAudits(repo.rooms, "heartbeat")
	if len(heartbeatAudits) != 2 {
		t.Fatalf("expected queued and matched heartbeat audit records, got %+v", heartbeatAudits)
	}
	if heartbeatAudits[0].RoomCode != created.RoomCode || heartbeatAudits[0].MatchID != "" || heartbeatAudits[0].TicketID != created.TicketID || heartbeatAudits[0].CurrentPlayers != 1 {
		t.Fatalf("queued heartbeat audit invalid: %+v", heartbeatAudits[0])
	}
	if heartbeatAudits[1].RoomCode != created.RoomCode || heartbeatAudits[1].MatchID != joined.MatchID || heartbeatAudits[1].TicketID != created.TicketID || heartbeatAudits[1].CurrentPlayers != 2 {
		t.Fatalf("matched heartbeat audit invalid: %+v", heartbeatAudits[1])
	}
	status := service.LobbyLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.ConnectionRecords != 2 || status.LastSuccessOperation != "heartbeat" || status.RoomRecords != 3 || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") {
		t.Fatalf("heartbeat audit status invalid: %+v", status)
	}
}

func TestRoomHostLeavePromotesRemainingParticipantAuthority(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	host := mustLogin(t, service, "Promoted Host Original")
	guest := mustLogin(t, service, "Promoted Host Guest")
	observer := mustLogin(t, service, "Promoted Host Observer")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "promote_host_deck",
		DeckSnapshot: validDeck("promote_host_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "promote_guest_deck",
		DeckSnapshot: validDeck("promote_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.CurrentPlayers != 2 || joined.RoomStatus != "waiting" {
		t.Fatalf("join should leave room waiting with two players: %+v", joined)
	}

	left, err := service.LeaveRoom(host.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("host leave: %v", err)
	}
	if left.QueueStatus != "cancelled" || left.RoomStatus != "cancelled" || left.CurrentPlayers != 1 {
		t.Fatalf("leaving host ticket response invalid: %+v", left)
	}
	room, err := service.Room(observer.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("observer room read: %v", err)
	}
	if room.RoomStatus != "waiting" || room.HostUserID != guest.UserID || room.CurrentPlayers != 1 || len(room.Participants) != 1 || room.Participants[0].UserID != guest.UserID {
		t.Fatalf("room should promote remaining queued participant as host: %+v", room)
	}
	if _, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "old-host-announcement",
		Kind:      "announcement",
		Text:      "stale host",
	}); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("old host must lose room announcement authority, got %v", err)
	}
	promotedAnnouncement, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "new-host-announcement",
		Kind:      "announcement",
		Text:      "new host ready",
	})
	if err != nil {
		t.Fatalf("promoted host announcement: %v", err)
	}
	if promotedAnnouncement.UserID != guest.UserID || promotedAnnouncement.Kind != "announcement" || !promotedAnnouncement.ServerAuthoritative {
		t.Fatalf("promoted host announcement invalid: %+v", promotedAnnouncement)
	}
	lastRoomAudit := repo.rooms[len(repo.rooms)-1]
	if lastRoomAudit.Action != "snapshot_read" || lastRoomAudit.HostUserID != guest.UserID || lastRoomAudit.CurrentPlayers != 1 {
		t.Fatalf("room read audit must expose promoted host state: %+v", lastRoomAudit)
	}
}

type captureLobbyLifecycleAuditRepo struct {
	rooms    []LobbyRoomAuditRecord
	messages []LobbyMessageAuditRecord
	err      error
}

func filterLobbyRoomAudits(records []LobbyRoomAuditRecord, action string) []LobbyRoomAuditRecord {
	out := []LobbyRoomAuditRecord{}
	for _, record := range records {
		if record.Action == action {
			out = append(out, record)
		}
	}
	return out
}

func (repo *captureLobbyLifecycleAuditRepo) RecordLobbyRoomAudit(record LobbyRoomAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.rooms = append(repo.rooms, record)
	return nil
}

func (repo *captureLobbyLifecycleAuditRepo) RecordLobbyMessageAudit(record LobbyMessageAuditRecord) error {
	if repo.err != nil {
		return repo.err
	}
	repo.messages = append(repo.messages, record)
	return nil
}

func TestInventoryDeckSaveAndMatchUsesServerActiveDeck(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Deck Alice")
	bob := mustLogin(t, service, "Deck Bob")

	bootstrap, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(bootstrap.Inventory.Items) != len(serverCardCatalog) || bootstrap.Decks.ActiveDeckID != defaultDeckID || len(bootstrap.Decks.Decks) != 1 {
		t.Fatalf("bootstrap missing inventory/decks: %+v", bootstrap)
	}

	savedIDs := []string{
		"draw_sigil", "draw_sigil",
		"graze_engine", "graze_engine",
		"purge_charm", "purge_charm",
		"curve_prism", "curve_prism",
		"tempo_break", "tempo_break",
		"hitbox_charm", "hitbox_charm",
		"bomb_amplifier", "bomb_amplifier",
		"focus_lens", "focus_lens",
		"guard_seal", "guard_seal",
		"aim_baffle", "aim_baffle",
	}
	save, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "server_active",
		Name:    "Server Active",
		Format:  defaultDeckFormat,
		CardIDs: savedIDs,
		Active:  true,
	})
	if err != nil {
		t.Fatalf("save deck: %v", err)
	}
	if save.ActiveDeckID != "server_active" || !save.Deck.Active || len(save.Deck.CardIDs) != deckSize {
		t.Fatalf("save response missing active deck: %+v", save)
	}
	decks, err := service.Decks(alice.SessionToken)
	if err != nil {
		t.Fatalf("decks: %v", err)
	}
	if decks.ActiveDeckID != "server_active" || len(decks.Decks) != 2 {
		t.Fatalf("deck list missing saved deck: %+v", decks)
	}
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "ranked_bad",
		Name:    "Ranked Bad",
		Format:  "ranked",
		CardIDs: append(copyStringSlice(savedIDs[:18]), "last_arc", "last_arc"),
		Active:  false,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("expected ranked ban deck error, got %v", err)
	}
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "too_many",
		Name:    "Too Many",
		Format:  defaultDeckFormat,
		CardIDs: append(copyStringSlice(savedIDs[:18]), "focus_lens", "focus_lens"),
		Active:  false,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("expected ownership/copy deck error, got %v", err)
	}

	clientSubmitted := validDeck("client_submitted")
	first, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "server_active",
		DeckSnapshot: clientSubmitted,
		ModeParams:   map[string]any{"stage_id": "starlit_lanes", "character_id": "balanced"},
	})
	if err != nil || first.QueueStatus != "queued" {
		t.Fatalf("alice queue: resp=%+v err=%v", first, err)
	}
	second, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: defaultDeckID,
		DeckSnapshot: validDeck("bob_submitted"),
		ModeParams:   map[string]any{"stage_id": "starlit_lanes", "character_id": "balanced"},
	})
	if err != nil || second.MatchID == "" {
		t.Fatalf("bob queue: resp=%+v err=%v", second, err)
	}
	match := service.matches[second.MatchID]
	if match == nil {
		t.Fatalf("match missing")
	}
	alicePlayer := match.Players[alice.UserID]
	if alicePlayer == nil || alicePlayer.DeckSnapshot.DeckID != "server_active" || alicePlayer.DeckSnapshot.CardIDs[0] != "draw_sigil" {
		t.Fatalf("match did not use server active deck: %+v", alicePlayer)
	}
	if alicePlayer.DeckSnapshot.DeckID == clientSubmitted.DeckID {
		t.Fatalf("match trusted client submitted deck: %+v", alicePlayer.DeckSnapshot)
	}
}

func TestMatchEntryRejectsIncompatibleClientVersions(t *testing.T) {
	now := time.Date(2026, 6, 30, 11, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Version Alice")
	bob := mustLogin(t, service, "Version Bob")
	cara := mustLogin(t, service, "Version Cara")

	current := currentVersionStamp()
	queued, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: current,
	})
	if err != nil || queued.QueueStatus != "queued" {
		t.Fatalf("current version should enter queue: resp=%+v err=%v", queued, err)
	}
	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{ProtocolVersion: ProtocolVersion + 1},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected protocol mismatch rejection, got %v", err)
	}
	if _, err := service.JoinQueue(cara.SessionToken, JoinQueueRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{ProtocolVersion: ProtocolVersion},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected partial client version rejection, got %v", err)
	}
	if _, err := service.CreateRoom(bob.SessionToken, CreateRoomRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{BusinessAPIVersion: "business-api-old"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected business api mismatch rejection, got %v", err)
	}

	room, err := service.CreateRoom(bob.SessionToken, CreateRoomRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: current,
	})
	if err != nil || room.RoomCode == "" {
		t.Fatalf("current version should create room: resp=%+v err=%v", room, err)
	}
	if _, err := service.JoinRoom(cara.SessionToken, room.RoomCode, JoinRoomRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{ProtocolVersion: ProtocolVersion},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected partial room join version rejection, got %v", err)
	}
	if _, err := service.JoinRoom(cara.SessionToken, room.RoomCode, JoinRoomRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{BattleAPIVersion: "battle-api-old"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected battle api mismatch rejection, got %v", err)
	}
	if _, err := service.JoinRoom(cara.SessionToken, room.RoomCode, JoinRoomRequest{
		ModeID:        "pvp_duel",
		ActiveDeckID:  defaultDeckID,
		ClientVersion: VersionStamp{RulesetVersion: "ruleset-old"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected ruleset mismatch rejection, got %v", err)
	}
}

func TestChestOpenUsesServerPoolPityInventoryAndAudit(t *testing.T) {
	now := time.Date(2026, 6, 26, 15, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Chest Alice")

	bootstrap, err := service.Bootstrap(alice.SessionToken)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !bootstrap.Chests.OK || len(bootstrap.Chests.Pools) != 1 || bootstrap.Chests.OwnedChests[defaultChestPoolID] != 1 || bootstrap.Wallet["chest_keys"] != 1 {
		t.Fatalf("bootstrap missing chest projection: %+v", bootstrap.Chests)
	}

	chests, err := service.Chests(alice.SessionToken)
	if err != nil {
		t.Fatalf("chests: %v", err)
	}
	if !chests.OK || !chests.ServerAuthoritative || chests.Pools[0].PoolID != defaultChestPoolID || chests.Pools[0].Pity.RareEvery != 10 {
		t.Fatalf("chest pools invalid: %+v", chests)
	}

	beforeDust := bootstrap.Wallet["card_dust"]
	opened, err := service.OpenChest(alice.SessionToken, ChestOpenRequest{PoolID: defaultChestPoolID, Count: 1})
	if err != nil {
		t.Fatalf("open chest: %v", err)
	}
	if !opened.OK || !opened.ServerAuthoritative || opened.ClientResultAuthoritative || len(opened.Results) != 1 || opened.Audit.OpeningID == "" {
		t.Fatalf("open response invalid: %+v", opened)
	}
	if opened.Wallet["chest_keys"] != 0 || opened.OwnedChests[defaultChestPoolID] != 0 || opened.Audit.Cost["chest_keys"] != 1 {
		t.Fatalf("open did not deduct cost/chest: %+v", opened)
	}
	if opened.Results[0].CardID == "" || opened.Results[0].Dust <= 0 || opened.Results[0].Overflow != 1 {
		t.Fatalf("default full inventory should convert duplicate into dust: %+v", opened.Results[0])
	}
	if opened.Wallet["card_dust"] != beforeDust+opened.Results[0].Dust {
		t.Fatalf("dust wallet not updated: before=%d opened=%+v", beforeDust, opened)
	}
	after, err := service.Chests(alice.SessionToken)
	if err != nil {
		t.Fatalf("chests after open: %v", err)
	}
	if len(after.OpeningLog) != 1 || len(after.LastResults) != 1 || after.OpeningLog[0].ServerSeed == "" {
		t.Fatalf("open audit not projected: %+v", after)
	}
	if _, err := service.OpenChest(alice.SessionToken, ChestOpenRequest{PoolID: defaultChestPoolID, Count: 1}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected insufficient chest/key rejection, got %v", err)
	}
	if _, err := service.OpenChest(alice.SessionToken, ChestOpenRequest{PoolID: defaultChestPoolID, Count: 1, ClientResultAuthoritative: true}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected client result authority rejection, got %v", err)
	}
}

func TestUpgradeCardUsesServerWalletInventoryAndRules(t *testing.T) {
	now := time.Date(2026, 6, 26, 15, 15, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Upgrade Alice")

	service.mu.Lock()
	service.users[alice.UserID].Wallet["card_dust"] = 100
	service.mu.Unlock()

	upgraded, err := service.UpgradeCard(alice.SessionToken, CardUpgradeRequest{CardID: "focus_lens"})
	if err != nil {
		t.Fatalf("upgrade card: %v", err)
	}
	if !upgraded.OK || !upgraded.ServerAuthoritative || upgraded.ClientResultAuthoritative || upgraded.CardID != "focus_lens" || upgraded.OldLevel != 1 || upgraded.NewLevel != 2 || upgraded.MaxLevel != maxCardLevel {
		t.Fatalf("upgrade response invalid: %+v", upgraded)
	}
	if upgraded.Cost["card_dust"] != dustValueForCard("focus_lens") || upgraded.Wallet["card_dust"] != 95 {
		t.Fatalf("upgrade did not deduct expected dust: %+v", upgraded)
	}
	var upgradedEntry CardInventoryEntry
	for _, item := range upgraded.Inventory.Items {
		if item.CardID == "focus_lens" {
			upgradedEntry = item
			break
		}
	}
	if upgradedEntry.Level != 2 || upgradedEntry.Copies != maxCopiesPerCard {
		t.Fatalf("inventory level not projected: %+v", upgraded.Inventory)
	}
	if _, err := service.UpgradeCard(alice.SessionToken, CardUpgradeRequest{CardID: "focus_lens", TargetLevel: 4}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected target level validation, got %v", err)
	}
	if _, err := service.UpgradeCard(alice.SessionToken, CardUpgradeRequest{CardID: "focus_lens", ClientResultAuthoritative: true}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected client authority rejection, got %v", err)
	}

	service.mu.Lock()
	user := service.users[alice.UserID]
	user.Wallet["card_dust"] = 0
	entry := user.Inventory["focus_lens"]
	entry.Level = 2
	user.Inventory["focus_lens"] = entry
	service.mu.Unlock()
	if _, err := service.UpgradeCard(alice.SessionToken, CardUpgradeRequest{CardID: "focus_lens"}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected insufficient dust rejection, got %v", err)
	}

	service.mu.Lock()
	user.Wallet["card_dust"] = 1000
	entry = user.Inventory["focus_lens"]
	entry.Level = maxCardLevel
	user.Inventory["focus_lens"] = entry
	service.mu.Unlock()
	if _, err := service.UpgradeCard(alice.SessionToken, CardUpgradeRequest{CardID: "focus_lens"}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("expected max level rejection, got %v", err)
	}
}

func TestRematchRequiresSettlementAndCreatesFreshMatch(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Rematch Alice")
	bob := mustLogin(t, service, "Rematch Bob")
	intruder := mustLogin(t, service, "Rematch Intruder")

	_, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "rematch_alice_deck",
		DeckSnapshot: validDeck("rematch_alice_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "spell_power"},
	})
	if err != nil {
		t.Fatalf("join alice: %v", err)
	}
	queueBob, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "rematch_bob_deck",
		DeckSnapshot: validDeck("rematch_bob_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	matchID := queueBob.MatchID
	if matchID == "" {
		t.Fatalf("expected match id: %+v", queueBob)
	}
	if _, err := service.RequestRematch(alice.SessionToken, matchID); ErrorCode(err) != codeMatchState {
		t.Fatalf("rematch before settlement should be rejected, got %v", err)
	}
	if _, err := service.RequestRematch(intruder.SessionToken, matchID); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("intruder rematch should be rejected, got %v", err)
	}
	if _, err := service.ReadyMatch(alice.SessionToken, matchID); err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if _, err := service.ReadyMatch(bob.SessionToken, matchID); err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"shoot":     true,
		"card_slot": -1,
	}); err != nil {
		t.Fatalf("input alice: %v", err)
	}
	if _, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{}); err != nil {
		t.Fatalf("settle alice: %v", err)
	}

	waiting, err := service.RequestRematch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("rematch alice: %v", err)
	}
	if !waiting.OK || waiting.RematchStatus != "waiting" || waiting.AcceptedCount != 1 || waiting.RequiredPlayers != 2 || waiting.NewMatchID != "" {
		t.Fatalf("waiting rematch invalid: %+v", waiting)
	}
	if waiting.ModeID != "certification" || waiting.StageID != "clockwork_bloom" || waiting.Loadout.CharacterID != "spell_power" || !waiting.ServerAuthoritative {
		t.Fatalf("waiting rematch missing authority/loadout: %+v", waiting)
	}
	duplicate, err := service.RequestRematch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("duplicate rematch alice: %v", err)
	}
	if duplicate.AcceptedCount != 1 || duplicate.RematchStatus != "waiting" {
		t.Fatalf("duplicate rematch should be idempotent: %+v", duplicate)
	}
	found, err := service.RequestRematch(bob.SessionToken, matchID)
	if err != nil {
		t.Fatalf("rematch bob: %v", err)
	}
	if found.RematchStatus != "found" || found.AcceptedCount != 2 || found.NewMatchID == "" || found.NewMatchID == matchID {
		t.Fatalf("found rematch invalid: %+v", found)
	}
	repeatedFound, err := service.RequestRematch(bob.SessionToken, matchID)
	if err != nil {
		t.Fatalf("repeat found rematch: %v", err)
	}
	if repeatedFound.NewMatchID != found.NewMatchID || repeatedFound.AcceptedCount != 2 {
		t.Fatalf("found rematch should be stable: first=%+v repeat=%+v", found, repeatedFound)
	}
	readyAlice, err := service.ReadyMatch(alice.SessionToken, found.NewMatchID)
	if err != nil {
		t.Fatalf("ready rematch alice: %v", err)
	}
	readyBob, err := service.ReadyMatch(bob.SessionToken, found.NewMatchID)
	if err != nil {
		t.Fatalf("ready rematch bob: %v", err)
	}
	if readyAlice.MatchStart != nil || readyBob.MatchStart == nil || readyBob.MatchStart.StageID != "clockwork_bloom" {
		t.Fatalf("rematch ready/start invalid: alice=%+v bob=%+v", readyAlice, readyBob)
	}
	if readyBob.MatchStart.Players[0].Loadout.StageID != "clockwork_bloom" {
		t.Fatalf("rematch start missing loadout: %+v", readyBob.MatchStart)
	}
	events, err := service.MatchEvents(alice.SessionToken, matchID, 0, 64)
	if err != nil {
		t.Fatalf("original events: %v", err)
	}
	if !hasEventType(events.Events, "rematch_requested") || !hasEventType(events.Events, "rematch_found") {
		t.Fatalf("rematch events missing: %+v", events.Events)
	}
}

func TestMatchmakingLoadoutStageBucketAndValidation(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Stage Alice")
	bob := mustLogin(t, service, "Stage Bob")
	cara := mustLogin(t, service, "Stage Cara")

	first, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "alice_stage_deck",
		DeckSnapshot: validDeck("alice_stage_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "spell_power"},
	})
	if err != nil {
		t.Fatalf("join alice stage: %v", err)
	}
	if first.Loadout.StageID != "lunar_maze" || first.Loadout.CharacterID != "spell_power" || !first.Loadout.ServerAuthoritative {
		t.Fatalf("queue response missing server loadout: %+v", first)
	}
	second, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_stage_deck",
		DeckSnapshot: validDeck("bob_stage_deck"),
		ModeParams:   map[string]any{"stage_id": "misty_crossfire", "character_id": "wide"},
	})
	if err != nil {
		t.Fatalf("join bob stage: %v", err)
	}
	if second.MatchID != "" {
		t.Fatalf("different stages should not be matched: %+v", second)
	}
	third, err := service.JoinQueue(cara.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "cara_stage_deck",
		DeckSnapshot: validDeck("cara_stage_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("join cara stage: %v", err)
	}
	if third.MatchID == "" || third.Loadout.StageID != "lunar_maze" {
		t.Fatalf("same stage should create match: %+v", third)
	}
	readyAlice, err := service.ReadyMatch(alice.SessionToken, third.MatchID)
	if err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	readyCara, err := service.ReadyMatch(cara.SessionToken, third.MatchID)
	if err != nil {
		t.Fatalf("ready cara: %v", err)
	}
	if readyAlice.MatchStart != nil {
		t.Fatalf("first ready should not start alone: %+v", readyAlice)
	}
	if readyCara.MatchStart == nil || readyCara.MatchStart.StageID != "lunar_maze" {
		t.Fatalf("match start should carry stage: %+v", readyCara)
	}
	if readyCara.MatchStart.Players[0].Loadout.RatingCode != defaultRatingCode || readyCara.MatchStart.Players[1].Loadout.RatingCode != defaultRatingCode {
		t.Fatalf("certification match should carry server rating: %+v", readyCara.MatchStart.Players)
	}
	snapshot, err := service.Snapshot(alice.SessionToken, third.MatchID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.StageID != "lunar_maze" || playerSnapshotByUser(*snapshot, alice.UserID).Loadout.CharacterID != "spell_power" {
		t.Fatalf("snapshot missing loadout: %+v", snapshot)
	}

	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_bad_stage_deck",
		DeckSnapshot: validDeck("bob_bad_stage_deck"),
		ModeParams:   map[string]any{"stage_id": "unknown_stage"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected invalid stage rejection, got %v", err)
	}
	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_bad_character_deck",
		DeckSnapshot: validDeck("bob_bad_character_deck"),
		ModeParams:   map[string]any{"character_id": "unknown_character"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected invalid character rejection, got %v", err)
	}
	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_forbidden_stage_deck",
		DeckSnapshot: validDeck("bob_forbidden_stage_deck"),
		ModeParams:   map[string]any{"stage_id": "starlit_lanes", "rank_score": 99999},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected forbidden mode param rejection, got %v", err)
	}
	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "bob_locked_rating_deck",
		DeckSnapshot: validDeck("bob_locked_rating_deck"),
		ModeParams:   map[string]any{"stage_id": "starlit_lanes", "rating_code": "silver"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected locked rating rejection, got %v", err)
	}
}

func TestCancelTicketRemovesQueueAndRoomWait(t *testing.T) {
	repo := &captureLobbyLifecycleAuditRepo{}
	service := NewService(Config{LobbyLifecycleAuditRepo: repo})
	alice := mustLogin(t, service, "Cancel Alice")
	bob := mustLogin(t, service, "Cancel Bob")
	cara := mustLogin(t, service, "Cancel Cara")
	host := mustLogin(t, service, "Cancel Host")
	guest := mustLogin(t, service, "Cancel Guest")
	guestHost := mustLogin(t, service, "Cancel Guest Host")
	guestCancel := mustLogin(t, service, "Cancel Guest Member")
	guestReplacement := mustLogin(t, service, "Cancel Guest Replacement")

	queued, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "cancel_alice_deck",
		DeckSnapshot: validDeck("cancel_alice_deck"),
	})
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	cancelled, err := service.CancelTicket(alice.SessionToken, queued.TicketID)
	if err != nil {
		t.Fatalf("cancel queue: %v", err)
	}
	if !cancelled.OK || cancelled.QueueStatus != "cancelled" || cancelled.CurrentPlayers != 0 || cancelled.MatchID != "" {
		t.Fatalf("cancel response invalid: %+v", cancelled)
	}
	duplicate, err := service.CancelTicket(alice.SessionToken, queued.TicketID)
	if err != nil {
		t.Fatalf("duplicate cancel: %v", err)
	}
	if duplicate.QueueStatus != "cancelled" || duplicate.CurrentPlayers != 0 {
		t.Fatalf("duplicate cancel should be idempotent: %+v", duplicate)
	}
	bobQueued, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "cancel_bob_deck",
		DeckSnapshot: validDeck("cancel_bob_deck"),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	if bobQueued.MatchID != "" || bobQueued.CurrentPlayers != 1 {
		t.Fatalf("cancelled ticket should not remain in queue: %+v", bobQueued)
	}
	caraFound, err := service.JoinQueue(cara.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "cancel_cara_deck",
		DeckSnapshot: validDeck("cancel_cara_deck"),
	})
	if err != nil {
		t.Fatalf("join cara: %v", err)
	}
	if caraFound.MatchID == "" {
		t.Fatalf("bob and cara should match after alice cancelled: %+v", caraFound)
	}
	if _, err := service.CancelTicket(cara.SessionToken, caraFound.TicketID); ErrorCode(err) != codeMatchState {
		t.Fatalf("matched ticket should not cancel, got %v", err)
	}

	room, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "cancel_host_deck",
		DeckSnapshot: validDeck("cancel_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	roomCancel, err := service.CancelTicket(host.SessionToken, room.TicketID)
	if err != nil {
		t.Fatalf("cancel host room: %v", err)
	}
	if roomCancel.QueueStatus != "cancelled" || roomCancel.RoomStatus != "cancelled" || roomCancel.CurrentPlayers != 0 {
		t.Fatalf("host room cancel invalid: %+v", roomCancel)
	}
	if last := repo.rooms[len(repo.rooms)-1]; last.Action != "cancelled" || last.RoomCode != room.RoomCode || last.UserID != host.UserID || last.CurrentPlayers != 0 || last.RoomStatus != "cancelled" {
		t.Fatalf("host room cancel audit invalid: %+v", last)
	}
	if _, err := service.JoinRoom(guest.SessionToken, room.RoomCode, JoinRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "cancel_guest_deck",
		DeckSnapshot: validDeck("cancel_guest_deck"),
	}); ErrorCode(err) != codeNotFound {
		t.Fatalf("empty room should be cleaned up, got %v", err)
	}

	largerRoom, err := service.CreateRoom(guestHost.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "cancel_guest_host_deck",
		DeckSnapshot: validDeck("cancel_guest_host_deck"),
	})
	if err != nil {
		t.Fatalf("create larger room: %v", err)
	}
	joined, err := service.JoinRoom(guestCancel.SessionToken, largerRoom.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "cancel_guest_member_deck",
		DeckSnapshot: validDeck("cancel_guest_member_deck"),
	})
	if err != nil {
		t.Fatalf("join larger room: %v", err)
	}
	guestCancelled, err := service.CancelTicket(guestCancel.SessionToken, joined.TicketID)
	if err != nil {
		t.Fatalf("cancel guest room ticket: %v", err)
	}
	if guestCancelled.QueueStatus != "cancelled" || guestCancelled.RoomStatus != "cancelled" || guestCancelled.CurrentPlayers != 1 {
		t.Fatalf("guest room cancel invalid: %+v", guestCancelled)
	}
	if last := repo.rooms[len(repo.rooms)-1]; last.Action != "cancelled" || last.RoomCode != largerRoom.RoomCode || last.UserID != guestCancel.UserID || last.CurrentPlayers != 1 || last.RoomStatus != "waiting" {
		t.Fatalf("member room cancel audit invalid: %+v", last)
	}
	replacementJoined, err := service.JoinRoom(guestReplacement.SessionToken, largerRoom.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "cancel_guest_replacement_deck",
		DeckSnapshot: validDeck("cancel_guest_replacement_deck"),
	})
	if err != nil {
		t.Fatalf("replacement should join room after guest cancel: %v", err)
	}
	if replacementJoined.RoomStatus != "waiting" || replacementJoined.CurrentPlayers != 2 {
		t.Fatalf("room should remain waiting after guest cancel: %+v", replacementJoined)
	}
	status := service.LobbyLifecycleAuditStatus()
	if !status.OK || !status.Configured || status.RoomRecords != 6 || status.RoomReadRecords != 0 || status.RejectedRecords != 0 {
		t.Fatalf("cancel audit status invalid: %+v", status)
	}
}

func TestHeartbeatReportsQueueRoomAndMatchPresence(t *testing.T) {
	now := time.Date(2026, 6, 26, 15, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Heartbeat Alice")
	bob := mustLogin(t, service, "Heartbeat Bob")
	host := mustLogin(t, service, "Heartbeat Host")

	queued, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "heartbeat_alice_deck",
		DeckSnapshot: validDeck("heartbeat_alice_deck"),
		ModeParams:   map[string]any{"stage_id": "misty_crossfire", "character_id": "wide"},
	})
	if err != nil {
		t.Fatalf("join queue: %v", err)
	}
	queueBeat, err := service.Heartbeat(alice.SessionToken, PresenceHeartbeatRequest{
		TicketID:        queued.TicketID,
		ClientTick:      12,
		LastEventCursor: -2,
	})
	if err != nil {
		t.Fatalf("queue heartbeat: %v", err)
	}
	if !queueBeat.OK || queueBeat.PresenceStatus != "queue_waiting" || queueBeat.QueueStatus != "queued" || queueBeat.CurrentPlayers != 1 || queueBeat.MatchID != "" || queueBeat.LastClientTick != 12 || queueBeat.LastEventCursor != 0 {
		t.Fatalf("queue heartbeat invalid: %+v", queueBeat)
	}
	if queueBeat.Loadout.StageID != "misty_crossfire" || !queueBeat.ServerAuthoritative {
		t.Fatalf("queue heartbeat missing loadout/authority: %+v", queueBeat)
	}

	room, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "heartbeat_host_deck",
		DeckSnapshot: validDeck("heartbeat_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	roomBeat, err := service.Heartbeat(host.SessionToken, PresenceHeartbeatRequest{TicketID: room.TicketID})
	if err != nil {
		t.Fatalf("room heartbeat: %v", err)
	}
	if roomBeat.PresenceStatus != "room_waiting" || roomBeat.RoomCode != room.RoomCode || roomBeat.RoomStatus != "waiting" || roomBeat.CurrentPlayers != 1 || roomBeat.RequiredPlayers != 4 {
		t.Fatalf("room heartbeat invalid: %+v", roomBeat)
	}

	found, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "certification",
		ActiveDeckID: "heartbeat_bob_deck",
		DeckSnapshot: validDeck("heartbeat_bob_deck"),
		ModeParams:   map[string]any{"stage_id": "misty_crossfire", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	if found.MatchID == "" {
		t.Fatalf("expected match found: %+v", found)
	}
	if _, err := service.ReadyMatch(alice.SessionToken, found.MatchID); err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if _, err := service.ReadyMatch(bob.SessionToken, found.MatchID); err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	if _, err := service.SubmitInput(alice.SessionToken, found.MatchID, map[string]any{
		"tick": 1, "seq": 1, "dir": 0, "slow": false, "shoot": true, "bomb": false, "card_slot": -1,
	}); err != nil {
		t.Fatalf("input alice: %v", err)
	}
	matchBeat, err := service.Heartbeat(alice.SessionToken, PresenceHeartbeatRequest{
		TicketID:        queued.TicketID,
		MatchID:         found.MatchID,
		ClientTick:      2,
		LastEventCursor: 1,
	})
	if err != nil {
		t.Fatalf("match heartbeat: %v", err)
	}
	if matchBeat.PresenceStatus != "in_match" || matchBeat.MatchStatus != "running" || !matchBeat.Connected || !matchBeat.Ready || matchBeat.MatchTick < 1 || matchBeat.LatestEventCursor <= matchBeat.LastEventCursor {
		t.Fatalf("match heartbeat invalid: %+v", matchBeat)
	}
	if matchBeat.TicketID != queued.TicketID || matchBeat.Loadout.CharacterID != "wide" {
		t.Fatalf("match heartbeat missing ticket/loadout: %+v", matchBeat)
	}
	if matchBeat.BattleAllocation == nil || matchBeat.BattleAllocation.MatchID != found.MatchID || !matchBeat.BattleAllocation.ServerAuthoritative {
		t.Fatalf("match heartbeat missing server battle allocation descriptor: %+v", matchBeat)
	}
	if matchBeat.BattleTicket == nil || matchBeat.BattleTicket.Ticket.MatchID != found.MatchID || matchBeat.BattleTicket.Ticket.UserID != alice.UserID || matchBeat.BattleTicket.Ticket.BattleServerID != matchBeat.BattleAllocation.BattleServerID || matchBeat.BattleTicket.SignatureHex == "" {
		t.Fatalf("match heartbeat missing signed user-bound battle ticket: %+v", matchBeat)
	}
	if _, err := service.DisconnectMatch(alice.SessionToken, found.MatchID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	disconnectBeat, err := service.Heartbeat(alice.SessionToken, PresenceHeartbeatRequest{MatchID: found.MatchID})
	if err != nil {
		t.Fatalf("disconnect heartbeat: %v", err)
	}
	if disconnectBeat.PresenceStatus != "disconnected" || disconnectBeat.Connected || disconnectBeat.ReconnectSecondsLeft != ReconnectWindowSeconds {
		t.Fatalf("disconnect heartbeat should not reconnect player: %+v", disconnectBeat)
	}
	if disconnectBeat.BattleAllocation == nil || disconnectBeat.BattleTicket == nil || disconnectBeat.BattleTicket.Ticket.UserID != alice.UserID {
		t.Fatalf("disconnect heartbeat should preserve reconnect allocation/ticket descriptor: %+v", disconnectBeat)
	}
}

func TestRoomCodeFlowCreatesMatchAndRejectsLateJoin(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Host")
	guest := mustLogin(t, service, "Guest")
	late := mustLogin(t, service, "Late")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "host_deck",
		DeckSnapshot: validDeck("host_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if created.RoomCode == "" || created.RoomStatus != "waiting" || created.QueueStatus != "queued" || created.MatchID != "" {
		t.Fatalf("created room response invalid: %+v", created)
	}

	hostTicket, err := service.QueueTicket(host.SessionToken, created.TicketID)
	if err != nil {
		t.Fatalf("host ticket before join: %v", err)
	}
	if hostTicket.RoomCode != created.RoomCode || hostTicket.MatchID != "" {
		t.Fatalf("host ticket should still wait in room: %+v", hostTicket)
	}

	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "guest_deck",
		DeckSnapshot: validDeck("guest_deck"),
		ModeParams:   map[string]any{"stage_id": "clockwork_bloom", "character_id": "wide"},
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.MatchID == "" || joined.RoomStatus != "found" || joined.QueueStatus != "found" {
		t.Fatalf("join should create match: %+v", joined)
	}

	hostFound, err := service.QueueTicket(host.SessionToken, created.TicketID)
	if err != nil {
		t.Fatalf("host ticket after join: %v", err)
	}
	if hostFound.MatchID != joined.MatchID || hostFound.RoomStatus != "found" || hostFound.CurrentPlayers != 2 {
		t.Fatalf("host should see room match: %+v", hostFound)
	}
	if hostFound.Loadout.StageID != "clockwork_bloom" || joined.Loadout.StageID != "clockwork_bloom" {
		t.Fatalf("room responses should carry room stage loadouts: host=%+v guest=%+v", hostFound, joined)
	}

	if _, err := service.JoinRoom(late.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "late_deck",
		DeckSnapshot: validDeck("late_deck"),
	}); ErrorCode(err) != codeRoomUnavailable {
		t.Fatalf("expected room_unavailable after match creation, got %v", err)
	}

	openHost := mustLogin(t, service, "Open Host")
	openGuest := mustLogin(t, service, "Open Guest")
	openRoom, err := service.CreateRoom(openHost.SessionToken, CreateRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "open_host_deck",
		DeckSnapshot: validDeck("open_host_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze"},
	})
	if err != nil {
		t.Fatalf("create open room: %v", err)
	}
	if _, err := service.JoinRoom(openGuest.SessionToken, openRoom.RoomCode, JoinRoomRequest{
		ModeID:       "certification",
		ActiveDeckID: "open_guest_deck",
		DeckSnapshot: validDeck("open_guest_deck"),
		ModeParams:   map[string]any{"stage_id": "starlit_lanes"},
	}); ErrorCode(err) != codeInvalidMode {
		t.Fatalf("expected mismatched room stage rejection, got %v", err)
	}
}

func TestBusinessEventNotificationKindsDriveDispatcher(t *testing.T) {
	service := NewService(Config{})
	user := mustLogin(t, service, "Business Notifications")

	presence, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{})
	if err != nil {
		t.Fatalf("default presence business event: %v", err)
	}
	if !presence.OK || presence.Kind != "presence" || presence.Topic != "nakama_wss.business.presence" {
		t.Fatalf("presence business event invalid: %+v", presence)
	}
	for _, expected := range []string{"presence", "queue", "room", "matchmaking", "match.ready", "activity", "battle.allocation", "battle.ticket", "settlement"} {
		if !stringSliceContains(presence.BusinessNotifications, expected) {
			t.Fatalf("business event notification contract missing %q: %+v", expected, presence.BusinessNotifications)
		}
	}
	if stringSliceContains(presence.BusinessNotifications, "battle.result.submit") {
		t.Fatalf("business notifications must not expose service-origin result submit: %+v", presence.BusinessNotifications)
	}
	activity, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{Kind: "activity"})
	if err != nil {
		t.Fatalf("activity business event: %v", err)
	}
	if activity.Activity == nil || !activity.Activity.OK || !activity.Activity.ServerAuthoritative || activity.Activity.UserID != user.UserID {
		t.Fatalf("activity business event should expose server-owned activity snapshot: %+v", activity)
	}
	if len(activity.Activity.Tasks) == 0 || len(activity.Activity.Events) == 0 || len(activity.Activity.Leaderboards) == 0 {
		t.Fatalf("activity business event missing activity projections: %+v", activity.Activity)
	}
	if activity.Activity.ClaimableTasks != 0 || activity.Activity.ClaimableEvents != 0 || activity.Activity.ClaimableLeaderboards != 0 {
		t.Fatalf("fresh activity snapshot should not mark rewards claimable: %+v", activity.Activity)
	}
	if !stringSliceContains(activity.BusinessNotifications, "activity") || !stringSliceContains(activity.AllowedClientRPCOperations, "activity.claim") || stringSliceContains(activity.AllowedClientWSSOperations, "activity.claim") {
		t.Fatalf("activity notification must stay read-only on WSS and keep claim RPC-only: %+v", activity)
	}
	if activity.HighFrequencyBattleTickAllowed || activity.ClientResultSubmitAllowed || stringSliceContains(activity.AllowedClientOperations, "battle.result.submit") {
		t.Fatalf("activity business event must not authorize battle tick or client result submit: %+v", activity)
	}
	if _, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{Kind: "battle.result.submit"}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("service-origin result submit must not be accepted as a business event kind, got %v", err)
	}

	queued, err := service.JoinQueue(user.SessionToken, JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "business_event_queue_deck",
		DeckSnapshot: validDeck("business_event_queue_deck"),
	})
	if err != nil {
		t.Fatalf("join queue for business event: %v", err)
	}
	queueEvent, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{
		Kind:     "queue",
		TicketID: queued.TicketID,
	})
	if err != nil {
		t.Fatalf("queue business event: %v", err)
	}
	if queueEvent.Queue == nil || queueEvent.Queue.TicketID != queued.TicketID || queueEvent.Room != nil || queueEvent.Ready != nil {
		t.Fatalf("queue business event should expose queue state only: %+v", queueEvent)
	}
	if _, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{
		Kind:     "room",
		TicketID: queued.TicketID,
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("room business event must require room-bound state, got %v", err)
	}
	if _, err := service.BusinessEvent(user.SessionToken, BusinessEventRequest{
		Kind:     "match.ready",
		TicketID: queued.TicketID,
	}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("match.ready business event must require matched state, got %v", err)
	}
}

func TestRoomLobbyListRulesAndLeave(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Lobby Host")
	guest := mustLogin(t, service, "Lobby Guest")
	replacement := mustLogin(t, service, "Lobby Replacement")
	intruder := mustLogin(t, service, "Lobby Intruder")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "host_room_deck",
		DeckSnapshot: validDeck("host_room_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if created.RoomCode == "" || created.RoomStatus != "waiting" || created.RequiredPlayers != 4 || created.CurrentPlayers != 1 {
		t.Fatalf("created room response invalid: %+v", created)
	}
	duplicateCreate, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "host_room_deck",
		DeckSnapshot: validDeck("host_room_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if err != nil {
		t.Fatalf("duplicate create room: %v", err)
	}
	if duplicateCreate.RoomCode != created.RoomCode || duplicateCreate.TicketID != created.TicketID || duplicateCreate.CurrentPlayers != 1 {
		t.Fatalf("duplicate room create should return existing room ticket: first=%+v duplicate=%+v", created, duplicateCreate)
	}
	if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "bad_authority",
		DeckSnapshot: validDeck("bad_authority"),
		ModeParams:   map[string]any{"damage": 999},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected forbidden mode param rejection, got %v", err)
	}

	list, err := service.ListRooms(guest.SessionToken)
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if !list.OK || len(list.Rooms) != 1 || list.Rooms[0].RoomCode != created.RoomCode || list.Rooms[0].CurrentPlayers != 1 {
		t.Fatalf("room list invalid: %+v", list)
	}
	if list.Rooms[0].Participants[0].DeckSnapshotHash == "" || list.Rooms[0].Participants[0].Loadout.StageID != "lunar_maze" {
		t.Fatalf("room participant should expose hash and server loadout only: %+v", list.Rooms[0].Participants[0])
	}

	rules, err := service.RoomRules(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room rules: %v", err)
	}
	if !rules.OK || !rules.ServerAuthoritative || rules.Version.ProtocolVersion != ProtocolVersion || rules.Mode.ModeID != "world_boss" {
		t.Fatalf("room rules protocol invalid: %+v", rules)
	}
	if rules.Room.ModeConfigHash == "" || rules.Room.RulesetVersion != RulesetVersion || rules.BattleTicketTTL != BattleTicketTTLSeconds {
		t.Fatalf("room rules missing version hashes: %+v", rules)
	}
	if !rules.BusinessEnvelope || rules.ClientResultSubmit || rules.HighFrequencyBattleTickAllowed {
		t.Fatalf("room rules should require business envelope and reject client result submit/high-frequency tick: %+v", rules)
	}
	if !stringSliceContains(rules.BusinessTransports, "nakama_https_rpc") || !stringSliceContains(rules.BusinessTransports, "nakama_wss") || !stringSliceContains(rules.BattleTransports, "kcp_udp") {
		t.Fatalf("room rules should publish business and battle transport contract: %+v", rules)
	}
	if !stringSliceContains(rules.ClientOperations, "battle.servers") || !stringSliceContains(rules.ClientOperations, "battle.ticket") || !stringSliceContains(rules.ClientOperations, "match.ready") || !stringSliceContains(rules.ClientOperations, "matchmaking.cancel") || !stringSliceContains(rules.ClientOperations, "rooms.chat") || !stringSliceContains(rules.ClientOperations, "rooms.announcement") || stringSliceContains(rules.ClientOperations, "battle.result.submit") || stringSliceContains(rules.ClientOperations, "battle.servers.register") {
		t.Fatalf("room rules should keep client operations read/intent only: %+v", rules)
	}
	for _, forbiddenOp := range []string{"match.input", "match.snapshot", "match.events", "match.settle", "battle.input", "battle.snapshot", "battle.events", "battle.result.submit"} {
		if !stringSliceContains(rules.DisallowedClientOperations, forbiddenOp) || stringSliceContains(rules.ClientOperations, forbiddenOp) || stringSliceContains(rules.ClientRPCOperations, forbiddenOp) || stringSliceContains(rules.ClientWSSOperations, forbiddenOp) {
			t.Fatalf("room rules should explicitly disallow high-frequency/result client op %q: %+v", forbiddenOp, rules)
		}
	}
	if !stringSliceContains(rules.ClientRPCOperations, "battle.allocation") || !stringSliceContains(rules.ClientWSSOperations, "battle.ticket") || stringSliceContains(rules.ClientRPCOperations, "battle.result.submit") || stringSliceContains(rules.ClientWSSOperations, "battle.ticket.consume") {
		t.Fatalf("room rules should publish split client RPC/WSS operations without service callbacks: %+v", rules)
	}
	if !stringSliceContains(rules.ClientRPCOperations, "activity.claim") || stringSliceContains(rules.ClientWSSOperations, "activity.claim") {
		t.Fatalf("room rules should keep RPC-only activity claim out of WSS contract: %+v", rules)
	}
	if !stringSliceContains(rules.ServiceCallbacks, "battle.result.submit") || !stringSliceContains(rules.ServiceCallbacks, "battle.ticket.consume") {
		t.Fatalf("room rules should publish service-only battle callbacks: %+v", rules)
	}
	if rules.ServiceCallbackContext["runtime_ctx_mode"] != "rpc" || rules.ServiceCallbackContext["gensoulkyo_service_origin"] != "battle_server" || rules.ServiceCallbackContext["gensoulkyo_battle_callback"] != "true" {
		t.Fatalf("room rules should publish trusted service callback context requirements: %+v", rules.ServiceCallbackContext)
	}
	if rules.ServiceCallbackContext["player_session_context_allowed"] != "false" || rules.ServiceCallbackContext["business_envelope_allowed"] != "false" {
		t.Fatalf("room rules should keep service callbacks out of player/envelope paths: %+v", rules.ServiceCallbackContext)
	}
	if !stringSliceContains(rules.BusinessNotifications, "activity") || !stringSliceContains(rules.BusinessNotifications, "battle.allocation") || !stringSliceContains(rules.BusinessNotifications, "battle.ticket") || !stringSliceContains(rules.BusinessNotifications, "settlement") || stringSliceContains(rules.BusinessNotifications, "battle.result.submit") {
		t.Fatalf("room rules should publish low-frequency business WSS notifications only: %+v", rules)
	}
	if !stringSliceContains(rules.ForbiddenFields, "damage") || !stringSliceContains(rules.ServerAuthority, "state_snapshot") || !stringSliceContains(rules.ClientAuthority, "input_packet") {
		t.Fatalf("room authority fields missing: %+v", rules)
	}
	event, err := service.BusinessEvent(host.SessionToken, BusinessEventRequest{
		Kind:     "room",
		RoomCode: created.RoomCode,
	})
	if err != nil {
		t.Fatalf("room business event: %v", err)
	}
	if !reflect.DeepEqual(event.AllowedClientOperations, rules.ClientOperations) || !reflect.DeepEqual(event.ServiceCallbacks, rules.ServiceCallbacks) {
		t.Fatalf("room rules and business event contract drifted: rules=%+v callbacks=%+v event=%+v callbacks=%+v", rules.ClientOperations, rules.ServiceCallbacks, event.AllowedClientOperations, event.ServiceCallbacks)
	}
	if !reflect.DeepEqual(event.DisallowedClientOperations, rules.DisallowedClientOperations) {
		t.Fatalf("room rules and business event disallowed operation contract drifted: rules=%+v event=%+v", rules.DisallowedClientOperations, event.DisallowedClientOperations)
	}
	if !reflect.DeepEqual(event.ServiceCallbackContext, rules.ServiceCallbackContext) {
		t.Fatalf("room rules and business event service callback context drifted: rules=%+v event=%+v", rules.ServiceCallbackContext, event.ServiceCallbackContext)
	}
	if !reflect.DeepEqual(event.AllowedClientRPCOperations, rules.ClientRPCOperations) || !reflect.DeepEqual(event.AllowedClientWSSOperations, rules.ClientWSSOperations) {
		t.Fatalf("room rules and business event split client operation contract drifted: rules rpc=%+v wss=%+v event rpc=%+v wss=%+v", rules.ClientRPCOperations, rules.ClientWSSOperations, event.AllowedClientRPCOperations, event.AllowedClientWSSOperations)
	}
	if !reflect.DeepEqual(event.BusinessNotifications, rules.BusinessNotifications) {
		t.Fatalf("room rules and business event notification contract drifted: rules=%+v event=%+v", rules.BusinessNotifications, event.BusinessNotifications)
	}
	if !event.BusinessEnvelopeRequired || !reflect.DeepEqual(event.ForbiddenFields, rules.ForbiddenFields) {
		t.Fatalf("room business event should expose the same security contract as room rules: rules=%+v event=%+v", rules.ForbiddenFields, event.ForbiddenFields)
	}
	if event.Version.ProtocolVersion != ProtocolVersion || event.Version.RulesetVersion != RulesetVersion || event.Version.BusinessAPIVersion != BusinessAPIVersion || event.Version.BattleAPIVersion != BattleAPIVersion {
		t.Fatalf("room business event must expose protocol version stamp: %+v", event.Version)
	}
	if rules.HighFrequencyBattleTickAllowed != event.HighFrequencyBattleTickAllowed || event.HighFrequencyBattleTickAllowed || event.ClientResultSubmitAllowed || stringSliceContains(event.AllowedClientOperations, "battle.result.submit") {
		t.Fatalf("room rules/event must not authorize battle tick or client result submit: rules=%+v event=%+v", rules, event)
	}

	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "guest_room_deck",
		DeckSnapshot: validDeck("guest_room_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.RoomStatus != "waiting" || joined.CurrentPlayers != 2 {
		t.Fatalf("joined room invalid: %+v", joined)
	}
	duplicateJoin, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "guest_room_deck",
		DeckSnapshot: validDeck("guest_room_deck"),
	})
	if err != nil {
		t.Fatalf("duplicate join room: %v", err)
	}
	if duplicateJoin.TicketID != joined.TicketID || duplicateJoin.CurrentPlayers != 2 || duplicateJoin.RoomStatus != "waiting" {
		t.Fatalf("duplicate join should return existing participant ticket: first=%+v duplicate=%+v", joined, duplicateJoin)
	}
	if _, err := service.LobbyMessage(intruder.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "intruder-msg",
		Kind:      "chat",
		Text:      "not in this room",
	}); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("expected non-participant chat rejection, got %v", err)
	}
	chat, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "guest-chat-1",
		Kind:      "chat",
		Text:      "ready for boss",
		Metadata:  map[string]any{"client_locale": "en-US"},
	})
	if err != nil {
		t.Fatalf("guest chat: %v", err)
	}
	if chat.Kind != "chat" || chat.UserID != guest.UserID || !chat.ServerAuthoritative || chat.Metadata["client_locale"] != "en-US" {
		t.Fatalf("chat metadata invalid: %+v", chat)
	}
	duplicate, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "guest-chat-1",
		Kind:      "chat",
		Text:      "different text should not replace",
	})
	if err != nil {
		t.Fatalf("duplicate guest chat: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Text != "ready for boss" {
		t.Fatalf("duplicate chat should return original metadata: %+v", duplicate)
	}
	if _, err := service.LobbyMessage(guest.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "guest-announcement",
		Kind:      "announcement",
		Text:      "guest cannot pin",
	}); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("expected guest announcement rejection, got %v", err)
	}
	announcement, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "host-announcement-1",
		Kind:      "announcement",
		Text:      "boss route locked",
		Metadata:  map[string]any{"cta": "ready"},
	})
	if err != nil {
		t.Fatalf("host announcement: %v", err)
	}
	if announcement.Kind != "announcement" || announcement.UserID != host.UserID || announcement.ModeID != "world_boss" {
		t.Fatalf("announcement invalid: %+v", announcement)
	}
	if _, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "bad-authority-message",
		Kind:      "chat",
		Text:      "bad",
		Metadata:  map[string]any{"reward": "client-authored"},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected authority metadata rejection, got %v", err)
	}
	if _, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "bad-nested-authority-message",
		Kind:      "chat",
		Text:      "bad",
		Metadata:  map[string]any{"nested": map[string]any{"server_authoritative": false}},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected nested authority metadata rejection, got %v", err)
	}
	afterMessages, err := service.Room(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room after messages: %v", err)
	}
	if len(afterMessages.Messages) != 2 || afterMessages.Messages[0].MessageID != "guest-chat-1" || afterMessages.Messages[1].Kind != "announcement" {
		t.Fatalf("room should expose lobby messages: %+v", afterMessages.Messages)
	}
	left, err := service.LeaveRoom(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("leave guest: %v", err)
	}
	if left.QueueStatus != "cancelled" || left.RoomStatus != "cancelled" || left.CurrentPlayers != 1 {
		t.Fatalf("guest leave should keep host room waiting: %+v", left)
	}
	afterGuestLeave, err := service.Room(host.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room after guest leave: %v", err)
	}
	if afterGuestLeave.RoomStatus != "waiting" || afterGuestLeave.CurrentPlayers != 1 {
		t.Fatalf("room should remain joinable after guest leave: %+v", afterGuestLeave)
	}
	rejoined, err := service.JoinRoom(replacement.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "replacement_room_deck",
		DeckSnapshot: validDeck("replacement_room_deck"),
	})
	if err != nil {
		t.Fatalf("replacement join: %v", err)
	}
	if rejoined.CurrentPlayers != 2 {
		t.Fatalf("replacement join invalid: %+v", rejoined)
	}
	hostLeft, err := service.LeaveRoom(host.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("host leave: %v", err)
	}
	if hostLeft.QueueStatus != "cancelled" || hostLeft.RoomStatus != "cancelled" || hostLeft.CurrentPlayers != 1 {
		t.Fatalf("host leave should keep replacement room waiting: %+v", hostLeft)
	}
	afterHostLeave, err := service.Room(replacement.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room after host leave: %v", err)
	}
	if afterHostLeave.HostUserID != replacement.UserID || afterHostLeave.RoomStatus != "waiting" || afterHostLeave.CurrentPlayers != 1 {
		t.Fatalf("host leave should transfer host to replacement: %+v", afterHostLeave)
	}
	replacementAnnouncement, err := service.LobbyMessage(replacement.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "replacement-announcement-1",
		Kind:      "announcement",
		Text:      "new host ready",
	})
	if err != nil || replacementAnnouncement.UserID != replacement.UserID {
		t.Fatalf("replacement host announcement failed: msg=%+v err=%v", replacementAnnouncement, err)
	}
	if _, err := service.LobbyMessage(host.SessionToken, LobbyMessageRequest{
		RoomCode:  created.RoomCode,
		MessageID: "former-host-announcement",
		Kind:      "announcement",
		Text:      "former host cannot pin",
	}); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("expected former host announcement rejection, got %v", err)
	}
	if _, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "guest_after_cancel",
		DeckSnapshot: validDeck("guest_after_cancel"),
	}); err != nil {
		t.Fatalf("guest should be able to rejoin after host transfer: %v", err)
	}
	guestLeftAgain, err := service.LeaveRoom(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("guest leave after rejoin: %v", err)
	}
	if guestLeftAgain.CurrentPlayers != 1 {
		t.Fatalf("guest leave after rejoin should leave replacement host: %+v", guestLeftAgain)
	}
	replacementLeft, err := service.LeaveRoom(replacement.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("replacement final leave: %v", err)
	}
	if replacementLeft.QueueStatus != "cancelled" || replacementLeft.RoomStatus != "cancelled" || replacementLeft.CurrentPlayers != 0 {
		t.Fatalf("last member leave should clean empty room: %+v", replacementLeft)
	}
	duplicateReplacementLeave, err := service.LeaveRoom(replacement.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("duplicate replacement leave: %v", err)
	}
	if duplicateReplacementLeave.TicketID != replacementLeft.TicketID || duplicateReplacementLeave.CurrentPlayers != 0 || duplicateReplacementLeave.RoomStatus != "cancelled" {
		t.Fatalf("duplicate leave should remain cancelled after empty-room cleanup: %+v", duplicateReplacementLeave)
	}
	if _, err := service.Room(host.SessionToken, created.RoomCode); ErrorCode(err) != codeNotFound {
		t.Fatalf("expected empty room cleanup, got %v", err)
	}
}

func TestServerSimulationRejectsClientAuthoredCombatAndAdvancesBullets(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Alice Sim")
	bob := mustLogin(t, service, "Bob Sim")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	resp, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	})
	if err != nil {
		t.Fatalf("input tick 1: %v", err)
	}
	assertAuthoritativeBulletSnapshot(t, resp.Snapshot)
	if got := intFromAny(resp.Snapshot.ModeState["boss_hp_preview"]); got != bossMaxHPForMode("certification")-4 {
		t.Fatalf("server should reduce boss hp from shoot intent, got %d state=%+v", got, resp.Snapshot.ModeState)
	}
	if resp.Snapshot.Players[0].DamageDealt <= 0 && resp.Snapshot.Players[1].DamageDealt <= 0 {
		t.Fatalf("expected server-authored player damage in snapshot: %+v", resp.Snapshot.Players)
	}

	move, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      10,
		"seq":       2,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	})
	if err != nil {
		t.Fatalf("input tick 10: %v", err)
	}
	if !hasBulletOp(move.Snapshot.BulletsDelta, "move") {
		t.Fatalf("expected movement delta by tick 10, got %+v", move.Snapshot.BulletsDelta)
	}

	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":    11,
		"seq":     3,
		"dir":     0,
		"boss_hp": 1,
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected boss_hp client field rejection, got %v", err)
	}
}

func TestServerSimulationRejectsLargeInitialTickJump(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Alice Jump")
	bob := mustLogin(t, service, "Bob Jump")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      TickRate*6 + 1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	}); ErrorCode(err) != codeInvalidInput {
		t.Fatalf("expected large tick jump rejection, got %v", err)
	}
}

func TestServerValidatesCardRequestsAndSnapshotsActiveCards(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Alice Cards")
	bob := mustLogin(t, service, "Bob Cards")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	denied, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     false,
		"bomb":      false,
		"card_slot": 2,
	})
	if err != nil {
		t.Fatalf("card denied input should still be accepted: %v", err)
	}
	if denied.Snapshot.Players[0].CardPlays != 0 && denied.Snapshot.Players[1].CardPlays != 0 {
		t.Fatalf("not-enough-energy card request should not increment plays: %+v", denied.Snapshot.Players)
	}
	if !hasEventType(denied.Snapshot.Events, "card_rejected") {
		t.Fatalf("expected card rejection event, got %+v", denied.Snapshot.Events)
	}

	accepted, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      2,
		"seq":       2,
		"dir":       0,
		"slow":      false,
		"shoot":     false,
		"bomb":      false,
		"card_slot": 0,
	})
	if err != nil {
		t.Fatalf("card accepted input: %v", err)
	}
	if len(accepted.Snapshot.ActiveCards) != 1 {
		t.Fatalf("expected active card snapshot, got %+v", accepted.Snapshot.ActiveCards)
	}
	card := accepted.Snapshot.ActiveCards[0]
	if card.CardID != "focus_lens" || card.UserID != alice.UserID || card.ExpiresTick <= accepted.Snapshot.Tick || card.Cost <= 0 {
		t.Fatalf("active card snapshot missing authority fields: %+v", card)
	}
	if !hasEventType(accepted.Snapshot.Events, "card_accepted") {
		t.Fatalf("expected card accepted event, got %+v", accepted.Snapshot.Events)
	}
	if accepted.Snapshot.Players[0].HandSize == 0 && accepted.Snapshot.Players[1].HandSize == 0 {
		t.Fatalf("expected hand size projection in player snapshot: %+v", accepted.Snapshot.Players)
	}

	full, err := service.Snapshot(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("full snapshot: %v", err)
	}
	if len(full.ActiveCards) != 1 || full.ActiveCards[0].ActivationID != card.ActivationID {
		t.Fatalf("full snapshot should preserve active card projection: %+v", full.ActiveCards)
	}

	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":         3,
		"seq":          3,
		"dir":          0,
		"active_cards": []any{},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected client active_cards rejection, got %v", err)
	}
}

func TestReconnectWindowRestoresFullSnapshotAndRejectsDisconnectedInput(t *testing.T) {
	now := time.Date(2026, 6, 26, 13, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice Reconnect")
	bob := mustLogin(t, service, "Bob Reconnect")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	}); err != nil {
		t.Fatalf("initial input: %v", err)
	}
	disconnected, err := service.DisconnectMatch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if disconnected.ReconnectStatus != "disconnected" || disconnected.Connected || disconnected.SecondsLeft != ReconnectWindowSeconds {
		t.Fatalf("disconnect response invalid: %+v", disconnected)
	}
	if len(disconnected.Snapshot.Players) == 0 || playerSnapshotByUser(disconnected.Snapshot, alice.UserID).Connected {
		t.Fatalf("snapshot should mark alice disconnected: %+v", disconnected.Snapshot.Players)
	}
	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      2,
		"seq":       2,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	}); ErrorCode(err) != codeMatchState {
		t.Fatalf("expected disconnected input rejection, got %v", err)
	}

	now = now.Add(12 * time.Second)
	restored, err := service.ReconnectMatch(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if restored.ReconnectStatus != "restored" || !restored.Connected || restored.MatchStart == nil || !restored.Snapshot.Full || restored.SecondsLeft >= ReconnectWindowSeconds {
		t.Fatalf("reconnect response invalid: %+v", restored)
	}
	if !playerSnapshotByUser(restored.Snapshot, alice.UserID).Connected {
		t.Fatalf("snapshot should mark alice connected: %+v", restored.Snapshot.Players)
	}
	if _, err := service.SubmitInput(alice.SessionToken, matchID, map[string]any{
		"tick":      2,
		"seq":       2,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	}); err != nil {
		t.Fatalf("input after reconnect: %v", err)
	}
	events, err := service.MatchEvents(alice.SessionToken, matchID, 0, 8)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !events.OK || events.MatchID != matchID || events.Cursor <= 0 || events.LatestCursor < events.Cursor || len(events.Events) == 0 {
		t.Fatalf("event stream response invalid: %+v", events)
	}
	if !hasEventType(events.Events, "player_disconnected") || !hasEventType(events.Events, "player_reconnected") {
		t.Fatalf("event stream should include reconnect lifecycle events: %+v", events.Events)
	}
	tail, err := service.MatchEvents(alice.SessionToken, matchID, events.Cursor, 8)
	if err != nil {
		t.Fatalf("tail events: %v", err)
	}
	if len(tail.Events) != 0 || tail.Cursor != events.Cursor {
		t.Fatalf("tail event stream should be empty at cursor: %+v", tail)
	}
}

func TestReconnectWindowExpires(t *testing.T) {
	now := time.Date(2026, 6, 26, 14, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Alice Expire")
	bob := mustLogin(t, service, "Bob Expire")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")

	if _, err := service.DisconnectMatch(alice.SessionToken, matchID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	now = now.Add(time.Duration(ReconnectWindowSeconds+1) * time.Second)
	if _, err := service.ReconnectMatch(alice.SessionToken, matchID); ErrorCode(err) != codeReconnectExpired {
		t.Fatalf("expected reconnect expiry, got %v", err)
	}
}

func TestServerValidatesModeActions(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "BR 1"),
		mustLogin(t, service, "BR 2"),
		mustLogin(t, service, "BR 3"),
		mustLogin(t, service, "BR 4"),
		mustLogin(t, service, "BR 5"),
	}
	matchID := matchPlayers(t, service, players, "battle_royale")
	candidates := battleRoyaleCandidates(service.matches[matchID], 0)
	if len(candidates) != 3 {
		t.Fatalf("expected server candidates: %+v", candidates)
	}
	var fixturePayload map[string]any
	if err := json.Unmarshal([]byte(phkv1.BattleModeActionPayloadJSON), &fixturePayload); err != nil {
		t.Fatalf("mode action fixture payload: %v", err)
	}
	fixtureCardID := strings.TrimSpace(asString(fixturePayload["card_id"]))
	if fixtureCardID == "" {
		t.Fatalf("mode action fixture missing card_id: %+v", fixturePayload)
	}
	candidates[0] = fixtureCardID
	fixturePayload["card_id"] = candidates[0]
	accepted, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "battle_royale",
		"action_type": phkv1.BattleModeActionActionType,
		"payload":     fixturePayload,
	})
	if err != nil {
		t.Fatalf("select mode action: %v", err)
	}
	if !accepted.Accepted || accepted.ActionType != phkv1.BattleModeActionActionType || accepted.Event.Seq == 0 || !accepted.ServerAuthoritative || accepted.ClientResultAuthoritative {
		t.Fatalf("mode action response invalid: %+v", accepted)
	}
	if got := intFromAny(accepted.ModeState["choice_deadline_tick"]); got != TickRate*30 {
		t.Fatalf("battle royale state should expose server deadline, got %+v", accepted.ModeState)
	}
	duplicate, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "battle_royale",
		"action_type": "select_round_card",
		"payload": map[string]any{
			"card_id":     candidates[1],
			"round_index": 0,
		},
	})
	if err != nil {
		t.Fatalf("duplicate mode action should be domain rejection: %v", err)
	}
	if duplicate.Accepted || duplicate.Reason != codeModeAction || duplicate.Status != "rejected" {
		t.Fatalf("expected duplicate selection rejection, got %+v", duplicate)
	}
	if _, err := service.SubmitModeAction(players[1].SessionToken, matchID, map[string]any{
		"mode_id":                     "battle_royale",
		"action_type":                 "select_round_card",
		"client_result_authoritative": true,
		"payload":                     map[string]any{"card_id": candidates[1], "round_index": 0},
	}); ErrorCode(err) != codeForbiddenField {
		t.Fatalf("expected client authority rejection, got %v", err)
	}
	events, err := service.MatchEvents(players[0].SessionToken, matchID, 0, 32)
	if err != nil {
		t.Fatalf("mode events: %v", err)
	}
	if !hasEventType(events.Events, "mode_action_accepted") || !hasEventType(events.Events, "mode_action_rejected") {
		t.Fatalf("mode action events missing: %+v", events.Events)
	}
	if !strings.HasPrefix(phkv1.BattleSnapshotStateHash, "sha256:") || phkv1.BattleSnapshotEventCursor != phkv1.BattleEventCursor {
		t.Fatalf("shared snapshot/event fixture drift: state=%s snapshot_cursor=%d event_cursor=%d", phkv1.BattleSnapshotStateHash, phkv1.BattleSnapshotEventCursor, phkv1.BattleEventCursor)
	}
	if phkv1.BattleEventType == "" || !phkv1.BattleEventServerAuthoritative {
		t.Fatalf("shared battle event fixture must remain authoritative: type=%s authoritative=%v", phkv1.BattleEventType, phkv1.BattleEventServerAuthoritative)
	}
	snapshot, err := service.Snapshot(players[0].SessionToken, matchID)
	if err != nil {
		t.Fatalf("mode snapshot: %v", err)
	}
	if snapshot.StateHash == "" || snapshot.Tick < 0 || snapshot.Events == nil {
		t.Fatalf("runtime snapshot missing protocol-shaped state: %+v", snapshot)
	}
}

func TestServerValidatesBossTransferModeAction(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Boss 1"),
		mustLogin(t, service, "Boss 2"),
		mustLogin(t, service, "Boss 3"),
		mustLogin(t, service, "Boss 4"),
	}
	matchID := matchPlayers(t, service, players, "world_boss")
	transfer, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "world_boss",
		"action_type": "transfer_card",
		"payload": map[string]any{
			"from_player_id": players[0].UserID,
			"to_player_id":   players[1].UserID,
			"card_id":        "focus_lens",
		},
	})
	if err != nil {
		t.Fatalf("boss transfer: %v", err)
	}
	if !transfer.Accepted || transfer.Event.FromUserID != players[0].UserID || transfer.Event.ToUserID != players[1].UserID {
		t.Fatalf("boss transfer response invalid: %+v", transfer)
	}
	if intFromAny(transfer.ModeState["transferred_card_count"]) != 1 {
		t.Fatalf("boss transfer state missing count: %+v", transfer.ModeState)
	}
	duplicate, err := service.SubmitModeAction(players[0].SessionToken, matchID, map[string]any{
		"mode_id":     "world_boss",
		"action_type": "transfer_card",
		"payload": map[string]any{
			"from_player_id": players[0].UserID,
			"to_player_id":   players[2].UserID,
			"card_id":        "focus_lens",
		},
	})
	if err != nil {
		t.Fatalf("duplicate transfer should be domain rejection: %v", err)
	}
	if duplicate.Accepted || duplicate.Reason != codeModeAction {
		t.Fatalf("expected duplicate transfer rejection, got %+v", duplicate)
	}
	unauthorizedTransfer, err := service.SubmitModeAction(players[1].SessionToken, matchID, map[string]any{
		"mode_id":     "world_boss",
		"action_type": "transfer_card",
		"payload": map[string]any{
			"from_player_id": players[0].UserID,
			"to_player_id":   players[1].UserID,
			"card_id":        "hitbox_charm",
		},
	})
	if err != nil {
		t.Fatalf("unauthorized transfer should be domain rejection: %v", err)
	}
	if unauthorizedTransfer.Accepted || unauthorizedTransfer.Reason != codeModeAction {
		t.Fatalf("expected unauthorized transfer rejection, got %+v", unauthorizedTransfer)
	}
}

func TestWorldBossPersistsGlobalHPAttemptsAndAnnouncement(t *testing.T) {
	now := time.Date(2026, 6, 26, 16, 30, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	players := []*AuthSession{
		mustLogin(t, service, "World Boss 1"),
		mustLogin(t, service, "World Boss 2"),
		mustLogin(t, service, "World Boss 3"),
		mustLogin(t, service, "World Boss 4"),
	}
	service.mu.Lock()
	service.ensureWorldBossLocked().CurrentHP = 40
	service.mu.Unlock()

	matchID := matchPlayers(t, service, players, "world_boss")
	firstInput, err := service.SubmitInput(players[0].SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      true,
		"card_slot": -1,
	})
	if err != nil {
		t.Fatalf("world boss first input: %v", err)
	}
	if intFromAny(firstInput.Snapshot.ModeState["boss_hp_global"]) != 40 || intFromAny(firstInput.Snapshot.ModeState["daily_attempts_left"]) != 2 {
		t.Fatalf("world boss snapshot should show global hp before settlement and consumed attempt: %+v", firstInput.Snapshot.ModeState)
	}
	firstSettle, err := service.SettleMatch(players[0].SessionToken, matchID, map[string]any{})
	if err != nil {
		t.Fatalf("world boss first settle: %v", err)
	}
	if intFromAny(firstSettle.ModeResult["boss_hp_before_global"]) != 40 || intFromAny(firstSettle.ModeResult["boss_hp_after_global"]) != 1 || boolFromAny(firstSettle.ModeResult["world_announcement_emitted"]) {
		t.Fatalf("world boss first settlement should persist partial damage without announcement: %+v", firstSettle.ModeResult)
	}
	afterFirst, err := service.Bootstrap(players[0].SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after first boss: %v", err)
	}
	if afterFirst.WorldBoss.CurrentHP != 1 || afterFirst.WorldBoss.DailyAttemptsLeft != 2 || !afterFirst.WorldBoss.ServerAuthoritative {
		t.Fatalf("bootstrap should expose authoritative world boss state: %+v", afterFirst.WorldBoss)
	}

	defeatMatchID := matchPlayers(t, service, players, "world_boss")
	defeatInput, err := service.SubmitInput(players[0].SessionToken, defeatMatchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	})
	if err != nil {
		t.Fatalf("world boss defeat input: %v", err)
	}
	if intFromAny(defeatInput.Snapshot.ModeState["boss_hp_global"]) != 1 || intFromAny(defeatInput.Snapshot.ModeState["boss_hp_preview"]) != 0 {
		t.Fatalf("defeat snapshot should keep global hp until settlement and local hp at zero: %+v", defeatInput.Snapshot.ModeState)
	}
	defeatSettle, err := service.SettleMatch(players[0].SessionToken, defeatMatchID, map[string]any{})
	if err != nil {
		t.Fatalf("world boss defeat settle: %v", err)
	}
	if intFromAny(defeatSettle.ModeResult["boss_hp_after_global"]) != 0 || !boolFromAny(defeatSettle.ModeResult["boss_defeated"]) || !boolFromAny(defeatSettle.ModeResult["world_announcement_emitted"]) {
		t.Fatalf("defeat settlement should zero global hp and announce once: %+v", defeatSettle.ModeResult)
	}
	afterDefeat, err := service.Bootstrap(players[0].SessionToken)
	if err != nil {
		t.Fatalf("bootstrap after defeat: %v", err)
	}
	if afterDefeat.WorldBoss.CurrentHP != 0 || afterDefeat.WorldBoss.DefeatedAt == nil || !afterDefeat.WorldBoss.AnnouncementEmitted || afterDefeat.WorldBoss.DefeatedByMatchID != defeatMatchID {
		t.Fatalf("bootstrap should expose defeated world boss: %+v", afterDefeat.WorldBoss)
	}
	if _, err := service.JoinQueue(players[0].SessionToken, JoinQueueRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "blocked_world_boss_deck",
		DeckSnapshot: validDeck("blocked_world_boss_deck"),
	}); ErrorCode(err) != codeMatchState {
		t.Fatalf("defeated world boss should reject new entry, got %v", err)
	}
	duplicate, err := service.SettleMatch(players[0].SessionToken, defeatMatchID, map[string]any{})
	if err != nil {
		t.Fatalf("duplicate defeat settle: %v", err)
	}
	if !duplicate.Duplicate || intFromAny(duplicate.ModeResult["boss_hp_after_global"]) != 0 {
		t.Fatalf("duplicate defeat settlement should be idempotent: %+v", duplicate)
	}
}

func TestInstanceBossRequiresServerSideBossDefeat(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Instance Boss 1"),
		mustLogin(t, service, "Instance Boss 2"),
		mustLogin(t, service, "Instance Boss 3"),
		mustLogin(t, service, "Instance Boss 4"),
	}
	matchID := matchPlayers(t, service, players, "instance_boss")
	if _, err := service.SubmitInput(players[0].SessionToken, matchID, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       0,
		"slow":      false,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	}); err != nil {
		t.Fatalf("instance boss input: %v", err)
	}
	settlement, err := service.SettleMatch(players[0].SessionToken, matchID, map[string]any{})
	if err != nil {
		t.Fatalf("instance boss settle: %v", err)
	}
	if settlement.Result != "loss" || boolFromAny(settlement.ModeResult["instance_cleared"]) || intFromAny(settlement.ModeResult["boss_hp_after"]) <= 0 {
		t.Fatalf("uncleared instance boss should be a server-owned loss: %+v", settlement)
	}
	if settlement.ModeResult["party_status"] != "failed" {
		t.Fatalf("instance boss mode result should report failed party status: %+v", settlement.ModeResult)
	}
}

func mustLogin(t *testing.T, service *Service, name string) *AuthSession {
	t.Helper()
	session, err := service.LoginAnonymous(AnonymousLoginRequest{DeviceID: "dev-" + name, DisplayName: name})
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return session
}

func matchTwoPlayers(t *testing.T, service *Service, alice *AuthSession, bob *AuthSession, modeID string) string {
	t.Helper()
	return matchPlayers(t, service, []*AuthSession{alice, bob}, modeID)
}

func matchPlayers(t *testing.T, service *Service, players []*AuthSession, modeID string) string {
	t.Helper()
	matchID := ""
	for index, player := range players {
		queue, err := service.JoinQueue(player.SessionToken, JoinQueueRequest{
			ModeID:       modeID,
			ActiveDeckID: fmt.Sprintf("%s_deck_%d", player.UserID, index),
			DeckSnapshot: validDeck(fmt.Sprintf("%s_deck_%d", player.UserID, index)),
		})
		if err != nil {
			t.Fatalf("join player %d: %v", index, err)
		}
		if queue.MatchID != "" {
			matchID = queue.MatchID
		}
	}
	if matchID == "" {
		t.Fatalf("expected match id for %s with %d players", modeID, len(players))
	}
	for index, player := range players {
		if _, err := service.ReadyMatch(player.SessionToken, matchID); err != nil {
			t.Fatalf("ready player %d: %v", index, err)
		}
	}
	return matchID
}

func modeConfigByID(modes []ModeConfig, modeID string) ModeConfig {
	for _, mode := range modes {
		if mode.ModeID == modeID {
			return mode
		}
	}
	return ModeConfig{}
}

func signedBattleResultForAllocation(allocation *BattleServerAllocation) SignedBattleResult {
	playerIDs := []string{}
	for _, player := range allocation.Players {
		playerIDs = append(playerIDs, player.PlayerID)
	}
	sort.Strings(playerIDs)
	return SignedBattleResult{
		OK: true,
		Result: BattleResult{
			Version:              currentVersionStamp(),
			MatchID:              allocation.MatchID,
			ModeID:               allocation.ModeID,
			ResultHash:           phkv1.BattleResultCallbackResultHash,
			ReplayID:             phkv1.BattleResultCallbackReplayID,
			PlayerIDs:            playerIDs,
			RewardProjectionJSON: phkv1.BattleResultCallbackRewardProjectionJSON,
			ModeResultJSON:       phkv1.BattleResultCallbackModeResultJSON,
			SettledAtMS:          phkv1.BattleResultCallbackSettledAtMS,
		},
		SignatureAlg:        phkv1.BattleResultCallbackSignatureAlg,
		KeyID:               allocation.BattleServerID,
		SignatureHex:        phkv1.BattleResultCallbackSignatureHex,
		PublicKeyHex:        phkv1.BattleResultCallbackPublicKeyHex,
		ServerAuthoritative: true,
	}
}

func assertAuthoritativeBulletSnapshot(t *testing.T, snapshot Snapshot) {
	t.Helper()
	if len(snapshot.BulletsDelta) == 0 {
		t.Fatalf("expected authoritative bullet delta: %+v", snapshot)
	}
	first := snapshot.BulletsDelta[0]
	if first.Op != "spawn" || first.BulletID == "" || first.PatternID == "" || first.Kind == "" || first.Radius <= 0 {
		t.Fatalf("bullet delta missing server fields: %+v", first)
	}
	if intFromAny(snapshot.ModeState["active_bullets"]) <= 0 {
		t.Fatalf("mode state should report active bullets: %+v", snapshot.ModeState)
	}
	if snapshot.StateHash == "" {
		t.Fatalf("snapshot missing state hash")
	}
}

func hasBulletOp(deltas []BulletDelta, op string) bool {
	for _, delta := range deltas {
		if delta.Op == op {
			return true
		}
	}
	return false
}

func hasEventType(events []MatchEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func boolFromAny(value any) bool {
	v, _ := value.(bool)
	return v
}

func playerSnapshotByUser(snapshot Snapshot, userID string) PlayerSnapshot {
	for _, player := range snapshot.Players {
		if player.UserID == userID {
			return player
		}
	}
	return PlayerSnapshot{}
}

func validDeck(deckID string) DeckSnapshot {
	cardIDs := []string{
		"focus_lens", "focus_lens",
		"hitbox_charm", "hitbox_charm",
		"density_surge", "density_surge",
		"tempo_break", "tempo_break",
		"bomb_amplifier", "bomb_amplifier",
		"guard_seal", "guard_seal",
		"graze_engine", "graze_engine",
		"draw_sigil", "draw_sigil",
		"aim_baffle", "aim_baffle",
		"purge_charm", "purge_charm",
	}
	return DeckSnapshot{
		DeckID:         deckID,
		Name:           deckID,
		RulesetVersion: RulesetVersion,
		CardIDs:        cardIDs,
	}
}

func verifySignedBattleTicket(t *testing.T, signed *SignedBattleTicket) bool {
	t.Helper()
	if signed == nil || signed.SignatureAlg != "ED25519" || signed.SignatureHex == "" || signed.PublicKeyHex == "" {
		return false
	}
	signature, err := hex.DecodeString(signed.SignatureHex)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	publicKey, err := hex.DecodeString(signed.PublicKeyHex)
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	payload, err := json.Marshal(signed.Ticket)
	if err != nil {
		t.Fatalf("marshal ticket: %v", err)
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
}
