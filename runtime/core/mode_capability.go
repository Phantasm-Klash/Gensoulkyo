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
// battle_royale -- fills its room and then fails at MatchServer::Start with
// add_player_failed. certification and pvp_duel are two-player and do start, but
// they are a race reskin: no PvP logic, no certification-specific simulation,
// and their settlement inputs (damage, boss HP) are not fed by anything the
// battle server emits.
//
// So "declared" and "can finish a match" are different sets, and conflating them
// is what let a four-player world-boss room be created in the first place.
var playableModes = map[string]struct{}{
	"mvp_boss_race": {},
	"coop_boss":     {},
}

// ModePlayable reports whether a mode can finish a match end to end.
func ModePlayable(modeID string) bool {
	_, ok := playableModes[strings.TrimSpace(modeID)]
	return ok
}

// PlayableModeIDs lists them in a stable order, for error messages, the
// placeholder battle server's SupportedModes, and the client's mode picker.
func PlayableModeIDs() []string {
	ids := make([]string, 0, len(playableModes))
	for id := range playableModes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RequirePlayableMode turns an unplayable mode into the error a caller returns.
//
// certification and pvp_duel are deliberately NOT in the playable set even
// though they are two-player and do start. They are a race reskin: the battle
// server has no certification or duel simulation, so the player would be told
// they are climbing a rating ladder or duelling while playing an ordinary race
// whose settlement inputs (damage, boss HP) nothing feeds. Refusing them is the
// honest answer until they exist. The default mode_id fallback ("certification")
// is therefore refused too, which is intended: a client that sends no mode gets
// told to pick one.
func RequirePlayableMode(modeID string) error {
	if ModePlayable(modeID) {
		return nil
	}
	return newError(codeModeUnavailable, "%s", ModeUnavailableDetail(modeID))
}

// ModeUnavailableDetail explains why a mode is refused, and what is available
// instead. The wording is part of the contract: the client shows it verbatim.
func ModeUnavailableDetail(modeID string) string {
	return "mode " + strings.TrimSpace(modeID) + " is not playable yet (playable: " +
		strings.Join(PlayableModeIDs(), ", ") + ")"
}
