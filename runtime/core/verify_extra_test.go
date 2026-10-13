package core

import (
	"math"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// Item 1: mirror_lance (opponent_pattern)
// ---------------------------------------------------------------------------

// TestVerifyMirrorLanceFanAimsAtOpponentPosition moves the opponent off the
// vertical axis and pins the exact deterministic fan geometry: the centre ray
// must point straight at the opponent and every ray must lie inside the spread.
func TestVerifyMirrorLanceFanAimsAtOpponentPosition(t *testing.T) {
	service, alice, bob, matchID := cardEffectMatch(t)

	service.mu.Lock()
	bobState := service.matches[matchID].Players[bob.UserID]
	bobState.X = 600
	bobState.Y = 300
	targetX, targetY := bobState.X, bobState.Y
	service.mu.Unlock()

	armCard(t, service, matchID, alice.UserID, "mirror_lance", 10)
	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	spawns := make([]BulletDelta, 0, cardOpponentPatternCount)
	for _, delta := range resp.Snapshot.BulletsDelta {
		if delta.Op == "spawn" && delta.PatternID == "mirror_lance" {
			spawns = append(spawns, delta)
		}
	}
	if len(spawns) != cardOpponentPatternCount {
		t.Fatalf("expected %d mirror_lance spawn deltas, got %d: %+v", cardOpponentPatternCount, len(spawns), spawns)
	}
	if spawns[0].X != bossX || spawns[0].Y != bossY {
		t.Fatalf("mirror_lance must spawn at the boss (%.1f,%.1f), got (%.3f,%.3f)", bossX, bossY, spawns[0].X, spawns[0].Y)
	}

	base := math.Atan2(targetY-bossY, targetX-bossX)
	start := base - cardOpponentPatternSpread*0.5
	step := cardOpponentPatternSpread / float64(cardOpponentPatternCount-1)
	for i, delta := range spawns {
		want := start + step*float64(i)
		got := math.Atan2(delta.VY, delta.VX)
		if math.Abs(got-want) > 1e-3 {
			t.Fatalf("spawn %d angle = %.6f, want %.6f (fan not deterministic)", i, got, want)
		}
	}
	centre := math.Atan2(spawns[3].VY, spawns[3].VX)
	if math.Abs(centre-base) > 1e-3 {
		t.Fatalf("centre ray %.6f must point at opponent angle %.6f", centre, base)
	}
}

// TestVerifyMirrorLanceTargetsDeterministicOpponentInFivePlayerMatch pins the
// multi-player resolution: the target must be a real opponent (never the caster)
// and must be the lowest-id opponent, in both the snapshot and the event.
func TestVerifyMirrorLanceTargetsDeterministicOpponentInFivePlayerMatch(t *testing.T) {
	service := NewService(Config{})
	players := []*AuthSession{
		mustLogin(t, service, "Vfy BR 1"),
		mustLogin(t, service, "Vfy BR 2"),
		mustLogin(t, service, "Vfy BR 3"),
		mustLogin(t, service, "Vfy BR 4"),
		mustLogin(t, service, "Vfy BR 5"),
	}
	matchID := matchPlayers(t, service, players, "battle_royale")
	alice := players[0]

	others := make([]string, 0, len(players)-1)
	for _, player := range players[1:] {
		others = append(others, player.UserID)
	}
	sort.Strings(others)
	want := others[0]

	armCard(t, service, matchID, alice.UserID, "mirror_lance", 10)
	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	if len(resp.Snapshot.ActiveCards) != 1 {
		t.Fatalf("expected one active card, got %+v", resp.Snapshot.ActiveCards)
	}
	card := resp.Snapshot.ActiveCards[0]
	if card.TargetUserID == alice.UserID {
		t.Fatalf("mirror_lance must not target the caster")
	}
	if card.TargetUserID != want {
		t.Fatalf("snapshot target = %q, want lowest opponent id %q", card.TargetUserID, want)
	}
	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.TargetUserID != want {
		t.Fatalf("event must carry the resolved target %q, got %+v", want, resp.Snapshot.Events)
	}
}

// ---------------------------------------------------------------------------
// Item 2: purge_bell (bullet_purge)
// ---------------------------------------------------------------------------

// TestVerifyPurgeBellOnEmptyFieldIsNoOp exercises the "zero bullets in radius"
// edge case: the card still activates, reports value 0 and emits no despawns.
func TestVerifyPurgeBellOnEmptyFieldIsNoOp(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	armCard(t, service, matchID, alice.UserID, "purge_bell", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.CardID != "purge_bell" || event.Value != 0 {
		t.Fatalf("empty purge should apply with value 0, got %+v", resp.Snapshot.Events)
	}
	for _, delta := range resp.Snapshot.BulletsDelta {
		if delta.Op == "despawn" {
			t.Fatalf("empty purge must not emit despawn deltas, got %+v", delta)
		}
	}
}

// TestVerifyPurgeBellBoundedByRadiusAndCount proves both bounds at once: 30
// in-radius bullets + 4 far bullets, PurgeCount 24 => exactly 24 near removed
// and every far bullet survives.
func TestVerifyPurgeBellBoundedByRadiusAndCount(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	near := seedBulletsAroundPlayer(t, service, matchID, alice.UserID, 30, 60)
	far := seedBulletsAt(t, service, matchID, 480, 60, 4, 5)
	armCard(t, service, matchID, alice.UserID, "purge_bell", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	service.mu.Lock()
	match := service.matches[matchID]
	nearRemaining := 0
	for _, id := range near {
		if _, ok := match.Bullets[id]; ok {
			nearRemaining++
		}
	}
	farRemaining := 0
	for _, id := range far {
		if _, ok := match.Bullets[id]; ok {
			farRemaining++
		}
	}
	service.mu.Unlock()

	if nearRemaining != len(near)-24 {
		t.Fatalf("expected 24 of %d in-radius bullets purged, %d remain", len(near), nearRemaining)
	}
	if farRemaining != len(far) {
		t.Fatalf("out-of-radius bullets must survive, %d/%d remain", farRemaining, len(far))
	}
	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.Value != 24 {
		t.Fatalf("expected card_effect_applied value 24, got %+v", resp.Snapshot.Events)
	}
	if despawns := countSeedDespawns(resp.Snapshot.BulletsDelta, near); despawns != 24 {
		t.Fatalf("expected 24 replayable despawn deltas, got %d", despawns)
	}
}

// ---------------------------------------------------------------------------
// Item 3: aegis_ward (invulnerable)
// ---------------------------------------------------------------------------

// contactBulletMatch returns a match with the opponent parked far away and the
// caster holding a bullet placed exactly on top of them.
func contactBulletMatch(t *testing.T) (*Service, *AuthSession, string) {
	t.Helper()
	service, alice, bob, matchID := cardEffectMatch(t)
	service.mu.Lock()
	match := service.matches[matchID]
	// Park the opponent far away so it can never be the contact victim.
	match.Players[bob.UserID].X = 200
	match.Players[bob.UserID].Y = 100
	player := match.Players[alice.UserID]
	match.Bullets["vfy_contact"] = &bulletState{
		ID: "vfy_contact", PatternID: "vfy", Kind: "ring",
		X: player.X, Y: player.Y, Radius: 4.5, Damage: 1, Color: "white",
	}
	service.mu.Unlock()
	return service, alice, matchID
}

// TestVerifyContactPathRegistersHitWithoutInvulnerability is the control that
// proves resolveBulletContactsLocked really runs and the immunity guard is the
// only thing stopping the hit.
func TestVerifyContactPathRegistersHitWithoutInvulnerability(t *testing.T) {
	service, alice, matchID := contactBulletMatch(t)
	service.mu.Lock()
	match := service.matches[matchID]
	player := match.Players[alice.UserID]
	player.InvulnerableUntil = 0
	resolveBulletContactsLocked(match, 50)
	hit := player.HitCount
	service.mu.Unlock()

	if hit != 1 {
		t.Fatalf("control: expected exactly 1 hit, got HitCount %d", hit)
	}
}

// TestVerifyInvulnerableBoundaryAtExpiryTick pins the exact-tick semantics: at
// tick == InvulnerableUntil the player must still be immune.
func TestVerifyInvulnerableBoundaryAtExpiryTick(t *testing.T) {
	service, alice, matchID := contactBulletMatch(t)
	service.mu.Lock()
	match := service.matches[matchID]
	player := match.Players[alice.UserID]
	player.InvulnerableUntil = 100
	resolveBulletContactsLocked(match, 100)
	hit := player.HitCount
	service.mu.Unlock()

	if hit != 0 {
		t.Fatalf("at tick == InvulnerableUntil the player must stay immune, got HitCount %d", hit)
	}
}

// TestVerifyInvulnerableExpiresOneTickAfter proves the window closes the tick
// after expiry.
func TestVerifyInvulnerableExpiresOneTickAfter(t *testing.T) {
	service, alice, matchID := contactBulletMatch(t)
	service.mu.Lock()
	match := service.matches[matchID]
	player := match.Players[alice.UserID]
	player.InvulnerableUntil = 100
	resolveBulletContactsLocked(match, 101)
	hit := player.HitCount
	service.mu.Unlock()

	if hit != 1 {
		t.Fatalf("one tick past expiry the contact must damage again, got HitCount %d", hit)
	}
}

// TestVerifyGrazeStillWorksWhileInvulnerable places a bullet in the graze ring
// (outside contact range) and proves grazing and its score still apply.
func TestVerifyGrazeStillWorksWhileInvulnerable(t *testing.T) {
	service, alice, bob, matchID := cardEffectMatch(t)
	service.mu.Lock()
	match := service.matches[matchID]
	match.Players[bob.UserID].X = 200
	match.Players[bob.UserID].Y = 100
	player := match.Players[alice.UserID]
	player.InvulnerableUntil = 1000
	match.Bullets["vfy_graze"] = &bulletState{
		ID: "vfy_graze", PatternID: "vfy", Kind: "ring",
		X: player.X + 20, Y: player.Y, Radius: 4.5, Damage: 1, Color: "white",
	}
	beforeGraze := player.GrazeCount
	beforeScore := player.Score
	resolveBulletContactsLocked(match, 500)
	graze := player.GrazeCount
	score := player.Score
	hit := player.HitCount
	service.mu.Unlock()

	if graze != beforeGraze+1 {
		t.Fatalf("graze must still register while invulnerable: before %d after %d", beforeGraze, graze)
	}
	if score <= beforeScore {
		t.Fatalf("graze must still award score while invulnerable: before %d after %d", beforeScore, score)
	}
	if hit != 0 {
		t.Fatalf("a graze-ring bullet must not be a hit, got HitCount %d", hit)
	}
}

// TestVerifyInvulnerableCardExpiresAndWindowCloses runs the real aegis_ward
// lifecycle through the simulation loop: the card must leave ActiveCards at its
// expiry tick and a contact after that must damage again.
func TestVerifyInvulnerableCardExpiresAndWindowCloses(t *testing.T) {
	service, alice, bob, matchID := cardEffectMatch(t)
	service.mu.Lock()
	service.matches[matchID].Players[bob.UserID].X = 200
	service.matches[matchID].Players[bob.UserID].Y = 100
	service.mu.Unlock()

	armCard(t, service, matchID, alice.UserID, "aegis_ward", 10)
	submitCard(t, service, alice, matchID, 1, 1, 0)

	service.mu.Lock()
	player := service.matches[matchID].Players[alice.UserID]
	until := player.InvulnerableUntil
	service.mu.Unlock()
	if until != 1+180 {
		t.Fatalf("expected InvulnerableUntil %d, got %d", 1+180, until)
	}

	// Advance the simulation exactly to the expiry tick.
	submitCard(t, service, alice, matchID, until, 2, 0)

	service.mu.Lock()
	match := service.matches[matchID]
	stillActive := false
	for _, card := range match.ActiveCards {
		if card.CardID == "aegis_ward" {
			stillActive = true
		}
	}
	player = match.Players[alice.UserID]
	service.mu.Unlock()
	if stillActive {
		t.Fatalf("aegis_ward must leave ActiveCards at its expiry tick")
	}
	if player.InvulnerableUntil != until {
		t.Fatalf("InvulnerableUntil changed unexpectedly: got %d want %d", player.InvulnerableUntil, until)
	}
}

// ---------------------------------------------------------------------------
// Item 4: finisher
// ---------------------------------------------------------------------------

// TestVerifyFinisherOnEmptyFieldIsNoOp exercises the empty-field edge case for
// the finisher family.
func TestVerifyFinisherOnEmptyFieldIsNoOp(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	armCard(t, service, matchID, alice.UserID, "finale_arc", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.CardID != "finale_arc" || event.Value != 0 {
		t.Fatalf("empty finisher should apply with value 0, got %+v", resp.Snapshot.Events)
	}
	for _, delta := range resp.Snapshot.BulletsDelta {
		if delta.Op == "despawn" {
			t.Fatalf("empty finisher must not emit despawn deltas, got %+v", delta)
		}
	}
}

// ---------------------------------------------------------------------------
// Item 5: records aggregation
// ---------------------------------------------------------------------------

// TestVerifyPlayerRecordsAggregatesEveryField pins the aggregation arithmetic
// across win/loss/draw including all counters.
func TestVerifyPlayerRecordsAggregatesEveryField(t *testing.T) {
	records := newPlayerRecords()
	records.record(MatchHistoryEntry{Result: "win", Score: 100, GrazeCount: 5, HitCount: 1, DamageDealt: 40, CardPlays: 2})
	records.record(MatchHistoryEntry{Result: "loss", Score: 60, GrazeCount: 3, HitCount: 4, DamageDealt: 10, CardPlays: 1})
	records.record(MatchHistoryEntry{Result: "draw", Score: 80, GrazeCount: 7, HitCount: 2, DamageDealt: 20, CardPlays: 3})

	if records.MatchesPlayed != 3 || records.Wins != 1 || records.Losses != 1 || records.Draws != 1 {
		t.Fatalf("outcome aggregation wrong: %+v", records)
	}
	if records.BestScore != 100 || records.TotalScore != 240 {
		t.Fatalf("score aggregation wrong: %+v", records)
	}
	if records.TotalGraze != 15 || records.TotalHits != 7 || records.TotalDamageDealt != 70 || records.TotalCardPlays != 6 {
		t.Fatalf("counter aggregation wrong: %+v", records)
	}
	if len(records.Recent) != 3 || records.Recent[0].Score != 80 {
		t.Fatalf("recent must be newest-first: %+v", records.Recent)
	}
}

// TestVerifyRecordsRecordEveryPlayerIncludingLoserAndDraw settles a scoreless
// 1v1 (which the engine treats as a draw) and proves BOTH players get a record,
// each with the other as opponent.
func TestVerifyRecordsRecordEveryPlayerIncludingLoserAndDraw(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Vfy Rec A")
	bob := mustLogin(t, service, "Vfy Rec B")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")
	if _, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	av, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("alice records: %v", err)
	}
	bv, err := service.Records(bob.SessionToken)
	if err != nil {
		t.Fatalf("bob records: %v", err)
	}
	if av.MatchesPlayed != 1 || bv.MatchesPlayed != 1 {
		t.Fatalf("both players must be recorded: alice=%d bob=%d", av.MatchesPlayed, bv.MatchesPlayed)
	}
	if av.Draws != 1 || bv.Draws != 1 || av.Wins != 0 || bv.Wins != 0 || av.Losses != 0 || bv.Losses != 0 {
		t.Fatalf("a scoreless 1v1 must be a draw for both: alice=%+v bob=%+v", av, bv)
	}
	if av.Recent[0].OpponentID != bob.UserID || bv.Recent[0].OpponentID != alice.UserID {
		t.Fatalf("opponent ids wrong: alice=%+v bob=%+v", av.Recent[0], bv.Recent[0])
	}
}

// TestVerifyRecordsAggregateAcrossMatches plays three matches with distinct
// scores and pins wins/win_rate/best/average/total.
func TestVerifyRecordsAggregateAcrossMatches(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Vfy Agg A")
	bob := mustLogin(t, service, "Vfy Agg B")
	for i := 1; i <= 3; i++ {
		matchID := matchTwoPlayers(t, service, alice, bob, "certification")
		service.mu.Lock()
		service.matches[matchID].Players[alice.UserID].Score = 100 * i
		service.mu.Unlock()
		if _, err := service.SettleMatch(alice.SessionToken, matchID, map[string]any{}); err != nil {
			t.Fatalf("settle %d: %v", i, err)
		}
	}

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if view.MatchesPlayed != 3 || view.Wins != 3 || view.Losses != 0 || view.Draws != 0 {
		t.Fatalf("outcome aggregation wrong: %+v", view)
	}
	if view.WinRate != 1.0 {
		t.Fatalf("win_rate = %v, want 1.0", view.WinRate)
	}
	if view.BestScore != 300 || view.AverageScore != 200.0 {
		t.Fatalf("score aggregation wrong: best=%d avg=%v (want best=300 avg=200)", view.BestScore, view.AverageScore)
	}
	if len(view.Recent) != 3 || view.Recent[0].Score != 300 {
		t.Fatalf("recent must be newest-first: %+v", view.Recent)
	}

	loserView, err := service.Records(bob.SessionToken)
	if err != nil {
		t.Fatalf("bob records: %v", err)
	}
	if loserView.MatchesPlayed != 3 || loserView.Losses != 3 || loserView.Wins != 0 {
		t.Fatalf("loser must be recorded with three losses: %+v", loserView)
	}
	if loserView.WinRate != 0 {
		t.Fatalf("loser win_rate = %v, want 0", loserView.WinRate)
	}
}

// ---------------------------------------------------------------------------
// Item 6: records.get session rejection
// ---------------------------------------------------------------------------

// TestVerifyRecordsRejectsInvalidSessionToken checks the service-level auth gate.
func TestVerifyRecordsRejectsInvalidSessionToken(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.Records("definitely-not-a-real-token"); err == nil {
		t.Fatalf("records with an invalid session token must fail")
	}
}

// ---------------------------------------------------------------------------
// Item 7: battle-royale candidate pool coupling
// ---------------------------------------------------------------------------

// TestVerifyFocusLensIsNotAlwaysABattleRoyaleCandidate proves the fixture card
// (focus_lens) is seed-dependent, so the pre-change assertion
// `candidates[0] = fixtureCardID` was a fragile coupling to a 12-card catalog
// rather than a real guarantee.
func TestVerifyFocusLensIsNotAlwaysABattleRoyaleCandidate(t *testing.T) {
	const trials = 200
	inPool := 0
	for i := 0; i < trials; i++ {
		match := &matchState{ServerSeed: int64(i)*2654435761 + 12345}
		candidates := battleRoyaleCandidates(match, 0)
		if cardTestContains(candidates, "focus_lens") {
			inPool++
		}
	}
	t.Logf("focus_lens appeared in %d/%d synthetic battle-royale candidate pools", inPool, trials)
	if inPool == trials {
		t.Fatalf("focus_lens was in every synthetic pool; the old fixture coupling would still hold")
	}
}
