package core

import (
	"testing"
	"time"
)

// TestPlayerRecordsEnsureAndRecord 覆盖 nil 接收者懒初始化与聚合/有界 Recent 的核心逻辑。
func TestPlayerRecordsEnsureAndRecord(t *testing.T) {
	var records *PlayerRecords
	ensured := records.ensure()
	if ensured == nil {
		t.Fatalf("ensure should lazily initialize a nil receiver")
	}
	ensured.record(MatchHistoryEntry{Result: "win", Score: 120})
	ensured.record(MatchHistoryEntry{Result: "loss", Score: 80})
	if ensured.MatchesPlayed != 2 || ensured.Wins != 1 || ensured.Losses != 1 || ensured.Draws != 0 {
		t.Fatalf("record outcome aggregation invalid: %+v", ensured)
	}
	if ensured.BestScore != 120 || ensured.TotalScore != 200 {
		t.Fatalf("record score aggregation invalid: %+v", ensured)
	}
	if len(ensured.Recent) != 2 || ensured.Recent[0].Score != 80 {
		t.Fatalf("record recent should be newest-first: %+v", ensured.Recent)
	}
}

// TestRecordsViewForFreshUserIsZeroed 覆盖新用户（Records == nil）返回零值且不 panic。
func TestRecordsViewForFreshUserIsZeroed(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Records Fresh")

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records for fresh user: %v", err)
	}
	if !view.OK || !view.ServerAuthoritative || view.ClientResultAuthoritative {
		t.Fatalf("fresh records authority invalid: %+v", view)
	}
	if view.UserID != alice.UserID || view.DisplayName != "Records Fresh" {
		t.Fatalf("fresh records identity invalid: %+v", view)
	}
	if view.MatchesPlayed != 0 || view.Wins != 0 || view.Losses != 0 || view.Draws != 0 || view.BestScore != 0 {
		t.Fatalf("fresh records should be zeroed: %+v", view)
	}
	if view.WinRate != 0 || view.AverageScore != 0 {
		t.Fatalf("fresh records rates should be divide-by-zero safe: %+v", view)
	}
	if len(view.Recent) != 0 {
		t.Fatalf("fresh records recent should be empty: %+v", view.Recent)
	}
	if view.RatingCode == "" || view.SeasonID == "" || view.Points <= 0 {
		t.Fatalf("fresh records certification projection invalid: %+v", view)
	}
}

// TestRecordsAggregateAfterSettledMatch 覆盖打完一局后 MatchesPlayed/Wins/BestScore/Recent 正确。
func TestRecordsAggregateAfterSettledMatch(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Records Alice")
	bob := mustLogin(t, service, "Records Bob")

	matchID := matchTwoPlayers(t, service, alice, bob, "certification")
	if _, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{}); err != nil {
		t.Fatalf("settle alice: %v", err)
	}

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records alice: %v", err)
	}
	if view.MatchesPlayed != 1 {
		t.Fatalf("expected one recorded match, got %+v", view)
	}
	if view.Wins+view.Losses+view.Draws != 1 {
		t.Fatalf("recorded outcome should be counted exactly once: %+v", view)
	}
	if len(view.Recent) != 1 {
		t.Fatalf("expected one recent entry, got %+v", view.Recent)
	}
	entry := view.Recent[0]
	if entry.MatchID != matchID || entry.ModeID != "certification" {
		t.Fatalf("recent entry identity invalid: %+v", entry)
	}
	if entry.Result != "win" && entry.Result != "loss" && entry.Result != "draw" {
		t.Fatalf("recent entry result invalid: %+v", entry)
	}
	if entry.OpponentID != bob.UserID {
		t.Fatalf("recent entry opponent invalid: %+v", entry)
	}
	if view.BestScore != entry.Score || view.AverageScore != float64(entry.Score) {
		t.Fatalf("single match should drive best/average score: view=%+v entry=%+v", view, entry)
	}
	if entry.EndedAt.IsZero() {
		t.Fatalf("recent entry should carry ended_at: %+v", entry)
	}
}

// TestRecordsRankDeltaDoesNotLeakAcrossModes 固定回归：非排位对局不得继承上一场排位赛遗留的 rank delta。
func TestRecordsRankDeltaDoesNotLeakAcrossModes(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Records Rank Alice")
	bob := mustLogin(t, service, "Records Rank Bob")

	certMatch := matchTwoPlayers(t, service, alice, bob, "certification")
	if _, err := service.SettleMatch(alice.SessionToken, certMatch, map[string]any{}); err != nil {
		t.Fatalf("settle certification: %v", err)
	}
	unrankedMatch := matchTwoPlayers(t, service, alice, bob, "mvp_boss_race")
	if _, err := service.SettleMatch(alice.SessionToken, unrankedMatch, map[string]any{}); err != nil {
		t.Fatalf("settle mvp_boss_race: %v", err)
	}

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records alice: %v", err)
	}
	if len(view.Recent) != 2 {
		t.Fatalf("expected two recent entries, got %+v", view.Recent)
	}
	if view.Recent[0].ModeID != "mvp_boss_race" || view.Recent[1].ModeID != "certification" {
		t.Fatalf("recent ordering invalid: %+v", view.Recent)
	}
	if view.Recent[1].RankDelta <= 0 {
		t.Fatalf("certification record should carry its rank delta: %+v", view.Recent[1])
	}
	if view.Recent[0].RankDelta != 0 {
		t.Fatalf("unranked record must not inherit a stale rank delta: %+v", view.Recent[0])
	}
}

// TestRecordsRecentIsBoundedAndNewestFirst 覆盖 Recent 上限 20：打 21 局后长度为 20 且最新在前。
func TestRecordsRecentIsBoundedAndNewestFirst(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Records Bounded Alice")
	bob := mustLogin(t, service, "Records Bounded Bob")

	matchIDs := make([]string, 0, recordsHistoryLimit+1)
	for i := 0; i < recordsHistoryLimit+1; i++ {
		matchID := matchTwoPlayers(t, service, alice, bob, "certification")
		if _, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{}); err != nil {
			t.Fatalf("settle match %d: %v", i, err)
		}
		matchIDs = append(matchIDs, matchID)
	}

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records alice: %v", err)
	}
	if view.MatchesPlayed != recordsHistoryLimit+1 {
		t.Fatalf("expected %d recorded matches, got %d", recordsHistoryLimit+1, view.MatchesPlayed)
	}
	if len(view.Recent) != recordsHistoryLimit {
		t.Fatalf("recent should be capped at %d, got %d", recordsHistoryLimit, len(view.Recent))
	}
	// 最新在前：第一条应是最后一局，最后一条是第 2 局（第 1 局已被裁掉）。
	if view.Recent[0].MatchID != matchIDs[len(matchIDs)-1] {
		t.Fatalf("newest entry should be the last match: got %s want %s", view.Recent[0].MatchID, matchIDs[len(matchIDs)-1])
	}
	if view.Recent[len(view.Recent)-1].MatchID != matchIDs[1] {
		t.Fatalf("oldest retained entry should be the second match: got %s want %s", view.Recent[len(view.Recent)-1].MatchID, matchIDs[1])
	}
}

// TestRecordsOperationIsInClientContract 覆盖 records.get 作为登录后业务操作进入契约并被信封门禁保护。
func TestRecordsOperationIsInClientContract(t *testing.T) {
	if !stringSliceContains(ContractClientOperations(), "records.get") {
		t.Fatalf("records.get missing from client contract: %+v", ContractClientOperations())
	}
	if !stringSliceContains(ContractClientRPCOperations(), "records.get") {
		t.Fatalf("records.get missing from client RPC contract: %+v", ContractClientRPCOperations())
	}
	found := false
	for _, contract := range ContractClientOperationContracts() {
		if contract.Operation != "records.get" {
			continue
		}
		found = true
		if !contract.BusinessEnvelopeRequired {
			t.Fatalf("records.get must require a business envelope: %+v", contract)
		}
		if !stringSliceContains(contract.Transports, "nakama_rpc") {
			t.Fatalf("records.get must expose the nakama_rpc transport: %+v", contract)
		}
	}
	if !found {
		t.Fatalf("records.get structured contract missing: %+v", ContractClientOperationContracts())
	}
}
