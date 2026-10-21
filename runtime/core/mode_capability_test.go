package core

import (
	"strings"
	"testing"
)

// TestModePlayableOnlyClaimsWhatCanFinish pins the playable set.
//
// The battle server has two simulations behind seven declared modes. Anything
// outside the two below is either a race reskin (certification, pvp_duel) or a
// mode that fills its room and then dies at MatchServer::Start because
// BossRaceSimulation caps at two players (world_boss, instance_boss,
// battle_royale). Both failure modes look fine until a player is in them.
func TestModePlayableOnlyClaimsWhatCanFinish(t *testing.T) {
	playable := map[string]bool{
		"mvp_boss_race": true,
		"coop_boss":     true,
	}
	unplayable := []string{
		"certification", "pvp_duel", "battle_royale",
		"world_boss", "instance_boss",
		"", "not_a_mode", "MVP_BOSS_RACE",
	}
	for id := range playable {
		if !ModePlayable(id) {
			t.Fatalf("ModePlayable(%q) = false, want true", id)
		}
	}
	for _, id := range unplayable {
		if ModePlayable(id) {
			t.Fatalf("ModePlayable(%q) = true, want false", id)
		}
	}

	ids := PlayableModeIDs()
	if len(ids) != 2 {
		t.Fatalf("PlayableModeIDs() = %v, want the two playable modes", ids)
	}
	// Stable order, so error text and the client's picker do not churn.
	if ids[0] != "coop_boss" || ids[1] != "mvp_boss_race" {
		t.Fatalf("PlayableModeIDs() = %v, want sorted", ids)
	}
	detail := ModeUnavailableDetail("world_boss")
	if !strings.Contains(detail, "world_boss") || !strings.Contains(detail, "coop_boss") {
		t.Fatalf("ModeUnavailableDetail does not name the mode or the alternatives: %q", detail)
	}
}

// TestCreateRoomRejectsUnplayableMode: a room for a mode that cannot finish must
// not exist, because the alternative is a full room that dies on start.
func TestCreateRoomRejectsUnplayableMode(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Mode Gate Host")

	for _, mode := range []string{"world_boss", "instance_boss", "battle_royale", "certification"} {
		_, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       mode,
			ActiveDeckID: "gate_deck",
			DeckSnapshot: validDeck("gate_deck"),
		})
		if ErrorCode(err) != codeModeUnavailable {
			t.Fatalf("CreateRoom(%s) = %v, want %s", mode, err, codeModeUnavailable)
		}
	}

	// The playable modes still work.
	if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "gate_deck",
		DeckSnapshot: validDeck("gate_deck"),
	}); err != nil {
		t.Fatalf("CreateRoom(mvp_boss_race) = %v, want success", err)
	}
}

// TestJoinQueueRejectsUnplayableMode covers the other entry into a match.
func TestJoinQueueRejectsUnplayableMode(t *testing.T) {
	service := NewService(Config{})
	player := mustLogin(t, service, "Mode Gate Queue")

	_, err := service.JoinQueue(player.SessionToken, JoinQueueRequest{
		ModeID:       "world_boss",
		ActiveDeckID: "gate_queue_deck",
		DeckSnapshot: validDeck("gate_queue_deck"),
	})
	if ErrorCode(err) != codeModeUnavailable {
		t.Fatalf("JoinQueue(world_boss) = %v, want %s", err, codeModeUnavailable)
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
		if !ModePlayable(mode) {
			t.Fatalf("placeholder claims unplayable mode %q", mode)
		}
	}
}
