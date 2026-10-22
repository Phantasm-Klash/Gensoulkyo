package core

import (
	"testing"
)

// TestBattleAllocationSurvivesLosingTheLiveMatch: a lobby restart empties the
// in-memory match map, but the battle server is on another host and still
// running. Reading the allocation back from the persisted match row is what
// lets the player rejoin instead of stranding a live match.
//
// The "restart" is simulated by dropping the match from memory only -- the store
// rows are untouched, which is exactly the state a restarted process is in.
func TestBattleAllocationSurvivesLosingTheLiveMatch(t *testing.T) {
	service := NewService(Config{
		Rooms:        NewMemoryRoomStore(),
		QueueTickets: NewMemoryQueueTicketStore(),
	})
	alice := mustLogin(t, service, "Recovery Alice")
	bob := mustLogin(t, service, "Recovery Bob")
	outsider := mustLogin(t, service, "Recovery Outsider")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "recovery_deck",
		DeckSnapshot: validDeck("recovery_deck"),
		ModeParams:   map[string]any{"max_ticks": 1800, "boss_id": "hui"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "recovery_guest_deck",
		DeckSnapshot: validDeck("recovery_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	matchID := joined.MatchID
	if matchID == "" {
		t.Fatalf("room did not form a match: %+v", joined)
	}

	before, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation before the restart: %v", err)
	}
	if before == nil || before.MaxTicks != 1800 {
		t.Fatalf("allocation before the restart = %+v, want max ticks 1800", before)
	}

	// Simulate the restart: the live match is gone, the rows are not.
	delete(service.matches, matchID)

	after, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation after the restart: %v", err)
	}
	if after == nil {
		t.Fatalf("allocation after the restart is nil")
	}
	if after.MatchID != matchID {
		t.Fatalf("recovered allocation match id = %q, want %q", after.MatchID, matchID)
	}
	if after.MaxTicks != 1800 {
		t.Fatalf("recovered allocation max ticks = %d, want 1800 (the room option must survive too)", after.MaxTicks)
	}
	if after.BattleServerID != before.BattleServerID || after.ServerSeed != before.ServerSeed {
		t.Fatalf("recovered allocation does not match the original: %+v vs %+v", after, before)
	}

	// Membership still has to be enforced: the persisted match row holds a player
	// count but no roster, so this is the check that could be skipped by accident.
	if _, err := service.BattleAllocation(outsider.SessionToken, matchID); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("outsider allocation = %v, want %s", err, codeUnauthorized)
	}
}

// TestBattleAllocationRefusesAnUnknownMatchWithoutLeaking: a match that never
// existed must still read as absent, not as an authorization failure.
func TestBattleAllocationRefusesAnUnknownMatchWithoutLeaking(t *testing.T) {
	service := NewService(Config{
		Rooms:        NewMemoryRoomStore(),
		QueueTickets: NewMemoryQueueTicketStore(),
	})
	alice := mustLogin(t, service, "Unknown Alice")
	if _, err := service.BattleAllocation(alice.SessionToken, "match_does_not_exist"); ErrorCode(err) != codeNotFound {
		t.Fatalf("unknown match = %v, want %s", err, codeNotFound)
	}
}
