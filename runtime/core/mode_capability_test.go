package core

import (
	"strings"
	"testing"
)

// TestPlayableModeIDsIsStableOrder pins the set the player-facing deployment
// offers. The battle server has two simulations behind seven declared modes;
// anything outside these two either dies at MatchServer::Start (MinPlayers >= 4
// against a two-player cap) or is a race reskin with no simulation of its own.
func TestPlayableModeIDsIsStableOrder(t *testing.T) {
	ids := PlayableModeIDs()
	if len(ids) != 2 || ids[0] != "coop_boss" || ids[1] != "mvp_boss_race" {
		t.Fatalf("PlayableModeIDs() = %v, want [coop_boss mvp_boss_race]", ids)
	}
	detail := ModeUnavailableDetail("world_boss")
	if !strings.Contains(detail, "world_boss") || !strings.Contains(detail, "coop_boss") {
		t.Fatalf("ModeUnavailableDetail does not name the mode or the alternatives: %q", detail)
	}
}

// TestInjectedPlayableModesGateRoomCreation: a deployment that injects the set
// refuses a mode the battle server cannot finish.
func TestInjectedPlayableModesGateRoomCreation(t *testing.T) {
	service := NewService(Config{PlayableModes: PlayableModeIDs()})
	host := mustLogin(t, service, "Gate Host")

	for _, mode := range []string{"world_boss", "instance_boss", "battle_royale", "certification", "pvp_duel"} {
		_, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       mode,
			ActiveDeckID: "gate_deck",
			DeckSnapshot: validDeck("gate_deck"),
		})
		if ErrorCode(err) != codeModeUnavailable {
			t.Fatalf("CreateRoom(%s) = %v, want %s", mode, err, codeModeUnavailable)
		}
	}
	// A fresh player: the refusals above do not consume the limiter, but this
	// success would be the sixth create for one user.
	playable := mustLogin(t, service, "Gate Playable")
	if _, err := service.CreateRoom(playable.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "gate_deck",
		DeckSnapshot: validDeck("gate_deck"),
	}); err != nil {
		t.Fatalf("CreateRoom(mvp_boss_race) = %v, want success", err)
	}
}

// TestInjectedPlayableModesGateMatchmaking covers the other entry into a match.
func TestInjectedPlayableModesGateMatchmaking(t *testing.T) {
	service := NewService(Config{PlayableModes: PlayableModeIDs()})
	player := mustLogin(t, service, "Gate Queue")

	_, err := service.JoinQueue(player.SessionToken, JoinQueueRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "gate_queue_deck",
		DeckSnapshot: validDeck("gate_queue_deck"),
	})
	if ErrorCode(err) != codeModeUnavailable {
		t.Fatalf("JoinQueue(world_boss) = %v, want %s", err, codeModeUnavailable)
	}
}

// TestNoInjectedPlayableModesMeansNoGate keeps every caller that does not inject
// a set on its current behaviour -- which is what makes the gate safe to add
// without rewriting the tests that build matches in modes they do not care about.
func TestNoInjectedPlayableModesMeansNoGate(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Ungated Host")
	for _, mode := range []string{"certification", "pvp_duel", "world_boss"} {
		if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       mode,
			ActiveDeckID: "ungated_deck",
			DeckSnapshot: validDeck("ungated_deck"),
		}); err != nil {
			t.Fatalf("CreateRoom(%s) without an injected set = %v, want success", mode, err)
		}
	}
}

// TestPlaceholderServerOnlyClaimsPlayableModes: the on-demand local placeholder
// used to claim every mode in ModeConfigs, which made it the catch-all that would
// accept a room no real battle server could serve.
func TestPlaceholderServerOnlyClaimsPlayableModes(t *testing.T) {
	service := NewService(Config{})
	service.registerDefaultBattleServerLocked()

	server := service.battleServers[DefaultBattleServerID]
	if server == nil {
		t.Fatalf("placeholder battle server %q was not registered", DefaultBattleServerID)
	}
	if len(server.SupportedModes) != len(PlayableModeIDs()) {
		t.Fatalf("placeholder claims %v, want %v", server.SupportedModes, PlayableModeIDs())
	}
	for _, mode := range server.SupportedModes {
		if _, ok := playableModes[mode]; !ok {
			t.Fatalf("placeholder claims unplayable mode %q", mode)
		}
	}
}
