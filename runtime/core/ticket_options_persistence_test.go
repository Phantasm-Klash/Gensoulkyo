package core

import (
	"testing"
)

// TestTicketOptionSnapshotSurvivesAStoreRoundTrip: the room's options ride on
// every ticket in the room, because the match snapshot is assembled from
// tickets. If the snapshot does not survive the trip through the store, a
// restart quietly reverts every room to the default options while the room
// record itself still claims the host's choice.
func TestTicketOptionSnapshotSurvivesAStoreRoundTrip(t *testing.T) {
	ticket := &queueTicket{
		TicketID:   "ticket_options_1",
		UserID:     "user_1",
		ModeID:     "mvp_boss_race",
		Status:     "queued",
		ModeParams: map[string]any{"max_ticks": 1800, "boss_id": "hui", "coop_players": 4},
		MaxTicks:   1800,
	}

	record := queueTicketRecordFromTicket(ticket)
	restored := record.toQueueTicket()

	if restored.MaxTicks != 1800 {
		t.Fatalf("max ticks after the round trip = %d, want 1800", restored.MaxTicks)
	}
	if got := intFromAnyValue(restored.ModeParams["max_ticks"]); got != 1800 {
		t.Fatalf("mode_params max_ticks = %v, want 1800", got)
	}
	if got, _ := restored.ModeParams["boss_id"].(string); got != "hui" {
		t.Fatalf("mode_params boss_id = %q, want hui", got)
	}
	if got := intFromAnyValue(restored.ModeParams["coop_players"]); got != 4 {
		t.Fatalf("mode_params coop_players = %v, want 4", got)
	}
}

// TestTicketWithoutOptionsRestoresZero: a ticket loaded from a row written before
// the options column existed must read as "no options", never as a stale value
// carried over from something else.
func TestTicketWithoutOptionsRestoresZero(t *testing.T) {
	restored := QueueTicketRecord{TicketID: "ticket_legacy", ModeID: "mvp_boss_race"}.toQueueTicket()
	if restored.MaxTicks != 0 {
		t.Fatalf("max ticks for a ticket without options = %d, want 0", restored.MaxTicks)
	}
	if len(restored.ModeParams) != 0 {
		t.Fatalf("mode params for a ticket without options = %v, want empty", restored.ModeParams)
	}
}

// TestTicketOptionSnapshotIsCopied: the ticket and the record must not share the
// map, or an edit to one silently rewrites the other.
func TestTicketOptionSnapshotIsCopied(t *testing.T) {
	ticket := &queueTicket{
		TicketID:   "ticket_options_2",
		ModeParams: map[string]any{"max_ticks": 1800},
	}
	record := queueTicketRecordFromTicket(ticket)
	record.ModeParams["max_ticks"] = 600
	if got := intFromAnyValue(ticket.ModeParams["max_ticks"]); got != 1800 {
		t.Fatalf("editing the record changed the ticket: %v", got)
	}

	restored := record.toQueueTicket()
	restored.ModeParams["max_ticks"] = 900
	if got := intFromAnyValue(record.ModeParams["max_ticks"]); got != 600 {
		t.Fatalf("editing the restored ticket changed the record: %v", got)
	}
}
