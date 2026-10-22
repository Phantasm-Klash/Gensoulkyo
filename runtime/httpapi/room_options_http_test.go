package httpapi

import (
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// TestHTTPRoomOptionsUpdateIsHostOnly exercises POST /v1/rooms/{code}/options
// end to end: the host can change the room, a guest cannot, and the change is
// visible on the room.
func TestHTTPRoomOptionsUpdateIsHostOnly(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	host := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id": "options-http-host", "display_name": "Options Host",
	})
	guest := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id": "options-http-guest", "display_name": "Options Guest",
	})

	created := postJSON[core.QueueResponse](t, server.URL+"/v1/rooms/create", host.SessionToken, map[string]any{
		"mode_id":        "coop_boss",
		"active_deck_id": "options_http_deck",
		"deck_snapshot":  validDeck("options_http_deck"),
		"mode_params":    map[string]any{"coop_players": 4},
	})
	if created.RoomCode == "" {
		t.Fatalf("create room returned no room code: %+v", created)
	}

	updated := postJSON[core.QueueResponse](t, server.URL+"/v1/rooms/"+created.RoomCode+"/options", host.SessionToken, map[string]any{
		"boss_id": "hui",
	})
	if updated.BossID != "hui" {
		t.Fatalf("host update boss = %q, want hui", updated.BossID)
	}

	refused := postRaw(t, server.URL+"/v1/rooms/"+created.RoomCode+"/options", guest.SessionToken, map[string]any{
		"boss_id": "tian",
	})
	if refused.OK || refused.ErrorCode != "unauthorized" {
		t.Fatalf("guest update = %+v, want unauthorized", refused)
	}

	room := getJSON[core.RoomSnapshot](t, server.URL+"/v1/rooms/"+created.RoomCode, host.SessionToken)
	if room.BossID != "hui" {
		t.Fatalf("room boss after guest attempt = %q, want hui (unchanged)", room.BossID)
	}
	if room.RoomCode != created.RoomCode {
		t.Fatalf("room lookup returned %q, want %q", room.RoomCode, created.RoomCode)
	}
}
