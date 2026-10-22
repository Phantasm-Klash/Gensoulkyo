package core

import (
	"testing"
)

func coopRoom(t *testing.T, service *Service, host *AuthSession, seats int) *QueueResponse {
	t.Helper()
	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "options_deck",
		DeckSnapshot: validDeck("options_deck"),
		ModeParams:   map[string]any{"coop_players": seats},
	})
	if err != nil {
		t.Fatalf("create coop room: %v", err)
	}
	return created
}

func joinCoopRoom(t *testing.T, service *Service, guest *AuthSession, roomCode string, seats int) {
	t.Helper()
	if _, err := service.JoinRoom(guest.SessionToken, roomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "options_guest_deck",
		DeckSnapshot: validDeck("options_guest_deck"),
		ModeParams:   map[string]any{"coop_players": seats},
	}); err != nil {
		t.Fatalf("join coop room: %v", err)
	}
}

// TestUpdateRoomOptionsWritesThroughToSeatedTickets: the match snapshot is built
// from the tickets, so an edit that only touches the room record would produce a
// match that disagrees with the room the players are looking at.
func TestUpdateRoomOptionsWritesThroughToSeatedTickets(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Options Host")
	guest := mustLogin(t, service, "Options Guest")

	created := coopRoom(t, service, host, 4)
	joinCoopRoom(t, service, guest, created.RoomCode, 4)

	boss := "hui"
	updated, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{BossID: &boss})
	if err != nil {
		t.Fatalf("update boss: %v", err)
	}
	if updated.BossID != "hui" {
		t.Fatalf("response boss = %q, want hui", updated.BossID)
	}

	room, found, err := service.loadLiveRoomRecordLocked(created.RoomCode)
	if err != nil || !found {
		t.Fatalf("reload room: found=%v err=%v", found, err)
	}
	if room.BossID != "hui" {
		t.Fatalf("room boss = %q, want hui", room.BossID)
	}
	seats := 0
	for _, ticketID := range room.TicketIDs {
		ticket := service.queueTicketLocked(ticketID)
		if ticket == nil {
			t.Fatalf("ticket %s not resolvable", ticketID)
		}
		if ticket.BossID != "hui" {
			t.Fatalf("seated ticket %s kept boss %q, want hui", ticketID, ticket.BossID)
		}
		seats++
	}
	if seats != 2 {
		t.Fatalf("expected two seated tickets, got %d", seats)
	}
}

// TestUpdateRoomOptionsRefusesNonHost: the check is server-side, so a guest that
// calls the endpoint directly is still refused.
func TestUpdateRoomOptionsRefusesNonHost(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Refuse Host")
	guest := mustLogin(t, service, "Refuse Guest")

	created := coopRoom(t, service, host, 4)
	joinCoopRoom(t, service, guest, created.RoomCode, 4)

	boss := "hui"
	if _, err := service.UpdateRoomOptions(guest.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{BossID: &boss}); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("guest update = %v, want %s", err, codeUnauthorized)
	}
}

// TestUpdateRoomOptionsLocksAfterMatchFound: once found, the options are baked
// into the allocation and the spawned process.
func TestUpdateRoomOptionsLocksAfterMatchFound(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Lock Host")
	guest := mustLogin(t, service, "Lock Guest")

	created := coopRoom(t, service, host, 2)
	joinCoopRoom(t, service, guest, created.RoomCode, 2)

	boss := "hui"
	if _, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{BossID: &boss}); ErrorCode(err) != codeRoomUnavailable {
		t.Fatalf("update after the match was found = %v, want %s", err, codeRoomUnavailable)
	}
}

// TestUpdateRoomOptionsShrinkFillsTheRoom: dropping a four-seat room to two while
// two players are already seated fills it, so the match forms on the spot.
//
// The guard that refuses shrinking below the seated count cannot fire for
// coop_boss (its only sizes are 2 and 4, and four seated means the room is
// already found), but the guard stays: the day a mode with 3-10 seats arrives it
// is the difference between an error and a room that can never fill.
func TestUpdateRoomOptionsShrinkFillsTheRoom(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Shrink Host")
	guest := mustLogin(t, service, "Shrink Guest")

	created := coopRoom(t, service, host, 4)
	joinCoopRoom(t, service, guest, created.RoomCode, 4)

	updated, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{CoopPlayers: intPtr(2)})
	if err != nil {
		t.Fatalf("shrink to 2 with 2 seated should fill the room: %v", err)
	}
	if updated.RequiredPlayers != 2 {
		t.Fatalf("required players after shrink = %d, want 2", updated.RequiredPlayers)
	}
	if updated.RoomStatus != RoomStatusFound {
		t.Fatalf("room status after shrink = %q, want %q", updated.RoomStatus, RoomStatusFound)
	}
}

// TestUpdateRoomOptionsIsIdempotent: re-sending the same options is not an error.
func TestUpdateRoomOptionsIsIdempotent(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Idempotent Host")
	created := coopRoom(t, service, host, 4)

	boss := "tian"
	first, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{BossID: &boss})
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	second, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{BossID: &boss})
	if err != nil {
		t.Fatalf("repeating the same update: %v", err)
	}
	if first.BossID != second.BossID || second.BossID != "tian" {
		t.Fatalf("idempotent update changed the answer: %q then %q", first.BossID, second.BossID)
	}
}

// TestUpdateRoomOptionsRejectsCoopSeatsOnOtherModes: coop_players is coop_boss's
// option, not a general one.
func TestUpdateRoomOptionsRejectsCoopSeatsOnOtherModes(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "Wrong Mode Host")
	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "wrong_mode_deck",
		DeckSnapshot: validDeck("wrong_mode_deck"),
	})
	if err != nil {
		t.Fatalf("create race room: %v", err)
	}
	if _, err := service.UpdateRoomOptions(host.SessionToken, created.RoomCode, UpdateRoomOptionsRequest{CoopPlayers: intPtr(4)}); ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("coop_players on a race room = %v, want %s", err, codeInvalidRequest)
	}
}

func intPtr(value int) *int { return &value }
