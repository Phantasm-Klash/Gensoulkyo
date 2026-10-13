package core

import (
	"testing"
	"time"
)

// The gateway deployment runs the lobby without a local battle binary, so the
// in-memory registry holds two entries: the auto-registered local placeholder
// (`battle-local-dev`, never heartbeated) and the remote agent's server
// (`bs-104-1`, heartbeated every 5s). Allocation picked the loopback
// placeholder and the match never reached the remote host.
func TestAllocationPrefersRemoteServerOverLocalPlaceholder(t *testing.T) {
	now := time.Date(2026, 10, 13, 5, 47, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})

	// Exactly what the RAKsmart agent sends over /v2/rpc/battle.servers.register.
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "bs-104-1",
		Endpoint:       "104.233.217.232:7400",
		Region:         "default",
		BuildID:        "dev",
		Capacity:       20,
		Status:         "online",
		SupportedModes: []string{"mvp_boss_race", "pvp_duel"},
	}); err != nil {
		t.Fatalf("register remote battle server: %v", err)
	}

	chosen := service.selectBattleServerLocked("mvp_boss_race")
	if chosen == nil {
		t.Fatal("no battle server was selected")
	}
	if chosen.BattleServerID == DefaultBattleServerID {
		t.Fatalf("allocation fell back to the local placeholder %s instead of the registered remote server", chosen.Endpoint)
	}
	if chosen.BattleServerID != "bs-104-1" {
		t.Fatalf("expected the remote server to win, got %s (%s)", chosen.BattleServerID, chosen.Endpoint)
	}
}

// A stale local placeholder must never beat a live remote server even when the
// remote one is under load: correctness (a server that actually exists) comes
// before balancing.
func TestAllocationIgnoresStaleLocalPlaceholderDespiteLoad(t *testing.T) {
	now := time.Date(2026, 10, 13, 5, 47, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})

	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "bs-104-1",
		Endpoint:       "104.233.217.232:7400",
		Region:         "default",
		BuildID:        "dev",
		Capacity:       20,
		Status:         "online",
		SupportedModes: []string{"mvp_boss_race", "pvp_duel"},
	}); err != nil {
		t.Fatalf("register remote battle server: %v", err)
	}
	// Give the remote server a real load: three of twenty slots busy.
	if _, err := service.BattleServerHeartbeat(BattleServerHeartbeatRequest{
		BattleServerID: "bs-104-1",
		Endpoint:       "104.233.217.232:7400",
		Capacity:       20,
		ActiveMatches:  3,
		Load:           0.4,
		Status:         "online",
		SupportedModes: []string{"mvp_boss_race", "pvp_duel"},
	}); err != nil {
		t.Fatalf("heartbeat remote battle server: %v", err)
	}

	chosen := service.selectBattleServerLocked("mvp_boss_race")
	if chosen == nil || chosen.BattleServerID != "bs-104-1" {
		t.Fatalf("a live remote server must be used even under load: %+v", chosen)
	}
}

// With no remote server at all the placeholder is still the right answer, so
// the fix must not break single-machine development.
func TestAllocationUsesLocalPlaceholderWhenItIsTheOnlyServer(t *testing.T) {
	service := NewService(Config{})
	chosen := service.selectBattleServerLocked("mvp_boss_race")
	if chosen == nil {
		t.Fatal("expected the local placeholder to be selected")
	}
	if chosen.BattleServerID != DefaultBattleServerID || chosen.Endpoint != DefaultBattleEndpoint {
		t.Fatalf("expected the local placeholder, got %+v", chosen)
	}
}

// An offline remote server must hand the match back to the placeholder.
func TestAllocationReturnsToLocalPlaceholderWhenRemoteGoesOffline(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "bs-104-1",
		Endpoint:       "104.233.217.232:7400",
		Capacity:       20,
		Status:         "online",
		SupportedModes: []string{"mvp_boss_race", "pvp_duel"},
	}); err != nil {
		t.Fatalf("register remote battle server: %v", err)
	}
	if _, err := service.BattleServerOffline(BattleServerOfflineRequest{BattleServerID: "bs-104-1"}); err != nil {
		t.Fatalf("mark remote battle server offline: %v", err)
	}
	chosen := service.selectBattleServerLocked("mvp_boss_race")
	if chosen == nil || chosen.BattleServerID != DefaultBattleServerID {
		t.Fatalf("expected the local placeholder after the remote went offline, got %+v", chosen)
	}
}

// A remote that only supports other modes must not steal a boss-race match.
func TestAllocationSkipsRemoteWithoutTheRequestedMode(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.RegisterBattleServer(RegisterBattleServerRequest{
		BattleServerID: "bs-104-1",
		Endpoint:       "104.233.217.232:7400",
		Capacity:       20,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register remote battle server: %v", err)
	}
	chosen := service.selectBattleServerLocked("mvp_boss_race")
	if chosen == nil || chosen.BattleServerID != DefaultBattleServerID {
		t.Fatalf("a remote without the mode must not be selected, got %+v", chosen)
	}
}
