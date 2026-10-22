package core

import (
	"testing"
)

// TestMaxTicksOptionReachesTheAllocation walks the option from the room to the
// allocation, which is where the battle host picks it up. If it stops anywhere in
// between, the room shows a match length the process never hears about.
func TestMaxTicksOptionReachesTheAllocation(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	alice := mustLogin(t, service, "Ticks Alice")
	bob := mustLogin(t, service, "Ticks Bob")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticks_deck",
		DeckSnapshot: validDeck("ticks_deck"),
		ModeParams:   map[string]any{"max_ticks": 1800},
	})
	if err != nil {
		t.Fatalf("create room with max_ticks: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticks_guest_deck",
		DeckSnapshot: validDeck("ticks_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.MatchID == "" {
		t.Fatalf("room did not form a match: %+v", joined)
	}
	allocation, ok := service.BattleAllocationForMatch(joined.MatchID)
	if !ok {
		t.Fatalf("no allocation for match %s", joined.MatchID)
	}
	if allocation.MaxTicks != 1800 {
		t.Fatalf("allocation max ticks = %d, want 1800", allocation.MaxTicks)
	}
}

// TestMaxTicksOptionDefaultsToTheHostBackstop: a room that says nothing leaves the
// field at zero, and the agent keeps its own --max-ticks.
func TestMaxTicksOptionDefaultsToTheHostBackstop(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	alice := mustLogin(t, service, "NoTicks Alice")
	bob := mustLogin(t, service, "NoTicks Bob")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "noticks_deck",
		DeckSnapshot: validDeck("noticks_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "noticks_guest_deck",
		DeckSnapshot: validDeck("noticks_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	allocation, ok := service.BattleAllocationForMatch(joined.MatchID)
	if !ok {
		t.Fatalf("no allocation for match %s", joined.MatchID)
	}
	if allocation.MaxTicks != 0 {
		t.Fatalf("allocation max ticks = %d, want 0 (host default)", allocation.MaxTicks)
	}
}

// TestMaxTicksOptionIsValidated covers the bounds and the step.
func TestMaxTicksOptionIsValidated(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "BadTicks Host")

	for _, ticks := range []int{59, 601, MinRoomMaxTicks - 60, MaxRoomMaxTicks + 60} {
		_, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       "mvp_boss_race",
			ActiveDeckID: "badticks_deck",
			DeckSnapshot: validDeck("badticks_deck"),
			ModeParams:   map[string]any{"max_ticks": ticks},
		})
		if ErrorCode(err) != codeInvalidRequest {
			t.Fatalf("max_ticks=%d = %v, want %s", ticks, err, codeInvalidRequest)
		}
	}

	// The bounds themselves are accepted. A second player, because the four
	// refusals above already spent this one's create budget.
	host = mustLogin(t, service, "GoodTicks Host")
	for _, ticks := range []int{MinRoomMaxTicks, 1800, MaxRoomMaxTicks} {
		if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       "mvp_boss_race",
			ActiveDeckID: "okticks_deck",
			DeckSnapshot: validDeck("okticks_deck"),
			ModeParams:   map[string]any{"max_ticks": ticks},
		}); err != nil {
			t.Fatalf("max_ticks=%d = %v, want accepted", ticks, err)
		}
	}
}

// TestUpdateRoomOptionsChangesMaxTicks: the host can retune the length, and the
// seated tickets carry it.
func TestUpdateRoomOptionsChangesMaxTicks(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Retune Host")
	created := coopRoom(t, service, host, 4)

	if _, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{
		ModeParams: map[string]any{"max_ticks": 900},
	}); err != nil {
		t.Fatalf("update max_ticks: %v", err)
	}
	room, found, err := service.loadLiveRoomRecordLocked(created.RoomCode)
	if err != nil || !found {
		t.Fatalf("reload room: found=%v err=%v", found, err)
	}
	for _, ticketID := range room.TicketIDs {
		ticket := service.queueTicketLocked(ticketID)
		if ticket == nil {
			t.Fatalf("ticket %s not resolvable", ticketID)
		}
		if ticket.MaxTicks != 900 {
			t.Fatalf("ticket %s max ticks = %d, want 900", ticketID, ticket.MaxTicks)
		}
	}
	if err := func() error {
		_, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{
			ModeParams: map[string]any{"max_ticks": 42},
		})
		return err
	}(); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("out-of-range update = %v, want %s", err, codeInvalidRequest)
	}
}
