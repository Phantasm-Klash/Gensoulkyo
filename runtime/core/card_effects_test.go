package core

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

func cardEffectMatch(t *testing.T) (*Service, *AuthSession, *AuthSession, string) {
	t.Helper()
	service := NewService(Config{})
	alice := mustLogin(t, service, "Alice Effect")
	bob := mustLogin(t, service, "Bob Effect")
	matchID := matchTwoPlayers(t, service, alice, bob, "certification")
	return service, alice, bob, matchID
}

// armCard replaces the caster's hand and energy so a specific card can be
// activated on the next input regardless of the shuffled deck draw order.
func armCard(t *testing.T, service *Service, matchID string, userID string, cardID string, energy float64) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	match := service.matches[matchID]
	if match == nil {
		t.Fatalf("match %s missing", matchID)
	}
	player := match.Players[userID]
	if player == nil {
		t.Fatalf("player %s missing in match %s", userID, matchID)
	}
	player.Hand = []string{cardID, "focus_lens", "focus_lens", "focus_lens"}
	player.Energy = energy
}

func submitCard(t *testing.T, service *Service, session *AuthSession, matchID string, tick int, seq int, slot int) *InputResponse {
	t.Helper()
	resp, err := service.SubmitInput(session.SessionToken, matchID, map[string]any{
		"tick":      tick,
		"seq":       seq,
		"dir":       0,
		"slow":      false,
		"shoot":     false,
		"bomb":      false,
		"card_slot": slot,
	})
	if err != nil {
		t.Fatalf("submit card tick %d seq %d: %v", tick, seq, err)
	}
	return resp
}

func seedBulletsAt(t *testing.T, service *Service, matchID string, centerX float64, centerY float64, count int, ringRadius float64) []string {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	match := service.matches[matchID]
	if match == nil {
		t.Fatalf("match %s missing", matchID)
	}
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		angle := (2 * math.Pi * float64(i)) / float64(count)
		id := fmt.Sprintf("%s_seed%03d", match.MatchID, i)
		match.Bullets[id] = &bulletState{
			ID:        id,
			PatternID: "test_seed",
			Kind:      "ring",
			X:         centerX + math.Cos(angle)*ringRadius,
			Y:         centerY + math.Sin(angle)*ringRadius,
			Radius:    4.5,
			Damage:    1,
			Color:     "white",
			SpawnTick: 0,
		}
		ids = append(ids, id)
	}
	return ids
}

func seedBulletsAroundPlayer(t *testing.T, service *Service, matchID string, userID string, count int, ringRadius float64) []string {
	t.Helper()
	service.mu.Lock()
	player := service.matches[matchID].Players[userID]
	centerX, centerY := player.X, player.Y
	service.mu.Unlock()
	return seedBulletsAt(t, service, matchID, centerX, centerY, count, ringRadius)
}

func findEvent(events []MatchEvent, eventType string) *MatchEvent {
	for index := range events {
		if events[index].Type == eventType {
			return &events[index]
		}
	}
	return nil
}

func hasBulletPatternOp(deltas []BulletDelta, op string, patternID string) bool {
	for _, delta := range deltas {
		if delta.Op == op && delta.PatternID == patternID {
			return true
		}
	}
	return false
}

func cardTestContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func countSeedDespawns(deltas []BulletDelta, seeded []string) int {
	count := 0
	for _, delta := range deltas {
		if delta.Op == "despawn" && cardTestContains(seeded, delta.BulletID) {
			count++
		}
	}
	return count
}

func TestCardEffectOpponentPatternTargetsOpponent(t *testing.T) {
	service, alice, bob, matchID := cardEffectMatch(t)
	armCard(t, service, matchID, alice.UserID, "mirror_lance", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	if !hasEventType(resp.Snapshot.Events, "card_accepted") {
		t.Fatalf("expected card_accepted, got %+v", resp.Snapshot.Events)
	}
	if aliceSnap := playerSnapshotByUser(resp.Snapshot, alice.UserID); aliceSnap.CardPlays != 1 {
		t.Fatalf("expected one card play for alice, got %+v", aliceSnap)
	}
	if len(resp.Snapshot.ActiveCards) != 1 {
		t.Fatalf("expected one active card, got %+v", resp.Snapshot.ActiveCards)
	}
	card := resp.Snapshot.ActiveCards[0]
	if card.CardID != "mirror_lance" || card.EffectKind != "opponent_pattern" || card.PatternID != "mirror_lance" {
		t.Fatalf("unexpected opponent_pattern active card: %+v", card)
	}
	if card.TargetUserID != bob.UserID {
		t.Fatalf("expected target %s, got %+v", bob.UserID, card)
	}
	if !hasBulletPatternOp(resp.Snapshot.BulletsDelta, "spawn", "mirror_lance") {
		t.Fatalf("expected mirror_lance spawn deltas, got %+v", resp.Snapshot.BulletsDelta)
	}
	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.CardID != "mirror_lance" || event.TargetUserID != bob.UserID {
		t.Fatalf("expected card_effect_applied with target, got %+v", resp.Snapshot.Events)
	}
	if event.Value != cardOpponentPatternCount {
		t.Fatalf("expected %d spawned bullets, got %d", cardOpponentPatternCount, event.Value)
	}
}

func TestCardEffectBulletPurgeRemovesDeterministicSelection(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	seeded := seedBulletsAroundPlayer(t, service, matchID, alice.UserID, 30, 60.0)
	armCard(t, service, matchID, alice.UserID, "purge_bell", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	service.mu.Lock()
	match := service.matches[matchID]
	remaining := 0
	for _, id := range seeded {
		if _, ok := match.Bullets[id]; ok {
			remaining++
		}
	}
	service.mu.Unlock()

	if remaining != 6 {
		t.Fatalf("expected 24 of 30 seeded bullets purged, %d remain", remaining)
	}
	event := findEvent(resp.Snapshot.Events, "card_effect_applied")
	if event == nil || event.CardID != "purge_bell" || event.Value != 24 {
		t.Fatalf("expected card_effect_applied value 24, got %+v", resp.Snapshot.Events)
	}
	if despawns := countSeedDespawns(resp.Snapshot.BulletsDelta, seeded); despawns != 24 {
		t.Fatalf("expected 24 despawn deltas for purged bullets, got %d", despawns)
	}
	card := resp.Snapshot.ActiveCards[0]
	if card.PurgeRadius != 120 || card.PurgeCount != 24 {
		t.Fatalf("purge_bell snapshot missing purge tuning: %+v", card)
	}

	// Pin the deterministic contract the client mirror reproduces: every bullet
	// in the canonical (bullet-id sorted) field order is scored once with
	// deterministicUnit(seed, tick, "purge_bell", index) and the lowest
	// PurgeCount scores are purged, ties broken by bullet id.
	service.mu.Lock()
	seed := service.matches[matchID].ServerSeed
	service.mu.Unlock()
	canonical := append([]string{}, seeded...)
	sort.Strings(canonical)
	type purgeScore struct {
		bulletID string
		unit     float64
	}
	scores := make([]purgeScore, 0, len(canonical))
	for index, id := range canonical {
		scores = append(scores, purgeScore{bulletID: id, unit: deterministicUnit(seed, 1, "purge_bell", index)})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].unit != scores[j].unit {
			return scores[i].unit < scores[j].unit
		}
		return scores[i].bulletID < scores[j].bulletID
	})
	expectedPurged := map[string]struct{}{}
	for _, pick := range scores[:24] {
		expectedPurged[pick.bulletID] = struct{}{}
	}
	service.mu.Lock()
	match = service.matches[matchID]
	for id := range expectedPurged {
		if _, ok := match.Bullets[id]; ok {
			t.Fatalf("expected bullet %s to be purged", id)
		}
	}
	for _, id := range seeded {
		if _, ok := expectedPurged[id]; ok {
			continue
		}
		if _, ok := match.Bullets[id]; !ok {
			t.Fatalf("expected bullet %s to survive the purge", id)
		}
	}
	service.mu.Unlock()
}

func TestCardEffectInvulnerableGrantsDamageImmunity(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	// Drop a bullet right on the caster so a contact would normally register a hit.
	service.mu.Lock()
	player := service.matches[matchID].Players[alice.UserID]
	centerX, centerY := player.X, player.Y
	service.mu.Unlock()
	seedBulletsAt(t, service, matchID, centerX, centerY, 1, 0.0)
	armCard(t, service, matchID, alice.UserID, "aegis_ward", 10)

	resp := submitCard(t, service, alice, matchID, 1, 1, 0)

	service.mu.Lock()
	player = service.matches[matchID].Players[alice.UserID]
	invulnerableUntil := player.InvulnerableUntil
	hitCount := player.HitCount
	service.mu.Unlock()

	if invulnerableUntil != 1+180 {
		t.Fatalf("expected InvulnerableUntil %d, got %d", 1+180, invulnerableUntil)
	}
	if hitCount != 0 {
		t.Fatalf("invulnerable caster must not take a hit, got HitCount %d", hitCount)
	}
	if event := findEvent(resp.Snapshot.Events, "card_effect_applied"); event == nil || event.CardID != "aegis_ward" {
		t.Fatalf("expected card_effect_applied for aegis_ward, got %+v", resp.Snapshot.Events)
	}
}

func TestCardEffectFinisherClearsFieldAndDamagesBoss(t *testing.T) {
	for _, cardID := range []string{"finale_arc", "last_arc"} {
		t.Run(cardID, func(t *testing.T) {
			service, alice, _, matchID := cardEffectMatch(t)
			seeded := seedBulletsAt(t, service, matchID, bossX, bossY+240, 12, 80.0)
			armCard(t, service, matchID, alice.UserID, cardID, 10)

			service.mu.Lock()
			beforeHP := service.matches[matchID].BossHP
			service.mu.Unlock()

			resp := submitCard(t, service, alice, matchID, 1, 1, 0)

			event := findEvent(resp.Snapshot.Events, "card_effect_applied")
			if event == nil || event.CardID != cardID || event.Value != len(seeded) {
				t.Fatalf("%s expected %d bullets cleared, got %+v", cardID, len(seeded), resp.Snapshot.Events)
			}
			if despawns := countSeedDespawns(resp.Snapshot.BulletsDelta, seeded); despawns != len(seeded) {
				t.Fatalf("%s expected %d despawn deltas, got %d", cardID, len(seeded), despawns)
			}
			afterHP := intFromAny(resp.Snapshot.ModeState["boss_hp_preview"])
			if delta := beforeHP - afterHP; delta != serverCardCatalog[cardID].Damage {
				t.Fatalf("%s expected boss damage %d, got %d", cardID, serverCardCatalog[cardID].Damage, delta)
			}
		})
	}
}

func TestCardEffectRejectsInsufficientEnergyAndCooldown(t *testing.T) {
	service, alice, _, matchID := cardEffectMatch(t)
	armCard(t, service, matchID, alice.UserID, "mirror_lance", 0)

	denied := submitCard(t, service, alice, matchID, 1, 1, 0)
	if !hasEventType(denied.Snapshot.Events, "card_rejected") {
		t.Fatalf("expected card_rejected for insufficient energy, got %+v", denied.Snapshot.Events)
	}
	if hasEventType(denied.Snapshot.Events, "card_effect_applied") {
		t.Fatalf("rejected card must not apply an effect, got %+v", denied.Snapshot.Events)
	}
	if plays := playerSnapshotByUser(denied.Snapshot, alice.UserID).CardPlays; plays != 0 {
		t.Fatalf("rejected card must not count as a play, got %d", plays)
	}

	armCard(t, service, matchID, alice.UserID, "mirror_lance", 10)
	accepted := submitCard(t, service, alice, matchID, 2, 2, 0)
	if !hasEventType(accepted.Snapshot.Events, "card_accepted") || !hasEventType(accepted.Snapshot.Events, "card_effect_applied") {
		t.Fatalf("expected first activation to succeed and apply, got %+v", accepted.Snapshot.Events)
	}

	armCard(t, service, matchID, alice.UserID, "mirror_lance", 10)
	cooldown := submitCard(t, service, alice, matchID, 3, 3, 0)
	if !hasEventType(cooldown.Snapshot.Events, "card_rejected") {
		t.Fatalf("expected cooldown rejection, got %+v", cooldown.Snapshot.Events)
	}
}

// The 16 -> 20 pool expansion gives opponent_pattern cards their own fan
// geometry: ember_lance fires 5 faster, smaller, amber bullets while
// mirror_lance keeps the original constants bit-for-bit (the pre-expansion
// behaviour other layers and goldens depend on).
func TestCardEffectOpponentPatternGeometryPerCard(t *testing.T) {
	for _, tc := range []struct {
		cardID     string
		wantCount  int
		wantSpeed  float64
		wantRadius float64
		wantColor  string
	}{
		{"mirror_lance", cardOpponentPatternCount, cardOpponentPatternSpeed, cardOpponentPatternRadius, cardOpponentPatternColor},
		{"ember_lance", 5, 6.5, 4.0, "amber"},
	} {
		t.Run(tc.cardID, func(t *testing.T) {
			service, alice, bob, matchID := cardEffectMatch(t)
			armCard(t, service, matchID, alice.UserID, tc.cardID, 10)

			resp := submitCard(t, service, alice, matchID, 1, 1, 0)

			event := findEvent(resp.Snapshot.Events, "card_effect_applied")
			if event == nil || event.CardID != tc.cardID || event.Value != tc.wantCount {
				t.Fatalf("%s expected card_effect_applied value %d, got %+v", tc.cardID, tc.wantCount, resp.Snapshot.Events)
			}
			if len(resp.Snapshot.ActiveCards) != 1 {
				t.Fatalf("%s expected one active card, got %+v", tc.cardID, resp.Snapshot.ActiveCards)
			}
			card := resp.Snapshot.ActiveCards[0]
			if card.CardID != tc.cardID || card.EffectKind != "opponent_pattern" || card.PatternID != tc.cardID || card.TargetUserID != bob.UserID {
				t.Fatalf("%s unexpected active card: %+v", tc.cardID, card)
			}
			spawns := 0
			for _, delta := range resp.Snapshot.BulletsDelta {
				if delta.Op != "spawn" || delta.PatternID != tc.cardID {
					continue
				}
				spawns++
				if delta.Color != tc.wantColor {
					t.Fatalf("%s spawn color = %q, want %q", tc.cardID, delta.Color, tc.wantColor)
				}
				if delta.Radius != tc.wantRadius {
					t.Fatalf("%s spawn radius = %v, want %v", tc.cardID, delta.Radius, tc.wantRadius)
				}
				if speed := math.Hypot(delta.VX, delta.VY); math.Abs(speed-tc.wantSpeed) > 0.001 {
					t.Fatalf("%s spawn speed = %v, want %v", tc.cardID, speed, tc.wantSpeed)
				}
			}
			if spawns != tc.wantCount {
				t.Fatalf("%s spawned %d bullets, want %d", tc.cardID, spawns, tc.wantCount)
			}
		})
	}
}
