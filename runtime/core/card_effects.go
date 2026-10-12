package core

import (
	"math"
	"sort"
)

// Card effect tuning. Opponent-directed fans reuse the same boss-centred
// geometry as the authoritative simulation so the client renders the exact
// deltas the server emits, without a second implementation.
const (
	cardOpponentPatternCount  = 7
	cardOpponentPatternSpread = math.Pi / 3.0
	cardOpponentPatternSpeed  = 5.0
	cardOpponentPatternRadius = 4.5
	cardOpponentPatternColor  = "violet"
)

// applyCardEffectLocked runs the EffectKind behaviour of a freshly accepted
// card activation. It is invoked by applyCardRequestLocked after the generic
// activation bookkeeping (energy, cooldown, damage, card_accepted event) so
// that active-card state is already recorded when an effect needs to attach a
// target to it.
func (s *Service) applyCardEffectLocked(match *matchState, player *playerState, activation serverCardActivation, tick int) {
	if match == nil || player == nil {
		return
	}
	switch activation.EffectKind {
	case "opponent_pattern":
		s.applyOpponentPatternLocked(match, player, activation, tick)
	case "bullet_purge":
		s.applyBulletPurgeLocked(match, player, activation, tick)
	case "invulnerable":
		s.applyInvulnerableLocked(match, player, activation, tick)
	case "finisher":
		s.applyFinisherLocked(match, player, activation, tick)
	}
}

// applyOpponentPatternLocked fires an aimed fan at the opponent's current
// position. The fan is seeded at the boss and points toward the target so it
// travels down the opponent's lane.
func (s *Service) applyOpponentPatternLocked(match *matchState, player *playerState, activation serverCardActivation, tick int) {
	targetID := opponentTargetID(match, player.UserID)
	if targetID == "" {
		appendMatchEventLocked(match, MatchEvent{Type: "card_effect_applied", Tick: tick, UserID: player.UserID, CardID: activation.CardID, Value: 0})
		return
	}
	patternID := activation.PatternID
	if patternID == "" {
		patternID = activation.CardID
	}
	target := match.Players[targetID]
	base := math.Atan2(target.Y-bossY, target.X-bossX)
	start := base - cardOpponentPatternSpread*0.5
	step := 0.0
	if cardOpponentPatternCount > 1 {
		step = cardOpponentPatternSpread / float64(cardOpponentPatternCount-1)
	}
	spawned := 0
	for i := 0; i < cardOpponentPatternCount; i++ {
		before := len(match.Bullets)
		addBulletLocked(match, tick, patternID, "nway", start+step*float64(i), cardOpponentPatternSpeed, cardOpponentPatternRadius, 1, cardOpponentPatternColor)
		if len(match.Bullets) > before {
			spawned++
		}
	}
	if card := match.ActiveCards[activation.ActivationID]; card != nil {
		card.TargetUserID = targetID
	}
	appendMatchEventLocked(match, MatchEvent{Type: "card_effect_applied", Tick: tick, UserID: player.UserID, CardID: activation.CardID, Value: spawned, TargetUserID: targetID})
}

// applyBulletPurgeLocked removes up to PurgeCount bullets inside PurgeRadius of
// the caster. Every bullet is scored once with deterministicUnit keyed on the
// server seed, tick, card id and the bullet's index in the canonical (bullet-id
// sorted) field order; the lowest scores are purged, ties broken by bullet id.
// This stateless pick is what the client mirror reproduces for local prediction.
func (s *Service) applyBulletPurgeLocked(match *matchState, player *playerState, activation serverCardActivation, tick int) {
	radius := activation.PurgeRadius
	count := activation.PurgeCount
	removed := 0
	if radius > 0 && count > 0 {
		type purgeCandidate struct {
			bulletID string
			unit     float64
		}
		candidates := make([]purgeCandidate, 0, len(match.Bullets))
		for index, id := range sortedBulletIDs(match) {
			bullet := match.Bullets[id]
			if bullet == nil {
				continue
			}
			if math.Hypot(bullet.X-player.X, bullet.Y-player.Y) > radius {
				continue
			}
			candidates = append(candidates, purgeCandidate{bulletID: id, unit: deterministicUnit(match.ServerSeed, tick, activation.CardID, index)})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].unit != candidates[j].unit {
				return candidates[i].unit < candidates[j].unit
			}
			return candidates[i].bulletID < candidates[j].bulletID
		})
		limit := count
		if limit > len(candidates) {
			limit = len(candidates)
		}
		for _, pick := range candidates[:limit] {
			if bullet := match.Bullets[pick.bulletID]; bullet != nil {
				match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("despawn", bullet, tick))
				delete(match.Bullets, pick.bulletID)
				removed++
			}
		}
	}
	appendMatchEventLocked(match, MatchEvent{Type: "card_effect_applied", Tick: tick, UserID: player.UserID, CardID: activation.CardID, Value: removed})
}

// applyInvulnerableLocked grants the caster a short damage-immune window using
// the existing playerState.InvulnerableUntil field consumed by
// resolveBulletContactsLocked. ExpiresTick already encodes tick+DurationTicks.
func (s *Service) applyInvulnerableLocked(match *matchState, player *playerState, activation serverCardActivation, tick int) {
	player.InvulnerableUntil = activation.ExpiresTick
	appendMatchEventLocked(match, MatchEvent{Type: "card_effect_applied", Tick: tick, UserID: player.UserID, CardID: activation.CardID, Value: 0})
}

// applyFinisherLocked is the ultimate: it clears every bullet on the field.
// Boss damage is applied once by the generic activation path in
// applyCardRequestLocked, so it is intentionally not repeated here.
func (s *Service) applyFinisherLocked(match *matchState, player *playerState, activation serverCardActivation, tick int) {
	cleared := 0
	for _, id := range sortedBulletIDs(match) {
		bullet := match.Bullets[id]
		if bullet == nil {
			continue
		}
		match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("despawn", bullet, tick))
		delete(match.Bullets, id)
		cleared++
	}
	appendMatchEventLocked(match, MatchEvent{Type: "card_effect_applied", Tick: tick, UserID: player.UserID, CardID: activation.CardID, Value: cleared})
}

// opponentTargetID picks the opponent a self-centred effect should aim at.
// With more than one opponent it resolves to the lowest user id so the choice
// is deterministic across server and replay.
func opponentTargetID(match *matchState, selfID string) string {
	candidates := make([]string, 0, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		if userID != selfID {
			candidates = append(candidates, userID)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	return candidates[0]
}
