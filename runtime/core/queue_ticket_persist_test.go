package core

import (
	"encoding/json"
	"testing"
)

// restartTicketCache wipes the in-process ticket cache, which is what a lobby
// restart does: the maps are gone, the durable rows are not.
func restartTicketCache(service *Service) {
	service.mu.Lock()
	service.tickets = map[string]*queueTicket{}
	service.missingQueueTickets = map[string]bool{}
	service.mu.Unlock()
}

// TestCreateRoomPersistsTheQueueTicket covers the write half. The room row and
// the seat were already durable; the ticket a client holds in its hand was not,
// so after a restart the lobby could not answer "what is this ticket" at all.
func TestCreateRoomPersistsTheQueueTicket(t *testing.T) {
	tickets := NewMemoryQueueTicketStore()
	service := NewService(Config{QueueTickets: tickets, Rooms: NewMemoryRoomStore()})
	host := mustLogin(t, service, "Ticket Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticket_host_deck",
		DeckSnapshot: validDeck("ticket_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}

	record, ok, err := tickets.Load(created.TicketID)
	if err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if !ok {
		t.Fatalf("ticket %q was not persisted", created.TicketID)
	}
	if record.UserID != host.UserID {
		t.Fatalf("ticket user = %q, want %q", record.UserID, host.UserID)
	}
	if record.RoomCode != created.RoomCode {
		t.Fatalf("ticket room = %q, want %q", record.RoomCode, created.RoomCode)
	}
	if record.Status != QueueTicketStatusQueued {
		t.Fatalf("ticket status = %q, want queued", record.Status)
	}
	if record.DeckSnapshot.DeckID != "ticket_host_deck" {
		t.Fatalf("ticket deck = %q, want the deck the client queued with", record.DeckSnapshot.DeckID)
	}
}

// TestQueueTicketIsReadableAfterRestart is the read half: the client still holds
// its ticket id, so QueueTicket must answer from the store instead of reporting
// not_found.
func TestQueueTicketIsReadableAfterRestart(t *testing.T) {
	tickets := NewMemoryQueueTicketStore()
	service := NewService(Config{QueueTickets: tickets, Rooms: NewMemoryRoomStore()})
	host := mustLogin(t, service, "Ticket Restart Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticket_restart_deck",
		DeckSnapshot: validDeck("ticket_restart_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}

	restartTicketCache(service)

	response, err := service.QueueTicket(host.SessionToken, created.TicketID)
	if err != nil {
		t.Fatalf("queue ticket after restart: %v", err)
	}
	if response.TicketID != created.TicketID {
		t.Fatalf("ticket id = %q, want %q", response.TicketID, created.TicketID)
	}
	if response.RoomCode != created.RoomCode {
		t.Fatalf("room code = %q, want %q", response.RoomCode, created.RoomCode)
	}
}

// TestJoinRoomRecognisesAnExistingSeatAfterRestart guards the duplicate-seat
// bug: without the fallback, a guest who reconnects takes a second seat in a
// room that already counts them, and a two-player room reports itself full.
func TestJoinRoomRecognisesAnExistingSeatAfterRestart(t *testing.T) {
	rooms := NewMemoryRoomStore()
	tickets := NewMemoryQueueTicketStore()
	service := NewService(Config{QueueTickets: tickets, Rooms: rooms})
	host := mustLogin(t, service, "Rejoin Host")
	guest := mustLogin(t, service, "Rejoin Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "rejoin_host_deck",
		DeckSnapshot: validDeck("rejoin_host_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "rejoin_guest_deck",
		DeckSnapshot: validDeck("rejoin_guest_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}

	restartTicketCache(service)

	again, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "rejoin_guest_deck",
		DeckSnapshot: validDeck("rejoin_guest_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	})
	if err != nil {
		t.Fatalf("rejoin after restart: %v", err)
	}
	if again.TicketID != joined.TicketID {
		t.Fatalf("rejoin minted a new ticket %q, want the original %q", again.TicketID, joined.TicketID)
	}

	members, err := rooms.ListMembers(created.RoomCode)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("seat count = %d, want 2 (a rejoin must not add a seat)", len(members))
	}
}

// TestCancelledTicketStaysCancelledAfterRestart keeps the terminal state honest:
// a cancelled ticket that read back as "queued" would let a client replay into a
// seat it already gave up.
func TestCancelledTicketStaysCancelledAfterRestart(t *testing.T) {
	tickets := NewMemoryQueueTicketStore()
	service := NewService(Config{QueueTickets: tickets, Rooms: NewMemoryRoomStore()})
	host := mustLogin(t, service, "Ticket Cancel Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticket_cancel_deck",
		DeckSnapshot: validDeck("ticket_cancel_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := service.LeaveRoom(host.SessionToken, created.RoomCode); err != nil {
		t.Fatalf("leave room: %v", err)
	}

	record, ok, err := tickets.Load(created.TicketID)
	if err != nil || !ok {
		t.Fatalf("load ticket: ok=%v err=%v", ok, err)
	}
	if record.Status != QueueTicketStatusCancelled {
		t.Fatalf("persisted status = %q, want cancelled", record.Status)
	}

	restartTicketCache(service)

	response, err := service.QueueTicket(host.SessionToken, created.TicketID)
	if err != nil {
		t.Fatalf("queue ticket after restart: %v", err)
	}
	if response.QueueStatus != QueueTicketStatusCancelled {
		t.Fatalf("status after restart = %q, want cancelled", response.QueueStatus)
	}
}

// TestCoopPlayersAcceptsEveryNumericShape pins the parameter decoding.
//
// The HTTP surface decodes bodies with decoder.UseNumber(), so a real client
// sends json.Number. Only the string and float64 arms existed, which meant the
// number 4 was rejected as unparseable while the string "4" was accepted -- the
// same request succeeding or failing depending on how the client quoted a
// number.
func TestCoopPlayersAcceptsEveryNumericShape(t *testing.T) {
	cases := []struct {
		value any
		want  int
	}{
		{json.Number("4"), 4},
		{json.Number("2"), 2},
		{float64(4), 4},
		{int(2), 2},
		{"4", 4},
		{" 2 ", 2},
	}
	for _, tc := range cases {
		params := map[string]any{"coop_players": tc.value}
		if got := coopPlayersFromModeParams(params); got != tc.want {
			t.Fatalf("coopPlayersFromModeParams(%#v) = %d, want %d", tc.value, got, tc.want)
		}
		if err := validateCoopPlayers("coop_boss", params); err != nil {
			t.Fatalf("validateCoopPlayers(%#v) rejected a valid count: %v", tc.value, err)
		}
	}

	// Values outside 2 and 4 must still be refused, whatever their shape.
	for _, bad := range []any{json.Number("3"), float64(8), "0"} {
		if err := validateCoopPlayers("coop_boss", map[string]any{"coop_players": bad}); err == nil {
			t.Fatalf("validateCoopPlayers(%#v) accepted an invalid count", bad)
		}
	}
}
