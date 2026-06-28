package core

import (
	"fmt"
	"math"
	"sort"
)

const (
	bossX        = 480.0
	bossY        = 112.0
	bulletMinX   = 96.0
	bulletMaxX   = 864.0
	bulletMinY   = -80.0
	bulletMaxY   = 752.0
	playerRadius = 4.5
	grazeRadius  = 28.0
	maxBullets   = 384
)

type bulletState struct {
	ID        string
	PatternID string
	Kind      string
	X         float64
	Y         float64
	VX        float64
	VY        float64
	Radius    float64
	Damage    int
	Color     string
	SpawnTick int
}

func (s *Service) advanceSimulationLocked(match *matchState, targetTick int) {
	if targetTick <= match.LastSimulatedTick {
		return
	}
	if match.LastSimulatedTick < 0 {
		match.LastSimulatedTick = 0
	}
	for tick := match.LastSimulatedTick + 1; tick <= targetTick; tick++ {
		emitTickBulletsLocked(match, tick)
		moveBulletsLocked(match, tick)
		resolveBulletContactsLocked(match, tick)
		expireActiveCardsLocked(match, tick)
		match.Tick = max(match.Tick, tick)
	}
	match.LastSimulatedTick = targetTick
	updateModeStateLocked(match)
}

func emitTickBulletsLocked(match *matchState, tick int) {
	switch match.ModeID {
	case "world_boss":
		if tick%30 == 1 {
			emitRingLocked(match, tick, "boss_ring", 18, 2.35, 4.8, "ruby")
		}
		if tick%42 == 7 {
			emitAimedFanLocked(match, tick, "boss_aimed_fan", 7, math.Pi/2.8, 3.4, 5.4, "amber")
		}
	case "instance_boss":
		if tick%36 == 1 {
			emitSeededArcLocked(match, tick, "instance_arc", 14, math.Pi*0.25, math.Pi*0.75, 2.6, 4.6, "violet")
		}
		if tick%54 == 11 {
			emitAimedFanLocked(match, tick, "instance_needle", 5, math.Pi/4.0, 4.2, 4.2, "cyan")
		}
	case "battle_royale":
		if tick%45 == 1 {
			emitSeededArcLocked(match, tick, "br_crossfire", 12, math.Pi*0.15, math.Pi*0.85, 3.0, 4.8, "lime")
		}
	default:
		emitStageBulletsLocked(match, tick)
	}
}

func emitStageBulletsLocked(match *matchState, tick int) {
	switch match.StageID {
	case "misty_crossfire":
		if tick%34 == 1 {
			emitSeededArcLocked(match, tick, "stage_misty_seeded_arc", 10, math.Pi*0.16, math.Pi*0.84, 2.7, 4.5, "gold")
		}
		if tick%58 == 19 {
			emitAimedFanLocked(match, tick, "stage_misty_split_fan_base", 7, math.Pi/3.0, 3.1, 5.0, "violet")
		}
	case "clockwork_bloom":
		if tick%72 == 1 {
			emitRingLocked(match, tick, "stage_clockwork_blossom_base", 24, 1.95, 5.8, "green")
		}
		if tick%36 == 9 {
			emitAimedFanLocked(match, tick, "stage_clockwork_homing_base", 4, math.Pi/2.5, 2.8, 5.0, "violet")
		}
	case "lunar_maze":
		if tick%26 == 1 {
			emitSeededArcLocked(match, tick, "stage_lunar_curtain", 14, math.Pi*0.08, math.Pi*0.92, 3.1, 4.8, "gold")
		}
		if tick%44 == 11 {
			emitAimedFanLocked(match, tick, "stage_lunar_knife_burst_base", 9, math.Pi/4.6, 3.7, 4.2, "white")
		}
		if tick%54 == 23 {
			emitRingLocked(match, tick, "stage_lunar_orbital_base", 16, 2.35, 4.5, "violet")
		}
	default:
		if tick%40 == 1 {
			emitRingLocked(match, tick, "stage_starlit_ring", 12, 2.75, 4.5, "blue")
		}
		if tick%60 == 15 {
			emitAimedFanLocked(match, tick, "stage_starlit_nway", 5, math.Pi/3.5, 3.2, 5.0, "pink")
		}
	}
}

func emitRingLocked(match *matchState, tick int, patternID string, count int, speed float64, radius float64, color string) {
	phase := seedAngle(match.ServerSeed, tick, patternID)
	for i := 0; i < count; i++ {
		angle := phase + (math.Pi*2.0*float64(i))/float64(count)
		addBulletLocked(match, tick, patternID, "ring", angle, speed, radius, 1, color)
	}
}

func emitAimedFanLocked(match *matchState, tick int, patternID string, count int, spread float64, speed float64, radius float64, color string) {
	targetX, targetY := averagePlayerPosition(match)
	base := math.Atan2(targetY-bossY, targetX-bossX)
	start := base - spread*0.5
	step := 0.0
	if count > 1 {
		step = spread / float64(count-1)
	}
	for i := 0; i < count; i++ {
		addBulletLocked(match, tick, patternID, "nway", start+step*float64(i), speed, radius, 1, color)
	}
}

func emitSeededArcLocked(match *matchState, tick int, patternID string, count int, startAngle float64, endAngle float64, speed float64, radius float64, color string) {
	span := endAngle - startAngle
	for i := 0; i < count; i++ {
		t := 0.0
		if count > 1 {
			t = float64(i) / float64(count-1)
		}
		wobble := deterministicUnit(match.ServerSeed, tick, patternID, i)*0.22 - 0.11
		angle := startAngle + span*t + wobble
		velocity := speed * (0.86 + deterministicUnit(match.ServerSeed, tick+17, patternID, i)*0.32)
		addBulletLocked(match, tick, patternID, "seeded_arc", angle, velocity, radius, 1, color)
	}
}

func addBulletLocked(match *matchState, tick int, patternID string, kind string, angle float64, speed float64, radius float64, damage int, color string) {
	if len(match.Bullets) >= maxBullets {
		return
	}
	match.NextBulletSeq++
	id := fmt.Sprintf("%s_b%06d", match.MatchID, match.NextBulletSeq)
	bullet := &bulletState{
		ID:        id,
		PatternID: patternID,
		Kind:      kind,
		X:         bossX,
		Y:         bossY,
		VX:        math.Cos(angle) * speed,
		VY:        math.Sin(angle) * speed,
		Radius:    radius,
		Damage:    damage,
		Color:     color,
		SpawnTick: tick,
	}
	match.Bullets[id] = bullet
	match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("spawn", bullet, tick))
}

func moveBulletsLocked(match *matchState, tick int) {
	for _, id := range sortedBulletIDs(match) {
		bullet := match.Bullets[id]
		if bullet == nil {
			continue
		}
		bullet.X += bullet.VX
		bullet.Y += bullet.VY
		if bullet.X < bulletMinX || bullet.X > bulletMaxX || bullet.Y < bulletMinY || bullet.Y > bulletMaxY {
			match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("despawn", bullet, tick))
			delete(match.Bullets, id)
			continue
		}
		if tick%10 == 0 {
			match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("move", bullet, tick))
		}
	}
}

func resolveBulletContactsLocked(match *matchState, tick int) {
	for _, id := range sortedBulletIDs(match) {
		bullet := match.Bullets[id]
		if bullet == nil {
			continue
		}
		removed := false
		for _, userID := range match.PlayerIDs {
			player := match.Players[userID]
			distance := math.Hypot(player.X-bullet.X, player.Y-bullet.Y)
			hitDistance := playerRadius + bullet.Radius
			if distance <= hitDistance {
				if tick > player.InvulnerableUntil {
					player.HitCount++
					player.Score = max(0, player.Score-25)
					player.InvulnerableUntil = tick + 90
					appendMatchEventLocked(match, MatchEvent{Type: "hit", Tick: tick, UserID: userID, BulletID: id, Value: bullet.Damage, X: round(player.X, 3), Y: round(player.Y, 3)})
				}
				match.LastBulletDeltas = append(match.LastBulletDeltas, bulletDelta("despawn", bullet, tick))
				delete(match.Bullets, id)
				removed = true
				break
			}
			if distance <= grazeRadius+bullet.Radius {
				if _, ok := player.GrazedBullets[id]; !ok {
					player.GrazedBullets[id] = struct{}{}
					player.GrazeCount++
					player.Score += 3
					appendMatchEventLocked(match, MatchEvent{Type: "graze", Tick: tick, UserID: userID, BulletID: id, Value: 1, X: round(player.X, 3), Y: round(player.Y, 3)})
				}
			}
		}
		if removed {
			continue
		}
	}
}

func bulletDelta(op string, bullet *bulletState, tick int) BulletDelta {
	return BulletDelta{
		Op:        op,
		BulletID:  bullet.ID,
		PatternID: bullet.PatternID,
		Kind:      bullet.Kind,
		Tick:      tick,
		X:         round(bullet.X, 3),
		Y:         round(bullet.Y, 3),
		VX:        round(bullet.VX, 4),
		VY:        round(bullet.VY, 4),
		Radius:    round(bullet.Radius, 3),
		Damage:    bullet.Damage,
		Color:     bullet.Color,
	}
}

func averagePlayerPosition(match *matchState) (float64, float64) {
	if len(match.PlayerIDs) == 0 {
		return startX, startY
	}
	x := 0.0
	y := 0.0
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		x += player.X
		y += player.Y
	}
	return x / float64(len(match.PlayerIDs)), y / float64(len(match.PlayerIDs))
}

func bossMaxHPForMode(modeID string) int {
	switch modeID {
	case "world_boss":
		return 100000
	case "instance_boss":
		return 16000
	case "battle_royale":
		return 0
	default:
		return 2400
	}
}

func updateModeStateLocked(match *matchState) {
	if match.ModeState == nil {
		match.ModeState = map[string]any{}
	}
	match.ModeState["active_cards"] = len(match.ActiveCards)
	match.ModeState["stage_id"] = match.StageID
	switch match.ModeID {
	case "certification":
		totalDamage := totalDamageDealt(match)
		match.ModeState["rating_code"] = "copper"
		match.ModeState["rank_score_preview"] = 1000 + totalDamage/5
		match.ModeState["challenge_progress"] = clamp(float64(totalDamage)/float64(max(1, match.BossMaxHP)), 0.0, 1.0)
		match.ModeState["boss_hp_preview"] = match.BossHP
		match.ModeState["active_bullets"] = len(match.Bullets)
	case "pvp_duel":
		match.ModeState["duel_round"] = 1 + match.Tick/(TickRate*30)
		match.ModeState["duel_score_limit"] = 1
		match.ModeState["duel_status"] = "running"
		match.ModeState["active_bullets"] = len(match.Bullets)
	case "battle_royale":
		match.ModeState["round_index"] = match.Tick / (TickRate * 30)
		match.ModeState["choice_deadline_tick"] = ((match.Tick / (TickRate * 30)) + 1) * TickRate * 30
		match.ModeState["public_pool_hash"] = shortHash(fmt.Sprintf("%d:%s:%d", match.ServerSeed, match.ModeID, match.Tick/(TickRate*30)))
		match.ModeState["active_bullets"] = len(match.Bullets)
	case "world_boss":
		match.ModeState["boss_hp_preview"] = match.BossHP
		match.ModeState["boss_max_hp"] = match.BossMaxHP
		match.ModeState["daily_attempts_left"] = 3
		match.ModeState["active_bullets"] = len(match.Bullets)
	case "instance_boss":
		phase := "opening"
		if match.BossHP < match.BossMaxHP*2/3 {
			phase = "mid"
		}
		if match.BossHP < match.BossMaxHP/3 {
			phase = "final"
		}
		if match.BossHP == 0 {
			phase = "cleared"
		}
		match.ModeState["boss_phase"] = phase
		match.ModeState["boss_hp_preview"] = match.BossHP
		match.ModeState["boss_max_hp"] = match.BossMaxHP
		if status, ok := match.ModeState["party_status"].(string); !ok || (status != "cleared" && status != "failed") {
			match.ModeState["party_status"] = "engaged"
		}
		match.ModeState["active_bullets"] = len(match.Bullets)
	default:
		match.ModeState["active_bullets"] = len(match.Bullets)
	}
}

func totalDamageDealt(match *matchState) int {
	total := 0
	for _, player := range match.Players {
		total += player.DamageDealt
	}
	return total
}

func topDamagePlayerID(match *matchState) string {
	winnerID := ""
	bestDamage := -1
	for _, userID := range sortedPlayerIDs(match) {
		player := match.Players[userID]
		if player == nil {
			continue
		}
		if player.DamageDealt > bestDamage {
			bestDamage = player.DamageDealt
			winnerID = userID
		}
	}
	return winnerID
}

func deterministicUnit(seed int64, tick int, patternID string, index int) float64 {
	hash := shortHash(fmt.Sprintf("%d:%d:%s:%d", seed, tick, patternID, index))
	value := 0
	for i := 0; i < len(hash); i++ {
		value = value*16 + hexValue(hash[i])
	}
	return float64(value%10000) / 10000.0
}

func seedAngle(seed int64, tick int, patternID string) float64 {
	return deterministicUnit(seed, tick, patternID, 0) * math.Pi * 2.0
}

func hexValue(char byte) int {
	if char >= '0' && char <= '9' {
		return int(char - '0')
	}
	if char >= 'a' && char <= 'f' {
		return int(char-'a') + 10
	}
	if char >= 'A' && char <= 'F' {
		return int(char-'A') + 10
	}
	return 0
}

func copyBulletDeltas(source []BulletDelta) []BulletDelta {
	out := make([]BulletDelta, len(source))
	copy(out, source)
	return out
}

func expireActiveCardsLocked(match *matchState, tick int) {
	for _, id := range sortedActiveCardIDs(match) {
		card := match.ActiveCards[id]
		if card == nil || card.ExpiresTick <= tick {
			if card != nil {
				appendMatchEventLocked(match, MatchEvent{Type: "card_expired", Tick: tick, UserID: card.UserID, CardID: card.CardID})
			}
			delete(match.ActiveCards, id)
		}
	}
}

func activeCardSnapshots(match *matchState) []ActiveCardSnapshot {
	ids := sortedActiveCardIDs(match)
	out := make([]ActiveCardSnapshot, 0, len(ids))
	for _, id := range ids {
		card := match.ActiveCards[id]
		if card == nil {
			continue
		}
		out = append(out, ActiveCardSnapshot{
			ActivationID:  card.ActivationID,
			UserID:        card.UserID,
			CardID:        card.CardID,
			Slot:          card.Slot,
			StartedTick:   card.StartedTick,
			ExpiresTick:   card.ExpiresTick,
			EffectKind:    card.EffectKind,
			Cost:          round(card.Cost, 3),
			Damage:        card.Damage,
			CooldownUntil: card.CooldownUntil,
		})
	}
	return out
}

func sortedActiveCardIDs(match *matchState) []string {
	ids := make([]string, 0, len(match.ActiveCards))
	for id := range match.ActiveCards {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func snapshotBulletDeltas(match *matchState, full bool) []BulletDelta {
	if !full {
		return copyBulletDeltas(match.LastBulletDeltas)
	}
	ids := sortedBulletIDs(match)
	out := make([]BulletDelta, 0, len(ids))
	for _, id := range ids {
		out = append(out, bulletDelta("sync", match.Bullets[id], match.Tick))
	}
	return out
}

func sortedBulletIDs(match *matchState) []string {
	ids := make([]string, 0, len(match.Bullets))
	for id := range match.Bullets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func copyMatchEvents(source []MatchEvent) []MatchEvent {
	out := make([]MatchEvent, len(source))
	copy(out, source)
	return out
}

func appendMatchEventLocked(match *matchState, event MatchEvent) {
	match.NextEventSeq++
	event.Seq = match.NextEventSeq
	match.LastEvents = append(match.LastEvents, event)
	match.EventLog = append(match.EventLog, event)
	if len(match.EventLog) > maxEventLogEntries {
		match.EventLog = append([]MatchEvent{}, match.EventLog[len(match.EventLog)-maxEventLogEntries:]...)
	}
}

func lastMatchEvent(match *matchState) MatchEvent {
	if match == nil || len(match.EventLog) == 0 {
		return MatchEvent{}
	}
	return match.EventLog[len(match.EventLog)-1]
}
