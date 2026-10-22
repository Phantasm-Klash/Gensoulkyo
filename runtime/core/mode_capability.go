package core

import (
	"sort"
	"strings"
)

// Mode capability: which declared modes can actually finish a match.
//
// ModeConfigs declares seven modes. The battle server's production binary has
// exactly two simulations behind them (apps/phk_battle_server/main.cpp builds a
// MatchServer, which is CoopBossSimulation for coop_boss and BossRaceSimulation
// for everything else):
//
//	mvp_boss_race -> BossRaceSimulation   (real danmaku tna-1.40, boss catalog)
//	coop_boss     -> CoopBossSimulation   (its own mechanic table)
//	anything else -> BossRaceSimulation too, mode_id only echoed into the
//	                 payload and the .dem header
//
// That last line is the problem. BossRaceSimulation caps at kBossRaceMaxPlayers
// (2), so a mode that declares MinPlayers >= 4 -- world_boss, instance_boss,
// battle_royale -- fills its room and then dies at MatchServer::Start with
// add_player_failed. certification and pvp_duel are two-player and do start, but
// they are a race reskin: no certification or duel simulation exists, so a
// player would be told they are climbing a rating ladder or duelling while
// playing an ordinary race whose settlement inputs nothing feeds.
//
// So "declared" and "can finish a match" are two different sets.
//
// The set is injected rather than defaulted on, deliberately. A great many tests
// build matches in modes that only exist to exercise some other behaviour --
// settlement shape, gift charges, battle-server selection, records aggregation --
// and their mode choice is incidental to what they assert. Making the gate a
// default would have meant rewriting dozens of those tests to use a mode they do
// not care about, and every rewrite is a chance to quietly change what the test
// covers. Instead the deployment that actually serves players passes the set in
// (see Config.PlayableModes and nakamaapi), and everything else keeps the old
// permissive behaviour.
var playableModes = map[string]struct{}{
	"mvp_boss_race": {},
	"coop_boss":     {},
}

// PlayableModeIDs lists the modes a deployment should offer, in a stable order.
func PlayableModeIDs() []string {
	ids := make([]string, 0, len(playableModes))
	for id := range playableModes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ModeUnavailableDetail explains why a mode is refused, and what is available
// instead. The wording is part of the contract: the client shows it verbatim.
func ModeUnavailableDetail(modeID string) string {
	return "mode " + strings.TrimSpace(modeID) + " is not playable yet (playable: " +
		strings.Join(PlayableModeIDs(), ", ") + ")"
}

// checkModePlayableLocked applies the injected playable set. An empty set means
// "no gate", which is what every caller that does not inject one gets.
func (s *Service) checkModePlayableLocked(modeID string) error {
	if len(s.playableModes) == 0 {
		return nil
	}
	if _, ok := s.playableModes[strings.TrimSpace(modeID)]; ok {
		return nil
	}
	return newError(codeModeUnavailable, "%s", ModeUnavailableDetail(modeID))
}

// playableModeSet normalises the injected list. Nil or empty means no gate.
func playableModeSet(modes []string) map[string]struct{} {
	if len(modes) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(modes))
	for _, mode := range modes {
		if trimmed := strings.TrimSpace(mode); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}
