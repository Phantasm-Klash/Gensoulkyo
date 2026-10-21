package core

import (
	"testing"
	"time"
)

// TestCreateRoomPersistsTheHostSeat pins the write half of seat persistence.
//
// CreateRoom used to write the room row and nothing else, so lobby_room_members
// stayed empty and a restarted lobby reported current_players = 0 for a room
// that still had its host sitting in it.
func TestCreateRoomPersistsTheHostSeat(t *testing.T) {
	rooms := NewMemoryRoomStore()
	service := NewService(Config{Rooms: rooms})
	host := mustLogin(t, service, "Seat Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "seat_host_deck",
		DeckSnapshot: validDeck("seat_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}

	members, err := rooms.ListMembers(created.RoomCode)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("host seat count = %d, want 1", len(members))
	}
	seat := members[0]
	if !seat.IsHost {
		t.Fatalf("seat is_host = false, want true")
	}
	if seat.SeatIndex != 0 {
		t.Fatalf("seat index = %d, want 0", seat.SeatIndex)
	}
	if seat.UserID != host.UserID {
		t.Fatalf("seat user = %q, want %q", seat.UserID, host.UserID)
	}
	if seat.TicketID == "" {
		t.Fatalf("seat ticket id is empty: the seat must name the ticket it belongs to")
	}
}

// TestJoinRoomPersistsTheGuestSeat covers the second half of the write path:
// a guest that joins gets its own seat row, and the seat count grows.
func TestJoinRoomPersistsTheGuestSeat(t *testing.T) {
	rooms := NewMemoryRoomStore()
	service := NewService(Config{Rooms: rooms})
	host := mustLogin(t, service, "Seat Join Host")
	guest := mustLogin(t, service, "Seat Join Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "seat_join_host_deck",
		DeckSnapshot: validDeck("seat_join_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "seat_join_guest_deck",
		DeckSnapshot: validDeck("seat_join_guest_deck"),
	}); err != nil {
		t.Fatalf("join room: %v", err)
	}

	members, err := rooms.ListMembers(created.RoomCode)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("seat count = %d, want 2", len(members))
	}
	seats := map[string]RoomMemberRecord{}
	for _, member := range members {
		seats[member.UserID] = member
	}
	if guestSeat, ok := seats[guest.UserID]; !ok {
		t.Fatalf("guest has no seat row: %+v", members)
	} else if guestSeat.IsHost {
		t.Fatalf("guest seat marked is_host")
	} else if guestSeat.SeatIndex != 1 {
		t.Fatalf("guest seat index = %d, want 1", guestSeat.SeatIndex)
	}
}

// TestRoomSnapshotRebuildsSeatsAfterRestart is the read half.
//
// A restart wipes the in-process ticket map while lobby_rooms keeps the ticket
// ids, so the snapshot has to fall back to the durable seats. Without that
// fallback a perfectly healthy room renders with zero players and no
// participants, which is exactly the bug this change fixes.
func TestRoomSnapshotRebuildsSeatsAfterRestart(t *testing.T) {
	rooms := NewMemoryRoomStore()
	service := NewService(Config{Rooms: rooms})
	host := mustLogin(t, service, "Restart Host")
	guest := mustLogin(t, service, "Restart Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "restart_host_deck",
		DeckSnapshot: validDeck("restart_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "restart_guest_deck",
		DeckSnapshot: validDeck("restart_guest_deck"),
	}); err != nil {
		t.Fatalf("join room: %v", err)
	}

	now := time.Now().UTC()
	before, ok, err := service.loadRoomRecordLocked(created.RoomCode)
	if err != nil || !ok {
		t.Fatalf("load room before restart: ok=%v err=%v", ok, err)
	}
	if got := service.roomSnapshotLocked(&before, now); got.CurrentPlayers != 2 {
		t.Fatalf("players before restart = %d, want 2", got.CurrentPlayers)
	}

	// Wipe the in-process tickets: this is what a process restart does. The
	// room row and its seats are the only things left.
	service.mu.Lock()
	service.tickets = map[string]*queueTicket{}
	service.mu.Unlock()

	after, ok, err := service.loadRoomRecordLocked(created.RoomCode)
	if err != nil || !ok {
		t.Fatalf("load room after restart: ok=%v err=%v", ok, err)
	}
	snapshot := service.roomSnapshotLocked(&after, now)
	if snapshot.CurrentPlayers != 2 {
		t.Fatalf("players after restart = %d, want 2 (seats were not rebuilt)", snapshot.CurrentPlayers)
	}
	if len(snapshot.Participants) != 2 {
		t.Fatalf("participants after restart = %d, want 2", len(snapshot.Participants))
	}
	seen := map[string]bool{}
	for _, participant := range snapshot.Participants {
		seen[participant.UserID] = true
		if participant.TicketID == "" {
			t.Fatalf("rebuilt participant lost its ticket id: %+v", participant)
		}
		if participant.DisplayName == "" {
			t.Fatalf("rebuilt participant has no display name: %+v", participant)
		}
	}
	if !seen[host.UserID] || !seen[guest.UserID] {
		t.Fatalf("rebuilt participants = %v, want host %q and guest %q", seen, host.UserID, guest.UserID)
	}
}

// TestLeavingRoomStampsTheSeatWithoutDeletingIt keeps the departure auditable:
// the seat row stays, with left_at set, and the snapshot stops counting it.
func TestLeavingRoomStampsTheSeatWithoutDeletingIt(t *testing.T) {
	rooms := NewMemoryRoomStore()
	service := NewService(Config{Rooms: rooms})
	host := mustLogin(t, service, "Leave Host")
	guest := mustLogin(t, service, "Leave Guest")

	// coop_boss seats up to four, so the room is still waiting after the
	// guest joins and the departure path is reachable. A two-player mode would
	// already have matched on the second seat.
	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "leave_host_deck",
		DeckSnapshot: validDeck("leave_host_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "leave_guest_deck",
		DeckSnapshot: validDeck("leave_guest_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	}); err != nil {
		t.Fatalf("join room: %v", err)
	}
	if _, err := service.LeaveRoom(guest.SessionToken, created.RoomCode); err != nil {
		t.Fatalf("leave room: %v", err)
	}

	members, err := rooms.ListMembers(created.RoomCode)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	var guestSeat *RoomMemberRecord
	for index := range members {
		if members[index].UserID == guest.UserID {
			guestSeat = &members[index]
		}
	}
	if guestSeat == nil {
		t.Fatalf("guest seat row was deleted; departures must be stamped, not removed")
	}
	if guestSeat.LeftAt.IsZero() {
		t.Fatalf("guest seat left_at is zero, want the departure time")
	}

	now := time.Now().UTC()
	record, ok, err := service.loadRoomRecordLocked(created.RoomCode)
	if err != nil || !ok {
		t.Fatalf("load room: ok=%v err=%v", ok, err)
	}
	if got := service.roomSnapshotLocked(&record, now); got.CurrentPlayers != 1 {
		t.Fatalf("players after departure = %d, want 1", got.CurrentPlayers)
	}
}
